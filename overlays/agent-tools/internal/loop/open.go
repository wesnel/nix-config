package loop

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/pushguard"
)

// remoteView is what GitHub reports about the head and base of a pull
// request about to be opened.
type remoteView struct {
	Heads map[string]string // branch to its tip, for branches that exist
	PRs   map[string]*PR    // head branch to an open same-repository PR
}

func viewRemote(repo *Repo, branch, base string) (*remoteView, error) {
	owner, name, _ := strings.Cut(repo.Slug, "/")
	type ref struct{ Target struct{ Oid string } }
	type prs struct{ Nodes []*PR }
	var data struct {
		Repository struct {
			Head, Base       *ref
			HeadPRs, BasePRs prs
		}
	}
	err := gh.GraphQL(`query($owner: String!, $name: String!, $head: String!, $base: String!, $headRef: String!, $baseRef: String!) {
		repository(owner: $owner, name: $name) {
			head: ref(qualifiedName: $headRef) { target { oid } }
			base: ref(qualifiedName: $baseRef) { target { oid } }
			headPRs: pullRequests(headRefName: $head, states: OPEN, first: 100) { nodes { `+prFields+` } }
			basePRs: pullRequests(headRefName: $base, states: OPEN, first: 100) { nodes { `+prFields+` } }
		}
	}`, map[string]any{"owner": owner, "name": name, "head": branch, "base": base,
		"headRef": "refs/heads/" + branch, "baseRef": "refs/heads/" + base}, &data)
	if err != nil {
		return nil, err
	}
	view := &remoteView{Heads: map[string]string{}, PRs: map[string]*PR{}}
	for b, r := range map[string]*ref{branch: data.Repository.Head, base: data.Repository.Base} {
		if r != nil {
			view.Heads[b] = r.Target.Oid
		}
	}
	// A fork's PR can share a head name with a branch here; it says nothing
	// about this repository's branches.
	for _, pr := range append(data.Repository.HeadPRs.Nodes, data.Repository.BasePRs.Nodes...) {
		if pr != nil && !pr.IsCrossRepository {
			view.PRs[pr.HeadRefName] = pr
		}
	}
	return view, nil
}

// checkOpen is the policy open applies before it writes: a new branch that
// is not a default branch, based on the default branch or on the head of an
// open pull request by the user in this repository. It returns the base's
// tip on GitHub.
func checkOpen(repo *Repo, view *remoteView, branch, base string) (string, error) {
	if branch == repo.Default || branch == "main" || branch == "master" {
		return "", refuse("%s is a default branch; commit the change to a branch of its own (`git switch -c NAME`) and open that", branch)
	}
	if branch == base {
		return "", refuse("%s cannot be its own base; pass the branch it stacks on as `--base`, or leave `--base` out", branch)
	}
	if pr := view.PRs[branch]; pr != nil {
		return "", refuse("%s is already the head of #%d, and open only opens new pull requests; "+
			"its later commits reach GitHub through `git sign-pass --push`, which Wesley runs", branch, pr.Number)
	}
	if oid := view.Heads[branch]; oid != "" {
		return "", refuse("%s already exists on GitHub at %.12s, and open only creates a branch; "+
			"ask Wesley to open its pull request", branch, oid)
	}
	if base != repo.Default {
		if _, err := checkBranch(repo, view.PRs, base); err != nil {
			return "", refuse("the base must be %s or the head of an open pull request by %s in %s (%v); "+
				"open the base with `pr-loop open` first, or leave `--base` out", repo.Default, repo.Viewer, repo.Slug, err)
		}
	}
	oid := view.Heads[base]
	if oid == "" {
		return "", refuse("%s does not exist on GitHub; open it with `pr-loop open` first, or leave `--base` out", base)
	}
	return oid, nil
}

