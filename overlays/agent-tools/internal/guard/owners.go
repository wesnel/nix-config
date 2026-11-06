package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

// checkOwner holds file edits, by a file tool or the shell, to the
// worktrees the plans of this host give agents (`agent-plan own`). A
// sub-agent edits only the worktrees it owns, scratch and the plans
// directory; one that owns none edits only scratch and plans, which closes
// the window between its spawn and `agent-plan own`. A worktree a plan
// names is judged by its owner wherever it is, scratch included. The main
// thread edits task worktrees associated with its session by the newest
// matching plan, except those delegated to another agent. Every path is
// compared with its symlinks resolved. The caller is the event's agent_id,
// which Claude Code and Codex set for a sub-agent's call alone.
//
// Shell edits are only those shellEditPaths names: a write through a script
// it cannot read passes.
func checkOwner(ctx context.Context, in input) string {
	var paths []string
	var reason string
	switch in.ToolName {
	case "Bash", "exec_command":
		var args struct {
			Command string `json:"command"`
		}
		json.Unmarshal(in.ToolInput, &args)
		paths, reason = shellEditPaths(ctx, args.Command, in.Cwd, 0, false)
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch":
		// An input that does not decode is denied before the fence is
		// asked.
		changes, _ := fileChanges(in.ToolName, in.ToolInput)
		for _, c := range changes {
			if c.path != "" {
				paths = append(paths, physical(rawPath(ctx, c.path, in.Cwd)))
			}
		}
	}
	if reason != "" {
		return reason
	}
	if len(paths) == 0 {
		return ""
	}
	// A sub-agent's edits fail closed: without plans it owns no worktree.
	c, err := plans.Load(env.From(ctx))
	owners := map[string]string{}
	if err == nil {
		named, _ := c.Owners()
		for wt, agent := range named {
			if real := realPath(wt); real != "" {
				wt = real
			}
			owners[wt] = agent
		}
	}
	if in.AgentID != "" {
		return subagentEdits(ctx, in.AgentID, paths, owners, c.Dir)
	}
	for _, p := range paths {
		top, _ := worktreeOf(ctx, p)
		if gitStalled(ctx).Load() {
			return stalledGit
		}
		if owner := owners[top]; top != "" && owner != "" && owner != plans.MainAgent {
			return fmt.Sprintf("The worktree %s belongs to agent %s, which alone edits files there (OWNERS in its plan). "+
				"Send the change to that agent, or have whoever coordinates hand the worktree over with `agent-plan own PLAN --worktree %s --agent ID`.",
				top, owner, top)
		}
	}
	if in.ToolName == "Bash" || in.ToolName == "exec_command" {
		var args struct {
			Command string `json:"command"`
		}
		json.Unmarshal(in.ToolInput, &args)
		// Creating isolation may start in the main checkout. Its existing
		// owner was checked above; redirections and other calls still count.
		paths, reason = shellEditPaths(ctx, args.Command, in.Cwd, 0, true)
		if reason != "" {
			return reason
		}
	}
	for _, p := range paths {
		top, g := worktreeOf(ctx, p)
		if gitStalled(ctx).Load() {
			return stalledGit
		}
		if g.dir == "" {
			continue
		}
		opened, ok := openedRepo(ctx, g.dir)
		if !ok {
			return "The guard cannot determine the repository this edit changes. Ask the operator to repair its Git metadata before editing."
		}
		if top == "" && len(opened) != 0 {
			return "This edit is outside a working tree, such as inside Git metadata or a bare repository. Make repository edits in your associated task worktree; report shared metadata changes to the operator."
		}
		if top == "" {
			continue
		}
		if err != nil {
			return fmt.Sprintf("The guard cannot read the plans configuration: %v. Ask the operator to repair it before editing.", err)
		}
		found, findErr := c.Find(plans.FindOptions{Session: in.SessionID, Worktree: top})
		if findErr != nil {
			return fmt.Sprintf("The guard cannot read worktree associations: %v. Ask the operator to repair the plans before editing.", findErr)
		}
		var association *plans.Found
		for i := range found {
			if slices.Contains(found[i].Matched, "worktree") {
				association = &found[i]
				break
			}
		}
		if association == nil && owners[top] == "" && len(opened) > 0 && !slices.ContainsFunc(opened, func(path string) bool {
			return !slices.ContainsFunc(scratchRoots(env.From(ctx)), func(root string) bool { return within(path, root) })
		}) {
			continue
		}
		if len(opened) < 3 || resolved(opened[0]) == resolved(opened[1]) {
			return "This edits the repository's main checkout. Create a task worktree at an absolute path outside this checkout (`git worktree add --no-track /ABSOLUTE/PATH -b BRANCH origin/HEAD`)."
		}
		branch, branchOK := g.run(ctx, "branch", "--show-current")
		defaults, defaultsOK := g.run(ctx, "for-each-ref", "--format=%(symref)", "refs/remotes/*/HEAD")
		if !branchOK || !defaultsOK {
			return "The guard cannot read this worktree's branch. Ask the operator to repair its Git metadata before editing."
		}
		protected := branch == "" || branch == "main" || branch == "master"
		for _, ref := range strings.Fields(defaults) {
			parts := strings.SplitN(ref, "/", 4)
			if len(parts) == 4 && branch == parts[3] {
				protected = true
			}
		}
		if protected {
			return "This worktree is on a default branch or a detached HEAD. Create a task worktree at an absolute path outside this checkout (`git worktree add --no-track /ABSOLUTE/PATH -b BRANCH HEAD`) before editing."
		}
		if in.SessionID == "" || association == nil || !slices.Contains(association.Matched, "session") {
			return fmt.Sprintf("This session is not associated with %s in its newest plan. Work in your own task worktree and record its session and worktree with `agent-plan new`; for an intentional continuation, associate the session with that plan before editing.", top)
		}
	}
	return ""
}

