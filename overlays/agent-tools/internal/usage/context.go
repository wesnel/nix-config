package usage

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// LargeContext is the context, in tokens, from which a stopping point
// suggests /clear: every request re-sends it, and after an idle hour
// writes it to the cache again.
const LargeContext = 200_000

// Context is a Claude Code session's context as its status line last saw
// it: the tokens of its last request, and whether its prompt cache was
// warm and until when.
type Context struct {
	Session        string `json:"session"`
	ObservedAt     int64  `json:"observed_at"`
	Tokens         int64  `json:"tokens"`
	WindowSize     int64  `json:"window_size"`
	CacheWarm      bool   `json:"cache_warm"`
	CacheExpiresAt int64  `json:"cache_expires_at"`
}

// sessionFile is a session ID usable as a file name.
var sessionFile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// FromClaudeContext reads Claude Code's status line input: its
// context_window.current_usage, absent until the session's first response,
// and its prompt_cache.
func FromClaudeContext(input []byte, now time.Time) (Context, bool) {
	var in struct {
		SessionID string `json:"session_id"`
		Context   *struct {
			Size    int64 `json:"context_window_size"`
			Current *struct {
				Input      int64 `json:"input_tokens"`
				CacheWrite int64 `json:"cache_creation_input_tokens"`
				CacheRead  int64 `json:"cache_read_input_tokens"`
			} `json:"current_usage"`
		} `json:"context_window"`
		Cache *struct {
			Warm      bool  `json:"warm"`
			ExpiresAt int64 `json:"expires_at"`
		} `json:"prompt_cache"`
	}
	if json.Unmarshal(input, &in) != nil || !sessionFile.MatchString(in.SessionID) || in.Context == nil ||
		in.Context.Current == nil {
		return Context{}, false
	}
	u := in.Context.Current
	c := Context{Session: in.SessionID, ObservedAt: now.Unix(), Tokens: u.Input + u.CacheWrite + u.CacheRead,
		WindowSize: in.Context.Size}
	if in.Cache != nil {
		c.CacheWarm, c.CacheExpiresAt = in.Cache.Warm, in.Cache.ExpiresAt
	}
	return c, true
}

// Heavy is why carrying c on costs more than starting again from the
// plan: a context of LargeContext tokens or more, or a prompt cache that
// has gone cold, which the next request writes whole; else "".
func (c Context) Heavy(now time.Time) string {
	var why []string
	if c.Tokens >= LargeContext {
		why = append(why, fmt.Sprintf("the context holds %dK tokens", c.Tokens/1000))
	}
	if !c.CacheWarm || (c.CacheExpiresAt > 0 && c.CacheExpiresAt <= now.Unix()) {
		why = append(why, "the prompt cache has gone cold")
	}
	return strings.Join(why, " and ")
}
