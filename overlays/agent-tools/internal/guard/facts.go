package guard

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// The facts a successful shell call leaves for the plan of the session or
// worktree it ran in, recorded by the PostToolUse hook rather than the
// agent: each commit and push in the plan's log, and each new worktree,
// branch and pull request in its properties. Each fact is read from what
// git or pr-loop printed and checked against the repository, so a failed
// command, or one that only prints the same words, records nothing; a
// commit git printed nothing for is read from the reflog (commitsMade).
// A commit some plan's log holds already is not logged again.

// gitCall is a git command a command line runs: the directory it runs in,
// "" when the hook cannot tell, its subcommand and the words after it.
type gitCall struct {
	dir, sub string
	args     []string
}

// gitCalls are the git commands text runs, started in dir.
func gitCalls(text, dir string, depth int) []gitCall {
	if depth > maxDepth {
		return nil
	}
	var out []gitCall
	cwd := dir
	v := visitor{
		script: func(text string) string {
			out = append(out, gitCalls(text, cwd, depth+1)...)
			return ""
		},
		call: func(c call) string {
			c = c.named()
			w := c.words
			switch {
			case len(w) == 0 || c.expands[0]:
			case w[0] == "cd" || w[0] == "pushd":
				cwd = cdTarget(c, cwd)
			case base(w[0]) == "git":
				where, i := cwd, 1
				for i < len(w) && strings.HasPrefix(w[i], "-") {
					switch {
					case w[i] == "-C" && i+1 < len(w):
						if where = expandPath(w[i+1], where); !filepath.IsAbs(where) {
							where = ""
						}
					case w[i] == "--git-dir" || w[i] == "--work-tree" || strings.HasPrefix(w[i], "--git-dir=") || strings.HasPrefix(w[i], "--work-tree="):
						where = ""
					}
					if gitValueFlags[w[i]] {
						i++
					}
					i++
				}
				if i < len(w) && where != "" {
					out = append(out, gitCall{dir: where, sub: w[i], args: w[i+1:]})
				}
			}
			return ""
		},
	}
	for _, segment := range segments(text) {
		if words, expands := split(segment); len(words) > 0 {
			unwrap(call{words: words, expands: expands}, v)
		}
	}
	return out
}

// toplevel is the top level of the worktree dir is in, free of symlinks,
// or "".
func toplevel(dir string) string {
	top, ok := gitState{dir: dir}.run("rev-parse", "--show-toplevel")
	if !ok {
		return ""
	}
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	return top
}

// summaryLine is the line git commit prints for the commit it made.
var summaryLine = regexp.MustCompile(`(?m)^\[(.+?)(?: \(root-commit\))? ([0-9a-f]{7,40})\] (.+?)\s*$`)

// quietAge is how old a commit HEAD's reflog records may be and still count
// as one a call made without printing its summary line, as git commit -q
// does. The hook reads the reflog only once the call ends, so this is the
// longest a foreground Bash call runs (Claude Code's limit is ten
// minutes): any shorter, and a commit followed by a long test run is lost.
const quietAge = 10 * time.Minute

// madeCommit is a commit a call made: the worktree it was made in, its
// full ID, the branch git named for it ("" when unknown), its subject,
// whether git printed its summary line, and, for one it did not, when the
// reflog says it was made.
type madeCommit struct {
	top, sha, branch, subject string
	printed                   bool
	at                        time.Time
}

