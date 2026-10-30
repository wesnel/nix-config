package guard

import (
	"fmt"

	"github.com/wesnel/agent-tools/internal/plans"
)

// checkPlanReview holds the main thread's file edits, by a file tool or the
// shell, while a plan whose SESSIONS name the session has a design review
// of the plan recorded and not yet answered (plans.PendingPlanReviewOf), so
// its findings shape the work before any of it is written. openRoots stay
// open. A sub-agent is not held: its edits are fenced to its own worktree
// already (checkOwner). paths are editPaths(in); conf is this host's plans,
// and with none nothing is held.
func checkPlanReview(in input, paths []string, conf *plans.Config) string {
	if in.SessionID == "" || in.AgentID != "" || len(paths) == 0 || conf == nil {
		return ""
	}
	p, recorded, pending := conf.PendingPlanReviewOf(in.SessionID)
	if !pending {
		return ""
	}
	free := openRoots(conf.Dir)
	for _, path := range paths {
		if isOpen(path, free) {
			continue
		}
		return fmt.Sprintf("The design review %q of the plan %s was recorded at %s and has not been answered, so edits outside "+
			openPlaces+" wait for it, %s among them. When its report is in, settle its "+
			"findings in the plan and log `agent-plan log %s --item %s --kind plan-reviewed`; if the review was stopped and "+
			"will not report, log that event with a summary saying so, and run the review again before building.",
			recorded.Summary, p.File(), recorded.Time, path, p.File(), p.Name)
	}
	return ""
}
