// Package usage reads the quota windows in Claude Code's status line input,
// without credentials or network, and records them in File for the quota
// stop.
package usage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// windowNames are the quota windows read: each one's key in Claude Code's
// rate_limits, which File uses too, and the name shown for it.
var windowNames = []struct{ key, name string }{
	{"five_hour", "5h"},
	{"seven_day", "7d"},
}

// Window is one quota window as observed at ObservedAt: its name ("5h",
// "7d"), the share of it used, in percent, and when it resets. Times are
// Unix seconds.
type Window struct {
	Name       string
	Used       float64
	ResetsAt   int64
	ObservedAt int64
}

// FromClaudeStatus reads the windows in Claude Code's status line input.
// Its rate_limits are absent or null until the session's first API
// response; after that, each refresh repeats the latest response's.
func FromClaudeStatus(input []byte, now time.Time) ([]Window, bool) {
	var in struct {
		RateLimits map[string]*struct {
			Used     *float64 `json:"used_percentage"`
			ResetsAt int64    `json:"resets_at"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil, false
	}
	var ws []Window
	for _, n := range windowNames {
		if w := in.RateLimits[n.key]; w != nil && w.Used != nil && w.ResetsAt > 0 {
			ws = append(ws, Window{Name: n.name, Used: *w.Used, ResetsAt: w.ResetsAt, ObservedAt: now.Unix()})
		}
	}
	return ws, len(ws) > 0
}

// File is where Record keeps the windows: agent-plan/usage.json in the
// state directory.
func File() string {
	return filepath.Join(hostpaths.StateHome(), "agent-plan", "usage.json")
}

// stored is one window in File, under its rate_limits key.
type stored struct {
	Used       float64 `json:"used_percentage"`
	ResetsAt   int64   `json:"resets_at"`
	ObservedAt int64   `json:"observed_at"`
}

// Load is the windows file holds, in windowNames' order; none where it is
// missing or unreadable.
func Load(file string) []Window {
	ws, _ := Read(file)
	return ws
}

// Read distinguishes missing evidence from corrupt or unreadable state.
// Admission callers must block on its error.
func Read(file string) ([]Window, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var in map[string]*struct {
		Used       *float64 `json:"used_percentage"`
		ResetsAt   *int64   `json:"resets_at"`
		ObservedAt *int64   `json:"observed_at"`
	}
	if err := json.Unmarshal(data, &in); err != nil || in == nil {
		return nil, fmt.Errorf("invalid usage state")
	}
	var ws []Window
	for _, n := range windowNames {
		if s, ok := in[n.key]; ok {
			if s == nil || s.Used == nil || s.ResetsAt == nil || s.ObservedAt == nil || *s.Used < 0 || *s.ResetsAt < 0 || *s.ObservedAt < 0 {
				return nil, fmt.Errorf("invalid %s usage state", n.key)
			}
			ws = append(ws, Window{Name: n.name, Used: *s.Used, ResetsAt: *s.ResetsAt, ObservedAt: *s.ObservedAt})
		}
	}
	if len(ws) != len(in) {
		return nil, fmt.Errorf("invalid usage state window")
	}
	return ws, nil
}

// Fresh limits threshold warnings below exhaustion. Exhaustion remains
// evidence until the window's verified reset, regardless of age.
const Fresh = 5 * time.Minute

// Over is the first window of ws at or above threshold percent that
// counts at now: unreset, and either exhausted or observed within Fresh.
func Over(ws []Window, threshold float64, now time.Time) (Window, bool) {
	for _, w := range ws {
		if (w.ResetsAt == 0 || w.ResetsAt > now.Unix()) && (w.Used >= 100 || now.Unix()-w.ObservedAt <= int64(Fresh/time.Second)) && w.Used >= threshold {
			return w, true
		}
	}
	return Window{}, false
}

// Record writes the windows seen into file, over what it holds. Every open
// session's status line records once a minute, an idle one repeating its
// latest response's reading, which may be lower or of a window that has
// since reset; within one window usage only grows. So a reading of the
// stored window (the same resets_at) keeps the higher share and the later
// observed_at, a reading of an earlier window changes nothing, and only a
// later window replaces it. Exhaustion without a reset stays until
// ClearUnknown, even when a later reading names a reset. Sessions record one
// at a time, each holding an
// exclusive flock on file's ".lock" from its load to its write, so none
// writes over another's newer window with what it loaded before. The file
// is replaced by a rename, so a reader, which takes no lock, sees one
// whole version. Without the lock, Record records nothing.
func Record(file string, seen []Window) error {
	return record(file, seen, false)
}

// ClearUnknown removes only readings without a verified reset. Known windows
// retain their high-water marks until reset even after an explicit clear.
func ClearUnknown(file string) error { return record(file, nil, true) }

func record(file string, seen []Window, clearUnknown bool) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(file+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	by := map[string]Window{}
	previous, err := Read(file)
	if err != nil {
		return err
	}
	for _, w := range previous {
		if !clearUnknown || w.ResetsAt != 0 {
			by[w.Name] = w
		}
	}
	for _, w := range seen {
		old, ok := by[w.Name]
		switch {
		case ok && old.ResetsAt == 0 && old.Used >= 100:
		case w.ResetsAt == 0 && w.Used >= 100:
			by[w.Name] = w
		case !ok || w.ResetsAt > old.ResetsAt:
			by[w.Name] = w
		case w.ResetsAt == old.ResetsAt:
			old.Used = max(old.Used, w.Used)
			old.ObservedAt = max(old.ObservedAt, w.ObservedAt)
			by[w.Name] = old
		}
	}
	out := map[string]stored{}
	for _, n := range windowNames {
		if w, ok := by[n.name]; ok {
			out[n.key] = stored{Used: w.Used, ResetsAt: w.ResetsAt, ObservedAt: w.ObservedAt}
		}
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".usage-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), file)
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
