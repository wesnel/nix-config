package loop

import (
	"fmt"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
)

// checkFixups enforces that everything added to a ready PR's branch is a
// fixup! of one of the PR's own commits, original, so the signing pass can
// squash the branch back to exactly those commits. amend! and squash! are
// refused because they rewrite the PR's commit messages, which the signing
// pass then refuses.
func checkFixups(branch string, original, now []Commit) error {
	subjects := map[string]bool{}
	for _, c := range original {
		subjects[subject(c.Message)] = true
	}
	var kept []Commit
	var stray []string
	for _, c := range now {
		kind, target := fold(c.Message)
		switch {
		case kind == history.Kept:
			kept = append(kept, c)
		case kind != history.Fixup:
			stray = append(stray, fmt.Sprintf("%q would rewrite a commit message; use `git commit --fixup=<commit>`", subject(c.Message)))
		case !subjects[target]:
			stray = append(stray, fmt.Sprintf("%q fixes up a commit that is not one of %s's", subject(c.Message), branch))
		default:
			if problems := history.LintMessage(c.Message); len(problems) > 0 {
				stray = append(stray, fmt.Sprintf("%q has a message where %s; its review details go in Review-* trailers alone",
					subject(c.Message), strings.Join(problems, ", and ")))
			}
		}
	}
	if len(stray) > 0 {
		return refuse("%s; recommit each as `git commit --fixup=<commit>` against a commit of its own branch", strings.Join(stray, "; "))
	}
	if len(kept) != len(original) {
		return refuse("%s has commits that are not fixups, and a ready PR gains only fixups of its own commits, which the "+
			"signing pass squashes; recommit each as `git commit --fixup=<commit>`, or say in the report why the PR "+
			"needs a commit of its own", branch)
	}
	for i := range kept {
		if kept[i] != original[i] {
			return refuse("%s's commit %q no longer matches %q on GitHub, and a push never rewrites a ready PR's own "+
				"commits; put the branch back as GitHub has it and commit the change as `git commit --fixup=<commit>`",
				branch, subject(kept[i].Message), subject(original[i].Message))
		}
	}
	return nil
}

// checkPatches holds a ready PR's own commits to the changes they make on
// GitHub, which checkFixups cannot see: it matches commits by message, and
// an amend keeps the message. Each own commit in localBase..localTip must
// be its counterpart in remoteBase..remoteTip, or have the same non-empty
// change as it; a rebase keeps the change, so a lease that only moves the
// commits passes.
func checkPatches(branch, remoteBase, remoteTip, localBase, localTip string) error {
	before, err := ownPatches(remoteBase, remoteTip)
	if err != nil {
		return err
	}
	now, err := ownPatches(localBase, localTip)
	if err != nil {
		return err
	}
	if len(before) != len(now) {
		return refuse("%s has %d commits of its own, and %d on GitHub; a ready PR gains only fixups of its own commits, "+
			"which the signing pass squashes, so recommit the change as `git commit --fixup=<commit>`", branch, len(now), len(before))
	}
	for i := range now {
		// An empty change matches nothing: it says nothing of what the
		// commit holds.
		same := now[i].sha == before[i].sha || now[i].id != "" && now[i].id == before[i].id
		if !same {
			return refuse("%s's commit %q changes other lines than it does on GitHub, and a push never rewrites a ready PR's "+
				"own commits; put the branch back as GitHub has it and commit the change as `git commit --fixup=<commit>`",
				branch, now[i].subject)
		}
	}
	return nil
}

// patch is a commit and the id of its change: `git patch-id --verbatim` of
// its diff against each parent in turn, so whitespace counts, a merge's
// resolution counts, and only line numbers, which a rebase moves, do not.
// Binary files are diffed whole, and renames as a delete and an add.
type patch struct{ sha, subject, id string }

// ownPatches are base..tip's commits but fixups, oldest first; a commit
// that changes nothing has an empty id.
func ownPatches(base, tip string) ([]patch, error) {
	list, err := git.Run("rev-list", "--reverse", base+".."+tip)
	if err != nil {
		return nil, err
	}
	var out []patch
	for _, sha := range strings.Fields(list) {
		message, err := git.Run("log", "-1", "--format=%B", sha)
		if err != nil {
			return nil, err
		}
		if kind, _ := fold(message); kind != history.Kept {
			continue
		}
		id, err := changeID(sha)
		if err != nil {
			return nil, err
		}
		out = append(out, patch{sha, subject(message), id})
	}
	return out, nil
}

// changeID is patch's id for the commit sha.
func changeID(sha string) (string, error) {
	line, err := git.Run("rev-list", "--parents", "-n", "1", sha)
	if err != nil {
		return "", err
	}
	parents := strings.Fields(line)[1:]
	// diff-tree, with diff.relative off and every submodule compared, so
	// neither the repository's config, the directory it runs in nor the
	// checked-out .gitmodules can hide a change.
	diffTree := []string{"-c", "diff.relative=false", "diff-tree", "-p", "--no-relative", "--ignore-submodules=none",
		"--no-color", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "--full-index"}
	var diffs [][]string
	if len(parents) == 0 {
		diffs = append(diffs, append(append([]string{}, diffTree...), "--root", sha))
	}
	for _, p := range parents {
		diffs = append(diffs, append(append([]string{}, diffTree...), p, sha))
	}
	var ids []string
	for _, args := range diffs {
		diff, err := git.Bytes(nil, args...)
		if err != nil {
			return "", err
		}
		out, err := git.Bytes(diff, "patch-id", "--verbatim")
		if err != nil {
			return "", err
		}
		id, _, _ := strings.Cut(string(out), " ")
		if id = strings.TrimSpace(id); id == "" {
			return "", nil
		}
		ids = append(ids, id)
	}
	return strings.Join(ids, " "), nil
}
