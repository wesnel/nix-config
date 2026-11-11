package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Warn is the share of a window, in percent, from which the status line
// shows its reset.
const Warn = 75

// StatusLine is the usage part of `agent-plan statusline`: the session's
// context, its prompt cache and the account's quota windows, as Claude
// Code's status line input carries them, or "". A window whose reset has
// passed is left out.
func StatusLine(input []byte, now time.Time) string {
	var in struct {
		Context *struct {
			Used *float64 `json:"used_percentage"`
		} `json:"context_window"`
		Cache json.RawMessage `json:"prompt_cache"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var parts []string
	if in.Context != nil && in.Context.Used != nil {
		parts = append(parts, fmt.Sprintf("ctx %d%%", int(math.Floor(*in.Context.Used))))
	}
	// A prompt_cache that does not decode drops only its own parts.
	if cache := shown[PromptCache](in.Cache); cache != nil {
		parts = append(parts, cache.parts()...)
	}
	if ws, ok := FromClaudeStatus(input, now); ok {
		parts = append(parts, windows(ws, now)...)
	}
	return strings.Join(parts, " · ")
}

// ttl and causeName are what a TTL ("5m", "1h") and a miss's cause
// ("tools_changed") look like; anything else is left out of the line.
var (
	ttl       = regexp.MustCompile(`^[0-9]+[smhd]$`)
	causeName = regexp.MustCompile(`^[a-z0-9_]+$`)
)

// parts are c's fields as the status line shows them: its TTL and hit
// ratio, its misses with the last one's first cause, and the tokens a cold
// cache would write again. A field absent from the input, or out of its
// range, shows nothing.
func (c PromptCache) parts() []string {
	var out, cache []string
	if ttl.MatchString(c.TTL) {
		cache = append(cache, c.TTL)
	}
	if r := c.HitRatio; r != nil && *r >= 0 && *r <= 1 {
		cache = append(cache, fmt.Sprintf("%d%%", int(math.Floor(*r*100))))
	}
	if len(cache) > 0 {
		out = append(out, "cache "+strings.Join(cache, " "))
	}
	if c.Misses != nil && *c.Misses >= 0 {
		miss := fmt.Sprintf("miss %d", *c.Misses)
		for _, cause := range c.LastMissCauses {
			if causeName.MatchString(cause) {
				miss += " " + cause
				break
			}
		}
		out = append(out, miss)
	}
	if c.RecacheIfCold != nil && *c.RecacheIfCold >= 0 {
		out = append(out, "cold "+tokens(*c.RecacheIfCold))
	}
	return out
}

// tokens is n, in thousands from 1,000 up.
func tokens(n int64) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%dK", n/1000)
}

// windows are the windows of ws that have not reset by now, as the status
// line shows them.
func windows(ws []Window, now time.Time) []string {
	var out []string
	for _, w := range ws {
		switch {
		case w.ResetsAt <= now.Unix():
		case w.Used >= Warn:
			out = append(out, fmt.Sprintf("%s %d%%→%s", w.Name, int(math.Floor(w.Used)), Clock(w.ResetsAt, now)))
		default:
			out = append(out, fmt.Sprintf("%s %d%%", w.Name, int(math.Floor(w.Used))))
		}
	}
	return out
}

// Clock is the Unix time t in now's zone: the time of day within a day of
// now, else with the weekday and day of the month.
func Clock(t int64, now time.Time) string {
	at := time.Unix(t, 0).In(now.Location())
	if at.Sub(now) < 24*time.Hour {
		return at.Format("15:04")
	}
	return at.Format("Mon 2 15:04")
}
