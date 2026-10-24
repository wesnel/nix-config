package guard

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// branchHint is the context for a file edit, by Claude Code's Edit, Write
// or MultiEdit, that lands in another worktree or on another branch than
// the session's last edit; "" otherwise. The session's plan, the one
// naming the session, else the edit's worktree, logs each change as a
// branch-changed event, from which the next edit reads the last; a
// session without a plan gets no hint. It never denies.
func branchHint(session string, toolInput json.RawMessage, cwd string) string {
	var in struct {
		FilePath string `json:"file_path"`
	}
	json.Unmarshal(toolInput, &in)
	if session == "" || in.FilePath == "" {
		return ""
	}
	top, g := worktreeOf(expandPath(in.FilePath, cwd))
	if top == "" {
		return ""
	}
	c, err := plans.Load()
	if err != nil {
		return ""
	}
	p, _, ok := c.Locate(session, "")
	if !ok {
		if p, _, ok = c.Locate("", top); !ok {
			return ""
		}
	}
	// A branch or tip git did not give, as when it stalled, is not noted.
	branch, ok := g.run("branch", "--show-current")
	if !ok {
		return ""
	}
	tip := ""
	if branch == "" {
		if tip, ok = g.run("rev-parse", "--short", "HEAD"); !ok {
			return ""
		}
	}
	last, had, logged, err := p.NoteBranch(session, branch, tip, top, time.Now())
	if err != nil || !had || !logged {
		return ""
	}
	return fmt.Sprintf("This edit is on %s in %s; the last one was on %s in %s.", describe(branch, tip), top,
		describe(last.Branch, last.Tip), last.Worktree)
}

// describe names a branch, or a detached HEAD at tip where branch is "".
func describe(branch, tip string) string {
	if branch == "" {
		return "a detached HEAD at " + tip
	}
	return branch
}
