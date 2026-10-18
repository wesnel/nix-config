package usage

import (
	"strings"
	"testing"
	"time"
)

// The status line input carries each Claude Code session's context size
// and cache warmth.
func TestSessionContext(t *testing.T) {
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
}
