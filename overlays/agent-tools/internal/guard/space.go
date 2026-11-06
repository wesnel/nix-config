package guard

import (
	"context"
	"path/filepath"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/env"
)

// spaceState is why a write to the Space publisher's state is refused: it
// names the block plan-dash space replaces, and a session that rewrote it
// could aim the publish at another block.
const spaceState = "The Space publisher's state names the block it replaces, so only plan-dash space and the operator write it. " +
	"Put what should change in your report for the operator."

// spaceStateFile reports whether path is the Space publisher's state, as
// written, through its links or, where names fold, in any case, or a
// link to it, there yet or not. The state is where the Env of ctx puts it.
func spaceStateFile(ctx context.Context, path string) bool {
	state := agentplans.SpaceStateFile(env.From(ctx))
	return fold(filepath.Clean(path)) == fold(filepath.Clean(state)) || fold(physical(path)) == fold(physical(state)) ||
		Reaches(path, state, false)
}

// checkSpaceStateShell is spaceState for a command that writes the state:
// by a redirection or a tool's output, or by writing a directory that
// holds it, such as an archive's extraction.
func checkSpaceStateShell(ctx context.Context, name string, args, redirected []string, dir string) string {
	isState := func(path string) bool { return spaceStateFile(ctx, path) }
	if shellWrites(ctx, name, args, redirected, dir, isState) {
		return spaceState
	}
	state := agentplans.SpaceStateFile(env.From(ctx))
	for _, value := range changedFiles(ctx, name, args, redirected) {
		path := expandPath(ctx, value, dir)
		if isState(path) || Reaches(state, path, true) {
			return spaceState
		}
	}
	return ""
}