// commitsMade are the commits the git commit calls among calls made, each
// once, oldest first in each worktree. They are read from HEAD's reflog in
// each call's worktree: its newest commit entries, at most as many as the
// calls that commit there, each counted when output holds its summary
// line, or when it is at most quietAge older than now. A summary line no
// reflog entry accounts for, as where the reflog is off, counts in the
// first of those worktrees that has its commit.
func commitsMade(calls []gitCall, output string, now time.Time) []madeCommit {
	printed := summaryLine.FindAllStringSubmatch(output, -1)
	var tops []string
	runs := map[string]int{}
	for _, g := range calls {
		if g.sub != "commit" {
			continue
		}
		top := toplevel(g.dir)
		if top == "" {
			continue
		}
		if runs[top] == 0 {
			tops = append(tops, top)
		}
		runs[top]++
	}
	var out []madeCommit
	seen := map[string]bool{}
	for _, top := range tops {
		state := gitState{dir: top}
		entries, _ := state.run("log", "-g", "--no-show-signature", "--grep-reflog=^commit", "--date=unix",
			"--format=%H%x09%gd%x09%s", "-n", strconv.Itoa(runs[top]), "HEAD")
		var made []madeCommit
		for _, entry := range strings.Split(entries, "\n") {
			sha, rest, _ := strings.Cut(entry, "\t")
			selector, subject, _ := strings.Cut(rest, "\t")
			if sha == "" || seen[sha] {
				continue
			}
			m := madeCommit{top: top, sha: sha, subject: subject}
			if i := slices.IndexFunc(printed, func(s []string) bool { return strings.HasPrefix(sha, s[2]) }); i >= 0 {
				m.branch, m.subject, m.printed = printed[i][1], printed[i][3], true
			} else if at, ok := reflogTime(selector); !ok || now.Sub(at) > quietAge {
				continue
			} else {
				m.at = at
				if _, ok := state.run("merge-base", "--is-ancestor", sha, "HEAD"); ok {
					m.branch, _ = state.run("branch", "--show-current")
				}
			}
			seen[sha] = true
			made = append(made, m)
		}
		slices.Reverse(made)
		out = append(out, made...)
	}
	for _, s := range printed {
		if slices.ContainsFunc(out, func(m madeCommit) bool { return strings.HasPrefix(m.sha, s[2]) }) {
			continue
		}
		for _, top := range tops {
			if sha, ok := (gitState{dir: top}).run("rev-parse", "--verify", "-q", s[2]+"^{commit}"); ok {
				out = append(out, madeCommit{top: top, sha: sha, branch: s[1], subject: s[3], printed: true})
				break
			}
		}
	}
	return out
}

// loggedQuiet reports whether the plan Locate finds for session and m's
// worktree already logs m, a commit git printed nothing for and that has
// no GitHub identity, so no revision to compare: whether it holds a
// committed event of m's worktree and subject, logged no earlier than the
// minute m was made. Two such commits of one subject in one minute count
// as one.
func loggedQuiet(c plans.Config, session string, m madeCommit) bool {
	p, _, ok := c.Locate(session, m.top)
	if !ok {
		return false
	}
	events, err := p.Events()
	if err != nil {
		return false
	}
	made := m.at.Truncate(time.Minute)
	for _, e := range events {
		if e.Kind != "committed" || e.Worktree != m.top || e.Commit != m.subject {
			continue
		}
		if at, err := time.ParseInLocation("[2006-01-02 Mon 15:04]", e.Time, m.at.Location()); err == nil && !at.Before(made) {
			return true
		}
	}
	return false
}

// reflogTime is the time of a reflog selector git prints with
// --date=unix, such as HEAD@{1793832295}.
func reflogTime(selector string) (time.Time, bool) {
	_, stamp, _ := strings.Cut(selector, "@{")
	seconds, err := strconv.ParseInt(strings.TrimSuffix(stamp, "}"), 10, 64)
	return time.Unix(seconds, 0), err == nil
}

// newBranch is the branch a branch-making call made, and the start point
// it names, if any.
func newBranch(g gitCall) (name, start string, switched bool) {
	positional := func(args []string, values map[string]bool) []string {
		var out []string
		for i := 0; i < len(args); i++ {
			switch a := args[i]; {
			case a == "--":
				return append(out, args[i+1:]...)
			case strings.HasPrefix(a, "-"):
				if values[a] {
					i++
				}
			default:
				out = append(out, a)
			}
		}
		return out
	}
	valueOf := func(names ...string) string {
		for i, a := range g.args {
			for _, n := range names {
				if a == n && i+1 < len(g.args) {
					return g.args[i+1]
				}
				if strings.HasPrefix(n, "--") && strings.HasPrefix(a, n+"=") {
					return strings.TrimPrefix(a, n+"=")
				}
			}
		}
		return ""
	}
	switch g.sub {
	case "switch", "checkout":
		flags := []string{"-c", "-C", "--create", "--force-create"}
		if g.sub == "checkout" {
			flags = []string{"-b", "-B"}
		}
		if name = valueOf(flags...); name == "" {
			return "", "", false
		}
		rest := positional(g.args, set(append(flags, "--orphan", "--conflict")...))
		if len(rest) > 0 {
			start = rest[0]
		}
		return name, start, true
	case "branch":
		for _, a := range g.args {
			if strings.HasPrefix(a, "-") && a != "--track" && a != "--no-track" {
				return "", "", false
			}
		}
		rest := positional(g.args, nil)
		if len(rest) == 0 || len(rest) > 2 {
			return "", "", false
		}
		if len(rest) == 2 {
			start = rest[1]
		}
		return rest[0], start, false
	case "worktree":
		if len(g.args) == 0 || g.args[0] != "add" {
			return "", "", false
		}
		args := g.args[1:]
		if name = valueOf("-b", "-B"); name == "" {
			return "", "", false
		}
		rest := positional(args, set("-b", "-B", "--reason"))
		if len(rest) > 1 {
			start = rest[1]
		}
		return name, start, false
	}
	return "", "", false
}

