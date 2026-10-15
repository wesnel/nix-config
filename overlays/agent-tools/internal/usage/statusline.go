package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// Warn is the share of a window, in percent, from which the status line
// shows its reset and the agents are asked whether to postpone.
const Warn = 75

// StatusLine is the usage part of `agent-plan statusline`: the session's
// context and its provider's windows, or "".
//
// Claude Code's input names no provider; it carries its own context and,
// after the first response, the account's windows, which are recorded.
// Emacs sends {cwd, provider, context_window{used_percentage}} for an agent
// buffer, its provider "claude" or "codex", or {cwd} alone elsewhere, which
// shows each provider with a window at or over Warn, named.
func StatusLine(s State, input []byte, now time.Time) string {
	var in struct {
		Provider  string `json:"provider"`
		SessionID string `json:"session_id"`
		Context   *struct {
			Used *float64 `json:"used_percentage"`
		} `json:"context_window"`
	}
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	if r, ok := FromClaudeStatus(input, now); ok {
		s.Record(r)
	}
	if in.Provider == "" && in.SessionID != "" {
		in.Provider = "claude"
	}
	var parts []string
	if in.Context != nil && in.Context.Used != nil {
		parts = append(parts, fmt.Sprintf("ctx %d%%", int(math.Floor(*in.Context.Used))))
	}
	if in.Provider != "" {
		if r, ok := s.Reading(in.Provider, now); ok {
			parts = append(parts, windows(r.Windows, now, false)...)
		}
		return strings.Join(append(parts, s.choiceMark(in.Provider, now)...), " · ")
	}
	for _, p := range Providers {
		if r, ok := s.Reading(p, now); ok {
			if shown := windows(r.Windows, now, true); len(shown) > 0 {
				parts = append(parts, p+" "+strings.Join(append(shown, s.choiceMark(p, now)...), " · "))
			}
		}
	}
	return strings.Join(parts, " · ")
}

// choiceMark is the provider's choice as the status line shows it, if any.
func (s State) choiceMark(provider string, now time.Time) []string {
	if c, ok := s.Choice(provider, now); ok && c.mark(now) != "" {
		return []string{c.mark(now)}
	}
	return nil
}

// windows are ws as the status line shows them; with warnOnly, those at or
// over Warn alone.
func windows(ws []Window, now time.Time, warnOnly bool) []string {
	var out []string
	for _, w := range ws {
		switch {
		case w.Used >= Warn:
			out = append(out, fmt.Sprintf("%s %d%%→%s", w.Name, int(math.Floor(w.Used)), Clock(w.ResetsAt, now)))
		case !warnOnly:
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
