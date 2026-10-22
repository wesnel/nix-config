package loop

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
)

// stacksVersion is the REST API version GitHub serves stacks under.
const stacksVersion = "2026-03-10"

// stacksREST changes stacks; tests replace it to fake GitHub.
var stacksREST = func(method, path string, body, out any) error {
	return gh.RESTVersion(stacksVersion, method, path, body, out)
}

// Link puts open PRs by the user, named by number or branch bottom first and
// each based on the one before, into one GitHub stack: onto the top of the
// stack the lowest is already in or is based on, or else into a new one.
// PRs already in that stack, in that order, are left as they are.
func Link(targets []string) (any, error) {
	if len(targets) == 0 {
		return nil, errors.New("link takes the PRs to stack, bottom first; see pr-loop --help")
	}
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	prs, err := openPRs(repo)
	if err != nil {
		return nil, err
	}
	var chain []*PR
	for _, t := range targets {
		branch, err := prBranch(repo, prs, t)
		if err != nil {
			return nil, err
		}
		pr, err := checkBranch(repo, prs, branch)
		if err != nil {
			return nil, err
		}
		chain = append(chain, pr)
	}
	if err := loopScope(chain, 0); err != nil {
		return nil, err
	}
	return link(repo, prs, chain)
}

// loopScope keeps a loop session's stacking to the loop's PRs, besides the
// PR numbered opened, which the session has just opened.
func loopScope(chain []*PR, opened int) error {
	if !inLoopSession() {
		return nil
	}
	state, err := loadState(true)
	if err != nil {
		return err
	}
	registered := state.prBranches()
	for _, pr := range chain {
		if _, ok := registered[pr.Number]; !ok && pr.Number != opened {
			return refuse("#%d is not part of this loop, and a loop session stacks only the loop's pull requests; "+
				"put `pr-loop link %s` in the report for the operator to run", pr.Number, numbersOf(chain))
		}
	}
	return nil
}

func numbersOf(chain []*PR) string {
	words := make([]string, len(chain))
	for i, pr := range chain {
		words[i] = fmt.Sprint(pr.Number)
	}
	return strings.Join(words, " ")
}

// link puts chain, bottom first, into one GitHub stack, as Link describes.
func link(repo *Repo, prs map[string]*PR, chain []*PR) (map[string]any, error) {
	for i := 1; i < len(chain); i++ {
		below, pr := chain[i-1], chain[i]
		if pr.BaseRefName != below.HeadRefName {
			return nil, refuse("#%d is based on %s, not on %s, the branch of #%d, and a GitHub stack is a chain of pull requests "+
				"each based on the one below; name them bottom first, or change the base with `gh pr edit %d --base %s` "+
				"once %s is rebased onto %s", pr.Number, pr.BaseRefName, below.HeadRefName, below.Number,
				pr.Number, below.HeadRefName, pr.HeadRefName, below.HeadRefName)
		}
	}
	stacks := make([]*GitHubStack, len(chain))
	for i, pr := range chain {
		var err error
		if stacks[i], err = githubStack(repo, pr.Number); err != nil {
			return nil, err
		}
	}
	// chain[:k] is already in a stack, and chain[k:] is to be added.
	k := 0
	for k < len(chain) && stacks[k] != nil {
		k++
	}
	for i := k; i < len(chain); i++ {
		if stacks[i] != nil {
			return nil, refuse("#%d is in GitHub stack #%d, but #%d below it is in none, and a stack is a single line; "+
				"`pr-loop stack %d` shows it, and unstacking is the operator's call", chain[i].Number, stacks[i].Number,
				chain[k].Number, chain[i].Number)
		}
	}
	var target *GitHubStack
	if k > 0 {
		target = stacks[0]
		at := position(target, chain[0].Number)
		for i := 1; i < k; i++ {
			if stacks[i].Number != target.Number || at+i >= len(target.PRs) || target.PRs[at+i].PR != chain[i].Number {
				return nil, refuse("#%d and #%d are not next to each other in one GitHub stack; `pr-loop stack %d` shows "+
					"GitHub's stacks, and unstacking is the operator's call", chain[i-1].Number, chain[i].Number, chain[i].Number)
			}
		}
		if k == len(chain) {
			return map[string]any{"stack": target.Number, "added": []int{}}, nil
		}
		if top := chain[k-1]; at+k != len(target.PRs) { //nolint:gosec // 0 < k <= len(chain) in this branch
			return nil, refuse(aboveTop, top.Number, target.PRs[at+k].PR, target.Number, top.Number, target.Number)
		}
	} else if below := prs[chain[0].BaseRefName]; below != nil {
		s, err := githubStack(repo, below.Number)
		if err != nil {
			return nil, err
		}
		if s != nil {
			if at := position(s, below.Number); at+1 != len(s.PRs) {
				return nil, refuse(aboveTop, below.Number, s.PRs[at+1].PR, s.Number, below.Number, s.Number)
			}
			target = s
		}
	}
	added := []int{}
	for _, pr := range chain[k:] {
		added = append(added, pr.Number)
	}
	body := map[string]any{"pull_requests": added}
	if target == nil {
		if len(added) < 2 {
			return nil, refuse("#%d is in no GitHub stack, and neither is a pull request it is based on, so it would be "+
				"a stack of one; name the pull requests to stack bottom first, as `pr-loop link LOWER UPPER`", added[0])
		}
		var created struct{ Number int }
		if err := stackWrite("POST", fmt.Sprintf("repos/%s/stacks", repo.Slug), body, &created); err != nil {
			return nil, err
		}
		return map[string]any{"stack": created.Number, "added": added, "created": true}, nil
	}
	// Adding changes the whole stack, so each of its open PRs must be one
	// the loop may write to.
	for _, m := range target.PRs {
		if pr := prs[m.Branch]; m.State == "OPEN" && (pr == nil || pr.Number != m.PR) {
			return nil, refuse("GitHub stack #%d holds #%d, which is not an open pull request by %s, so pr-loop leaves "+
				"the stack alone; name the stack in the report for the operator", target.Number, m.PR, repo.Viewer)
		}
	}
	if err := stackWrite("POST", fmt.Sprintf("repos/%s/stacks/%d/add", repo.Slug, target.Number), body, nil); err != nil {
		return nil, err
	}
	return map[string]any{"stack": target.Number, "added": added}, nil
}

