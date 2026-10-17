// Package loop implements the review loop's commands and the policy they
// enforce. Every write to GitHub the loop makes goes through here.
package loop

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/repos"
)

// githubRemotes maps each remote whose fetch URL is on GitHub to owner/name.
func githubRemotes() (map[string]string, error) { return repos.Remotes() }

// Repo is the GitHub repository a checkout belongs to.
type Repo struct {
	ID      string
	Slug    string
	Default string
	Viewer  string
}

func repository() (*Repo, error) {
	remotes, err := githubRemotes()
	if err != nil {
		return nil, err
	}
	slugs := map[string]bool{}
	for _, slug := range remotes {
		slugs[slug] = true
	}
	if len(slugs) != 1 {
		found := make([]string, 0, len(slugs))
		for slug := range slugs {
			found = append(found, slug)
		}
		sort.Strings(found)
		return nil, refuse("expected exactly one GitHub repository among this checkout's fetch URLs, found %v; "+
			"run pr-loop from a checkout of one repository, or ask the operator to fix the remotes", found)
	}
	var slug string
	for s := range slugs {
		slug = s
	}
	owner, name, _ := strings.Cut(slug, "/")
	var data struct {
		Viewer     struct{ Login string }
		Repository struct {
			ID               string
			NameWithOwner    string
			DefaultBranchRef struct{ Name string }
		}
	}
	err = graphQL(`query($owner: String!, $name: String!) {
		viewer { login }
		repository(owner: $owner, name: $name) { id nameWithOwner defaultBranchRef { name } }
	}`, map[string]any{"owner": owner, "name": name}, &data)
	if err != nil {
		return nil, err
	}
	return &Repo{
		ID:      data.Repository.ID,
		Slug:    data.Repository.NameWithOwner,
		Default: data.Repository.DefaultBranchRef.Name,
		Viewer:  data.Viewer.Login,
	}, nil
}

// PR is an open pull request by the authenticated user.
type PR struct {
	ID                string `json:"id"`
	Number            int    `json:"number"`
	IsDraft           bool   `json:"isDraft"`
	URL               string `json:"url"`
	Title             string `json:"title"`
	IsCrossRepository bool   `json:"isCrossRepository"`
	Author            struct {
		Login string `json:"login"`
	} `json:"author"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
	BaseRefOid  string `json:"baseRefOid"`
}

// prFields are the GraphQL fields a PR is decoded from.
const prFields = `id number isDraft url title isCrossRepository author { login }
				headRefName headRefOid baseRefName baseRefOid`

// openPRs maps head branch to PR, for the user's open PRs in repo.
func openPRs(repo *Repo) (map[string]*PR, error) {
	var data struct {
		Search struct{ Nodes []*PR }
	}
	err := graphQL(`query($q: String!) {
		search(query: $q, type: ISSUE, first: 100) {
			nodes { ... on PullRequest { `+prFields+` } }
		}
	}`, map[string]any{"q": fmt.Sprintf("repo:%s is:pr is:open author:%s", repo.Slug, repo.Viewer)}, &data)
	if err != nil {
		return nil, err
	}
	prs := map[string]*PR{}
	for _, pr := range data.Search.Nodes {
		if pr != nil && pr.Number != 0 {
			prs[pr.HeadRefName] = pr
		}
	}
	return prs, nil
}

// checkBranch is the policy every write re-applies: an open PR by the user,
// in this repository, that is not the default branch. Ready PRs are accepted
// as well as drafts, so on one the loop's pushes, replies and review
// requests notify its reviewers and code owners.
func checkBranch(repo *Repo, prs map[string]*PR, branch string) (*PR, error) {
	if branch == repo.Default || branch == "main" || branch == "master" {
		return nil, refuse("%s is a default branch, which the loop never writes to; name a pull request's branch", branch)
	}
	pr := prs[branch]
	if pr == nil {
		return nil, refuse(notHead, branch, repo.Viewer)
	}
	if pr.IsCrossRepository || pr.Author.Login != repo.Viewer {
		return nil, refuse("#%d is not a same-repository pull request by %s, which the loop never writes to; "+
			"leave it to the operator", pr.Number, repo.Viewer)
	}
	return pr, nil
}

// stackOrder lists the stack containing branch, parents before children.
func stackOrder(prs map[string]*PR, branch string) []string {
	root := branch
	for prs[prs[root].BaseRefName] != nil {
		root = prs[root].BaseRefName
	}
	order, queue := []string{}, []string{root}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		order = append(order, current)
		var children []string
		for b, pr := range prs {
			if pr.BaseRefName == current {
				children = append(children, b)
			}
		}
		sort.Strings(children)
		queue = append(queue, children...)
	}
	return order
}

func currentBranch() (string, error) {
	branch, err := git.Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if branch == "HEAD" {
		return "", refuse("HEAD is detached; name the branches explicitly")
	}
	return branch, nil
}

// trackingRef is the freshest local ref for a branch on GitHub.
func trackingRef(repo *Repo, branch string) (string, error) {
	remotes, err := githubRemotes()
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(remotes))
	for name := range remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, remote := range names {
		ref := "refs/remotes/" + remote + "/" + branch
		if remotes[remote] == repo.Slug && git.OK("rev-parse", "--verify", "-q", ref) {
			return ref, nil
		}
	}
	if git.OK("rev-parse", "--verify", "-q", "refs/heads/"+branch) {
		return "refs/heads/" + branch, nil
	}
	return "", refuse("no local ref for %s, which a pull request is based on; ask the operator to fetch it", branch)
}

func recordTracking(repo *Repo, branch, oid string) error {
	remotes, err := githubRemotes()
	if err != nil {
		return err
	}
	for remote, slug := range remotes {
		if slug == repo.Slug {
			if _, err := git.Run("update-ref", "refs/remotes/"+remote+"/"+branch, oid); err != nil {
				return err
			}
		}
	}
	return nil
}

func diffLines(base, branch string) (int, error) {
	out, err := git.Run("diff", "--numstat", base+"...refs/heads/"+branch)
	if err != nil {
		return 0, err
	}
	total := 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		for _, n := range fields[:2] {
			if v, err := strconv.Atoi(n); err == nil {
				total += v
			}
		}
	}
	return total, nil
}