// newWorktree is the worktree a `git worktree add` made, or "".
func newWorktree(g gitCall) string {
	if g.sub != "worktree" || len(g.args) < 2 || g.args[0] != "add" {
		return ""
	}
	values := set("-b", "-B", "--reason")
	for i := 1; i < len(g.args); i++ {
		switch a := g.args[i]; {
		case a == "--":
			if i+1 < len(g.args) {
				return expandPath(g.args[i+1], g.dir)
			}
			return ""
		case strings.HasPrefix(a, "-"):
			if values[a] {
				i++
			}
		default:
			return expandPath(a, g.dir)
		}
	}
	return ""
}

// recordFacts records in the matching plan what the shell call command,
// run in cwd by session, did, by its output texts, and returns a line of
// context for each commit recorded, asking which step it completes, which
// is the agent's judgement. A commit no plan takes makes a plan for it
// (plans.Config.RecordCommit). A commit, branch or pull request recorded
// links the plan to the plans that hard evidence relates it to
// (plans.Config.LinkEvidence). Every failure leaves the plan as it was.
func recordFacts(session, cwd, command string, texts []string, now time.Time) (context []string) {
	c, err := plans.Load()
	if err != nil {
		return nil
	}
	at := func(dir string) (plans.Plan, string, bool) {
		top := ""
		if dir != "" {
			top = toplevel(dir)
		}
		return c.Locate(session, top)
	}
	output := strings.Join(texts, "\n")
	calls := gitCalls(command, cwd, 0)
	// logged are the revisions some plan's log holds, by repository.
	logged := map[string]map[string]bool{}
	for _, m := range commitsMade(calls, output, now) {
		branch := m.branch
		if strings.ContainsAny(branch, " \t") {
			branch = ""
		}
		who := session
		if strings.ContainsAny(who, " \t\r\n") {
			who = ""
		}
		e := plans.Event{Branch: branch, Commit: m.subject, Summary: m.subject, Worktree: m.top}
		if identity, err := plans.CommitIdentity(m.top, m.sha); err == nil {
			e.Repository, e.Revision = identity.Repository, identity.Revision
			if logged[e.Repository] == nil {
				revisions := map[string]bool{}
				found, err := c.Commits(plans.CommitOptions{Repository: e.Repository})
				for _, a := range found {
					revisions[a.OriginalRevision], revisions[a.Revision] = true, true
				}
				// Where the logs cannot be read, only a commit git
				// printed counts.
				if err != nil {
					revisions = nil
				}
				logged[e.Repository] = revisions
			}
			if revisions := logged[e.Repository]; revisions[e.Revision] || revisions == nil && !m.printed {
				continue
			}
			if revisions := logged[e.Repository]; revisions != nil {
				revisions[e.Revision] = true
			}
		} else if !m.printed && loggedQuiet(c, who, m) {
			continue
		}
		p, created, err := c.RecordCommit(who, m.top, e, now)
		if err != nil {
			continue
		}
		c.LinkEvidence(p.File(), now)
		if created {
			context = append(context, fmt.Sprintf("No plan named this session or worktree, so %s was made for this commit. "+
				"Write its goal and steps with `agent-plan section` and `agent-plan step` as the plan-work skill says, or, if the work belongs to another plan, say so in your report.", p.File()))
		}
		association := "--commit " + shellQuote(m.subject)
		if e.Revision != "" {
			association = "--revision " + e.Revision
		}
		context = append(context, fmt.Sprintf("The commit %q is logged in %s. If it completes a step, which step? "+
			"Log `done` for it with `%s` in its worktree; if none, carry on.", m.subject, p.File(), association))
		if who != "" && commits(p, who) == 1 {
			context = append(context, commitRules)
		}
	}
	for _, g := range calls {
		if g.sub == "commit" {
			continue
		}
		state := gitState{dir: g.dir}
		if path := newWorktree(g); path != "" {
			if real, err := filepath.EvalSymlinks(path); err == nil && toplevel(real) == real {
				if p, part, ok := at(g.dir); ok {
					p.AddToProperty(part, "WORKTREES", real, now)
				}
			}
		}
		name, start, switched := newBranch(g)
		if name == "" {
			continue
		}
		if switched {
			if current, _ := state.run("branch", "--show-current"); current != name {
				continue
			}
			if start == "" {
				start, _ = state.run("rev-parse", "--abbrev-ref", "@{-1}")
			}
		} else {
			if _, ok := state.run("rev-parse", "--verify", "-q", "refs/heads/"+name); !ok {
				continue
			}
			if start == "" {
				start, _ = state.run("branch", "--show-current")
			}
		}
		value := name
		if start != "" {
			value = start + ".." + name
		}
		if p, part, ok := at(g.dir); ok {
			if added, _ := p.AddToProperty(part, "BRANCHES", value, now); added {
				c.LinkEvidence(p.File(), now)
			}
		}
	}
	who := pushers(command, 0)
	if len(who) == 0 {
		return
	}
	p, part, ok := at(cwd)
	if !ok {
		return
	}
	for _, result := range jsonObjects(texts) {
		if url, isURL := result["url"].(string); isURL && result["pr"] != nil {
			branch, _ := result["branch"].(string)
			if added, _ := p.AddToProperty(part, "PULL_REQUESTS", url, now); added {
				c.LinkEvidence(p.File(), now)
			}
			p.Record(part, plans.Event{Kind: "pushed", Branch: branch, Summary: "opened " + url}, now)
			return
		}
	}
	// pr-loop push logs its push itself.
	delete(who, "pr-loop push")
	if branches := pushedTo(texts); len(branches) > 0 && len(who) > 0 {
		p.Record(part, plans.Event{Kind: "pushed", Summary: "pushed " + strings.Join(branches, " ")}, now)
	}
	return context
}

