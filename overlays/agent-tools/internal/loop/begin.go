package loop

import (
	"fmt"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
)

// StackEntry describes one PR of a stack.
type StackEntry struct {
	Branch    string   `json:"branch"`
	PR        int      `json:"pr"`
	Base      string   `json:"base"`
	Draft     bool     `json:"draft"`
	Title     string   `json:"title"`
	URL       string   `json:"url"`
	RemoteOid string   `json:"remoteOid"`
	LocalOid  string   `json:"localOid,omitempty"`
	Children  []string `json:"children"`
}

// Stack describes the stack of open PRs containing branch (default: HEAD).
func Stack(branch string) (any, error) {
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	prs, err := openPRs(repo)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		if branch, err = currentBranch(); err != nil {
			return nil, err
		}
	}
	if prs[branch] == nil {
		return nil, refuse(notHead, branch, repo.Viewer)
	}
	order := stackOrder(prs, branch)
	linear := true
	var entries []StackEntry
	for _, b := range order {
		pr := prs[b]
		entry := StackEntry{Branch: b, PR: pr.Number, Base: pr.BaseRefName, Draft: pr.IsDraft,
			Title: pr.Title, URL: pr.URL, RemoteOid: pr.HeadRefOid, Children: []string{}}
		if oid, err := git.Run("rev-parse", "--verify", "-q", "refs/heads/"+b); err == nil {
			entry.LocalOid = oid
		}
		for _, c := range order {
			if prs[c].BaseRefName == b {
				entry.Children = append(entry.Children, c)
			}
		}
		linear = linear && len(entry.Children) <= 1
		entries = append(entries, entry)
	}
	return map[string]any{"repo": repo.Slug, "linear": linear, "stack": entries}, nil
}

// Begin registers branches of one stack (default: the stack containing HEAD).
func Begin(branches []string) (any, error) {
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	prs, err := openPRs(repo)
	if err != nil {
		return nil, err
	}
	if len(branches) == 0 {
		current, err := currentBranch()
		if err != nil {
			return nil, err
		}
		if prs[current] == nil {
			return nil, refuse(notHead, current, repo.Viewer)
		}
		branches = stackOrder(prs, current)
	} else if prs[branches[0]] != nil {
		// Only one stack per loop, so an agent cannot widen the loop to
		// PRs it was not asked about.
		stack := map[string]bool{}
		for _, b := range stackOrder(prs, branches[0]) {
			stack[b] = true
		}
		for _, b := range branches {
			if !stack[b] {
				return nil, refuse("%s is not in the same stack as %s, and a loop covers one stack; leave it out, "+
					"and name it in the report for a loop of its own", b, branches[0])
			}
		}
	}
	state := &State{Repo: repo.Slug, Branches: map[string]*Branch{}, Triggers: map[string]*Trigger{}, AcceptedFailures: map[string][]string{}}
	for _, b := range branches {
		pr, err := checkBranch(repo, prs, b)
		if err != nil {
			return nil, err
		}
		if !git.OK("rev-parse", "--verify", "-q", "refs/heads/"+b) {
			return nil, refuse("no local branch %s; ask Wesley to fetch and check it out", b)
		}
		remote := pr.HeadRefOid
		if !git.OK("cat-file", "-e", remote+"^{commit}") {
			return nil, refuse("%s on GitHub is at %.12s, which is not in this repository; ask Wesley to fetch it, "+
				"then begin again", b, remote)
		}
		if !git.OK("merge-base", "--is-ancestor", remote, "refs/heads/"+b) {
			return nil, refuse("local %s does not contain %.12s from GitHub, which has commits it lacks; "+
				"stop and ask Wesley how to reconcile them", b, remote)
		}
		state.Order = append(state.Order, b)
		state.Branches[b] = &Branch{PR: pr.Number, Base: pr.BaseRefName, RemoteOid: remote}
	}
	for _, b := range state.Order {
		parent, err := state.parentRef(repo, b)
		if err != nil {
			return nil, err
		}
		entry := state.Branches[b]
		if entry.BaselineLines, err = diffLines(parent, b); err != nil {
			return nil, err
		}
		if entry.Commits, err = commitsBetween(parent, "refs/heads/"+b); err != nil {
			return nil, err
		}
		for _, c := range entry.Commits {
			if kind, _ := fold(c.Message); kind != history.Kept {
				return nil, refuse("%s already has fixup!, amend! or squash! commits (%q), which the loop would mistake for its own; "+
					"hand Wesley `git sign-pass` on that branch to squash them, then begin again", b, subject(c.Message))
			}
		}
	}
	if err := snapshotRound(state); err != nil {
		return nil, err
	}
	return state, state.save()
}

// snapshotRound records every branch tip under refs/pr-loop/rounds/<n>/, so
// `git range-diff` (or magit-tbdiff) can show what each round changed.
func snapshotRound(state *State) error {
	for _, b := range state.Order {
		oid, err := git.Run("rev-parse", "refs/heads/"+b)
		if err != nil {
			return err
		}
		if _, err := git.Run("update-ref", fmt.Sprintf("refs/pr-loop/rounds/%d/%s", state.Round, b), oid); err != nil {
			return err
		}
	}
	return nil
}

// context re-checks the loop's state, repository and PRs before any write.
func context(numbers []int) (*State, *Repo, map[string]*PR, error) {
	state, err := loadState(true)
	if err != nil {
		return nil, nil, nil, err
	}
	repo, err := repository()
	if err != nil {
		return nil, nil, nil, err
	}
	if repo.Slug != state.Repo {
		return nil, nil, nil, refuse("this checkout is %s, but the loop was started for %s; run pr-loop from a checkout of %s",
			repo.Slug, state.Repo, state.Repo)
	}
	prs, err := openPRs(repo)
	if err != nil {
		return nil, nil, nil, err
	}
	byNumber := state.prBranches()
	branches := state.Order
	if len(numbers) > 0 {
		branches = nil
		for _, n := range numbers {
			b, ok := byNumber[n]
			if !ok {
				return nil, nil, nil, refuse(notInLoop, n)
			}
			branches = append(branches, b)
		}
	}
	checked := map[string]*PR{}
	for _, b := range branches {
		pr, err := checkBranch(repo, prs, b)
		if err != nil {
			return nil, nil, nil, err
		}
		checked[b] = pr
	}
	return state, repo, checked, nil
}
