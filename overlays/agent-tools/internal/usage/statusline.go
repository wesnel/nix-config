package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Warn is the share of a window, in percent, from which the status line
// shows its reset.
const Warn = 75

// StatusLine is the usage part of `agent-plan statusline`: the session's
// context and the account's quota windows, as Claude Code's status line
// input carries them, or "". A window whose reset has passed is left out.
func StatusLine(input []byte, now time.Time) string {
	var in struct {
		Context *struct {
			Used *float64 `json:"used_percentage"`
		} `json:"context_window"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	var parts []string
	if in.Context != nil && in.Context.Used != nil {
		parts = append(parts, fmt.Sprintf("ctx %d%%", int(math.Floor(*in.Context.Used))))
	}
	if ws, ok := FromClaudeStatus(input, now); ok {
		parts = append(parts, windows(ws, now)...)
	}
	return strings.Join(parts, " · ")
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
