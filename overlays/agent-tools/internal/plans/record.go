package plans

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Locate is the plan file that what a session did in a worktree belongs
// to. The session picks the plan: of this host's plans, the newest whose
// files name both the session and the worktree, else the newest naming the
// session, else the newest naming the worktree. Only a linked worktree
// counts: a repository's main checkout is named by many plans, so it picks
// neither a plan nor a file within one, and a session no plan names gets
// no plan there. A linked worktree never outranks the session either. The
// worktree picks the file within the plan: a part naming the worktree,
// else the plan's own file naming it, else its own file naming the
// session, else its first part naming the session. A part's SESSIONS may
// not name the session (a part made without one, or after a /clear), so a
// commit in a part's worktree lands on the part all the same. part is the
// part whose file it is, or "" for the plan's own.
func (c Config) Locate(session, worktree string) (p Plan, part string, ok bool) {
	if !linkedWorktree(worktree) {
		worktree = ""
	}
	if session == "" && worktree == "" {
		return Plan{}, "", false
	}
	found, err := c.Find(FindOptions{Session: session, Worktree: worktree})
	if err != nil || len(found) == 0 {
		return Plan{}, "", false
	}
	type named struct{ session, worktree bool }
	plans := map[string]*named{}
	var order []string
	for _, f := range found {
		n := plans[f.Plan]
		if n == nil {
			n = &named{}
			plans[f.Plan] = n
			order = append(order, f.Plan)
		}
		n.session = n.session || slices.Contains(f.Matched, "session")
		n.worktree = n.worktree || slices.Contains(f.Matched, "worktree")
	}
	planRank := func(n *named) int {
		switch {
		case n.session && n.worktree:
			return 0
		case n.session:
			return 1
		}
		return 2
	}
	plan := order[0]
	for _, name := range order[1:] {
		if planRank(plans[name]) < planRank(plans[plan]) {
			plan = name
		}
	}
	fileRank := func(f Found) int {
		inWorktree := slices.Contains(f.Matched, "worktree")
		switch {
		case inWorktree && f.Part != "":
			return 0
		case inWorktree:
			return 1
		case f.Part == "":
			return 2
		}
		return 3
	}
	var pick Found
	best := 4
	for _, f := range found {
		if r := fileRank(f); f.Plan == plan && r < best {
			pick, best = f, r
		}
	}
	p, err = c.Open(pick.Plan)
	return p, pick.Part, err == nil
}

// linkedWorktree is whether top is a linked worktree of its repository,
// whose git directory differs from the repository's common one; false for
// the main checkout, for "" and for what git cannot read.
func linkedWorktree(top string) bool {
	if top == "" {
		return false
	}
	out, err := git.Run("-C", top, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	dirs := strings.Split(out, "\n")
	return err == nil && len(dirs) == 2 && dirs[0] != dirs[1]
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

// EnvSession returns the harness and its raw session ID from the environment,
// or two empty strings outside Claude Code and Codex.
func EnvSession() (harness, id string) {
	for _, env := range []struct{ harness, name string }{{"claude", "CLAUDE_CODE_SESSION_ID"}, {"codex", "CODEX_THREAD_ID"}} {
		if id := os.Getenv(env.name); id != "" {
			return env.harness, id
		}
	}
	return "", ""
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
	if err := os.MkdirAll(c.Dir, 0o755); err != nil { //nolint:gosec // the plans: notes on the work, synced through git
		return Plan{}, "", false, err
	}
	lock, err := os.OpenFile(filepath.Join(c.Dir, ".new.lock"), os.O_RDWR|os.O_CREATE, 0o644) //nolint:gosec // an empty lock file
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

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(title string) string {
	s := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" {
		return "plan"
	}
	return s
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
		}, "", now)
	})
	return added, err
}