// aboveTop is the refusal to stack onto a PR that is not its stack's top.
const aboveTop = "#%d has #%d above it in GitHub stack #%d, and a stack is a single line; base the pull request on " +
	"the top of that stack instead of #%d, or name the stack (#%d) in the report for the operator"

// position is where number is in s, or -1.
func position(s *GitHubStack, number int) int {
	for i, m := range s.PRs {
		if m.PR == number {
			return i
		}
	}
	return -1
}

// stackWrite changes a stack, trying once more when GitHub answers 409
// because another request is changing the stack.
func stackWrite(method, path string, body, out any) error {
	err := stacksREST(method, path, body, out)
	var status *gh.StatusError
	if errors.As(err, &status) && status.Code == 409 {
		sleep(2 * time.Second)
		err = stacksREST(method, path, body, out)
	}
	return err
}

// stackOpened puts the PR numbered number, which open has just opened for
// branch on base, into the GitHub stack of the PR whose head is base. It
// returns what to add to open's result: the stack, or why the PR is in none
// and how to stack it later, since the PR stays open either way.
func stackOpened(repo *Repo, branch, base string, number int) map[string]any {
	if base == repo.Default {
		return nil
	}
	command := fmt.Sprintf("pr-loop link %s %d", base, number)
	later := func(err error) map[string]any {
		reason := err.Error()
		if !strings.Contains(reason, command) {
			reason += "; the pull request is open, in no GitHub stack, and `" + command + "` stacks it later"
		}
		return map[string]any{"unstacked": reason}
	}
	prs, err := openPRs(repo)
	if err != nil {
		return later(err)
	}
	below, err := checkBranch(repo, prs, base)
	if err != nil {
		return later(err)
	}
	command = fmt.Sprintf("pr-loop link %d %d", below.Number, number)
	opened := &PR{Number: number, HeadRefName: branch, BaseRefName: base}
	opened.Author.Login = repo.Viewer
	// Search may not list a PR this new.
	prs[branch] = opened
	chain := []*PR{below, opened}
	if err := loopScope(chain, number); err != nil {
		return later(err)
	}
	stacked, err := link(repo, prs, chain)
	if err != nil {
		return later(err)
	}
	return map[string]any{"stack": stacked}
}

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
