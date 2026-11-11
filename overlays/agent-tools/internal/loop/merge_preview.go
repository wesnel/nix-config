package loop

import (
	"fmt"
	"strings"
)

// MergePlan lists the open downstack PRs GitHub would merge, bottom first.
type MergePlan struct {
	Repository  string        `json:"repository"`
	PR          int           `json:"pr"`
	Stack       int           `json:"stack,omitempty"`
	Destination string        `json:"destination"`
	PRs         []MergePlanPR `json:"prs"`
}

// MergePlanPR preserves GitHub's readiness states; absent states are UNKNOWN.
type MergePlanPR struct {
	PR
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
	CIState          string `json:"ciState"`
}

// MergePreview reads GitHub without prompting or changing local or remote state.
func MergePreview(target string) (*MergePlan, error) {
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	prs, err := openPRs(repo)
	if err != nil {
		return nil, err
	}
	branch, err := prBranch(repo, prs, target)
	if err != nil {
		return nil, err
	}
	top, err := checkBranch(repo, prs, branch)
	if err != nil {
		return nil, err
	}
	stack, err := githubStack(repo, top.Number)
	if err != nil {
		return nil, err
	}
	if stack != nil && position(stack, top.Number) < 0 {
		return nil, refuse("GitHub's stack no longer contains #%d; run `agent-pr merge-preview` again", top.Number)
	}
	members, into, err := mergeTargets(repo, prs, top, stack)
	if err != nil {
		return nil, err
	}
	plan := &MergePlan{Repository: repo.Slug, PR: top.Number, Destination: into, PRs: []MergePlanPR{}}
	if stack != nil {
		plan.Stack = stack.Number
	}
	for _, member := range members {
		if _, err := checkBranch(repo, prs, member.HeadRefName); err != nil {
			return nil, err
		}
		pr, err := mergeReadiness(repo.Slug, member)
		if err != nil {
			return nil, err
		}
		plan.PRs = append(plan.PRs, pr)
	}
	return plan, nil
}

func mergeReadiness(slug string, member *PR) (MergePlanPR, error) {
	owner, name, _ := strings.Cut(slug, "/")
	var data struct {
		Repository struct {
			PullRequest struct {
				HeadRefOid                                  string
				Mergeable, MergeStateStatus, ReviewDecision string
				Commits                                     struct {
					Nodes []struct {
						Commit struct {
							Oid               string
							StatusCheckRollup *struct{ State string }
						}
					}
				}
			}
		}
	}
	err := graphQL(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { pullRequest(number: $number) {
			headRefOid mergeable mergeStateStatus reviewDecision
			commits(last: 1) { nodes { commit { oid statusCheckRollup { state } } } }
		} }
	}`, map[string]any{"owner": owner, "name": name, "number": member.Number}, &data)
	if err != nil {
		return MergePlanPR{}, err
	}
	status := data.Repository.PullRequest
	if status.HeadRefOid == "" || status.HeadRefOid != member.HeadRefOid || len(status.Commits.Nodes) != 1 || status.Commits.Nodes[0].Commit.Oid != status.HeadRefOid {
		return MergePlanPR{}, refuse("readiness for #%d has a missing or changed head commit; run `agent-pr merge-preview %d` again", member.Number, member.Number)
	}
	pr := MergePlanPR{PR: *member, Mergeable: status.Mergeable, MergeStateStatus: status.MergeStateStatus, ReviewDecision: status.ReviewDecision, CIState: "UNKNOWN"}
	if pr.Mergeable == "" {
		pr.Mergeable = "UNKNOWN"
	}
	if pr.MergeStateStatus == "" {
		pr.MergeStateStatus = "UNKNOWN"
	}
	if pr.ReviewDecision == "" {
		pr.ReviewDecision = "UNKNOWN"
	}
	if rollup := status.Commits.Nodes[0].Commit.StatusCheckRollup; rollup != nil && rollup.State != "" {
		pr.CIState = rollup.State
	}
	return pr, nil
}

func sameMergeSelection(before, after *MergePlan) bool {
	if before.Repository != after.Repository || before.PR != after.PR || before.Stack != after.Stack || before.Destination != after.Destination || len(before.PRs) != len(after.PRs) {
		return false
	}
	for i, pr := range before.PRs {
		other := after.PRs[i]
		if pr.Number != other.Number || pr.ID != other.ID || pr.HeadRefName != other.HeadRefName || pr.HeadRefOid != other.HeadRefOid || pr.BaseRefName != other.BaseRefName {
			return false
		}
	}
	return true
}

func recheckMergePlan(plan *MergePlan) (*MergePlan, error) {
	fresh, err := MergePreview(fmt.Sprint(plan.PR))
	if err != nil {
		return nil, err
	}
	if !sameMergeSelection(plan, fresh) {
		return nil, refuse("the pull requests, heads or bases changed after confirmation; run `agent-pr merge-preview %d` and confirm again", plan.PR)
	}
	return fresh, nil
}
