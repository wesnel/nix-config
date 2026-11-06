package availability

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/usage"
)

// stateEnv is an Env whose state directory is a new one of the test's.
func stateEnv(t *testing.T) env.Env {
	t.Helper()
	return env.New(t.TempDir(), "XDG_STATE_HOME="+t.TempDir())
}

func TestHold(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			e := stateEnv(t)
			for _, s := range []State{c.first, c.then} {
				if err := Hold(e, Codex, s, now); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := Check(e, Codex, now); err != nil || got != c.want {
				t.Errorf("held %+v (%v), want %+v", got, err, c.want)
			}
		})
	}
}

// A rate limit always ends: one stored with no end is invalid, which
// callers treat as a hold until Clear repairs it.
func TestRateLimitEnds(t *testing.T) {
	t.Parallel()
	e := stateEnv(t)
	if err := Hold(e, Codex, State{Reason: RateLimit}, time.Now()); err == nil {
		t.Error("a rate limit with no end was recorded")
	}
	if err := os.MkdirAll(filepath.Dir(File(e, Codex)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(File(e, Codex), []byte(`{"reason":"rate_limit"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(e, Codex, time.Now()); err == nil {
		t.Error("a stored rate limit with no end was read as valid")
	}
	if err := Clear(e, Codex); err != nil {
		t.Fatal(err)
	}
	if s, err := Check(e, Codex, time.Now()); err != nil || s != (State{}) {
		t.Errorf("after Clear: %+v, %v", s, err)
	}
}

// The holds and the Claude quota windows are read from the Env's state
// directory, so two Envs keep apart what each records.
func TestStateOfTheEnv(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_900_000_000, 0)
	e := stateEnv(t)
	dir := e.Get("XDG_STATE_HOME")
	if got, want := File(e, Codex), filepath.Join(dir, "agent-tools", "review-availability", "codex.json"); got != want {
		t.Errorf("File = %s, want %s", got, want)
	}
	reset := now.Add(time.Hour).Unix()
	if err := usage.Record(usage.File(e), []usage.Window{{Name: "5h", Used: 100, ResetsAt: reset, ObservedAt: now.Unix()}}); err != nil {
		t.Fatal(err)
	}
	if got, err := Check(e, Claude, now); err != nil || got != (State{Quota, reset}) {
		t.Errorf("Claude with its window exhausted in the Env's state: %+v, %v; want a quota hold until %d", got, err, reset)
	}
	if got, err := Check(stateEnv(t), Claude, now); err != nil || got != (State{}) {
		t.Errorf("Claude in another Env: %+v, %v; want no hold", got, err)
	}
}
