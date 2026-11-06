package loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/operatorkey"
)

// verifyContext bounds one check of the operator's signatures by the
// verification timeout ctx carries, 30 s unless a test sets another, and does
// not end when ctx does.
func verifyContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), reach(ctx).verifyTimeout)
}

// ownCommits authenticates the PR's actual range, not the mutable tracking
// refs used to avoid uploading objects GitHub already has.
func ownCommits(ctx context.Context, branch, base, tip string) error {
	ctx, cancel := verifyContext(ctx)
	defer cancel()
	n, err := operatorkey.Verify(ctx, tip, []string{base})
	if err == nil && n == 0 {
		err = refuse("there are no commits beyond the GitHub base to authorize")
	}
	if err != nil {
		return fmt.Errorf("cannot authorize %s's existing GitHub branch: %w", branch, err)
	}
	return nil
}

// operatorFormatSigned is what ownCommits checks short of verifying the
// signatures, which needs files it writes: the protected operator profile
// loads, and every commit in base..tip, read from the objects alone, carries
// a signature in its format. It writes nothing, for pr-loop next; open
// still proves them.
func operatorFormatSigned(ctx context.Context, branch, base, tip string) error {
	ctx, cancel := verifyContext(ctx)
	defer cancel()
	ctx = git.KillingTree(ctx)
	err := func() error {
		p, err := operatorkey.Load(ctx)
		if err != nil {
			return err
		}
		objects := []string{"GIT_GRAFT_FILE=/dev/null", "GIT_SHALLOW_FILE=/dev/null"}
		out, err := operatorkey.Git(ctx, objects, nil, "-c", "core.commitGraph=false", "rev-list", base+".."+tip)
		if err != nil {
			return err
		}
		commits := strings.Fields(string(out))
		if len(commits) == 0 {
			return refuse("there are no commits beyond the GitHub base to authorize")
		}
		for _, commit := range commits {
			raw, err := operatorkey.Git(ctx, objects, nil, "cat-file", "commit", commit)
			if err != nil {
				return err
			}
			if !operatorkey.SignedIn(raw, p.Format()) {
				return refuse("commit %.12s has no signature in the operator's signing format", commit)
			}
		}
		return nil
	}()
	if err != nil {
		return fmt.Errorf("cannot authorize %s's existing GitHub branch: %w", branch, err)
	}
	return nil
}
