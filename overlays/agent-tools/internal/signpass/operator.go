package signpass

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/operatorkey"
)

// objectView is the environment, and objectArgs the options, in which git
// reads history from the commit objects alone, as a push sends them:
// replace refs, grafts, a shallow file and the commit-graph, which anyone
// able to write the repository can change, could otherwise show other
// commits or other parents than those pushed.
var (
	objectView = []string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null", "GIT_SHALLOW_FILE=/dev/null"}
	objectArgs = []string{"-c", "core.commitGraph=false"}
)

// objectGit runs git in objectView.
func objectGit(args ...string) (string, error) {
	return git.RunEnv(objectView, append(append([]string{}, objectArgs...), args...)...)
}

// gate refuses the push of r to url, before it, unless url's branch is
// still at r.Old, the lease the push goes on, and every commit the push
// sends carries a good signature by the protected operator key: the commits
// r.New reaches and no branch or tag url has reaches (operatorkey.Verify).
// url's branches and tags are what ls-remote reads now, over the login env
// shares with the push, so it costs no touch of its own. Nothing the
// repository holds decides either: not its remote-tracking refs, its
// pass's state, its signing config or the history it shows, all of which
// anyone able to write the repository can set.
func gate(ctx context.Context, url string, r refPush, env []string) error {
	if !Brief {
		fmt.Fprintf(os.Stderr, "reading what %s has; touch the key if it blinks\n", url)
	}
	listed, err := lsRemote(ctx, url, env)
	if err != nil {
		return err
	}
	at, refs := "", []string{}
	for _, line := range strings.Split(listed, "\n") {
		oid, ref, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if ref == "refs/heads/"+r.Branch {
			at = oid
		}
		refs = append(refs, oid)
	}
	if at != r.Old {
		return refuse("%s's %s is at %.12s, not at %.12s where the pass leased its push, so nothing was pushed there; "+
			"look at what moved it before pushing again, and report it to the operator", url, r.Branch, orNone(at), orNone(r.Old))
	}
	// A ref whose object this repository lacks says nothing of the commits
	// it has.
	present, err := git.BytesContext(git.WithEnv(ctx, objectView...), []byte(strings.Join(refs, "\n")+"\n"),
		"cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return err
	}
	var have []string
	for _, line := range strings.Split(string(present), "\n") {
		if oid, kind, _ := strings.Cut(line, " "); kind == "commit" || kind == "tag" {
			have = append(have, oid)
		}
	}
	if _, err := operatorkey.Verify(ctx, r.New, have); err != nil {
		return fmt.Errorf("the push of %s to %s would send a commit the operator's key did not sign, so nothing was pushed there: %w",
			r.Branch, url, err)
	}
	return nil
}

func orNone(oid string) string {
	if oid == "" {
		return "nothing"
	}
	return oid
}

// lsRemote is url's branches and tags, as ls-remote prints them, asking
// again when the key refuses the login (retrier).
func lsRemote(ctx context.Context, url string, env []string) (string, error) {
	what := "reading what " + url + " has"
	retry := &retrier{}
	run := ctx
	for {
		out, err := git.BytesContext(git.WithEnv(run, env...), nil, "ls-remote", "--heads", "--tags", url)
		if err == nil {
			return string(out), nil
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("%s was interrupted", what)
		}
		var done context.CancelFunc
		var again bool
		if run, done, again = retry.next(ctx, what, "", err.Error()); !again {
			return "", fmt.Errorf("%s failed%s: %w", what, retry.locked(), err)
		}
		defer done()
	}
}
