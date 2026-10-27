package review

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/usage"
)

type provider string

const (
	claudeProvider provider = "claude"
	codexProvider  provider = "codex"
)

// Provider state holds no prompts or diagnostic text. A zero reset needs
// an explicit clear; guessing a cooldown would probe an exhausted account.
type providerState struct {
	Reason string `json:"reason,omitempty"`
	Until  int64  `json:"until,omitempty"`
}

type providerUnavailable struct {
	provider provider
	reason   string
}

func (e providerUnavailable) Error() string { return string(e.provider) + " unavailable: " + e.reason }

// availability is shared by every stage of one invocation. The in-memory
// block survives a failed state write, so later stages still skip the provider.
type availability struct{ blocked map[provider]error }

func newAvailability() *availability { return &availability{blocked: make(map[provider]error)} }

func providerFile(p provider) string {
	return filepath.Join(hostpaths.StateHome(), "agent-tools", "review-availability", string(p)+".json")
}

func readProvider(p provider) (providerState, error) {
	var state providerState
	data, err := os.ReadFile(providerFile(p))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Until < 0 || (state.Reason != "" && state.Reason != "quota" && state.Reason != "disabled") ||
		(state.Reason != "quota" && state.Until != 0) {
		return state, fmt.Errorf("invalid availability state")
	}
	return state, nil
}

func (s providerState) active(now time.Time) bool {
	return s.Reason != "" && (s.Until == 0 || s.Until > now.Unix())
}

func claudeReset(now time.Time) int64 {
	var until int64
	for _, w := range usage.Load(usage.File()) {
		if w.Used >= 100 && w.ResetsAt > now.Unix() && w.ResetsAt > until {
			until = w.ResetsAt
		}
	}
	return until
}

func (a *availability) check(p provider) error {
	if err := a.blocked[p]; err != nil {
		return err
	}
	if p == codexProvider && os.Getenv("PR_LOOP_SESSION") != "" {
		return providerUnavailable{p, "this is a review-loop session; its review text must stay out of Codex"}
	}
	s, err := readProvider(p)
	if err != nil {
		return providerUnavailable{p, fmt.Sprintf("cannot read availability state: %v; use `git delegate availability --clear %s` after checking it", err, p)}
	}
	now := time.Now()
	if s.active(now) {
		reason := s.Reason + "; use `git delegate availability --clear " + string(p) + "` after recovery"
		if s.Until != 0 {
			reason = fmt.Sprintf("quota until %s", time.Unix(s.Until, 0).Format(time.RFC3339))
		}
		return providerUnavailable{p, reason}
	}
	if p == claudeProvider {
		if until := claudeReset(now); until != 0 {
			return providerUnavailable{p, "quota until " + time.Unix(until, 0).Format(time.RFC3339)}
		}
	}
	return nil
}

func (a *availability) exhausted(p provider) error {
	s := providerState{Reason: "quota"}
	if p == claudeProvider {
		s.Until = claudeReset(time.Now())
	}
	reason := "quota exhausted; use `git delegate availability --clear " + string(p) + "` after recovery"
	if s.Until != 0 {
		reason = "quota exhausted until " + time.Unix(s.Until, 0).Format(time.RFC3339)
	}
	if err := writeProvider(p, s, false); err != nil {
		reason += fmt.Sprintf("; could not remember it for later commands: %v; keep this provider disabled in the primary harness", err)
	}
	err := providerUnavailable{p, reason}
	a.blocked[p] = err
	return err
}

// A provider is one shared account. Serialize its read/merge/replace, not
// the model call; attempts admitted before an exhaustion report may finish.
func writeProvider(p provider, next providerState, clear bool) error {
	path := providerFile(p)
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
		old, err := readProvider(p)
		if err != nil {
			return err
		}
		if old.active(time.Now()) && (old.Reason == "disabled" ||
			(next.Reason != "disabled" && (old.Until == 0 || next.Until != 0 && old.Until > next.Until))) {
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

const availabilityUsage = `usage: git delegate availability [--disable claude|codex | --clear claude|codex]

Show remembered reviewer availability without starting either provider.
--disable records an operator-known unavailable provider before any review.
--clear permits another attempt after recovery or an account change. It does
not override a still-exhausted Claude quota window or review-loop policy.
Quota with no verified reset stays blocked until cleared. State is shared
across repositories under $XDG_STATE_HOME/agent-tools/review-availability.
`

func availabilityCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" availability", flag.ContinueOnError)
	disable := flags.String("disable", "", "")
	clear := flags.String("clear", "", "")
	if code, ok := cli.Parse(flags, args, availabilityUsage, stdout, stderr); !ok {
		return code
	}
	if flags.NArg() != 0 || (*disable != "" && *clear != "") {
		fmt.Fprint(stderr, availabilityUsage)
		return 2
	}
	selected := *disable
	if *clear != "" {
		selected = *clear
	}
	if selected != "" {
		if selected != string(claudeProvider) && selected != string(codexProvider) {
			fmt.Fprint(stderr, availabilityUsage)
			return 2
		}
		s := providerState{}
		if *disable != "" {
			s.Reason = "disabled"
		}
		if err := writeProvider(provider(selected), s, *clear != ""); err != nil {
			return cli.Report(name, nil, err, 0, stdout, stderr)
		}
	}
	status := map[provider]string{}
	a := newAvailability()
	for _, p := range []provider{claudeProvider, codexProvider} {
		status[p] = "available"
		if err := a.check(p); err != nil {
			status[p] = err.Error()
		}
	}
	return cli.Report(name, status, nil, 0, stdout, stderr)
}

// Only terminal provider error fields reach this classifier, never a
// successful answer, command output, or the echoed review prompt.
func quotaMessage(message string) bool {
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(message, "’", "'")))
	for _, prefix := range []string{"you've hit your limit", "you've hit your usage limit", "you have hit your usage limit",
		"you're out of usage credits", "you're out of extra usage", "you've hit your channel's monthly spend limit",
		"you've hit your monthly spend limit",
		"you exceeded your current quota", "your workspace is out of credits", "credit balance is too low",
		"your credit balance is too low", "rate limit reached", "rate_limit_error", "usage_limit_reached", "api error: 429"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// Codex's --json separates terminal failures from untrusted tool output.
// Keep only bounded partial lines and the classification, not a transcript.
type quotaEvents struct {
	w       io.Writer
	line    []byte
	discard bool
	quota   bool
}

func (e *quotaEvents) Write(p []byte) (int, error) {
	for _, c := range p {
		if c == '\n' {
			if !e.discard {
				e.event(e.line)
			}
			e.line, e.discard = e.line[:0], false
		} else if len(e.line) < 1<<20 && !e.discard {
			e.line = append(e.line, c)
		} else {
			e.discard = true
		}
	}
	return e.w.Write(p)
}

func (e *quotaEvents) event(data []byte) {
	var v struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Error   struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(data), &v) != nil {
		return
	}
	if v.Type == "turn.failed" {
		e.quota = e.quota || quotaMessage(v.Error.Message)
	}
	if v.Type == "error" {
		e.quota = e.quota || quotaMessage(v.Message)
	}
}
