package loop

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
)

// record logs e, an action of pr-loop's that has happened, in the plan of
// the session running it: the plan plans.Config.LocateBranches finds for
// the harness's session, this worktree and the loop's branches, which
// takes a relayed loop session's, whose harness names no session here.
// state is the loop's, or nil to read it. The loop's state stays where it
// is; the events are its record. A failure is reported on stderr and never
// fails the action.
func record(state *State, e plans.Event) {
	c, err := plans.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr-loop: %s is not logged: %v\n", e.Kind, err)
		return
	}
	if state == nil {
		state, _ = loadState(false)
	}
	top, _ := git.Run("rev-parse", "--show-toplevel")
	var branches []string
	if state != nil {
		branches = state.Order
	}
	p, part, ok := c.LocateBranches(plans.EnvSession(), top, branches)
	if !ok {
		return
	}
	if _, err := p.Record(part, e, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "pr-loop: %s is not logged in %s: %v\n", e.Kind, p.File(), err)
	}
}

// branchTips is each of the loop's branches that oids holds with its
// commit, as "branch oid", in the loop's order.
func branchTips(state *State, oids map[string]string) string {
	var out []string
	for _, b := range state.Order {
		if oid, ok := oids[b]; ok {
			out = append(out, b+" "+oid)
		}
	}
	return strings.Join(out, ", ")
}

func prList(numbers []int) string {
	var out []string
	for _, n := range numbers {
		out = append(out, fmt.Sprintf("#%d", n))
	}
	return strings.Join(out, " ")
}
