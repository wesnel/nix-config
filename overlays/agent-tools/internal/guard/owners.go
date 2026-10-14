package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/wesnel/agent-tools/internal/plans"
)

// checkOwner denies a file edit, by a file tool or the shell, inside a
// worktree a plan of this host gives to another agent (`agent-plan own`).
// The caller is the event's agent_id, which Claude Code and Codex set for a
// sub-agent's call alone, or plans.MainAgent; ECA's events name no agent
// instance, so they pass.
func checkOwner(in input) string {
	if in.HookType != "" {
		return ""
	}
	var paths []string
	switch in.ToolName {
	case "Bash", "exec_command", "shell_command":
		var args struct {
			Command string `json:"command"`
		}
		json.Unmarshal(in.ToolInput, &args)
		paths = shellEditPaths(args.Command, in.Cwd, 0)
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch":
		for _, c := range fileChanges(in.ToolName, in.ToolInput) {
			if c.path != "" {
				paths = append(paths, expandPath(c.path, in.Cwd))
			}
		}
	}
	if len(paths) == 0 {
		return ""
	}
	c, err := plans.Load()
	if err != nil {
		return ""
	}
	owners, err := c.Owners()
	if err != nil || len(owners) == 0 {
		return ""
	}
	me := in.AgentID
	if me == "" {
		me = plans.MainAgent
	}
	for _, p := range paths {
		top, _ := worktreeOf(p)
		if owner := owners[top]; top != "" && owner != "" && owner != me {
			return fmt.Sprintf("The worktree %s belongs to agent %s, which alone edits files there (OWNERS in its plan). "+
				"Send the change to that agent, or have whoever coordinates hand the worktree over with `agent-plan own PLAN --worktree %s --agent ID`.",
				top, owner, top)
		}
	}
	return ""
}

// worktreeOf is the top level of the worktree holding path, which need not
// exist yet, with its symlinks resolved, and the git state there; "" when
// none holds it.
func worktreeOf(path string) (string, gitState) {
	if !filepath.IsAbs(path) {
		return "", gitState{}
	}
	dir := filepath.Dir(path)
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

// shellEditPaths are the absolute paths of the files a command line edits,
// as editTargets names them, following cd as command does.
func shellEditPaths(text, dir string, depth int) []string {
	if depth > maxDepth {
		return nil
	}
	var out []string
	cwd := dir
	v := visitor{
		script: func(text string) string {
			out = append(out, shellEditPaths(text, cwd, depth+1)...)
			return ""
		},
		call: func(c call) string {
			if len(c.words) == 0 || c.expands[0] {
				return ""
			}
			if c.words[0] == "cd" || c.words[0] == "pushd" {
				cwd = cdTarget(c, cwd)
				return ""
			}
			for _, t := range editTargets(base(c.words[0]), c.words[1:], redirections(c.words)) {
				if p := expandPath(t, cwd); filepath.IsAbs(p) {
					out = append(out, p)
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
