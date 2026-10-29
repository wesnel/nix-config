package guard

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// The facts a successful shell call leaves for the plan of the session or
// worktree it ran in, recorded by the PostToolUse hook rather than the
// agent: each commit and push in the plan's log, and each new worktree,
// branch and pull request in its properties. Each fact is read from what
// git or pr-loop printed and checked against the repository, so a failed
// command, or one that only prints the same words, records nothing.

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
// (plans.Config.RecordCommit). Every failure leaves the plan as it was.
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
	for _, g := range gitCalls(command, cwd, 0) {
		if g.sub == "commit" {
			for _, m := range summaryLine.FindAllStringSubmatch(output, -1) {
				if _, ok := (gitState{dir: g.dir}).run("cat-file", "-e", m[2]+"^{commit}"); !ok {
					continue
				}
				branch := m[1]
				if strings.ContainsAny(branch, " \t") {
					branch = ""
				}
				who := session
				if strings.ContainsAny(who, " \t\r\n") {
					who = ""
				}
				top := toplevel(g.dir)
				e := plans.Event{Branch: branch, Commit: m[3], Summary: m[3], Worktree: top}
				if identity, err := plans.CommitIdentity(top, m[2]); err == nil {
					e.Repository, e.Revision = identity.Repository, identity.Revision
				}
				p, created, err := c.RecordCommit(who, top, e, now)
				if err != nil {
					continue
				}
				if created {
					context = append(context, fmt.Sprintf("No plan named this session or worktree, so %s was made for this commit. "+
						"Write its goal and steps with `agent-plan section` and `agent-plan step` as the plan-work skill says, or, if the work belongs to another plan, say so in your report.", p.File()))
				}
				association := "--commit " + shellQuote(m[3])
				if e.Revision != "" {
					association = "--revision " + e.Revision
				}
				context = append(context, fmt.Sprintf("The commit %q is logged in %s. If it completes a step, which step? "+
					"Log `done` for it with `%s` in its worktree; if none, carry on.", m[3], p.File(), association))
				if who != "" && commits(p, who) == 1 {
					context = append(context, commitRules)
				}
			}
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
			p.AddToProperty(part, "BRANCHES", value, now)
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
			p.AddToProperty(part, "PULL_REQUESTS", url, now)
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
