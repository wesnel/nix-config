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
// heading in the plan's file, or the part's, unless it is there, and
// reports whether it added it.
func (p Plan) AddToProperty(part, name, value string) (bool, error) {
	if err := p.checkOwn(); err != nil {
		return false, err
	}
	if err := checkWords(strings.ToLower(name), []string{value}); err != nil {
		return false, err
	}
	added := false
	err := p.editPlanProperty(p.file(part), name, func(values []string) []string {
		added = !slices.Contains(values, value)
		if !added {
			return values
		}
		return append(values, value)
	})
	return added, err
}

// editPlanProperty sets the list property name of file's `* Plan` heading
// to what edit makes of its values, adding the property at the end of the
// drawer if it has none. edit may run more than once, each time on the
// values the file then holds.
func (p Plan) editPlanProperty(file, name string, edit func(values []string) []string) error {
	return p.withLock(func(l *lockedPlan) error {
		return l.edit(file, func(lines []string) ([]string, error) {
			lines = slices.Clone(lines)
			start := slices.Index(lines, "* Plan")
			if start < 0 {
				return nil, refusal.New("%s has no `* Plan` heading. Restore it as the plan-work skill describes, then try again", file)
			}
			at, end := -1, -1
			var values []string
			for i := start + 1; i < len(lines) && end < 0; i++ {
				switch {
				case lines[i] == ":END:":
					end = i
				case strings.HasPrefix(lines[i], ":"+name+":"):
					at = i
					values = strings.Fields(strings.TrimPrefix(lines[i], ":"+name+":"))
				}
			}
			if end < 0 {
				return nil, refusal.New("%s's `* Plan` heading has no property drawer. Restore it as the plan-work skill describes, then try again", file)
			}
			line := ":" + name + ":"
			if values = edit(values); len(values) > 0 {
				line += " " + strings.Join(values, " ")
			}
			if at >= 0 {
				lines[at] = line
				return lines, nil
			}
			return slices.Insert(lines, end, line), nil
		})
	})
}
