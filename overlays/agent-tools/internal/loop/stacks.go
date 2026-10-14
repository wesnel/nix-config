package loop

import (
	"sort"
	"strings"
)

// GitHubStack is GitHub's record of a stack of pull requests, which GitHub
// shows on each of them and merges from the bottom.
type GitHubStack struct {
	Number int           `json:"number"`
	Base   string        `json:"base"`
	PRs    []StackMember `json:"prs"`
}

// StackMember is one pull request of a GitHubStack.
type StackMember struct {
	PR     int    `json:"pr"`
	Branch string `json:"branch"`
	Base   string `json:"base"`
	State  string `json:"state"`
	Draft  bool   `json:"draft"`
	URL    string `json:"url"`
}

// prBranch is the head branch of target, one of prs named by number or by
// branch, or by default the current branch.
func prBranch(repo *Repo, prs map[string]*PR, target string) (string, error) {
	if target == "" {
		var err error
		if target, err = currentBranch(); err != nil {
			return "", err
		}
	} else if n, ok := prNumber(target); ok {
		for b, pr := range prs {
			if pr.Number == n {
				return b, nil
			}
		}
		return "", refuse("#%d is not an open pull request by %s in %s; name one that is, as `pr-loop stack` lists them",
			n, repo.Viewer, repo.Slug)
	}
	if prs[target] == nil {
		return "", refuse(notHead, target, repo.Viewer)
	}
	return target, nil
}

// githubStack is the stack GitHub has pull request number in, bottom first,
// or nil.
func githubStack(repo *Repo, number int) (*GitHubStack, error) {
	owner, name, _ := strings.Cut(repo.Slug, "/")
	var data struct {
		Repository struct {
			PullRequest struct {
				Stack *struct {
					Number      int
					BaseRefName string
					Entries     struct {
						Nodes []struct {
							Position    int
							PullRequest struct {
								Number      int
								HeadRefName string
								BaseRefName string
								State       string
								IsDraft     bool
								URL         string
							}
						}
					}
				}
			}
		}
	}
	err := graphQL(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { pullRequest(number: $number) {
			stack { number baseRefName entries(first: 100) { nodes { position
				pullRequest { number headRefName baseRefName state isDraft url } } } }
		} }
	}`, map[string]any{"owner": owner, "name": name, "number": number}, &data)
	if err != nil || data.Repository.PullRequest.Stack == nil {
		return nil, err
	}
	s := data.Repository.PullRequest.Stack
	nodes := s.Entries.Nodes
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Position < nodes[j].Position })
	stack := &GitHubStack{Number: s.Number, Base: s.BaseRefName, PRs: []StackMember{}}
	for _, n := range nodes {
		pr := n.PullRequest
		stack.PRs = append(stack.PRs, StackMember{PR: pr.Number, Branch: pr.HeadRefName, Base: pr.BaseRefName,
			State: pr.State, Draft: pr.IsDraft, URL: pr.URL})
	}
	return stack, nil
}