// checkLocal returns the tip of the local branch and the commits GitHub
// already has that it can be measured against. A stacked branch must contain
// its base as GitHub has it, so the pull request shows only its own commits.
func checkLocal(repo *Repo, branch, base, baseOid string) (string, []string, error) {
	local, err := git.Run("rev-parse", "--verify", "-q", "refs/heads/"+branch)
	if err != nil {
		return "", nil, refuse("no local branch %s; name a branch that `git branch --list` shows", branch)
	}
	var known []string
	if git.OK("cat-file", "-e", baseOid+"^{commit}") {
		known = append(known, baseOid)
	}
	if base != repo.Default {
		if known == nil {
			return "", nil, refuse("%s on GitHub is at %.12s, which is not in this repository; ask Wesley to fetch it, "+
				"then open again", base, baseOid)
		}
		if !git.OK("merge-base", "--is-ancestor", baseOid, local) {
			return "", nil, refuse("%s does not contain %.12s, the tip of %s on GitHub; rebase it onto that commit "+
				"with `git -c commit.gpgsign=false rebase`, then open again", branch, baseOid, base)
		}
	}
	names, err := remotesOf(repo)
	if err != nil {
		return "", nil, err
	}
	if parent, err := unmergedParent(repo, names, branch, base, local, known); err != nil {
		return "", nil, err
	} else if parent != "" {
		return "", nil, refuse("%s contains commits of %s, a branch on GitHub that %s lacks, so its pull request would carry them; "+
			"open it with `--base %s`", branch, parent, base, parent)
	}
	for _, name := range names {
		known = append(known, "--remotes="+name+"/*")
	}
	count, err := git.Run(append([]string{"rev-list", "--count", local, "--not"}, known...)...)
	if err != nil {
		return "", nil, err
	}
	if n, _ := strconv.Atoi(count); n == 0 {
		return "", nil, refuse("%s has no commits that GitHub lacks, so there is nothing to open; "+
			"commit the change first, or say in the report that nothing changed", branch)
	}
	if err := lintNew(branch, local, known); err != nil {
		return "", nil, err
	}
	return local, known, nil
}

// unmergedParent names the remote branch, other than branch, base and the
// default branch, that local contains and the base's commits (baseCommits)
// lack: the nearest one, when there are several. It is "" when there is none.
func unmergedParent(repo *Repo, remotes []string, branch, base, local string, baseCommits []string) (string, error) {
	if len(baseCommits) == 0 {
		// The base's tip is not in this checkout; what was fetched of it stands in.
		for _, name := range remotes {
			if git.OK("rev-parse", "--verify", "-q", "refs/remotes/"+name+"/"+base) {
				baseCommits = append(baseCommits, "refs/remotes/"+name+"/"+base)
			}
		}
	}
	best, bestCount := "", 0
	for _, name := range remotes {
		refs, err := git.Run("for-each-ref", "--format=%(refname)", "refs/remotes/"+name+"/")
		if err != nil {
			return "", err
		}
		for _, ref := range strings.Fields(refs) {
			short := strings.TrimPrefix(ref, "refs/remotes/"+name+"/")
			if short == "HEAD" || short == branch || short == base || short == repo.Default ||
				!git.OK("merge-base", "--is-ancestor", ref, local) {
				continue
			}
			out, err := git.Run(append([]string{"rev-list", "--count", ref, "--not"}, baseCommits...)...)
			if err != nil {
				return "", err
			}
			// The branch with the most commits beyond the base is the one
			// nearest the head; a tie goes to the first name.
			if n, _ := strconv.Atoi(out); n > bestCount || (n == bestCount && n > 0 && short < best) {
				best, bestCount = short, n
			}
		}
	}
	return best, nil
}

// lintNew refuses unless the commits local has and known lacks pass
// history.LintMessage: each fold as it is, and the rest as the signing
// pass will leave them.
func lintNew(branch, local string, known []string) error {
	commits, err := history.Log(append([]string{local, "--not"}, known...)...)
	if err != nil {
		return err
	}
	check := history.Squashed(commits)
	for _, c := range commits {
		if kind, _ := history.Fold(c.Message); kind != history.Kept {
			check = append(check, c)
		}
	}
	for _, c := range check {
		if problems := history.LintMessage(c.Message); len(problems) > 0 {
			return refuse("%s's commit %q has a message where %s. Fix it before opening: reword a commit with an amend! commit "+
				"(`git -c commit.gpgsign=false commit --allow-empty -m 'amend! SUBJECT' -m 'NEW MESSAGE'`), "+
				"or commit a fixup again without what is wrong, then run `pr-loop open` again",
				branch, history.Subject(c.Message), strings.Join(problems, ", and "))
		}
	}
	return nil
}

// remotesOf names the remotes that fetch from repo, origin first.
func remotesOf(repo *Repo) ([]string, error) {
	remotes, err := githubRemotes()
	if err != nil {
		return nil, err
	}
	var names []string
	for name, slug := range remotes {
		if slug == repo.Slug {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if (names[i] == "origin") != (names[j] == "origin") {
			return names[i] == "origin"
		}
		return names[i] < names[j]
	})
	return names, nil
}

