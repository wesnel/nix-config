package usage

import (
	"testing"
	"time"
)

// pdt is the zone the reset times are shown in: the operator's.
var pdt = time.FixedZone("PDT", -7*3600)

// withCache is a status line input of a session past its first response,
// with prompt_cache set to cache.
func withCache(cache string) []byte {
	return []byte(`{"session_id":"abc","context_window":{"used_percentage":4,"context_window_size":1000,` +
		`"current_usage":{"input_tokens":1}},"rate_limits":{"seven_day":{"used_percentage":31,"resets_at":1792602000}},` +
		`"prompt_cache":` + cache + `}`)
}

// The status line shows the session's context, its prompt cache and the
// account's windows, with the reset of a window at or over 75%.
func TestStatusLine(t *testing.T) {
	now := at.In(pdt)

	t.Run("Claude Code", func(t *testing.T) {
		got := StatusLine(fixture(t, "claude-statusline.json"), now)
		if want := "ctx 4% · cache 5m 63% · miss 0 · cold 42K · 5h 100%→19:40 · 7d 31%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// The fixture's recache_tokens_if_cold is null, as right after a
	// compaction.
	t.Run("a cache miss with its cause", func(t *testing.T) {
		got := StatusLine(fixture(t, "claude-statusline-miss.json"), now)
		want := "ctx 4% · cache 1h 91% · miss 2 tools_changed · 5h 100%→19:40 · 7d 31%"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	// Each case is a prompt_cache value and the line it gives, with the
	// context and windows withCache adds around it.
	type cacheCase struct{ name, cache, want string }
	runCache := func(t *testing.T, cases []cacheCase) {
		t.Helper()
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				if got := StatusLine(withCache(c.cache), now); got != c.want {
					t.Errorf("%s: got %q, want %q", c.cache, got, c.want)
				}
			})
		}
	}

	t.Run("a prompt cache with fields absent", func(t *testing.T) {
		runCache(t, []cacheCase{
			{"nulls", `{"warm":true,"hit_ratio":null,"last_miss_cause":null}`, "ctx 4% · 7d 31%"},
			{"no ttl or misses", `{"hit_ratio":0.5,"recache_tokens_if_cold":812}`, "ctx 4% · cache 50% · cold 812 · 7d 31%"},
			{"no ratio or causes", `{"ttl":"5m","misses":1,"last_miss_cause":{"causes":[]}}`, "ctx 4% · cache 5m · miss 1 · 7d 31%"},
		})
	})

	// A prompt_cache field of a type Claude Code does not send drops that
	// field alone: the context and the windows still show.
	t.Run("a prompt cache field of an unexpected type", func(t *testing.T) {
		runCache(t, []cacheCase{
			{"ttl a number", `{"warm":true,"ttl":300}`, "ctx 4% · 7d 31%"},
			{"misses a float", `{"warm":true,"ttl":"5m","misses":2.0}`, "ctx 4% · cache 5m · 7d 31%"},
			{"recache a fraction", `{"warm":true,"recache_tokens_if_cold":42518.5}`, "ctx 4% · 7d 31%"},
			{"recache past int64", `{"warm":true,"recache_tokens_if_cold":1e20}`, "ctx 4% · 7d 31%"},
			{"causes a string", `{"misses":1,"last_miss_cause":{"causes":"tools_changed"}}`, "ctx 4% · miss 1 · 7d 31%"},
			{"ratio a string", `{"warm":true,"hit_ratio":"NaN","recache_tokens_if_cold":1}`, "ctx 4% · cold 1 · 7d 31%"},
			{"expires_at a float", `{"warm":true,"expires_at":1792602000.0,"hit_ratio":0.5}`, "ctx 4% · 7d 31%"},
			{"an array", `[]`, "ctx 4% · 7d 31%"},
		})
	})

	// A value outside its range shows nothing, and of the last miss's
	// causes only the first that is a cause name shows.
	t.Run("a prompt cache value out of range", func(t *testing.T) {
		runCache(t, []cacheCase{
			{"ratio below 0", `{"hit_ratio":-0.001}`, "ctx 4% · 7d 31%"},
			{"ratio above 1", `{"hit_ratio":1.001}`, "ctx 4% · 7d 31%"},
			{"ratio huge", `{"hit_ratio":1e300}`, "ctx 4% · 7d 31%"},
			{"ratio 1", `{"hit_ratio":1}`, "ctx 4% · cache 100% · 7d 31%"},
			{"ratio 0", `{"hit_ratio":0}`, "ctx 4% · cache 0% · 7d 31%"},
			{"recache negative", `{"recache_tokens_if_cold":-5000}`, "ctx 4% · 7d 31%"},
			{"recache 999", `{"recache_tokens_if_cold":999}`, "ctx 4% · cold 999 · 7d 31%"},
			{"recache 1000", `{"recache_tokens_if_cold":1000}`, "ctx 4% · cold 1K · 7d 31%"},
			{"recache 0", `{"recache_tokens_if_cold":0}`, "ctx 4% · cold 0 · 7d 31%"},
			{"misses negative", `{"misses":-1}`, "ctx 4% · 7d 31%"},
			{"ttl with an escape", `{"ttl":"5m\u001b[31m"}`, "ctx 4% · 7d 31%"},
			{"a null cause", `{"misses":1,"last_miss_cause":{"causes":[null]}}`, "ctx 4% · miss 1 · 7d 31%"},
			{"first cause name", `{"misses":1,"last_miss_cause":{"causes":["","a b","x_1"]}}`, "ctx 4% · miss 1 x_1 · 7d 31%"},
			{"cause with an escape", `{"misses":1,"last_miss_cause":{"causes":["a\u001b[31m"]}}`, "ctx 4% · miss 1 · 7d 31%"},
			{"many causes", `{"misses":6,"last_miss_cause":{"causes":["a","b","c","d"]}}`, "ctx 4% · miss 6 a · 7d 31%"},
		})
	})

	t.Run("Claude Code before anything is known", func(t *testing.T) {
		if got := StatusLine(fixture(t, "claude-statusline-first.json"), now); got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("windows under 75%", func(t *testing.T) {
		input := `{"context_window":{"used_percentage":42.7},"rate_limits":{` +
			`"five_hour":{"used_percentage":11,"resets_at":1792024111},` +
			`"seven_day":{"used_percentage":2,"resets_at":1792567353}}}`
		if got, want := StatusLine([]byte(input), now), "ctx 42% · 5h 11% · 7d 2%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a reset more than a day away", func(t *testing.T) {
		input := `{"rate_limits":{"seven_day":{"used_percentage":80,"resets_at":1792602000}}}`
		if got, want := StatusLine([]byte(input), now), "7d 80%→Wed 21 10:00"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("a window that has reset", func(t *testing.T) {
		later := time.Unix(1792032000, 0).In(pdt)
		if got, want := StatusLine(fixture(t, "claude-statusline.json"), later), "ctx 4% · cache 5m 63% · miss 0 · cold 42K · 7d 31%"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("broken input", func(t *testing.T) {
		if got := StatusLine([]byte("{"), now); got != "" {
			t.Errorf("got %q", got)
		}
	})
}
