package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
)

// rewrite is how a branch's push rewrites what the loop last pushed.
type rewrite struct {
	// why is why the rewrite is not a clean rebase, or "" when it is.
	why string
	// oldRange and newRange are the branch's own commits before and after,
	// as `git range-diff` takes them.
	oldRange, newRange string
	// rebaseOnly is a clean rebase that adds no commit of the branch's own.
	rebaseOnly bool
}

// checkRewrite compares the branch's local tip with entry.RemoteOid, the tip
// the loop last pushed, which it does not descend from, so the push
// rewrites the branch. The branch's own commits as pushed, those after the
// commit of its parent they sat on, must lead its commits now, rebased
// onto parent, in order, by message and author; and the last of them must
// have the files a clean rebase gives: the pushed tip merged, from that old
// parent commit, onto parent. Commits after them are new, held to the
// fixup rule by the push. Anything else, such as a conflict resolved by
// hand or a commit given changes it did not have, is why.
func checkRewrite(state *State, branch, parent, local string) (*rewrite, error) {
	entry := state.Branches[branch]
	mergeBase, err := git.Run("merge-base", entry.RemoteOid, parent)
	if err != nil {
		return nil, err
	}
	listed, err := git.Run("rev-list", "--reverse", mergeBase+".."+entry.RemoteOid)
	if err != nil {
		return nil, err
	}
	old := strings.Fields(listed)
	subjects := map[string]bool{}
	for _, c := range entry.Commits {
		subjects[subject(c.Message)] = true
	}
	first := -1
	for i, sha := range old {
		c, err := history.Log("-1", sha)
		if err != nil {
			return nil, err
		}
		if len(c) == 1 && ownCommit(c[0], subjects) {
			first = i
			break
		}
	}
	out := &rewrite{newRange: parent + ".." + local}
	if first < 0 {
		out.oldRange = mergeBase + ".." + entry.RemoteOid
		out.why = "none of the commits pushed before are the branch's own"
		return out, nil
	}
	onto := old[first] + "^"
	out.oldRange = onto + ".." + entry.RemoteOid
	before, err := commitsBetween(onto, entry.RemoteOid)
	if err != nil {
		return nil, err
	}
	listed, err = git.Run("rev-list", "--reverse", parent+".."+local)
	if err != nil {
		return nil, err
	}
	now := strings.Fields(listed)
	after, err := commitsBetween(parent, local)
	if err != nil {
		return nil, err
	}
	if len(after) < len(before) {
		out.why = fmt.Sprintf("%d commits were pushed, and %d are on the branch now", len(before), len(after))
		return out, nil
	}
	for i := range before {
		if before[i] != after[i] {
			out.why = fmt.Sprintf("%q is now %q", subject(before[i].Message), subject(after[i].Message))
			return out, nil
		}
	}
	merged, err := git.Run("merge-tree", "--write-tree", "--merge-base", onto, parent, entry.RemoteOid)
	if err != nil {
		out.why = "rebasing the commits pushed onto " + parent + " conflicts, so they were resolved by hand"
		return out, nil
	}
	tree, err := git.Run("rev-parse", now[len(before)-1]+"^{tree}")
	if err != nil {
		return nil, err
	}
	if tree != strings.Fields(merged)[0] {
		out.why = "the rebased commits' files differ from what rebasing the commits pushed gives"
		return out, nil
	}
	out.rebaseOnly = len(after) == len(before)
	return out, nil
}

// ownCommit reports whether c is one of the branch's own commits, whose
// subjects are given, or a fixup of one.
func ownCommit(c Commit, subjects map[string]bool) bool {
	kind, target := fold(c.Message)
	if kind == history.Kept {
		return subjects[subject(c.Message)]
	}
	return subjects[target]
}

// recordRewrite writes the range-diff of a rewrite that is not a clean
// rebase beside the loop's state, and records it on the branch.
func recordRewrite(state *State, branch string, rw *rewrite, from, to string) (*Rewrite, error) {
	// The commits keep their messages, so pair them however much their
	// changes differ, which is what the operator has to see.
	diff, err := git.Run("range-diff", "--no-color", "--creation-factor=100", rw.oldRange, rw.newRange)
	if err != nil {
		return nil, err
	}
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "rewrites")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, fmt.Sprintf("%s-%d.range-diff", strings.ReplaceAll(branch, "/", "_"), state.Round+1))
	if err := os.WriteFile(file, []byte(diff+"\n"), 0o600); err != nil {
		return nil, err
	}
	r := Rewrite{Round: state.Round + 1, From: from, To: to, Why: rw.why, File: file}
	state.Branches[branch].Rewrites = append(state.Branches[branch].Rewrites, r)
	return &r, nil
}
