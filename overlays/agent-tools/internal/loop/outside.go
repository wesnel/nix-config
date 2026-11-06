package loop

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
)

// outside is what the loop reaches beyond this process that a test fakes:
// GitHub, the operator's terminal and the clock. A context carries one for
// the calls made under it; a field it leaves unset, and every field where
// the context carries none, as in production, is the real thing.
type outside struct {
	graphQL func(ctx context.Context, query string, vars map[string]any, out any) error
	// objectsREST creates Git objects, commentsREST posts and deletes
	// top-level comments, stacksREST changes stacks, and mergeREST asks
	// GitHub to merge.
	objectsREST, commentsREST, stacksREST, mergeREST rest
	// requestReviewer asks reviewer to review PR number in slug.
	requestReviewer func(ctx context.Context, slug string, number int, reviewer string) error
	// openTTY opens the operator's terminal.
	openTTY func() (io.ReadWriteCloser, error)
	now     func() time.Time
	sleep   func(time.Duration)
	// verifyTimeout bounds each check of the operator's signatures.
	verifyTimeout time.Duration
}

// rest is a call to GitHub's REST API.
type rest func(ctx context.Context, method, path string, body, out any) error

// stacksVersion is the REST API version GitHub serves stacks under, and
// documents merge-async beside them.
const stacksVersion = "2026-03-10"

type outsideKey struct{}

// reach is the outside of ctx, each field it leaves unset the real one.
func reach(ctx context.Context) outside {
	o, _ := ctx.Value(outsideKey{}).(outside)
	if o.graphQL == nil {
		o.graphQL = gh.GraphQL
	}
	if o.objectsREST == nil {
		o.objectsREST = gh.REST
	}
	if o.commentsREST == nil {
		o.commentsREST = gh.REST
	}
	if o.stacksREST == nil {
		o.stacksREST = func(ctx context.Context, method, path string, body, out any) error {
			return gh.RESTVersion(ctx, stacksVersion, method, path, body, out)
		}
	}
	if o.mergeREST == nil {
		o.mergeREST = o.stacksREST
	}
	if o.requestReviewer == nil {
		o.requestReviewer = func(ctx context.Context, slug string, number int, reviewer string) error {
			cmd := gh.Command(ctx, "pr", "edit", fmt.Sprint(number), "--repo", slug, "--add-reviewer", reviewer)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("%w: %s", err, out)
			}
			return nil
		}
	}
	if o.openTTY == nil {
		o.openTTY = func() (io.ReadWriteCloser, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }
	}
	if o.now == nil {
		o.now = time.Now
	}
	if o.sleep == nil {
		o.sleep = time.Sleep
	}
	if o.verifyTimeout == 0 {
		o.verifyTimeout = 30 * time.Second
	}
	return o
}

// The calls below go to the outside of their context (reach).

func graphQL(ctx context.Context, query string, vars map[string]any, out any) error {
	return reach(ctx).graphQL(ctx, query, vars, out)
}

func objectsREST(ctx context.Context, method, path string, body, out any) error {
	return reach(ctx).objectsREST(ctx, method, path, body, out)
}

func commentsREST(ctx context.Context, method, path string, body, out any) error {
	return reach(ctx).commentsREST(ctx, method, path, body, out)
}

func stacksREST(ctx context.Context, method, path string, body, out any) error {
	return reach(ctx).stacksREST(ctx, method, path, body, out)
}

func mergeREST(ctx context.Context, method, path string, body, out any) error {
	return reach(ctx).mergeREST(ctx, method, path, body, out)
}

func requestReviewer(ctx context.Context, slug string, number int, reviewer string) error {
	return reach(ctx).requestReviewer(ctx, slug, number, reviewer)
}

func openTTY(ctx context.Context) (io.ReadWriteCloser, error) { return reach(ctx).openTTY() }

func now(ctx context.Context) time.Time { return reach(ctx).now() }

func sleep(ctx context.Context, d time.Duration) { reach(ctx).sleep(d) }