// subagentEdits returns why agent may not edit one of paths, or "": it
// edits only the worktrees owners gives it, the scratch roots, the plans
// directory and devices such as /dev/null. A path in a worktree owners
// names is the owner's alone, even in scratch.
func subagentEdits(ctx context.Context, agent string, paths []string, owners map[string]string, plansDir string) string {
	var mine []string
	for wt, a := range owners {
		if a == agent {
			mine = append(mine, wt)
		}
	}
	sort.Strings(mine)
	free := scratchRoots(env.From(ctx))
	if plansDir != "" {
		free = append(free, plansDir)
	}
	for _, p := range paths {
		if within(p, "/dev") {
			continue
		}
		top, _ := worktreeOf(ctx, p)
		if owner, named := owners[top]; top != "" && named {
			if owner == agent {
				continue
			}
		} else if slices.ContainsFunc(free, func(root string) bool { return within(p, root) }) {
			continue
		}
		if len(mine) == 0 {
			return fmt.Sprintf("Sub-agent %s owns no worktree, so it edits only scratch ($TMPDIR, /tmp) and the plans directory, and %s is outside them. "+
				"Have whoever coordinates give it its worktree with `agent-plan own PLAN --worktree WORKTREE --agent %s` before it writes there, "+
				"or put the change in your report; reads are not limited.", agent, p, agent)
		}
		return fmt.Sprintf("Sub-agent %s edits only the worktree it owns, %s (OWNERS in its plan), scratch ($TMPDIR, /tmp) and the plans directory, and %s is outside them. "+
			"Make the change in %s, or put it in your report for the operator or whoever coordinates; "+
			"moving the agent to another worktree is `agent-plan own PLAN --worktree WORKTREE --agent %s`, run by whoever coordinates.",
			agent, strings.Join(mine, ", "), p, strings.Join(mine, ", "), agent)
	}
	return ""
}

// within reports whether path, which need not exist yet, is root or under
// it, with the symlinks of both resolved: as written, a path in scratch can
// lead anywhere.
func within(path, root string) bool {
	p, r := resolved(path), realPath(root)
	return p != "" && r != "" && (p == r || strings.HasPrefix(p, strings.TrimSuffix(r, "/")+"/"))
}

