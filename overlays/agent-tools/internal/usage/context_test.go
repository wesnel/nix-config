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

	// Only warm and expires_at make the stored context: a field the
	// status line shows, of an unexpected type, leaves it as it is.
	t.Run("a shown field of an unexpected type", func(t *testing.T) {
		for _, c := range []struct{ name, cache string }{
			{"ttl a number", `{"warm":true,"expires_at":1792021218,"ttl":300}`},
			{"misses a float", `{"warm":true,"expires_at":1792021218,"misses":2.0}`},
			{"recache past int64", `{"warm":true,"expires_at":1792021218,"recache_tokens_if_cold":1e20}`},
			{"causes a string", `{"warm":true,"expires_at":1792021218,"last_miss_cause":{"causes":"tools_changed"}}`},
		} {
			t.Run(c.name, func(t *testing.T) {
				got, ok := FromClaudeContext(withCache(c.cache), at)
				if !ok || !got.CacheWarm || got.CacheExpiresAt != 1792021218 {
					t.Errorf("%s: got %+v %v", c.cache, got, ok)
				}
			})
		}
	})

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