// setUpstream makes the branch track its copy on GitHub, which is where `git
// sign-pass --push` sends it. Without this a branch started from
// origin/<default> would still track the default branch.
func setUpstream(repo *Repo, branch string) error {
	names, err := remotesOf(repo)
	if err != nil || len(names) == 0 {
		return err
	}
	if _, err := git.Run("config", "branch."+branch+".remote", names[0]); err != nil {
		return err
	}
	_, err = git.Run("config", "branch."+branch+".merge", "refs/heads/"+branch)
	return err
}

// Open pushes a branch GitHub does not have through the API, as Push does,
// and opens it as a draft pull request against base (default: the default
// branch). It never marks the pull request ready for review.
func Open(branch, base, title, body string, dryRun bool) (any, error) {
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("open needs --title")
	}
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	if branch == "" {
		if branch, err = currentBranch(); err != nil {
			return nil, err
		}
	}
	if base == "" {
		base = repo.Default
	}
	view, err := viewRemote(repo, branch, base)
	if err != nil {
		return nil, err
	}
	baseOid, err := checkOpen(repo, view, branch, base)
	if err != nil {
		return nil, err
	}
	local, known, err := checkLocal(repo, branch, base, baseOid)
	if err != nil {
		return nil, err
	}
	// The API bypasses git's pre-push hooks, so the check git-push-guard makes
	// is made here.
	absent := strings.Repeat("0", 40)
	update := pushguard.Update{LocalRef: "refs/heads/" + branch, Local: local, RemoteRef: "refs/heads/" + branch, Remote: absent}
	if err := pushguard.Check("https://github.com/"+repo.Slug, []pushguard.Update{update}, pushguard.GitHub()); err != nil {
		return nil, err
	}
	objects, err := missingObjects([]string{local}, known)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return map[string]any{"wouldOpen": map[string]any{"branch": branch, "base": base, "head": local, "draft": true},
			"objects": objects.counts()}, nil
	}

	calls, err := objects.upload(repo.Slug)
	if err != nil {
		return nil, err
	}
	var created struct {
		CreateRef struct{ Ref struct{ ID string } }
	}
	err = gh.GraphQL(`mutation($input: CreateRefInput!) { createRef(input: $input) { ref { id } } }`,
		map[string]any{"input": map[string]any{"repositoryId": repo.ID, "name": "refs/heads/" + branch, "oid": local}}, &created)
	if err != nil {
		return nil, err
	}
	var opened struct {
		CreatePullRequest struct {
			PullRequest struct {
				Number  int
				URL     string
				IsDraft bool
			}
		}
	}
	err = gh.GraphQL(`mutation($input: CreatePullRequestInput!) {
		createPullRequest(input: $input) { pullRequest { number url isDraft } }
	}`, map[string]any{"input": map[string]any{"repositoryId": repo.ID, "baseRefName": base, "headRefName": branch,
		"title": title, "body": body, "draft": true}}, &opened)
	if err != nil {
		// Without a pull request the branch is outside every later check, so
		// it is taken back down.
		undo := gh.GraphQL(`mutation($input: DeleteRefInput!) { deleteRef(input: $input) { clientMutationId } }`,
			map[string]any{"input": map[string]any{"refId": created.CreateRef.Ref.ID}}, nil)
		if undo != nil {
			return nil, fmt.Errorf("opening the pull request failed (%w), and deleting %s from GitHub again failed too: %v", err, branch, undo)
		}
		return nil, fmt.Errorf("opening the pull request failed, so %s was deleted from GitHub again: %w", branch, err)
	}
	pr := opened.CreatePullRequest.PullRequest
	if err := recordTracking(repo, branch, local); err != nil {
		return nil, err
	}
	if err := setUpstream(repo, branch); err != nil {
		return nil, err
	}
	if !pr.IsDraft {
		return nil, refuse("GitHub opened #%d (%s) ready for review although a draft was asked for; tell Wesley", pr.Number, pr.URL)
	}
	result := map[string]any{"pr": pr.Number, "url": pr.URL, "draft": pr.IsDraft, "branch": branch, "base": base,
		"head": local, "apiCalls": calls + 2}
	for k, v := range stackOpened(repo, branch, base, pr.Number) {
		result[k] = v
	}
	return result, nil
}
