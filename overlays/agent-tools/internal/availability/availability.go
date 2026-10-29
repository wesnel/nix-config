// Package availability keeps this host's knowledge of whether each model
// provider's account can take a call: a hold recorded after a failure or by
// the operator, and Claude's exhausted quota windows. It holds no prompt and
// no provider diagnostic. Calls already started when another caller records
// a hold may finish.
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

// Provider is a model provider account: Claude or Codex.
type Provider string

const (
	Claude Provider = "claude"
	Codex  Provider = "codex"
)

// Reason is why a provider is held.
type Reason string

const (
	// Quota is an exhausted account. Its Until is the verified reset, or
	// zero where none is known, which holds until Clear.
	Quota Reason = "quota"
	// Disabled is the operator's hold, until Clear.
	Disabled Reason = "disabled"
	// RateLimit is a 429 or rate-limit error: a pause of RateLimitPause,
	// always with an Until.
	RateLimit Reason = "rate_limit"
)

// RateLimitPause is how long a rate limit holds its provider. Request and
// token rate limits refill within a minute, and both CLIs have retried
// before they report one; five minutes keeps the rest of a run off the
// provider, and lets the next review round, minutes later, try again.
const RateLimitPause = 5 * time.Minute

// State is a provider's hold, as File stores it; the zero State is none.
// Until is a Unix time; zero means until Clear.
type State struct {
	Reason Reason `json:"reason,omitempty"`
	Until  int64  `json:"until,omitempty"`
}

// Active is whether s holds the provider at now.
func (s State) Active(now time.Time) bool {
	return s.Reason != "" && (s.Until == 0 || s.Until > now.Unix())
}

func (s State) String() string {
	if s.Until != 0 {
		return string(s.Reason) + " until " + time.Unix(s.Until, 0).Format(time.RFC3339)
	}
	return string(s.Reason) + "; explicit clear required after recovery"
}

func (s State) validate() error {
	switch {
	case s.Until < 0,
		s.Reason != "" && s.Reason != Quota && s.Reason != Disabled && s.Reason != RateLimit,
		(s.Reason == "" || s.Reason == Disabled) && s.Until != 0,
		s.Reason == RateLimit && s.Until == 0:
		return fmt.Errorf("invalid availability state")
	}
	return nil
}

func validProvider(p Provider) bool { return p == Claude || p == Codex }

// File is where p's hold is kept, shared by every repository on this host.
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
	for key, value := range raw {
		if key != "reason" && key != "until" || string(value) == "null" {
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

// Check is p's hold at now, or the zero State where it may be called. An
// unreadable or invalid File is an error, which callers treat as a hold.
func Check(p Provider, now time.Time) (State, error) {
	if !validProvider(p) {
		return State{}, fmt.Errorf("invalid provider")
	}
	s, err := read(p)
	if err != nil || s.Active(now) {
		return s, err
	}
	if p == Claude {
		return ClaudeQuota(now), nil
	}
	return State{}, nil
}

// ClaudeQuota is a Quota hold until the latest reset of the Claude quota
// windows usage.File records as exhausted at now, or the zero State.
func ClaudeQuota(now time.Time) State {
	var state State
	for _, w := range usage.Load(usage.File()) {
		if w.Used >= 100 && w.ResetsAt > now.Unix() {
			state.Reason, state.Until = Quota, max(state.Until, w.ResetsAt)
		}
	}
	return state
}

// Hold records next for p, merged with the hold File has at now under a
// lock on p: Disabled outlasts every other hold, and otherwise the hold
// that ends later stands.
func Hold(p Provider, next State, now time.Time) error {
	if next.Reason == "" {
		return fmt.Errorf("hold requires a reason")
	}
	return write(p, next, false, now)
}

// Clear removes p's hold, an invalid File included. It cannot clear an
// exhausted Claude window, which holds until its reset.
func Clear(p Provider) error {
	return write(p, State{}, true, time.Now())
}

func write(p Provider, next State, clear bool, now time.Time) error {
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
	if !clear {
		old, err := read(p)
		if err != nil {
			return err
		}
		if old.Active(now) && (old.Reason == Disabled ||
			next.Reason != Disabled && (old.Until == 0 || next.Until != 0 && old.Until > next.Until)) {
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
