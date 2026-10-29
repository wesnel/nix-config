package guard

import (
	"fmt"
	"slices"

	"github.com/wesnel/agent-tools/internal/plans"
)

// checkPlanReview holds the main thread's file edits, by a file tool or the
// shell, while a plan whose SESSIONS name the session has a design review
// of the plan recorded and not yet answered (plans.PendingPlanReviewOf), so
// its findings shape the work before any of it is written. The plans
// directory, scratch and devices stay open. A sub-agent is not held: its
// edits are fenced to its own worktree already (checkOwner).
func checkPlanReview(in input) string {
	if in.SessionID == "" || in.AgentID != "" {
		return ""
	}
	paths := editPaths(in)
	if len(paths) == 0 {
		return ""
	}
	c, err := plans.Load()
	if err != nil {
		return ""
	}
	p, recorded, pending := c.PendingPlanReviewOf(in.SessionID)
	if !pending {
		return ""
	}
	free := append(scratchRoots(), c.Dir, "/dev")
	for _, path := range paths {
		if slices.ContainsFunc(free, func(root string) bool { return within(path, root) }) {
			continue
		}
		return fmt.Sprintf("The design review %q of the plan %s was recorded at %s and has not been answered, so edits outside "+
			"the plans directory and scratch ($TMPDIR, /tmp) wait for it, %s among them. When its report is in, settle its "+
			"findings in the plan and log `agent-plan log %s --item %s --kind plan-reviewed`; if the review was stopped and "+
			"will not report, log that event with a summary saying so, and run the review again before building.",
			recorded.Summary, p.File(), recorded.Time, path, p.File(), p.Name)
	}
	return ""
}
