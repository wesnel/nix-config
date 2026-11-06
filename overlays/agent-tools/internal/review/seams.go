package review

import (
	"context"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/loop"
	"github.com/wesnel/agent-tools/internal/upgrade"
)

// seams are what a review reaches GitHub, the upstream of a pin and the
// clock through, which tests stand in for. Production uses
// productionSeams; a test gives its own in the context it hands the code
// under test, so tests that run at once each keep theirs.
type seams struct {
	// graphQL asks GitHub's GraphQL API in ctx's Env; ctx's end does not
	// stop it, as before it took one.
	graphQL func(ctx context.Context, query string, variables map[string]any, out any) error
	// remoteURL is where a pull request of slug is fetched from.
	remoteURL func(slug string) string
	// transport is the one protocol git may fetch a pull request over.
	transport string
	// refreshThreads reads a pull request's threads and writes them to its
	// record, pr-loop threads' writer, in ctx's Env. ctx's end does not stop
	// it, as before it took one.
	refreshThreads func(ctx context.Context, slug string, number int) ([]loop.Thread, error)
	// mermaid renders a diagram's stored JSON as Mermaid, with the renderer
	// internal/diagram builds from numbered IDs and labels Mermaid can only
	// draw; where it is nil, or fails, a diagram is shown without it.
	mermaid func(string) (string, error)
	// upgradeOptions are how an upgrade review gathers upstream text.
	upgradeOptions func(context.Context) upgrade.Options
	// scratchRoots are the directories outside the repository a question
	// may be written in.
	scratchRoots func(context.Context) []string
	// now is the clock the overlap list's budget is kept by.
	now func() time.Time
	// prListLimit is the most open pull requests gh lists, newest first,
	// for the overlap list.
	prListLimit int
}

// productionSeams are a review's seams outside tests.
func productionSeams() *seams {
	return &seams{
		graphQL: func(ctx context.Context, query string, variables map[string]any, out any) error {
			return gh.GraphQL(context.WithoutCancel(ctx), query, variables, out)
		},
		remoteURL: func(slug string) string { return "https://github.com/" + slug + ".git" },
		transport: "https",
		refreshThreads: func(ctx context.Context, slug string, number int) ([]loop.Thread, error) {
			return loop.RefreshThreads(context.WithoutCancel(ctx), slug, number)
		},
		mermaid:        diagramMermaid,
		upgradeOptions: upgrade.DefaultOptions,
		scratchRoots:   guard.ScratchRoots,
		now:            time.Now,
		prListLimit:    200,
	}
}

// seamsKey is the context key of a test's seams.
type seamsKey struct{}

// seamsOf are the seams ctx carries, which only a test sets, or else
// production's.
func seamsOf(ctx context.Context) *seams {
	if s, ok := ctx.Value(seamsKey{}).(*seams); ok {
		return s
	}
	return productionSeams()
}