// resolved is path with the symlinks of its nearest existing ancestor
// resolved, or "" when none can be.
func resolved(path string) string {
	rest := ""
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

const subagentStash = "Every worktree shares one stash, so a sub-agent's entry can be taken by, or take, another session's work. " +
	"Set the work aside as a WIP commit on your branch instead (`git -c commit.gpgsign=false commit -m 'WIP: ...'`). " +
	"`git stash list` and `git stash show` still read the stash."

// checkSubagentStash denies a sub-agent's shell command that writes or
// restores a stash entry. It runs before check, whose rules for tagged
// entries would otherwise answer first. The sub-agent is the event's
// agent_id, as for checkOwner.
func checkSubagentStash(ctx context.Context, in input) string {
	if in.AgentID == "" {
		return ""
	}
	switch in.ToolName {
	case "Bash", "exec_command":
	default:
		return ""
	}
	var args struct {
		Command string `json:"command"`
	}
	json.Unmarshal(in.ToolInput, &args)
	return stashWrites(ctx, args.Command, 0)
}

// stashWrites returns subagentStash if a command line runs a `git stash`
// other than list or show, or "".
func stashWrites(ctx context.Context, text string, depth int) string {
	if depth > maxDepth {
		return ""
	}
	v := visitor{
		script: func(text string) string { return stashWrites(ctx, text, depth+1) },
		call: func(c call) string {
			if c = c.named(); len(c.words) == 0 || c.expands[0] || base(c.words[0]) != "git" {
				return ""
			}
			words := c.words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				if gitValueFlags[words[0]] {
					words = words[1:]
				}
				words = words[min(1, len(words)):]
			}
			if len(words) == 0 || words[0] != "stash" {
				return ""
			}
			if len(words) > 1 && (words[1] == "list" || words[1] == "show") {
				return ""
			}
			return subagentStash
		},
	}
	for _, segment := range segments(text) {
		if words, expands := split(segment); len(words) > 0 {
			if reason := unwrap(ctx, call{words: words, expands: expands}, v); reason != "" {
				return reason
			}
		}
	}
	return ""
}

// worktreeOf is the top level of the worktree holding path, which need not
// exist yet and may be a directory, with its symlinks resolved, and the git
// state there; "" when none holds it.
func worktreeOf(ctx context.Context, path string) (string, gitState) {
	if !filepath.IsAbs(path) {
		return "", gitState{}
	}
	dir := path
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", gitState{}
		}
		dir = parent
	}
	g := gitState{dir: dir}
	top, ok := g.run(ctx, "rev-parse", "--show-toplevel")
	if !ok {
		return "", g
	}
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	return top, g
}

// shellEditPaths are the absolute paths of the files a command line
// writes, moves or removes, as changedFiles names them, and the directory
// a git command that writes the worktree runs in (gitWrites), following cd
// as command does. Each file is named as fencePath has it.
func shellEditPaths(ctx context.Context, text, dir string, depth int, creatingWorktree bool) ([]string, string) {
	if depth > maxDepth {
		return nil, ""
	}
	var out []string
	w := cwds{here: []string{dir}}
	end := ""
	v := visitor{
		script: func(text string) string {
			for _, cwd := range w.here {
				paths, reason := shellEditPaths(ctx, text, cwd, depth+1, creatingWorktree)
				if reason != "" {
					return reason
				}
				out = append(out, paths...)
			}
			return ""
		},
		call: func(c call) string {
			if len(c.words) == 0 || c.expands[0] && c.named().expands[0] {
				return ""
			}
			named := c.named()
			if named.words[0] == "cd" || named.words[0] == "pushd" {
				// Redirections before cd open their files where the
				// command starts, before it changes directory.
				for _, cwd := range w.here {
					for _, t := range redirections(c.words) {
						if p := fencePath(ctx, t, cwd); filepath.IsAbs(p) {
							out = append(out, p)
						}
					}
				}
				w.cd(ctx, named, end)
				return ""
			}
			for _, cwd := range w.here {
				for _, t := range changedFiles(ctx, base(named.words[0]), named.words[1:], redirections(c.words)) {
					if p := fencePath(ctx, t, cwd); filepath.IsAbs(p) {
						out = append(out, p)
					}
				}
				paths, reason := gitWritePaths(ctx, named, cwd, creatingWorktree)
				if reason != "" {
					return reason
				}
				out = append(out, paths...)
			}
			return ""
		},
	}
	texts, _, ends, _ := backgroundSegments(text)
	for i, segment := range texts {
		if words, expands := split(segment); len(words) > 0 {
			end = ends[i]
			c := call{words: words, expands: expands}
			w.begin(ctx, c)
			if reason := unwrap(ctx, c, v); reason != "" {
				return nil, reason
			}
			w.next(end)
		}
	}
	return out, ""
}

