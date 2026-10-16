package plans

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Locate is the plan file that what a session did in a worktree belongs
// to: of this host's plan files that name the worktree, the newest, else
// of those that name the session, the newest. part is the part whose file
// it is, or "" for the plan's own.
func (c Config) Locate(session, worktree string) (p Plan, part string, ok bool) {
	if session == "" && worktree == "" {
		return Plan{}, "", false
	}
	found, err := c.Find(FindOptions{Session: session, Worktree: worktree})
	if err != nil || len(found) == 0 {
		return Plan{}, "", false
	}
	pick := found[0]
	for _, f := range found {
		if slices.Contains(f.Matched, "worktree") {
			pick = f
			break
		}
	}
	p, err = c.Open(pick.Plan)
	return p, pick.Part, err == nil
}

// LocateBranches is Locate, else the newest of this host's plan files
// whose BRANCHES name one of branches, the first that one names.
func (c Config) LocateBranches(session, worktree string, branches []string) (p Plan, part string, ok bool) {
	if p, part, ok = c.Locate(session, worktree); ok {
		return p, part, true
	}
	for _, b := range branches {
		found, err := c.Find(FindOptions{Branch: b})
		if err != nil || len(found) == 0 {
			continue
		}
		p, err = c.Open(found[0].Plan)
		return p, found[0].Part, err == nil
	}
	return Plan{}, "", false
}

// branchChanged is the kind of the event NoteBranch logs.
const branchChanged = "branch-changed"

// NoteBranch logs that session works on branch, or on a detached HEAD at
// tip when branch is "", in worktree, unless its last branch-changed event
// in the plan says so already, and returns that last one, if any, and
// whether it logged. Read and append hold the log's lock, so edits running
// at once log a change once.
func (p Plan) NoteBranch(session, branch, tip, worktree string, now time.Time) (last Event, had, logged bool, err error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, false, false, err
	}
	err = p.withLock(func(l *lockedPlan) error {
		events, err := p.Events()
		if err != nil {
			return err
		}
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Kind == branchChanged && events[i].Session == session {
				last, had = events[i], true
				break
			}
		}
		if had && last.Branch == branch && last.Tip == tip && last.Worktree == worktree {
			return nil
		}
		summary := branch
		if branch == "" {
			summary = "detached at " + tip
		}
		_, err = l.log(Event{Kind: branchChanged, Item: p.Name, Session: session, Branch: branch, Tip: tip,
			Worktree: worktree, Summary: summary + " in " + worktree}, now)
		logged = err == nil
		return err
	})
	return last, had, logged, err
}

// usageAsked is the kind of the event RecordUsageAsk logs.
const usageAsked = "usage-asked"

