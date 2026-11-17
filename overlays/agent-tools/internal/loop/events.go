package loop

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
)

// sessionPlan is the plan of the session running pr-loop, and its part: the
// one plans.Config.Locate finds for the harness's session or this worktree.
// ok is false where there is none, or the plans cannot be read, which is
// reported on stderr for what, the fact going unrecorded.
func sessionPlan(what string) (c plans.Config, p plans.Plan, part string, ok bool) {
	c, err := plans.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr-loop: %s is not logged: %v\n", what, err)
		return c, p, "", false
	}
	top, _ := git.Run("rev-parse", "--show-toplevel")
	harness, id := plans.EnvSession()
	session := ""
	if id != "" {
		session = harness + ":" + id
	}
	p, part, ok = c.Locate(session, top)
	return c, p, part, ok
}

// logEvent logs e, an action of pr-loop's that has happened, in the
// session's plan (sessionPlan). A failure is reported on stderr and never
// fails the action.
func logEvent(e plans.Event) {
	_, p, part, ok := sessionPlan(e.Kind)
	if !ok {
		return
	}
	if _, err := p.Record(part, e, time.Now()); err != nil {
		fmt.Fprintf(os.Stderr, "pr-loop: %s is not logged in %s: %v\n", e.Kind, p.File(), err)
	}
}

// recordOpened records the pull request at url, which open just opened for
// branch, in the session's plan: in PULL_REQUESTS, linking the plans that
// name it too, and as the push that opened it. open records it here rather
// than leaving it to a shell hook, which Codex's MCP tool call never
// reaches. A failure is reported on stderr and never fails the open.
func recordOpened(branch, url string) {
	c, p, part, ok := sessionPlan("the pull request")
	if !ok {
		return
	}
	now := time.Now()
	added, err := p.AddToProperty(part, "PULL_REQUESTS", url, now)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr-loop: %s is not recorded in %s: %v\n", url, p.File(), err)
	}
	if added {
		if _, err := c.LinkEvidence(p.File(), now); err != nil {
			fmt.Fprintf(os.Stderr, "pr-loop: plans naming %s are not linked to %s: %v\n", url, p.File(), err)
		}
	}
	if _, err := p.Record(part, plans.Event{Kind: "pushed", Branch: branch, Summary: "opened " + url}, now); err != nil {
		fmt.Fprintf(os.Stderr, "pr-loop: the push that opened %s is not logged in %s: %v\n", url, p.File(), err)
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
