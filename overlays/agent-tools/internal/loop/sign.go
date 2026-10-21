package loop

import (
	"os"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
)

// SignCheck squashes the fixups of the stack containing HEAD, as
// `pr-loop stack` chains it, unsigned in a throwaway worktree and checks
// that every branch would end with exactly its own commits, those that are
// no fixup, and exactly the files it has now, so a conflict, a stray commit
// or a fixup in the wrong branch shows up before the operator's
// `git sign-pass --push`. It changes no branch.
func SignCheck() (any, error) {
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	prs, err := openPRs(repo)
	if err != nil {
		return nil, err
	}
	current, err := currentBranch()
	if err != nil {
		return nil, err
	}
	if prs[current] == nil {
		return nil, refuse(notHead, current, repo.Viewer)
	}
	branches := stackOrder(prs, current)
	for i := 1; i < len(branches); i++ {
		if prs[branches[i]].BaseRefName != branches[i-1] {
			return nil, refuse("signing needs a linear stack, and %s's has branches side by side; check out each chain's top and check it separately", current)
		}
	}
	for _, b := range branches {
		if !git.OK("rev-parse", "--verify", "-q", "refs/heads/"+b) {
			return nil, refuse("no local branch %s, which is in the stack; check it out, then check again", b)
		}
	}
	parent, err := trackingRef(repo, prs[branches[0]].BaseRefName)
	if err != nil {
		return nil, err
	}
	upstream, err := git.Run("merge-base", "refs/heads/"+branches[0], parent)
	if err != nil {
		return nil, err
	}
	top := branches[len(branches)-1]
	if merges, _ := git.Run("rev-list", "--merges", upstream+"..refs/heads/"+top); merges != "" {
		return nil, refuse("the stack contains merge commits, which squashing would flatten; hand it to the operator to sign by hand")
	}
	stack := &signedStack{Order: branches, Upstream: upstream, Trees: map[string]string{}, Commits: map[string][]Commit{}}
	below := upstream
	for _, b := range branches {
		if stack.Trees[b], err = git.Run("rev-parse", "refs/heads/"+b+"^{tree}"); err != nil {
			return nil, err
		}
		commits, err := commitsBetween(below, "refs/heads/"+b)
		if err != nil {
			return nil, err
		}
		stack.Commits[b] = own(commits)
		below = "refs/heads/" + b
	}
	if err := rehearse(stack); err != nil {
		return nil, err
	}
	return map[string]any{"verified": "squashing the fixups leaves every branch with its own commits and the same files",
		"branches": branches}, nil
}

// signedStack is what a squash of a linear stack must leave: the commit
// below its lowest branch, and each branch, bottom first, with its own
// commits and its tree.
type signedStack struct {
	Order    []string
	Upstream string
	Trees    map[string]string
	Commits  map[string][]Commit
}

// rehearse squashes the stack, unsigned, in a throwaway worktree and checks
// the result, so a conflict or a stray commit shows up before any key touch.
func rehearse(stack *signedStack) error {
	scratch, err := os.MkdirTemp("", "pr-loop-rehearsal-")
	if err != nil {
		return err
	}
	os.Remove(scratch)
	top := stack.Order[len(stack.Order)-1]
	if _, err := git.Run("worktree", "add", "--detach", scratch, "refs/heads/"+top); err != nil {
		return err
	}
	defer func() {
		git.Run("-C", scratch, "rebase", "--abort")
		git.Run("worktree", "remove", "--force", scratch)
	}()
	// rebase.updateRefs off: a user's rebase.updateRefs=true would move every
	// branch of the stack not checked out elsewhere onto these unsigned
	// commits.
	_, err = git.RunEnv(noEditor, "-C", scratch, "-c", "commit.gpgsign=false", "-c", "rebase.updateRefs=false",
		"rebase", "-i", "--autosquash", "--force-rebase", stack.Upstream)
	if err != nil {
		if m := couldNotApply.FindStringSubmatch(err.Error()); m != nil {
			return refuse("squashing conflicts at %s %q: retarget that fixup at a later commit (or the PR's last one) and push again", m[1], m[2])
		}
		return refuse("squashing the fixups failed: %v", err)
	}
	// The rehearsal leaves the branches alone, so each branch's end is found
	// by counting its own commits.
	list, err := git.Run("-C", scratch, "rev-list", "--reverse", stack.Upstream+"..HEAD")
	if err != nil {
		return err
	}
	squashed := strings.Fields(list)
	at := 0
	for _, b := range stack.Order {
		want := stack.Commits[b]
		if len(want) == 0 {
			return refuse("%s has no commits of its own, only fixups; aim them at the commits they fix in their own branches", b)
		}
		if at+len(want) > len(squashed) {
			return refuse("squashing leaves %s with fewer commits than it has of its own", b)
		}
		base := stack.Upstream
		if at > 0 {
			base = squashed[at-1]
		}
		end := squashed[at+len(want)-1]
		if err := matches(want, base, end, b); err != nil {
			return err
		}
		if tree, _ := git.Run("rev-parse", end+"^{tree}"); tree != stack.Trees[b] {
			return refuse("squashing changes %s's files; a fixup is probably in the wrong branch", b)
		}
		at += len(want)
	}
	if at != len(squashed) {
		return refuse("squashing leaves %d commits that belong to no branch", len(squashed)-at)
	}
	return nil
}
