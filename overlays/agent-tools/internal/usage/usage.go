// Package usage reads the quota windows in Claude Code's status line input,
// without credentials or network, and records them in File for the quota
// stop.
package usage

import (
	"encoding/json"
	"errors"
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
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var in map[string]stored
	if json.Unmarshal(data, &in) != nil {
		return nil
	}
	var ws []Window
	for _, n := range windowNames {
		if s, ok := in[n.key]; ok && s.ResetsAt > 0 {
			ws = append(ws, Window{Name: n.name, Used: s.Used, ResetsAt: s.ResetsAt, ObservedAt: s.ObservedAt})
		}
	}
	return ws
}

// Fresh is how long a recorded window counts after it was observed: five
// of the status line's one-minute refreshes. With no session refreshing,
// the record stops counting rather than hold on an old reading.
const Fresh = 5 * time.Minute

// Over is the first window of ws at or above threshold percent that
// counts at now: its reset still ahead and observed within Fresh.
func Over(ws []Window, threshold float64, now time.Time) (Window, bool) {
	for _, w := range ws {
		if w.ResetsAt > now.Unix() && now.Unix()-w.ObservedAt <= int64(Fresh/time.Second) && w.Used >= threshold {
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
// later window replaces it. Sessions record one at a time, each holding an
// exclusive flock on file's ".lock" from its load to its write, so none
// writes over another's newer window with what it loaded before. The file
// is replaced by a rename, so a reader, which takes no lock, sees one
// whole version. Without the lock, Record records nothing.
func Record(file string, seen []Window) error {
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
	for _, w := range Load(file) {
		by[w.Name] = w
	}
	for _, w := range seen {
		old, ok := by[w.Name]
		switch {
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
