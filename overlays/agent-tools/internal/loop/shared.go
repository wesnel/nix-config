package loop

import (
	"fmt"
	"os"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Refusal is a failed policy check.
type Refusal = refusal.Refusal

// Commit is a commit as the signing pass compares it.
type Commit = history.Commit

func refuse(format string, args ...any) error { return refusal.New(format, args...) }

// routeGate refuses branch at tip while a plan of this host that names it
// owes a review route (plans.RouteGate), and says on stderr which waivers
// and opt-outs let it pass.
func routeGate(branch, tip string) error {
	c, err := plans.Load()
	if err != nil {
		return err
	}
	top, err := git.Toplevel()
	if err != nil {
		return err
	}
	worktree := ""
	if current, _ := git.Run("branch", "--show-current"); current == branch {
		worktree = top
	}
	g, err := c.RouteGate(plans.EnvSession(), top, worktree, branch, tip)
	if err != nil {
		return err
	}
	for _, n := range g.Notes {
		fmt.Fprintf(os.Stderr, "pr-loop: %s\n", n)
	}
	if g.Refusal != "" {
		return refuse("%s", g.Refusal)
	}
	return nil
}

// Refusals shared by several commands, as formats for refuse.
const (
	notHead   = "%s is not the head of an open pull request by %s; name a branch that is, or ask the operator to open one"
	notInLoop = "#%d is not part of this loop; `pr-loop status` lists the loop's pull requests"
)

var (
	commitsBetween = history.Between
	subject        = history.Subject
	fold           = history.Fold
	matches        = history.Match
	couldNotApply  = history.CouldNotApply
	noEditor       = history.NoEditor
	// graphQL and objectsREST, which creates Git objects, are replaced by
	// tests that fake GitHub.
	graphQL     = gh.GraphQL
	objectsREST = gh.REST
)