// RecordUsageAsk logs ask, the quota ask a prompt hook's input showed its
// session, as usage-asked in the plan Locate finds for the session and
// its worktree, its first sentence as the summary; with no plan, nothing.
// Which session is asked, once per window and threshold on the host, is
// the usage package's.
func (c Config) RecordUsageAsk(input []byte, ask string, now time.Time) error {
	var in struct {
		SessionID string `json:"session_id"`
		ChatID    string `json:"chat_id"`
		Cwd       string `json:"cwd"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return err
	}
	session := in.SessionID
	if in.ChatID != "" {
		session = in.ChatID
	}
	top, _ := checkout(in.Cwd)
	p, part, ok := c.Locate(session, top)
	if !ok {
		return nil
	}
	summary, _, _ := strings.Cut(ask, ". ")
	_, err := p.Record(part, Event{Kind: usageAsked, Session: session, Summary: strings.TrimSuffix(summary, ".")}, now)
	return err
}

// EnvSession is the session of the harness running this process, as its
// environment names it: Claude Code's, Codex's or ECA's; or "".
func EnvSession() string {
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "ECA_CHAT_ID"} {
		if id := os.Getenv(v); id != "" {
			return id
		}
	}
	return ""
}

// Receipt is what a review that ran leaves in the plan's log, for
// StopCheck to compare with the change as it then stands.
type Receipt struct {
	// Range is the range reviewed as it was given, BASE..TIP, so that
	// its base is read again at the stop, wherever it has moved.
	Range string
	// Tip is the commit TIP named.
	Tip string
	// PatchID is git.PatchID of the range's change.
	PatchID string
	// Reviewer is codex or subagent, and Model the model, where known.
	Reviewer, Model string
	Findings        int
	// Raised are the IDs of the findings the review raised itself, with
	// no reviewer's judgement, which a rebuttal must name a round of.
	Raised []string
	// Focus is a focused review's route, which makes the receipt a route
	// receipt; "" for the general review. Branch is the branch checked out
	// where the review ran, which a route receipt's range is read again
	// at after a rewrite.
	Focus, Branch string
	// FindingIDs are a route receipt's findings, which the gates want
	// settled.
	FindingIDs []string
	// Nonce is the issued review the reply answered, for a check of one.
	Nonce string
}

// ReceiptKind is the kind of the event Receipt logs for a general review.
const ReceiptKind = receiptKind

const receiptKind = "review-receipt"

// Receipt logs r about part, or about the plan itself when part is "": a
// review receipt, or a route receipt where r has a focus. stop-check's
// general review counts review receipts alone, so a focused review never
// stands in for it.
func (p Plan) Receipt(part string, r Receipt, now time.Time) (Event, error) {
	e := Event{Kind: receiptKind, Range: r.Range, Tip: r.Tip, PatchID: r.PatchID, Agent: r.Reviewer,
		Model: r.Model, Summary: fmt.Sprintf("%s: %d findings", r.Range, r.Findings), Raised: strings.Join(r.Raised, " "),
		Nonce: r.Nonce}
	if r.Focus != "" {
		e.Kind, e.Focus, e.Branch, e.Findings = routeReceiptKind, r.Focus, r.Branch, strings.Join(r.FindingIDs, " ")
		e.Summary = r.Focus + " " + e.Summary
	}
	return p.Record(part, e, now)
}

// WriteReceipt logs r in the plan Locate finds for session and the
// worktree at top, and returns that plan's file, or "" where no plan names
// either.
func WriteReceipt(session, top string, r Receipt, now time.Time) (string, error) {
	c, err := Load()
	if err != nil {
		return "", err
	}
	p, part, ok := c.Locate(session, top)
	if !ok {
		return "", nil
	}
	if _, err := p.Receipt(part, r, now); err != nil {
		return "", err
	}
	return p.File(), nil
}

// RecordCommit logs e as a commit session made in the worktree at top, in
// the plan Locate finds for them. Where there is none, it first makes one:
// a plan of the repository's unplanned work, working under the full
// pipeline, that names the session and the worktree. It returns the plan
// and whether it made it. Calls running at once make one plan between
// them.
func (c Config) RecordCommit(session, top string, e Event, now time.Time) (Plan, bool, error) {
	e.Kind, e.Session = "committed", session
	p, part, ok := c.Locate(session, top)
	created := false
	if !ok {
		if top == "" {
			return Plan{}, false, refusal.New("a commit outside a worktree has no plan to go in. Run the commit inside the repository")
		}
		var err error
		if p, part, created, err = c.unplanned(session, top, now); err != nil {
			return Plan{}, false, err
		}
	}
	_, err := p.Record(part, e, now)
	return p, created, err
}

// unplanned is the plan Locate finds for session and top once this host's
// plans directory is locked, or else a new one for them, and whether it is
// new.
func (c Config) unplanned(session, top string, now time.Time) (Plan, string, bool, error) {
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return Plan{}, "", false, err
	}
	lock, err := os.OpenFile(filepath.Join(c.Dir, ".new.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return Plan{}, "", false, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return Plan{}, "", false, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if p, part, ok := c.Locate(session, top); ok {
		return p, part, false, nil
	}
	name := repoName(top)
	o := NewOptions{Title: "Unplanned work in " + name, Slug: slug("unplanned " + name), Repos: []string{name},
		Worktrees: []string{top}, Goal: "Unplanned work in " + name + ".", Status: "working"}
	if checkWords("session", []string{session}) == nil {
		o.Sessions = []string{session}
	}
	made, err := c.New(o, now)
	if err != nil {
		return Plan{}, "", false, err
	}
	p, err := c.Open(made.Plan)
	return p, "", err == nil, err
}

// repoName is the GitHub owner/name origin of the worktree at top fetches
// from, else the name of the directory its repository is in.
func repoName(top string) string {
	if url, err := git.Run("-C", top, "remote", "get-url", "origin"); err == nil {
		if s := gh.Slug(url); s != "" {
			return s
		}
	}
	common, err := git.Run("-C", top, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return filepath.Base(top)
	}
	if filepath.Base(common) == ".git" {
		common = filepath.Dir(common)
	}
	return strings.TrimSuffix(filepath.Base(common), ".git")
}

// Record logs e about part, or about the plan itself when part is "", as
// one of agent-tools' own commands or hooks: unlike Log, it takes the
// kinds only they log.
func (p Plan) Record(part string, e Event, now time.Time) (Event, error) {
	e.Item = part
	if part == "" {
		e.Item = p.Name
	}
	return p.record(e, now)
}

// AddToProperty adds value to the list property name of the `* Plan`
// heading in the plan's file, or the part's, unless it is there, logs the
// property as tracked properties are, and reports whether it added it.
func (p Plan) AddToProperty(part, name, value string, now time.Time) (bool, error) {
	if err := p.checkOwn(); err != nil {
		return false, err
	}
	if err := checkWords(strings.ToLower(name), []string{value}); err != nil {
		return false, err
	}
	added := false
	err := p.withLock(func(l *lockedPlan) error {
		return l.setPlanProperty(part, name, func(values []string) []string {
			added = !slices.Contains(values, value)
			if !added {
				return values
			}
			return append(values, value)
		}, now)
	})
	return added, err
}
