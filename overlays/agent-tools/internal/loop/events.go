package loop

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
)

// logEvent logs e, an action of pr-loop's that has happened, in the plan of
// the session running it: the plan plans.Config.Locate finds for the
// harness's session or this worktree. A failure is reported on stderr and
// never fails the action.
func logEvent(e plans.Event) {
	c, err := plans.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr-loop: %s is not logged: %v\n", e.Kind, err)
		return
	}
	top, _ := git.Run("rev-parse", "--show-toplevel")
	p, part, ok := c.Locate(plans.EnvSession(), top)
	if !ok {
		return
	}
	if _, err := p.Record(part, e, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "pr-loop: %s is not logged in %s: %v\n", e.Kind, p.File(), err)
	}
}

// branchTips is each branch of order that oids holds with its commit, as
// "branch oid".
func branchTips(order []string, oids map[string]string) string {
	var out []string
	for _, b := range order {
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
