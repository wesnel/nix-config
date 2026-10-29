package review

import (
	"bytes"
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

type providerUnavailable struct {
	provider shared.Provider
	reason   string
}

func (e providerUnavailable) Error() string { return string(e.provider) + " unavailable: " + e.reason }

// availability is shared by every stage of one invocation. A hold it
// records stays in memory too, so later stages skip the provider even
// where the hold could not be written.
type availability struct {
	blocked map[shared.Provider]held
	now     func() time.Time
}

type held struct {
	state shared.State
	err   error
}

func newAvailability() *availability {
	return &availability{blocked: make(map[shared.Provider]held), now: time.Now}
}

func (a *availability) check(p shared.Provider) error {
	now := a.now()
	if b, ok := a.blocked[p]; ok && b.state.Active(now) {
		return b.err
	}
	if p == shared.Codex && os.Getenv("PR_LOOP_SESSION") != "" {
		return providerUnavailable{p, "this is a review-loop session; its review text must stay out of Codex"}
	}
	s, err := shared.Check(p, now)
	if err != nil {
		return providerUnavailable{p, fmt.Sprintf("cannot read availability state: %v; use `git delegate availability --clear %s` after checking it", err, p)}
	}
	if s.Reason != "" {
		return providerUnavailable{p, s.String() + "; use `git delegate availability --clear " + string(p) + "` after recovery"}
	}
	return nil
}

// failed records that p's call failed for why, shared.Quota or
// shared.RateLimit, and returns the error that says so. A rate limit
// pauses p for shared.RateLimitPause; quota holds p until Clear. Where
// Claude's quota windows show one exhausted, either holds until its reset.
func (a *availability) failed(p shared.Provider, why shared.Reason) error {
	now := a.now()
	s := shared.State{Reason: why}
	if why == shared.RateLimit {
		s.Until = now.Add(shared.RateLimitPause).Unix()
	}
	if p == shared.Claude {
		if observed := shared.ClaudeQuota(now); observed.Reason != "" {
			s = observed
		}
	}
	reason := "failed for " + string(why) + "; " + s.String()
	if err := shared.Hold(p, s, now); err != nil {
		reason += fmt.Sprintf("; could not remember it for later commands: %v; keep this provider disabled in the primary harness", err)
	}
	err := providerUnavailable{p, reason}
	a.blocked[p] = held{s, err}
	return err
}

const availabilityUsage = `usage: git delegate availability [--disable claude|codex | --clear claude|codex]

Prints, as JSON, whether each provider may be called, or why not, and
calls neither. A held provider is not called by any review or ask on this
host until its hold ends: a rate limit's after five minutes, an exhausted
quota's at the reset Claude's quota windows show, and any other only at
--clear. The holds are kept under
$XDG_STATE_HOME/agent-tools/review-availability, with no provider output.

  --disable P  hold P, claude or codex, until --clear
  --clear P    end P's hold, after recovery or a change of account; an
               exhausted Claude quota window still holds until its reset,
               and a review-loop session still keeps Codex out
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
	selected := shared.Provider(*disable + *clear)
	if selected != "" {
		if selected != shared.Claude && selected != shared.Codex {
			fmt.Fprint(stderr, availabilityUsage)
			return 2
		}
		var err error
		if *disable != "" {
			err = shared.Hold(selected, shared.State{Reason: shared.Disabled}, time.Now())
		} else {
			err = shared.Clear(selected)
		}
		if err != nil {
			return cli.Report(name, nil, err, 0, stdout, stderr)
		}
	}
	status := map[shared.Provider]string{}
	a := newAvailability()
	for _, p := range []shared.Provider{shared.Claude, shared.Codex} {
		status[p] = "available"
		if err := a.check(p); err != nil {
			status[p] = err.Error()
		}
	}
	return cli.Report(name, status, nil, 0, stdout, stderr)
}

// failurePrefixes are the starts of the terminal errors the CLIs report for
// an exhausted account (shared.Quota) and for a rate limit
// (shared.RateLimit), lower-cased, with ’ as '. Codex reports a 429 as
// "exceeded retry limit, last status: 429" and an insufficient_quota error
// as "quota exceeded"; Claude Code reports a 429 as "api error:" followed
// by the status.
var failurePrefixes = []struct {
	prefix string
	why    shared.Reason
}{
	{"you've hit your limit", shared.Quota},
	{"you've hit your usage limit", shared.Quota},
	{"you have hit your usage limit", shared.Quota},
	{"you're out of usage credits", shared.Quota},
	{"you're out of extra usage", shared.Quota},
	{"you've hit your channel's monthly spend limit", shared.Quota},
	{"you've hit your monthly spend limit", shared.Quota},
	{"you exceeded your current quota", shared.Quota},
	{"quota exceeded", shared.Quota},
	{"your workspace is out of credits", shared.Quota},
	{"credit balance is too low", shared.Quota},
	{"your credit balance is too low", shared.Quota},
	{"usage_limit_reached", shared.Quota},
	{"rate limit reached", shared.RateLimit},
	{"rate_limit_error", shared.RateLimit},
	{"api error: 429", shared.RateLimit},
	{"api error: request rejected (429)", shared.RateLimit},
	{"exceeded retry limit, last status: 429", shared.RateLimit},
}

// failure is what message says the call failed for: shared.Quota,
// shared.RateLimit, or "" for anything else. Only terminal provider error
// fields reach it, never a successful answer, command output, or the
// echoed review prompt.
func failure(message string) shared.Reason {
	s := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(message, "’", "'")))
	for _, f := range failurePrefixes {
		if strings.HasPrefix(s, f.prefix) {
			return f.why
		}
	}
	return ""
}

// worse is the failure that holds its provider longer: quota over a rate
// limit over none.
func worse(a, b shared.Reason) shared.Reason {
	if a == shared.Quota || b == "" {
		return a
	}
	return b
}

// Codex's --json separates terminal failures from untrusted tool output.
// Keep only bounded partial lines and the classification, not a transcript.
type failureEvents struct {
	w       io.Writer
	line    []byte
	discard bool
	why     shared.Reason
}

func (e *failureEvents) Write(p []byte) (int, error) {
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

func (e *failureEvents) event(data []byte) {
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
	switch v.Type {
	case "turn.failed":
		e.why = worse(e.why, failure(v.Error.Message))
	case "error":
		e.why = worse(e.why, failure(v.Message))
	}
}
