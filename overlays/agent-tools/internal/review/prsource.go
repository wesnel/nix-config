package review

// A pull request source is fetched into the operator's clone of its
// repository or a clone of its own, for reviewers that did not write it.
// Nothing here writes to GitHub.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/record"
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
	// Clone is where the pull request is fetched, kept across rounds: the
	// operator's clone of its repository the review runs from, or else a
	// bare clone of its own. Refs is where its head and base are there,
	// Refs/head and Refs/base. Dir is this head's review directory.
	Clone string `json:"clone"`
	Refs  string `json:"refs"`
	Dir   string `json:"dir"`
	// Home is whether Clone is the operator's clone.
	Home bool `json:"home"`
	// Own is whether its author is the user of the gh login: the
	// operator's own change, for which review.privateModules names the
	// private repositories an upgrade review may read.
	Own bool `json:"own"`
}

// checkSlug refuses an owner/name that is not one, which would put a review
// outside the cache directory.
func checkSlug(slug string) (owner, repo string, err error) {
	owner, repo, ok := strings.Cut(slug, "/")
	if !ok || !record.ValidName(owner) || !record.ValidName(repo) {
		return "", "", refusal.New("%q is not a GitHub owner/name; name the repository as o/r, such as `wesnel/emacs-config`", slug)
	}
	return owner, repo, nil
}

// Root is where reviews are kept: pr-review under $XDG_CACHE_HOME, or under
// ~/.cache.
func Root() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if !filepath.IsAbs(base) {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "pr-review")
}

// Fetch fetches pull request number of slug, pinned to the head GitHub
// reports, and makes that head's review directory. It fetches into home,
// the operator's clone of slug, as refs/pr-review/NUMBER/head and base,
// where home is not ""; else into a bare clone of its own under Root, as
// refs/pr-review/head and base. Neither is checked out.
func Fetch(ctx context.Context, slug string, number int, home string) (*PR, error) {
	owner, repo, err := checkSlug(slug)
	if err != nil {
		return nil, err
	}
	if number <= 0 {
		return nil, refusal.New("%d is not a pull request number; pass the number GitHub shows, such as 12", number)
	}
	type login struct{ Login string }
	var data struct {
		Viewer     login
		Repository struct {
			PullRequest *struct {
				URL                     string
				Title                   string
				Body                    string
				HeadRefOid              string
				BaseRefName             string
				Author                  *login
				ClosingIssuesReferences struct{ Nodes []Issue }
			}
		}
	}
	err = graphQL(`query($owner: String!, $name: String!, $number: Int!) {
		viewer { login }
		repository(owner: $owner, name: $name) { pullRequest(number: $number) {
			url title body headRefOid baseRefName author { login }
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
		BaseRef: p.BaseRefName, Issues: p.ClosingIssuesReferences.Nodes, Head: p.HeadRefOid,
		Own: p.Author != nil && p.Author.Login != "" && strings.EqualFold(p.Author.Login, data.Viewer.Login)}
	if pr.Issues == nil {
		pr.Issues = []Issue{}
	}
	top := filepath.Join(Root(), owner, repo, strconv.Itoa(number))
	pr.Clone, pr.Refs = filepath.Join(top, "clone"), "refs/pr-review"
	if home != "" {
		pr.Clone, pr.Refs, pr.Home = home, fmt.Sprintf("refs/pr-review/%d", number), true
	} else if _, err := os.Stat(pr.Clone); err != nil {
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
		fmt.Sprintf("+refs/pull/%d/head:%s/head", number, pr.Refs),
		"+refs/heads/"+pr.BaseRef+":"+pr.Refs+"/base")
	if err != nil {
		return nil, err
	}
	fetched, err := gitRun(ctx, pr.Clone, "rev-parse", "--verify", pr.Refs+"/head^{commit}")
	if err != nil {
		return nil, err
	}
	if fetched != pr.Head {
		return nil, fmt.Errorf("GitHub reports %s as the head of %s#%d, but the fetch got %s, so it moved; "+
			"run the review again", pr.Head, slug, number, fetched)
	}
	if pr.Base, err = gitRun(ctx, pr.Clone, "merge-base", pr.Refs+"/base", pr.Head); err != nil {
		return nil, err
	}
	pr.Dir = filepath.Join(top, pr.Head[:12])
	if err := os.MkdirAll(pr.Dir, 0o755); err != nil {
		return nil, err
	}
	return pr, nil
}