// fencePath is word, a file a command run in cwd names, as the owner fence
// judges it: with $PWD as cwd, and $TMPDIR and $HOME in their place
// (shellPath), cut at its first part still holding a variable or a
// command substitution, which the shell decides, so that the directory
// before it is judged: MAIN/$x.go as MAIN, and $(cmd)/a.go, which split
// gives as $_/a.go, as cwd, where $(pwd) and $(git rev-parse
// --show-toplevel) lead. Where a .. follows that part, the word is judged
// whole, cleaned, as if the variable held one name: MAIN/$x/../../b as
// b. A word that starts with a variable ($f.go, $out) is judged as cwd,
// where the variable most often leads, so the guard never passes it
// unjudged.
func fencePath(ctx context.Context, word, cwd string) string {
	for _, p := range []string{"$PWD", "${PWD}"} {
		if (word == p || strings.HasPrefix(word, p+"/")) && cwd != "" {
			word = cwd + strings.TrimPrefix(word, p)
			break
		}
	}
	word = shellPath(ctx, word, true)
	parts := strings.Split(word, "/")
	i := slices.IndexFunc(parts, func(p string) bool { return strings.ContainsAny(p, "$`") })
	switch {
	case i < 0, slices.Contains(parts[i+1:], ".."):
		return physical(rawPath(ctx, word, cwd))
	case i == 1 && parts[0] == "":
		return "/"
	}
	return physical(rawPath(ctx, strings.Join(parts[:i], "/"), cwd))
}

// gitWritePaths names the absolute paths a git command, run in cwd, writes,
// with its -C options applied, when gitWrites names its command and gitRead
// does not pass it as a reading form (stash list, tag -l, apply --check);
// a refusal when a worktree target cannot be resolved.
func gitWritePaths(ctx context.Context, c call, cwd string, creatingWorktree bool) ([]string, string) {
	if base(c.words[0]) != "git" {
		return nil, ""
	}
	dir, words := cwd, c.words[1:]
	relocated := false
	for _, kv := range append(slices.Clone(c.env), c.assigned...) {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(gitenv.Location, name) {
			relocated = true
		}
	}
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		flag, _, _ := strings.Cut(words[0], "=")
		if flag == "--git-dir" || flag == "--work-tree" || flag == "--bare" {
			relocated = true
		}
		switch {
		case words[0] == "-C" && len(words) > 1:
			dir = fencePath(ctx, words[1], dir)
			words = words[2:]
		case gitValueFlags[words[0]]:
			words = words[min(2, len(words)):]
		default:
			words = words[1:]
		}
	}
	if len(words) == 0 || !gitWrites[words[0]] || gitRead(words[0], words[1:]) || !filepath.IsAbs(dir) {
		return nil, ""
	}
	if relocated {
		return nil, "The guard requires an explicit working directory for Git mutations. Use `git -C /ABSOLUTE/TASK/WORKTREE` without repository-location flags or GIT_* path overrides."
	}
	if len(words) > 1 && words[0] == "worktree" && words[1] == "add" {
		positional := gitPositionals(words[0], words[1:])
		var paths []string
		if len(positional) > 1 {
			paths = append(paths, fencePath(ctx, words[1+positional[1]], dir))
		}
		if !creatingWorktree {
			paths = append(paths, dir)
		}
		return paths, ""
	}
	if len(words) > 1 && words[0] == "worktree" && (words[1] == "remove" || words[1] == "move") {
		args := words[2:]
		for len(args) > 0 && (args[0] == "-f" || args[0] == "--force") {
			args = args[1:]
		}
		if len(args) > 0 && args[0] == "--" {
			args = args[1:]
		}
		want := 1
		if words[1] == "move" {
			want = 2
		}
		if len(args) != want || strings.HasPrefix(args[0], "-") {
			return nil, "The guard cannot determine this worktree command's targets. Use an explicit worktree path and only --force or -- before it."
		}
		target, reason := worktreeTarget(ctx, dir, args[0])
		if reason != "" {
			return nil, reason
		}
		paths := []string{target}
		if want == 2 {
			paths = append(paths, fencePath(ctx, args[1], dir))
		}
		return paths, ""
	}
	return []string{dir}, ""
}

// Require an absolute path so Git's suffix and case-matching rules cannot
// select a different worktree from the one whose ownership is checked.
func worktreeTarget(ctx context.Context, dir, word string) (string, string) {
	refusal := "The guard cannot resolve this worktree target. Use an existing worktree's absolute path from `git worktree list`."
	if !filepath.IsAbs(word) || strings.ContainsAny(word, "$`") {
		return "", refusal
	}
	g := gitState{dir: dir}
	listing, ok := g.run(ctx, "worktree", "list", "--porcelain", "-z")
	if !ok {
		if gitStalled(ctx).Load() {
			return "", stalledGit
		}
		return "", refusal
	}
	path := resolved(fencePath(ctx, word, dir))
	for _, field := range strings.Split(listing, "\x00") {
		if target, ok := strings.CutPrefix(field, "worktree "); ok {
			if resolved(target) == path {
				top, _ := worktreeOf(ctx, target)
				if top != "" && resolved(top) == path {
					return top, ""
				}
			}
		}
	}
	return "", refusal
}
