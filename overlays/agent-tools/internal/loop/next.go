package loop

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/signpass"
)

// NextStep is a branch's single next step, as pr-loop next reports it. Its
// type is the branch's state: Done, ToOpen, ToSign, ToPush or Blocked, each
// with Kind naming it in the JSON.
type NextStep interface {
	text() string
}

// Done: nothing to do, for Why; PR is the branch's open pull request, if any.
type Done struct {
	Kind   string `json:"kind"` // "nothing"
	Branch string `json:"branch"`
	Why    string `json:"why"`
	PR     int    `json:"pr,omitempty"`
}

// ToOpen: the session opens the branch's draft pull request with Command,
// once it has written the title and the description's file.
type ToOpen struct {
	Kind     string `json:"kind"` // "open"
	Branch   string `json:"branch"`
	Why      string `json:"why"`
	Command  string `json:"command"`
	OnGitHub bool   `json:"onGitHub"`
}

// ToSign: the operator signs the branch's commits and pushes them with
// Command, whose ID the session mints first with Notify.
type ToSign struct {
	Kind    string `json:"kind"` // "sign"
	Branch  string `json:"branch"`
	Why     string `json:"why"`
	Command string `json:"command"`
	Notify  string `json:"notify"`
	PR      int    `json:"pr,omitempty"`
}

// ToPush: the operator pushes the branch, signed already, with Command; when
// that is a sign-pass, Notify mints its ID first.
type ToPush struct {
	Kind    string `json:"kind"` // "push"
	Branch  string `json:"branch"`
	Why     string `json:"why"`
	Command string `json:"command"`
	Notify  string `json:"notify,omitempty"`
	PR      int    `json:"pr,omitempty"`
}

// Blocked: no step can be taken until what Why says is done.
type Blocked struct {
	Kind   string `json:"kind"` // "blocked"
	Branch string `json:"branch"`
	Why    string `json:"why"`
}

func (s Done) text() string    { return s.Branch + ": nothing: " + s.Why + "\n" }
func (s Blocked) text() string { return s.Branch + ": blocked: " + s.Why + "\n" }
func (s ToOpen) text() string {
	return fmt.Sprintf("%s: open: %s; open its draft pull request:\n  %s\n", s.Branch, s.Why, s.Command)
}
func (s ToSign) text() string {
	return fmt.Sprintf("%s: sign: %s; mint the ID, then hand the operator the pass:\n  %s\n  %s\n", s.Branch, s.Why, s.Notify, s.Command)
}
func (s ToPush) text() string {
	if s.Notify == "" {
		return fmt.Sprintf("%s: push: %s; hand the operator:\n  %s\n", s.Branch, s.Why, s.Command)
	}
	return fmt.Sprintf("%s: push: %s; mint the ID, then hand the operator the pass:\n  %s\n  %s\n", s.Branch, s.Why, s.Notify, s.Command)
}

// FormatNext is steps as pr-loop next prints them without --json.
func FormatNext(steps []NextStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(s.text())
	}
	return b.String()
}

// Next is the single next step for each of branches (default: the current
// branch), from what this repository and GitHub hold now. It only reads, so
// the guard runs it as a read: where the branch starts and what is left to
// sign come from the signing pass (signpass.DefaultBase,
// signpass.RangeStart), where a pass pushes it from signpass.DestinationOf,
// GitHub's branch and pull request from viewRemote, and whether open would
// take it from planOpen, Open's dry run. It runs no signature verifier,
// which writes in its home: a push it proposes is checked by the pass
// (signpass.OperatorSigned), and a branch already on GitHub it opens only
// with every commit signed in the protected operator profile's format
// (operatorFormatSigned), which open proves before it opens. A branch
// GitHub lacks is opened unsigned.
// Policy refusals become the branch's Blocked step rather than an error.
func Next(branches []string) ([]NextStep, error) {
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	if len(branches) == 0 {
		b, err := currentBranch()
		if err != nil {
			return nil, err
		}
		branches = []string{b}
	}
	var steps []NextStep
	for _, b := range branches {
		step, err := next(repo, b)
		var r Refusal
		if errors.As(err, &r) {
			step, err = Blocked{Kind: "blocked", Branch: b, Why: r.Error()}, nil
		}
		if err != nil {
			return nil, err
		}
		steps = append(steps, step)
	}
	return steps, nil
}

