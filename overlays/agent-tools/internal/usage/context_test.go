package usage

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The status line records each Claude Code session's context size and
// cache warmth, which its Stop hook reads to suggest /clear.
func TestSessionContext(t *testing.T) {
	s := State{Dir: filepath.Join(t.TempDir(), "usage")}
	input := fixture(t, "claude-statusline.json")
	const id = "61b2faa6-8f5d-40d9-b846-205b90ff107f"

	got, ok := FromClaudeContext(input, at)
	want := Context{Session: id, ObservedAt: at.Unix(), Tokens: 42518, WindowSize: 1000000, CacheWarm: true,
		CacheExpiresAt: 1792021218}
	if !ok || got != want {
		t.Errorf("got %+v %v, want %+v", got, ok, want)
	}
	if _, ok := FromClaudeContext(fixture(t, "claude-statusline-first.json"), at); ok {
		t.Error("a first input, before any response, has a context")
	}

	StatusLine(s, input, at)
	if recorded, ok := s.Context(id); !ok || recorded != want {
		t.Errorf("recorded %+v %v", recorded, ok)
	}
	for _, bad := range []string{"", "../x", "a/b"} {
		if _, ok := s.Context(bad); ok {
			t.Errorf("%q read", bad)
		}
	}

	t.Run("heavy", func(t *testing.T) {
		if why := want.Heavy(at); why != "" {
			t.Errorf("a small warm context is heavy: %q", why)
		}
		if why := want.Heavy(at.Add(time.Minute)); !strings.Contains(why, "cache") {
			t.Errorf("an expired cache: %q", why)
		}
		large := want
		large.Tokens = LargeContext
		if why := large.Heavy(at); !strings.Contains(why, "200K tokens") {
			t.Errorf("a large context: %q", why)
		}
		cold := want
		cold.CacheWarm = false
		if why := cold.Heavy(at); !strings.Contains(why, "cache") {
			t.Errorf("a cold cache: %q", why)
		}
	})

	t.Run("nudged once per checkpoint", func(t *testing.T) {
		if s.Nudged(id, "e1") {
			t.Error("nudged before any nudge")
		}
		if err := s.MarkNudged(id, "e1"); err != nil {
			t.Fatal(err)
		}
		if !s.Nudged(id, "e1") || s.Nudged(id, "e2") {
			t.Error("the nudge was not kept per checkpoint")
		}
	})
}
