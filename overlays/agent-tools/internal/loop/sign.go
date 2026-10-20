package loop

import (
	"os"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
)

// SignCheck squashes the loop's fixups unsigned in a throwaway worktree and
// checks that every branch would end with exactly its original commits and
// exactly the files the loop left it with, so a conflict, a stray commit or
// a fixup in the wrong branch shows up before the operator's
// `git sign-pass --push`. It changes no branch.
func SignCheck() (any, error) {
	state, err := loadState(true)
	if err != nil {
		return nil, err
	}
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	branches := state.Order
	top := branches[len(branches)-1]
	for i := 1; i < len(branches); i++ {
		if state.Branches[branches[i]].Base != branches[i-1] {
			return nil, refuse("signing needs a linear stack; check each chain separately")
		}
	}
	parent, err := state.parentRef(repo, branches[0])
	if err != nil {
		return nil, err
	}
	upstream, err := git.Run("merge-base", "refs/heads/"+branches[0], parent)
	if err != nil {
		return nil, err
	}
	if merges, _ := git.Run("rev-list", "--merges", upstream+"..refs/heads/"+top); merges != "" {
		return nil, refuse("the stack contains merge commits, which squashing would flatten; hand it to the operator to sign by hand")
	}
	signing := &Signing{Upstream: upstream, Trees: map[string]string{}}
	for _, b := range branches {
		if signing.Trees[b], err = git.Run("rev-parse", "refs/heads/"+b+"^{tree}"); err != nil {
			return nil, err
		}
	}
	if err := rehearse(state, signing); err != nil {
		return nil, err
	}
	return map[string]any{"verified": "squashing the fixups leaves every branch with its original commits and the same files",
		"branches": branches}, nil
}

// Signing is what a squash of the stack must leave: the commit below its
// lowest branch, and each branch's tree.
type Signing struct {
	Upstream string
	Trees    map[string]string
}

// rehearse squashes the stack, unsigned, in a throwaway worktree and checks
// the result, so a conflict or a stray commit shows up before any key touch.
func rehearse(state *State, signing *Signing) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(dir, "rehearsal-")
	if err != nil {
		return err
	}
	os.Remove(scratch)
	top := state.Order[len(state.Order)-1]
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
		"rebase", "-i", "--autosquash", "--force-rebase", signing.Upstream)
	if err != nil {
		if m := couldNotApply.FindStringSubmatch(err.Error()); m != nil {
			return refuse("squashing conflicts at %s %q: retarget that fixup at a later commit (or the PR's last one) and push again", m[1], m[2])
		}
		return refuse("squashing the fixups failed: %v", err)
	}
	// The rehearsal leaves the branches alone, so each branch's end is found
	// by counting its original commits.
	list, err := git.Run("-C", scratch, "rev-list", "--reverse", signing.Upstream+"..HEAD")
	if err != nil {
		return err
	}
	squashed := strings.Fields(list)
	at := 0
	for _, b := range state.Order {
		want := state.Branches[b].Commits
		if at+len(want) > len(squashed) {
			return refuse("squashing leaves %s with fewer commits than it began with", b)
		}
		base := signing.Upstream
		if at > 0 {
			base = squashed[at-1]
		}
		end := squashed[at+len(want)-1]
		if err := matches(want, base, end, b); err != nil {
			return err
		}
		if tree, _ := git.Run("rev-parse", end+"^{tree}"); tree != signing.Trees[b] {
			return refuse("squashing changes %s's files; a fixup is probably in the wrong branch", b)
		}
		at += len(want)
	}
	if at != len(squashed) {
		return refuse("squashing leaves %d commits that belong to no branch", len(squashed)-at)
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
