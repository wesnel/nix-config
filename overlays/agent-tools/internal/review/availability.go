package review

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	shared "github.com/wesnel/agent-tools/internal/availability"
	"github.com/wesnel/agent-tools/internal/cli"
)

type provider = shared.Provider

const (
	claudeProvider = shared.Claude
	codexProvider  = shared.Codex
)

type providerUnavailable struct {
	provider provider
	reason   string
}

func (e providerUnavailable) Error() string { return string(e.provider) + " unavailable: " + e.reason }

// availability is shared by every stage of one invocation. The in-memory
// block survives a failed state write, so later stages still skip the provider.
type availability struct {
	blocked      map[provider]error
	codexChecked bool
}

func newAvailability() *availability { return &availability{blocked: make(map[provider]error)} }

// admit adds one model-free Codex read to the local check. Unknown acquisition
// leaves eligibility unchanged; no result can clear remembered evidence.
func (a *availability) admit(ctx context.Context, p provider) error {
	if err := a.check(p); err != nil {
		return err
	}
	if p == codexProvider && !a.codexChecked {
		a.codexChecked = true
		if codexAllowanceDenied(ctx) {
			return a.exhausted(p)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.check(p)
}

func (a *availability) check(p provider) error {
	if err := a.blocked[p]; err != nil {
		return err
	}
	if p == codexProvider && os.Getenv("PR_LOOP_SESSION") != "" {
		return providerUnavailable{p, "this is a review-loop session; its review text must stay out of Codex"}
	}
	s, err := shared.Check(p, time.Now())
	if err != nil {
		return providerUnavailable{p, fmt.Sprintf("cannot read availability state: %v; use `git delegate availability --clear %s` after checking it", err, p)}
	}
	if s.Reason != "" {
		return providerUnavailable{p, s.String() + "; use `git delegate availability --clear " + string(p) + "` after recovery"}
	}
	return nil
}

func (a *availability) exhausted(p provider) error {
	s := shared.State{Reason: shared.Quota}
	if p == claudeProvider {
		if observed, err := shared.ClaudeQuota(time.Now()); err == nil && observed.Reason != "" {
			s = observed
		}
	}
	reason := "quota exhausted; " + s.String()
	if err := shared.Hold(p, s); err != nil {
		reason += fmt.Sprintf("; could not remember it for later commands: %v; keep this provider disabled in the primary harness", err)
	}
	err := providerUnavailable{p, reason}
	a.blocked[p] = err
	return err
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
		var err error
		if *disable != "" {
			err = shared.Hold(provider(selected), shared.State{Reason: shared.Disabled})
		} else {
			err = shared.Clear(provider(selected))
		}
		if err != nil {
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