// commitRules hands over the commit rules, which AGENTS.md keeps in the
// signing-pass skill, on a session's first commit.
const commitRules = "This is the session's first commit. Load the `signing-pass` skill, if it is not loaded: " +
	"its commit rules apply to this commit and every later one, so amend this one, unsigned, if it breaks one."

// commits is how many commits p's log records for session.
func commits(p plans.Plan, session string) int {
	events, err := p.Events()
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range events {
		if e.Kind == "committed" && e.Session == session {
			n++
		}
	}
	return n
}

// shellQuote is s as one shell word.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// responseTexts are the strings of a tool's output: Claude Code's
// {"stdout": ...}, or every string of any other JSON.
func responseTexts(response json.RawMessage) []string {
	var value any
	if json.Unmarshal(response, &value) != nil {
		return nil
	}
	var texts []string
	var collect func(any)
	collect = func(v any) {
		switch v := v.(type) {
		case string:
			texts = append(texts, v)
		case map[string]any:
			if s, ok := v["stdout"].(string); ok {
				texts = append(texts, s)
				return
			}
			for _, x := range v {
				collect(x)
			}
		case []any:
			for _, x := range v {
				collect(x)
			}
		}
	}
	collect(value)
	return texts
}

// jsonObjects are the JSON objects that start anywhere in texts, in order.
func jsonObjects(texts []string) []map[string]any {
	var out []map[string]any
	for _, text := range texts {
		for i := strings.Index(text, "{"); i >= 0; {
			var result map[string]any
			if json.NewDecoder(strings.NewReader(text[i:])).Decode(&result) == nil {
				out = append(out, result)
			}
			next := strings.Index(text[i+1:], "{")
			if next < 0 {
				break
			}
			i += 1 + next
		}
	}
	return out
}
