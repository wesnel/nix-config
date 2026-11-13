package guard

import (
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

// spaceState is why a write to the Space publisher's state is refused: it
// names the block plan-dash refresh replaces, and a session that rewrote it
// could aim the publish at another block.
const spaceState = "The publisher's state binds pages, blocks and session ownership, so only plan-dash publication commands, native receipt hooks and the operator write it. " +
	"Put what should change in your report for the operator."

// spaceStateFile reports whether path is the Space publisher's state, as
// written, through its links or, where names fold, in any case, or a
// link to it, there yet or not.
func spaceStateFile(path string) bool {
	state := agentplans.SpaceStateFile()
	mirrors := fold(physical(filepath.Join(agentplans.Dir(), "mirrors")))
	resolved := fold(physical(path))
	return fold(filepath.Clean(path)) == fold(filepath.Clean(state)) || fold(physical(path)) == fold(physical(state)) ||
		Reaches(path, state, false) || resolved == mirrors || strings.HasPrefix(resolved, mirrors+string(filepath.Separator)) ||
		Reaches(path, filepath.Join(agentplans.Dir(), "mirrors"), true)
}

// checkSpaceStateShell is spaceState for a command that writes the state:
// by a redirection or a tool's output, or by writing a directory that
// holds it, such as an archive's extraction.
func checkSpaceStateShell(name string, args, redirected []string, dir string) string {
	if name == "plan-dash" && len(args) > 0 && args[0] == "record" {
		return "Mirror receipts come only from the native PostToolUse hook. Run plan-dash snapshot to prepare a mirror; put an uncertain publication in your report for the operator."
	}
	if shellWrites(name, args, redirected, dir, spaceStateFile) {
		return spaceState
	}
	state := agentplans.SpaceStateFile()
	for _, value := range changedFiles(name, args, redirected) {
		path := expandPath(value, dir)
		if spaceStateFile(path) || Reaches(state, path, true) || Reaches(filepath.Join(agentplans.Dir(), "mirrors"), path, true) {
			return spaceState
		}
	}
	return ""
}
