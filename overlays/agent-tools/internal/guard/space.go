package guard

import (
	"path/filepath"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

// spaceState is why a write to the Space publisher's state is refused: it
// names the block plan-dash space replaces, and a session that rewrote it
// could aim the publish at another block.
const spaceState = "The Space publisher's state names the block it replaces, so only plan-dash space and the operator write it. " +
	"Put what should change in your report for the operator."

// spaceStateFile reports whether path is the Space publisher's state, as
// written, through its links or, where names fold, in any case, or a
// link to it, there yet or not.
func spaceStateFile(path string) bool {
	state := agentplans.SpaceStateFile()
	return fold(filepath.Clean(path)) == fold(filepath.Clean(state)) || fold(physical(path)) == fold(physical(state)) ||
		Reaches(path, state, false)
}

// checkSpaceStateShell is spaceState for a command that writes the state:
// by a redirection or a tool's output, or by writing a directory that
// holds it, such as an archive's extraction.
func checkSpaceStateShell(name string, args, redirected []string, dir string) string {
	if shellWrites(name, args, redirected, dir, spaceStateFile) {
		return spaceState
	}
	state := agentplans.SpaceStateFile()
	for _, value := range changedFiles(name, args, redirected) {
		path := expandPath(value, dir)
		if spaceStateFile(path) || Reaches(state, path, true) {
			return spaceState
		}
	}
	return ""
}
