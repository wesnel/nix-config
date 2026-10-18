// Package prreview reviews someone else's pull request: it fetches the pull
// request into a clone of its own, vets what its code would run, runs its
// tests only in a VM with no network, and has a reviewer that did not write
// it judge it, for the operator to edit and post. It writes nothing to GitHub.
package prreview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/loop"
	"github.com/wesnel/agent-tools/internal/refusal"
)

var (
	// graphQL and remoteURL are replaced by tests that fake GitHub.
	graphQL   = gh.GraphQL
	remoteURL = func(slug string) string { return "https://github.com/" + slug + ".git" }
)

// credentialHelper lets the fetch alone use the gh login, for a private
// repository, through the environment, so it is in no argv and in no
// config git reads for any other command.
var credentialHelper = []string{
	"GIT_CONFIG_COUNT=1",
	"GIT_CONFIG_KEY_0=credential.https://github.com.helper",
	"GIT_CONFIG_VALUE_0=!gh auth git-credential",
}

// Issue is an issue the pull request says it closes.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// PR is a fetched pull request. Title, Body and Issues are the author's
// words, not ours.
type PR struct {
	Repo    string  `json:"repo"`
	Number  int     `json:"pr"`
	URL     string  `json:"url"`
	Title   string  `json:"title"`
	Body    string  `json:"body"`
	BaseRef string  `json:"baseRef"`
	Issues  []Issue `json:"issues"`
	// Head is the pinned head; Base is its merge base with BaseRef.
	Head string `json:"head"`
	Base string `json:"base"`
	// Clone is the pull request's bare clone, kept across rounds; Dir is
	// this head's review directory.
	Clone string `json:"clone"`
	Dir   string `json:"dir"`
}

var name = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// ValidName reports whether s is what a GitHub owner or repository name
// must be for a review to be kept under its OWNER/NAME.
func ValidName(s string) bool { return name.MatchString(s) }

// checkSlug refuses an owner/name that is not one, which would put a review
// outside the cache directory.
func checkSlug(slug string) (owner, repo string, err error) {
	owner, repo, ok := strings.Cut(slug, "/")
	if !ok || !name.MatchString(owner) || !name.MatchString(repo) || strings.Contains(repo, "/") {
		return "", "", refusal.New("%q is not a GitHub owner/name; name the repository as o/r, such as `wesnel/emacs-config`", slug)
	}
	return owner, repo, nil
}

// Root is where reviews are kept: "dir" under "review" in pr-loop's config,
// which home-manager writes from wgn.home.llm.prLoop.reviewDir, the path the
// loop launchers and Emacs also hold. Emacs treats it as untrusted, and loop
// sessions started in a review's directory read only that review.
func Root() (string, error) {
	config, err := loadRunConfig()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(config.Dir) {
		return "", refusal.New("no review directory is set as an absolute \"dir\" under \"review\" in %s; "+
			"set wgn.home.llm.prLoop in home-manager, which writes it", loop.ConfigPath())
	}
	return filepath.Clean(config.Dir), nil
}

// Fetch fetches pull request number of slug into its clone, pinned to the
// head GitHub reports, and makes that head's review directory.
func Fetch(ctx context.Context, slug string, number int) (*PR, error) {
	owner, repo, err := checkSlug(slug)
	if err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, refusal.New("%d is not a pull request number; pass the number GitHub shows, such as 12", number)
	}
	var data struct {
		Repository struct {
			PullRequest *struct {
				URL                     string
				Title                   string
				Body                    string
				HeadRefOid              string
				BaseRefName             string
				ClosingIssuesReferences struct{ Nodes []Issue }
			}
		}
	}
	err = graphQL(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { pullRequest(number: $number) {
			url title body headRefOid baseRefName
			closingIssuesReferences(first: 10) { nodes { number title body } }
		} }
	}`, map[string]any{"owner": owner, "name": repo, "number": number}, &data)
	if err != nil {
		return nil, err
	}
	p := data.Repository.PullRequest
	if p == nil {
		return nil, fmt.Errorf("%s has no pull request #%d", slug, number)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.HeadRefOid) {
		return nil, fmt.Errorf("GitHub gave %q as the head of %s#%d", p.HeadRefOid, slug, number)
	}
	pr := &PR{Repo: owner + "/" + repo, Number: number, URL: p.URL, Title: p.Title, Body: p.Body,
		BaseRef: p.BaseRefName, Issues: p.ClosingIssuesReferences.Nodes, Head: p.HeadRefOid}
	if pr.Issues == nil {
		pr.Issues = []Issue{}
	}
	root, err := Root()
	if err != nil {
		return nil, err
	}
	top := filepath.Join(root, owner, repo, strconv.Itoa(number))
	pr.Clone = filepath.Join(top, "clone")
	if _, err := os.Stat(pr.Clone); err != nil {
		if err := os.MkdirAll(top, 0o755); err != nil {
			return nil, err
		}
		// Bare, so that nothing on this side ever checks the pull
		// request out but Checkout.
		if _, err := gitRun(ctx, top, "init", "--quiet", "--bare", pr.Clone); err != nil {
			return nil, err
		}
	}
	if strings.HasPrefix(pr.BaseRef, "-") || strings.ContainsAny(pr.BaseRef, ": \t\n") {
		return nil, fmt.Errorf("GitHub gave %q as the base branch of %s#%d", pr.BaseRef, slug, number)
	}
	_, err = gitOut(ctx, pr.Clone, credentialHelper, "fetch", "--quiet", "--no-tags", "--no-recurse-submodules",
		"--no-write-fetch-head", remoteURL(pr.Repo),
		fmt.Sprintf("+refs/pull/%d/head:refs/pr-review/head", number),
		"+refs/heads/"+pr.BaseRef+":refs/pr-review/base")
	if err != nil {
		return nil, err
	}
	fetched, err := gitRun(ctx, pr.Clone, "rev-parse", "--verify", "refs/pr-review/head^{commit}")
	if err != nil {
		return nil, err
	}
	if fetched != pr.Head {
		return nil, fmt.Errorf("GitHub reports %s as the head of %s#%d, but the fetch got %s, so it moved; "+
			"run the review again", pr.Head, slug, number, fetched)
	}
	if pr.Base, err = gitRun(ctx, pr.Clone, "merge-base", "refs/pr-review/base", pr.Head); err != nil {
		return nil, err
	}
	pr.Dir = filepath.Join(top, pr.Head[:12])
	if err := os.MkdirAll(pr.Dir, 0o755); err != nil {
		return nil, err
	}
	return pr, nil
}
