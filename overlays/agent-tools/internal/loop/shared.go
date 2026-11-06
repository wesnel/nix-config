package loop

import (
	"context"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
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
	commitsBetween = func(ctx context.Context, base, tip string) ([]Commit, error) {
		return history.Between(git.WithDir(ctx), base, tip)
	}
	subject = history.Subject
	fold    = history.Fold
	matches = func(ctx context.Context, want []Commit, base, tip, branch string) error {
		return history.Match(git.WithDir(ctx), want, base, tip, branch)
	}
	couldNotApply = history.CouldNotApply
	noEditor      = history.NoEditor
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

// mode is the loop's mode in ctx's Env.
func mode(ctx context.Context) string { return env.From(ctx).Get(ModeEnv) }

// requireMode refuses command outside a loop in mode want.
func requireMode(ctx context.Context, command, want string) error {
	if mode(ctx) != want {
		return refuse("`pr-loop %s` runs only in a %s-mode review loop. Only the operator starts a loop, with "+
			"`review-loop --harness claude|codex --bots|--people`; outside one, put what you would do in the report", command, want)
	}
	return nil
}
