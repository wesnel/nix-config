package loop

import (
	"os"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Refusal is a failed policy check.
type Refusal = refusal.Refusal

// Commit is a commit as the signing pass compares it.
type Commit = history.Commit

func refuse(format string, args ...any) error { return refusal.New(format, args...) }

// Refusals shared by several commands, as formats for refuse.
const (
	notHead = "%s is not the head of an open pull request by %s; name a branch that is, or ask the operator to open one"
)

var (
	commitsBetween = history.Between
	subject        = history.Subject
	fold           = history.Fold
	matches        = history.Match
	couldNotApply  = history.CouldNotApply
	noEditor       = history.NoEditor
	// graphQL, objectsREST, which creates Git objects, and commentsREST,
	// which posts and deletes top-level comments, are replaced by tests that
	// fake GitHub.
	graphQL      = gh.GraphQL
	objectsREST  = gh.REST
	commentsREST = gh.REST
)

// ModeEnv is the loop's mode, bots or people, which only the operator's
// review-loop sets: the guard refuses an agent's assignment of it.
const ModeEnv = "PR_LOOP_MODE"

// requireMode refuses command outside a loop in mode want.
func requireMode(command, want string) error {
	if os.Getenv(ModeEnv) != want {
		return refuse("`pr-loop %s` runs only in a %s-mode review loop. Only the operator starts a loop, with "+
			"`review-loop --harness claude|codex --bots|--people`; outside one, put what you would do in the report", command, want)
	}
	return nil
}
