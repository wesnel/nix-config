// Package usage reads the quota windows in Claude Code's status line input,
// without credentials or network.
package usage

import (
	"encoding/json"
	"time"
)

// Window is one quota window: its name ("5h", "7d"), the share of it used,
// in percent, and when it resets, in Unix seconds.
type Window struct {
	Name     string  `json:"name"`
	Used     float64 `json:"used_percent"`
	ResetsAt int64   `json:"resets_at"`
}

// Reading is a provider's windows as observed at ObservedAt.
type Reading struct {
	Provider   string   `json:"provider"`
	ObservedAt int64    `json:"observed_at"`
	Windows    []Window `json:"windows"`
}

// FromClaudeStatus reads Claude Code's status line input. Its rate_limits
// are absent or null until the session's first API response.
func FromClaudeStatus(input []byte, now time.Time) (Reading, bool) {
	type window struct {
		Used     *float64 `json:"used_percentage"`
		ResetsAt int64    `json:"resets_at"`
	}
	var in struct {
		RateLimits *struct {
			FiveHour *window `json:"five_hour"`
			SevenDay *window `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(input, &in) != nil || in.RateLimits == nil {
		return Reading{}, false
	}
	r := Reading{Provider: "claude", ObservedAt: now.Unix()}
	for _, w := range []struct {
		name string
		w    *window
	}{{"5h", in.RateLimits.FiveHour}, {"7d", in.RateLimits.SevenDay}} {
		if w.w != nil && w.w.Used != nil && w.w.ResetsAt > 0 {
			r.Windows = append(r.Windows, Window{Name: w.name, Used: *w.w.Used, ResetsAt: w.w.ResetsAt})
		}
	}
	return r, len(r.Windows) > 0
}
