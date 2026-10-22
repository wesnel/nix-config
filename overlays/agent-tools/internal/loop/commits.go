package loop

import (
	"fmt"
	"strings"

	"github.com/wesnel/agent-tools/internal/history"
)

// checkFixups enforces that everything the loop added to a branch is a fixup!
// of one of the PR's own commits, so the signing pass can squash the branch
// back to exactly those commits. amend! and squash! are refused because they
// rewrite the PR's commit messages, which the signing pass then refuses.
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
		return refuse("%s has commits that are not fixups: the loop's own commits must be "+
			"`git commit --fixup=<commit>` of the PR's commits, so the signing pass can squash them", branch)
	}
	for i := range kept {
		if kept[i] != original[i] {
			return refuse("%s's commit %q no longer matches %q from when the loop began, and the loop never rewrites "+
				"the PR's own commits; put the branch back as it was (refs/pr-loop/rounds/ holds each round's tips) "+
				"and commit the change as `git commit --fixup=<commit>`",
				branch, subject(kept[i].Message), subject(original[i].Message))
		}
	}
	return nil
}
