package availability

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHold(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	hour, pause := now.Add(time.Hour).Unix(), now.Add(5*time.Minute).Unix()
	for _, c := range []struct {
		name        string
		first, then State
		want        State
	}{
		{"a rate limit leaves a later quota reset", State{Quota, hour}, State{RateLimit, pause}, State{Quota, hour}},
		{"a quota with no known reset outlasts a rate limit", State{RateLimit, pause}, State{Quota, 0}, State{Quota, 0}},
		{"a later rate limit extends an earlier one", State{RateLimit, pause}, State{RateLimit, pause + 60}, State{RateLimit, pause + 60}},
		{"disabled outlasts quota", State{Disabled, 0}, State{Quota, hour}, State{Disabled, 0}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			for _, s := range []State{c.first, c.then} {
				if err := Hold(Codex, s, now); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := Check(Codex, now); err != nil || got != c.want {
				t.Errorf("held %+v (%v), want %+v", got, err, c.want)
			}
		})
	}
}

// A rate limit always ends: one stored with no end is invalid, which
// callers treat as a hold until Clear repairs it.
func TestRateLimitEnds(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := Hold(Codex, State{Reason: RateLimit}, time.Now()); err == nil {
		t.Error("a rate limit with no end was recorded")
	}
	if err := os.MkdirAll(filepath.Dir(File(Codex)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(File(Codex), []byte(`{"reason":"rate_limit"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(Codex, time.Now()); err == nil {
		t.Error("a stored rate limit with no end was read as valid")
	}
	if err := Clear(Codex); err != nil {
		t.Fatal(err)
	}
	if s, err := Check(Codex, time.Now()); err != nil || s != (State{}) {
		t.Errorf("after Clear: %+v, %v", s, err)
	}
}
