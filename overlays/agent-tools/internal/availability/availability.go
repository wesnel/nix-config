// Package availability keeps host-local knowledge of the current provider
// accounts. Account changes require an explicit Clear. Calls already admitted
// may remain in flight when another caller records a hold.
package availability

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/usage"
)

type Provider string

const (
	Claude Provider = "claude"
	Codex  Provider = "codex"
)

type Reason string

const (
	Quota     Reason = "quota"
	Disabled  Reason = "disabled"
	RateLimit Reason = "rate_limit"
)

// State preserves the review-availability schema. Until is a verified Unix
// reset time; zero requires an explicit clear. RateLimit is a conservative
// pause on a structured error, not a claim of confirmed quota exhaustion.
type State struct {
	Reason Reason `json:"reason,omitempty"`
	Until  int64  `json:"until,omitempty"`
}

func (s State) Active(now time.Time) bool {
	return s.Reason != "" && (s.Until == 0 || s.Until > now.Unix())
}

func (s State) String() string {
	if s.Until != 0 {
		return string(s.Reason) + " until " + time.Unix(s.Until, 0).Format(time.RFC3339)
	}
	return string(s.Reason) + "; explicit clear required after recovery"
}

func validProvider(p Provider) bool { return p == Claude || p == Codex }

// File remains shared with git delegate availability.
func File(p Provider) string {
	return filepath.Join(hostpaths.StateHome(), "agent-tools", "review-availability", string(p)+".json")
}

func read(p Provider) (State, error) {
	var state State
	data, err := os.ReadFile(File(p))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return state, fmt.Errorf("invalid availability state")
	}
	for key := range raw {
		if key != "reason" && key != "until" {
			return state, fmt.Errorf("invalid availability state field %q", key)
		}
		if string(raw[key]) == "null" {
			return state, fmt.Errorf("invalid availability state field %q", key)
		}
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if err := state.validate(); err != nil {
		return State{}, err
	}
	return state, nil
}

func (s State) validate() error {
	if s.Until < 0 || (s.Reason != "" && s.Reason != Quota && s.Reason != Disabled && s.Reason != RateLimit) ||
		(s.Reason != Quota && s.Until != 0) {
		return fmt.Errorf("invalid availability state")
	}
	return nil
}

// Check returns a blocking state, or a zero state for unknown/eligible.
// Missing evidence is unknown; corrupt or unreadable evidence is an error
// that callers must treat as blocking. Exhausted windows never age out.
func Check(p Provider, now time.Time) (State, error) {
	if !validProvider(p) {
		return State{}, fmt.Errorf("invalid provider")
	}
	s, err := read(p)
	if err != nil || s.Active(now) {
		return s, err
	}
	if p == Claude {
		return ClaudeQuota(now)
	}
	return State{}, nil
}

func ClaudeQuota(now time.Time) (State, error) {
	ws, err := usage.Read(usage.File())
	if err != nil {
		return State{}, err
	}
	var state State
	for _, w := range ws {
		if w.Used < 100 || w.ResetsAt != 0 && w.ResetsAt <= now.Unix() {
			continue
		}
		if w.ResetsAt == 0 {
			return State{Reason: Quota}, nil
		}
		state.Reason, state.Until = Quota, max(state.Until, w.ResetsAt)
	}
	return state, nil
}

// Hold merges under a per-provider lock. An indefinite hold wins over a timed
// one; disabled wins over both. Lock acquisition is bounded so a hook can
// still stop its worker when persistence fails. No provider diagnostic is saved.
func Hold(p Provider, next State) error {
	if next.Reason == "" {
		return fmt.Errorf("hold requires a reason")
	}
	return write(p, next, false)
}

// Clear repairs malformed provider and usage state too. A valid exhausted
// usage window still blocks Check until its verified reset.
func Clear(p Provider) error {
	if err := write(p, State{}, true); err != nil {
		return err
	}
	if p == Claude {
		return usage.ClearUnknown(usage.File())
	}
	return nil
}

func write(p Provider, next State, clear bool) error {
	if !validProvider(p) {
		return fmt.Errorf("invalid provider")
	}
	if err := next.validate(); err != nil {
		return err
	}
	path := File(p)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	retry := time.NewTicker(10 * time.Millisecond)
	defer retry.Stop()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("availability lock is busy")
		case <-retry.C:
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if !clear {
		old, err := read(p)
		if err != nil {
			return err
		}
		if old.Active(time.Now()) && (old.Reason == Disabled ||
			(next.Reason != Disabled && (old.Until == 0 || next.Until != 0 && old.Until > next.Until))) {
			next = old
		}
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".availability-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
