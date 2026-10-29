package usage

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// FromCodexRateLimits reads the windows in the result of Codex's app-server
// call account/rateLimits/read. Its primary and secondary windows are
// matched to windowNames by their length, since the call does not say which
// is which; a window of any other length is left out.
func FromCodexRateLimits(result []byte, now time.Time) ([]Window, bool) {
	type window struct {
		Used     *float64 `json:"usedPercent"`
		Minutes  int      `json:"windowDurationMins"`
		ResetsAt int64    `json:"resetsAt"`
	}
	var in struct {
		RateLimits struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
		} `json:"rateLimits"`
	}
	if json.Unmarshal(result, &in) != nil {
		return nil, false
	}
	var ws []Window
	for _, n := range windowNames {
		for _, w := range []*window{in.RateLimits.Primary, in.RateLimits.Secondary} {
			if w != nil && w.Minutes == n.minutes && w.Used != nil && w.ResetsAt > 0 {
				ws = append(ws, Window{Name: n.name, Used: *w.Used, ResetsAt: w.ResetsAt, ObservedAt: now.Unix()})
				break
			}
		}
	}
	return ws, len(ws) > 0
}

// CodexFile is where `agent-plan codex-usage` records the Codex account's
// windows, in File's form: agent-plan/codex-usage.json in the state
// directory.
func CodexFile() string {
	return filepath.Join(hostpaths.StateHome(), "agent-plan", "codex-usage.json")
}
