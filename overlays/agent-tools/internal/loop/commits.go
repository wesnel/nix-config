package loop

import (
	"fmt"
	"strings"
)

// checkFixups enforces that everything the loop added to a branch is a fixup
// of one of the PR's own commits, so the signing pass can squash the branch
// back to exactly those commits.
func checkFixups(branch string, original, now []Commit) error {
	subjects := map[string]bool{}
	for _, c := range original {
		subjects[subject(c.Message)] = true
	}
	var kept []Commit
	var stray []string
	for _, c := range now {
		target := fixupTarget(c.Message)
		switch {
		case target == "":
			kept = append(kept, c)
		case !subjects[target]:
			stray = append(stray, fmt.Sprintf("%q fixes up a commit that is not one of %s's", subject(c.Message), branch))
		}
	}
	if len(stray) > 0 {
		return refuse("%s", strings.Join(stray, "; "))
	}
	if len(kept) != len(original) {
		return refuse("%s has commits that are not fixups: the loop's own commits must be "+
			"`git commit --fixup=<commit>` of the PR's commits, so the signing pass can squash them", branch)
	}
	for i := range kept {
		if kept[i] != original[i] {
			return refuse("%s's commit %q no longer matches %q from when the loop began; "+
				"the loop only adds fixups and never rewrites the PR's own commits",
				branch, subject(kept[i].Message), subject(original[i].Message))
		}
	}
	return nil
}
