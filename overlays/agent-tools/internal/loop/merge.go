package loop

import (
	"bufio"
	"fmt"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
)

// mergeREST asks GitHub to merge a pull request; tests replace it to fake
// GitHub. GitHub documents merge-async beside the stacks endpoints, under
// the REST API version they are served at.
var mergeREST = func(method, path string, body, out any) error {
	return gh.RESTVersion(stacksVersion, method, path, body, out)
}

// mergeMethods are the merge_method values merge-async takes.
var mergeMethods = []string{"merge", "squash", "rebase"}

// MergeResult is what Merge did.
type MergeResult struct {
	PR      int    `json:"pr"`
	Stack   int    `json:"stack,omitempty"`
	PRs     []int  `json:"prs"`
	Status  string `json:"status,omitempty"`
	UUID    string `json:"uuid,omitempty"`
	Skipped string `json:"skipped,omitempty"`
}

// Merge merges target, an open PR by the user named by number or branch
// (default: the current branch's), together with every open PR below it in
// its GitHub stack, as GitHub's merge-async endpoint does for a stacked PR,
// by method (default merge). It shows those PRs on the operator's terminal
// and asks GitHub only once the operator answers y there, pinning the top's
// head to the one shown. It is the operator's: the guard refuses it to
// agents, and it refuses where no terminal can be opened.
func Merge(target, method string) (any, error) {
	if method == "" {
		method = "merge"
	}
	if !slices.Contains(mergeMethods, method) {
		return nil, refuse("%q is not a merge method GitHub takes; pass `--method` as one of %s",
			method, strings.Join(mergeMethods, ", "))
	}
	tty, err := openTTY()
	if err != nil {
		return nil, refuse("`pr-loop merge` asks the operator on the terminal before merging, and /dev/tty cannot be "+
			"opened here (%v); the operator runs it from a terminal of their own", err)
	}
	defer tty.Close()
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
	// What GitHub merges: the top and, in a stack, every open PR below it.
	merged := []*PR{top}
	into := top.BaseRefName
	if stack != nil {
		merged, into = nil, stack.Base
		for _, m := range stack.PRs[:position(stack, top.Number)+1] {
			if m.State != "OPEN" {
				continue
			}
			pr := prs[m.Branch]
			if pr == nil || pr.Number != m.PR {
				return nil, refuse("#%d is below #%d in GitHub stack #%d and is not an open pull request by %s, so "+
					"pr-loop leaves the stack alone; the operator merges it from GitHub's page instead", m.PR, top.Number, stack.Number, repo.Viewer)
			}
			merged = append(merged, pr)
		}
	}
	result := MergeResult{PR: top.Number, PRs: []int{}}
	if stack != nil {
		result.Stack = stack.Number
	}
	for _, pr := range merged {
		if pr.IsDraft {
			return nil, refuse("#%d is a draft, which GitHub does not merge; mark it ready with `gh pr ready %d`, "+
				"then run `pr-loop merge %d` again", pr.Number, pr.Number, top.Number)
		}
		result.PRs = append(result.PRs, pr.Number)
	}

	fmt.Fprintf(tty, "\n=== Merge into %s, by %s, bottom first:\n", into, method)
	for _, pr := range merged {
		fmt.Fprintf(tty, "  #%d %s: %s\n", pr.Number, pr.HeadRefName, pr.Title)
	}
	fmt.Fprintf(tty, "=== with #%d's head at %s. Merge? [y/N] ", top.Number, top.HeadRefOid)
	answer, err := bufio.NewReader(tty).ReadString('\n')
	if err != nil && answer == "" {
		return nil, fmt.Errorf("reading the answer: %w", err)
	}
	if strings.TrimSpace(answer) != "y" {
		result.Skipped = "not confirmed"
		return result, nil
	}

	var reply struct {
		Status  string `json:"status"`
		Details struct {
			UUID string `json:"uuid"`
		} `json:"details"`
	}
	// sha makes GitHub refuse the merge if the top moved since it was shown.
	body := map[string]any{"merge_method": method, "sha": top.HeadRefOid}
	if err := mergeREST("PUT", fmt.Sprintf("repos/%s/pulls/%d/merge-async", repo.Slug, top.Number), body, &reply); err != nil {
		return nil, fmt.Errorf("asking GitHub to merge #%d: %w", top.Number, err)
	}
	result.Status, result.UUID = reply.Status, reply.Details.UUID
	return result, nil
}
