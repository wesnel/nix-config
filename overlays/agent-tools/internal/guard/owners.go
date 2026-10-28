package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/plans"
)

// checkOwner holds file edits, by a file tool or the shell, to the
// worktrees the plans of this host give agents (`agent-plan own`). A
// sub-agent edits only the worktrees it owns, scratch and the plans
// directory; one that owns none edits only scratch and plans, which closes
// the window between its spawn and `agent-plan own`. A worktree a plan
// names is judged by its owner wherever it is, scratch included. The main
// thread edits anywhere but a worktree another agent owns. Every path is
// compared with its symlinks resolved. The caller is the event's agent_id,
// which Claude Code and Codex set for a sub-agent's call alone.
//
// Shell edits are only those shellEditPaths names: a write through a script
// it cannot read passes.
func checkOwner(in input) string {
	var paths []string
	switch in.ToolName {
	case "Bash", "exec_command":
		var args struct {
			Command string `json:"command"`
		}
		json.Unmarshal(in.ToolInput, &args)
		var unknown bool
		paths, unknown = shellEditPaths(args.Command, in.Cwd, 0, environment{})
		if unknown {
			return unknownShellWrite
		}
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch":
		// An input that does not decode is denied before the fence is
		// asked.
		changes, _ := fileChanges(in.ToolName, in.ToolInput)
		for _, c := range changes {
			if c.path != "" {
				paths = append(paths, expandPath(c.path, in.Cwd))
			}
		}
	}
	if len(paths) == 0 {
		return ""
	}
	// A sub-agent's edits fail closed: without plans it owns no worktree.
	c, err := plans.Load()
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
		return subagentEdits(in.AgentID, paths, owners, c.Dir)
	}
	for _, p := range paths {
		top, _ := worktreeOf(p)
		if owner := owners[top]; top != "" && owner != "" && owner != plans.MainAgent {
			return fmt.Sprintf("The worktree %s belongs to agent %s, which alone edits files there (OWNERS in its plan). "+
				"Send the change to that agent, or have whoever coordinates hand the worktree over with `agent-plan own PLAN --worktree %s --agent ID`.",
				top, owner, top)
		}
	}
	return ""
}

const unknownShellWrite = "The path or directory of a shell write cannot be resolved, so its ownership cannot be checked. " +
	"Use an explicit directory and write target, or put the command in your report."

// subagentEdits returns why agent may not edit one of paths, or "": it
// edits only the worktrees owners gives it, the scratch roots, the plans
// directory and devices such as /dev/null. A path in a worktree owners
// names is the owner's alone, even in scratch.
func subagentEdits(agent string, paths []string, owners map[string]string, plansDir string) string {
	var mine []string
	for wt, a := range owners {
		if a == agent {
			mine = append(mine, wt)
		}
	}
	sort.Strings(mine)
	free := scratchRoots()
	if plansDir != "" {
		free = append(free, plansDir)
	}
	for _, p := range paths {
		if within(p, "/dev") {
			continue
		}
		top, _ := worktreeOf(p)
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
func checkSubagentStash(in input) string {
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
	return stashWrites(args.Command, 0)
}

// stashWrites returns subagentStash if a command line runs a `git stash`
// other than list or show, or "".
func stashWrites(text string, depth int) string {
	if depth > maxDepth {
		return ""
	}
	v := visitor{
		script: func(text string, _ environment) string { return stashWrites(text, depth+1) },
		call: func(c call) string {
			if len(c.words) == 0 || c.expands[0] || base(c.words[0]) != "git" {
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
		if c := split(segment); len(c.words) > 0 {
			if reason := unwrap(c, v); reason != "" {
				return reason
			}
		}
	}
	return ""
}

// worktreeOf is the top level of the worktree holding path, which need not
// exist yet and may be a directory, with its symlinks resolved, and the git
// state there; "" when none holds it.
func worktreeOf(path string) (string, gitState) {
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
	top, ok := g.run("rev-parse", "--show-toplevel")
	if !ok {
		return "", gitState{}
	}
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	return top, g
}

// shellEditPaths are the absolute paths of the files a command line
// writes, moves or removes, as changedFiles names them, and the directory
// a git command that writes the worktree runs in (gitWrites), following cd
// as command does. The second result says a write could not be resolved
// to an absolute path, so its ownership cannot be checked.
func shellEditPaths(text, dir string, depth int, env environment) ([]string, bool) {
	if depth > maxDepth {
		return nil, false
	}
	var out []string
	unknown := false
	w := cwds{here: []string{dir}, env: env}
	end := ""
	add := func(target, cwd string, expanded bool) {
		word, known := shellPathIn(target, expanded, env)
		if !known {
			unknown = true
			return
		}
		p := word
		if !expanded && strings.ContainsAny(word, "$`") {
			if !filepath.IsAbs(p) && cwd != "" {
				p = filepath.Join(cwd, p)
			}
		} else {
			p = expandPath(word, cwd)
		}
		if filepath.IsAbs(p) {
			out = append(out, p)
		} else {
			unknown = true
		}
	}
	v := visitor{
		chdir: w.inDir,
		redirects: func(redirects []redirection) string {
			for _, cwd := range w.here {
				for _, r := range redirects {
					if !r.output() {
						continue
					}
					if path, known := r.path(cwd, env); known {
						out = append(out, path)
					} else {
						unknown = true
					}
				}
			}
			return ""
		},
		script: func(text string, child environment) string {
			for _, cwd := range w.here {
				paths, unresolved := shellEditPaths(text, cwd, depth+1, child)
				out = append(out, paths...)
				unknown = unknown || unresolved
			}
			return ""
		},
		call: func(c call) string {
			if len(c.words) == 0 || c.expands[0] {
				return ""
			}
			if c.words[0] == "cd" || c.words[0] == "pushd" {
				w.cd(c, end)
				return ""
			}
			for _, cwd := range w.here {
				for _, target := range changedFiles(base(c.words[0]), c.words[1:], nil) {
					expanded := false
					for i, word := range c.words {
						if (word == target || afterEquals(word) == target) && c.expands[i] {
							expanded = true
						}
					}
					add(target, cwd, expanded)
				}
				if dir, writes := gitWriteDir(c, cwd); writes {
					if filepath.IsAbs(dir) {
						out = append(out, dir)
					} else {
						unknown = true
					}
				}
			}
			return ""
		},
	}
	texts, _, ends := backgroundSegments(text)
	for i, segment := range texts {
		if c := split(segment); len(c.words) > 0 || len(c.redirects) > 0 {
			c.env = env
			end = ends[i]
			w.begin(c)
			unwrap(c, v)
			w.next(end)
		}
	}
	return out, unknown
}

// gitWriteDir is the absolute directory a git command, run in cwd, writes,
// with its -C options applied, when gitWrites names its command and gitRead
// does not pass it as a reading form (stash list, tag -l, apply --check);
// The second result distinguishes a write in an unknown directory from a
// command that does not write.
func gitWriteDir(c call, cwd string) (string, bool) {
	if base(c.words[0]) != "git" {
		return "", false
	}
	dir, words := cwd, c.words[1:]
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		switch {
		case words[0] == "-C" && len(words) > 1:
			dir = expandPath(words[1], dir)
			words = words[2:]
		case gitValueFlags[words[0]]:
			words = words[min(2, len(words)):]
		default:
			words = words[1:]
		}
	}
	if len(words) == 0 || !gitWrites[words[0]] || gitRead(words[0], words[1:]) {
		return "", false
	}
	return dir, true
}
