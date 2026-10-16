// Package usage keeps this host's latest reading of each provider's quota
// windows, as the agents' own inputs carry them, without credentials or
// network: Claude Code's status line input and print sessions' stream, and
// Codex's rollout.
package usage

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
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

// Providers are the providers whose quota is read.
var Providers = []string{"claude", "codex"}

var providerName = regexp.MustCompile(`^[a-z]+$`)

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

// FromClaudeStream reads the last rate_limit_event of Claude Code's
// `--output-format stream-json` output, whose utilization is a fraction.
func FromClaudeStream(stream io.Reader, now time.Time) (Reading, bool) {
	type window struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    int64    `json:"resetsAt"`
	}
	var last Reading
	found := false
	lines := bufio.NewReader(stream)
	for {
		line, err := lines.ReadBytes('\n')
		var in struct {
			Type string `json:"type"`
			Info struct {
				Windows struct {
					FiveHour *window `json:"five_hour"`
					SevenDay *window `json:"seven_day"`
				} `json:"unifiedWindows"`
			} `json:"rate_limit_info"`
		}
		if json.Unmarshal(line, &in) == nil && in.Type == "rate_limit_event" {
			r := Reading{Provider: "claude", ObservedAt: now.Unix()}
			for _, w := range []struct {
				name string
				w    *window
			}{{"5h", in.Info.Windows.FiveHour}, {"7d", in.Info.Windows.SevenDay}} {
				if w.w != nil && w.w.Utilization != nil && w.w.ResetsAt > 0 {
					r.Windows = append(r.Windows, Window{Name: w.name, Used: math.Round(*w.w.Utilization*10000) / 100,
						ResetsAt: w.w.ResetsAt})
				}
			}
			if len(r.Windows) > 0 {
				last, found = r, true
			}
		}
		if err != nil {
			return last, found
		}
	}
}

// FromCodexTokenCount reads one line of a Codex rollout, which carries the
// windows when it is a token_count event with rate limits.
func FromCodexTokenCount(line []byte, now time.Time) (Reading, bool) {
	type window struct {
		Used     *float64 `json:"used_percent"`
		Minutes  int64    `json:"window_minutes"`
		ResetsAt int64    `json:"resets_at"`
	}
	var in struct {
		Type    string `json:"type"`
		Payload struct {
			Type       string `json:"type"`
			RateLimits *struct {
				Primary   *window `json:"primary"`
				Secondary *window `json:"secondary"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &in) != nil || in.Type != "event_msg" || in.Payload.Type != "token_count" ||
		in.Payload.RateLimits == nil {
		return Reading{}, false
	}
	r := Reading{Provider: "codex", ObservedAt: now.Unix()}
	for _, w := range []*window{in.Payload.RateLimits.Primary, in.Payload.RateLimits.Secondary} {
		if w != nil && w.Used != nil && w.Minutes > 0 && w.ResetsAt > 0 {
			r.Windows = append(r.Windows, Window{Name: windowName(w.Minutes), Used: *w.Used, ResetsAt: w.ResetsAt})
		}
	}
	return r, len(r.Windows) > 0
}

func windowName(minutes int64) string {
	switch {
	case minutes%(24*60) == 0:
		return fmt.Sprintf("%dd", minutes/(24*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dm", minutes)
}

// rolloutTail is how much of the end of a rollout is searched: a token_count
// follows every response, so the last one is near the end.
const rolloutTail = 2 << 20

// FromCodexRollout reads the last token_count with rate limits in the
// rollout at path, the transcript_path of Codex's hook input.
func FromCodexRollout(path string, now time.Time) (Reading, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Reading{}, false
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > rolloutTail {
		f.Seek(info.Size()-rolloutTail, io.SeekStart)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return Reading{}, false
	}
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		if r, ok := FromCodexTokenCount(lines[i], now); ok {
			return r, true
		}
	}
	return Reading{}, false
}

// State is the directory holding this host's readings.
type State struct{ Dir string }

// DefaultState is $XDG_STATE_HOME/agent-tools/usage.
func DefaultState() State {
	dir := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(dir) {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return State{Dir: filepath.Join(dir, "agent-tools", "usage")}
}

// Record replaces the provider's reading with r.
func (s State) Record(r Reading) error {
	if !providerName.MatchString(r.Provider) {
		return fmt.Errorf("usage: %q is not a provider", r.Provider)
	}
	return s.write(r.Provider+".json", r)
}

// write replaces the file name with v as JSON, by rename, so a reader sees
// the old file or the new one.
func (s State) write(name string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.Dir, name)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// Reading is the provider's last reading without the windows that have
// reset by now, and whether any window is left.
func (s State) Reading(provider string, now time.Time) (Reading, bool) {
	if !providerName.MatchString(provider) {
		return Reading{}, false
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, provider+".json"))
	if err != nil {
		return Reading{}, false
	}
	var r Reading
	if json.Unmarshal(data, &r) != nil || r.Provider != provider {
		return Reading{}, false
	}
	var live []Window
	for _, w := range r.Windows {
		if w.ResetsAt > now.Unix() {
			live = append(live, w)
		}
	}
	r.Windows = live
	return r, len(live) > 0
}