// next is branch's step, or the refusal that blocks it.
func next(repo *Repo, branch string) (NextStep, error) {
	tip, err := git.Run("rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil {
		return nil, refuse("no local branch %s; name a branch that `git branch --list` shows", branch)
	}
	base, err := signpass.DefaultBase(branch)
	if err != nil {
		return nil, err
	}
	n, err := git.Run("rev-list", "--count", base+".."+tip)
	if err != nil {
		return nil, err
	}
	empty := n == "0"
	start, _, err := signpass.RangeStart(base, tip)
	if err != nil {
		return nil, err
	}
	unsigned := start != ""
	view, err := viewRemote(repo, branch, repo.Default)
	if err != nil {
		return nil, err
	}
	onGitHub := view.Heads[branch]
	pr := 0
	if view.PRs[branch] != nil {
		// Every step but nothing pushes to the pull request's branch,
		// which the loop does only to the viewer's own.
		p, err := checkBranch(repo, view.PRs, branch)
		if err != nil {
			return nil, err
		}
		pr = p.Number
	}
	if empty && (onGitHub == "" || onGitHub == tip) {
		return Done{Kind: "nothing", Branch: branch, Why: "it has no commits since it left the default branch"}, nil
	}
	if onGitHub == "" || onGitHub == tip && pr == 0 {
		o, err := planOpen(repo, view, branch, repo.Default, operatorFormatSigned)
		if err != nil {
			return nil, err
		}
		why := "GitHub does not have it"
		if o.onGitHub {
			why = "GitHub has it at its tip, with no pull request"
		}
		return ToOpen{Kind: "open", Branch: branch, Why: why, OnGitHub: o.onGitHub,
			Command: "pr-loop open --title TITLE --body-file FILE " + shellQuote(branch)}, nil
	}

	retry := "run `pr-loop next` again"
	dest, err := signpass.DestinationOf(branch, retry)
	if err != nil {
		return nil, err
	}
	if dest.Tracking != onGitHub {
		return nil, refuse("%s/%s is at %.12s here but GitHub has %s at %.12s; run `git fetch %s %s`, then %s",
			dest.Remote, branch, dest.Tracking, branch, onGitHub, shellQuote(dest.Remote), shellQuote(branch), retry)
	}
	if onGitHub != tip && !git.OK("merge-base", "--is-ancestor", onGitHub, tip) {
		return nil, refuse("GitHub's %s, at %.12s, has history the local branch does not contain; fetch with "+
			"`git fetch %s %s`, then reconcile the histories by hand and %s", branch, onGitHub,
			shellQuote(dest.Remote), shellQuote(branch), retry)
	}
	if empty {
		return Done{Kind: "nothing", Branch: branch, Why: "it has no commits since it left the default branch"}, nil
	}
	pass := fmt.Sprintf("git sign-pass %s --notify ID %s", dest.Flag, shellQuote(branch))
	notify := "agent-plan sign-id --branch " + shellQuote(branch)
	switch {
	case unsigned:
		return ToSign{Kind: "sign", Branch: branch, Why: "it has commits to sign", Command: pass, Notify: notify, PR: pr}, nil
	case onGitHub == tip:
		return Done{Kind: "nothing", Branch: branch, Why: fmt.Sprintf("GitHub has its signed tip, in #%d", pr), PR: pr}, nil
	default:
		return ToPush{Kind: "push", Branch: branch, Why: "GitHub is behind its signed tip", Command: pass, Notify: notify, PR: pr}, nil
	}
}
