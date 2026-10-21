// Package status fetches the operator's open pull requests and the reviews
// asked of them from GitHub, model-free and cached. Of others' text it asks
// only for titles; every key, state and head is checked against its shape
// as it is read, so a hostile repository name, state or head is dropped or
// kept as "unknown".
package status

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/record"
)

// headPattern is a full commit SHA, which a record's file name holds.
var headPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// cacheFor is how long a fetch from GitHub serves before the next: one
// search query costs a point of GitHub's 5000 an hour.
const cacheFor = 60 * time.Second

// PR is one of the operator's open pull requests.
type PR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Title is the author's text, for an escaped page alone.
	Title string `json:"title"`
	Draft bool   `json:"draft"`
	// CI and Review are the head's check rollup and the review decision,
	// lower-case, "none" where GitHub has none and "unknown" where it gave
	// a value outside its schema.
	CI         string `json:"ci"`
	Review     string `json:"review"`
	Unresolved int    `json:"unresolved"`
	// Parent is the number of the pull request this one is stacked on, of
	// those listed with it, or 0.
	Parent int `json:"parent"`
}

// Request is an open pull request whose review is asked of the operator.
type Request struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Title is the author's text, as PR's.
	Title string `json:"title"`
	Draft bool   `json:"draft"`
	// Team is whether only a team was asked, not the operator themselves, and
	// Requested when the last request of that kind was made.
	Team      bool      `json:"team"`
	Requested time.Time `json:"requested"`
	Head      string    `json:"head"`
	// CI is Head's check rollup, as PR's.
	CI string `json:"ci"`
	// Record is "yes" or "no" for whether a git delegate review record of
	// Head exists, or "unknown" where Repo or Head cannot name one.
	Record string `json:"-"`
	// Parent is as PR's.
	Parent int `json:"parent"`
}

// GitHub is what one fetch found; a Total counts what the search matched,
// of which GitHub returns at most the first 100.
type GitHub struct {
	At            time.Time `json:"at"`
	Mine          []PR      `json:"mine"`
	MineTotal     int       `json:"mineTotal"`
	Requests      []Request `json:"requests"`
	RequestsTotal int       `json:"requestsTotal"`
}

// GitHubAt is github's result through cache, a file only github writes, as
// at now, each request with its Record.
func GitHubAt(ctx context.Context, cache string, now time.Time) (*GitHub, bool, error) {
	g, stale, err := github(ctx, cache, now)
	if g != nil {
		for i := range g.Requests {
			r := &g.Requests[i]
			r.Record = "unknown"
			owner, repo, _ := strings.Cut(r.Repo, "/")
			if !record.ValidName(owner) || !record.ValidName(repo) || !headPattern.MatchString(r.Head) {
				continue
			}
			// Any range a review took to this head: git delegate's of the
			// pull request is merge-base..head.
			if found, err := filepath.Glob(filepath.Join(record.Dir(), owner, repo, "*.."+r.Head+".org")); err == nil {
				r.Record = "no"
				if len(found) > 0 {
					r.Record = "yes"
				}
			}
		}
	}
	return g, stale, err
}

// github is the cached result while under cacheFor old, else a fresh one,
// cached; or, where the fetch fails, the cached one, stale.
func github(ctx context.Context, cache string, now time.Time) (*GitHub, bool, error) {
	var cached *GitHub
	if data, err := os.ReadFile(cache); err == nil {
		var g GitHub
		if json.Unmarshal(data, &g) == nil {
			cached = &g
		}
	}
	if cached != nil && !now.Before(cached.At) && now.Sub(cached.At) < cacheFor {
		return cached, false, nil
	}
	g, err := fetch(ctx, now)
	if err != nil {
		return cached, cached != nil, fmt.Errorf("GitHub: %w", err)
	}
	data, err := json.Marshal(g)
	if err == nil {
		err = writeFile(cache, data, 0o600)
	}
	return g, false, err
}

// query asks for keys, states and counts, and of others' text only the
// title; never a body or comment. Branch names only link stacked pull
// requests in fetch and are never kept.
const query = `query {
  viewer { login }
  mine: search(query: "is:pr is:open archived:false author:@me sort:updated-desc", type: ISSUE, first: 100) {
    issueCount
    nodes { ... on PullRequest {
      number title isDraft reviewDecision repository { nameWithOwner }
      baseRefName headRefName headRepository { nameWithOwner }
      commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      reviewThreads(first: 100) { nodes { isResolved } }
    } }
  }
  requested: search(query: "is:pr is:open archived:false review-requested:@me sort:updated-desc", type: ISSUE, first: 100) {
    issueCount
    nodes { ... on PullRequest {
      number title isDraft headRefOid repository { nameWithOwner }
      baseRefName headRefName headRepository { nameWithOwner }
      commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      reviewRequests(first: 50) { nodes { requestedReviewer { __typename ... on User { login } } } }
      timelineItems(itemTypes: [REVIEW_REQUESTED_EVENT], last: 50) { nodes { ... on ReviewRequestedEvent {
        createdAt requestedReviewer { __typename ... on User { login } }
      } } }
    } }
  }
}`

type reviewer struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

type searchPR struct {
	Number         int     `json:"number"`
	Title          string  `json:"title"`
	IsDraft        bool    `json:"isDraft"`
	ReviewDecision *string `json:"reviewDecision"`
	HeadRefOid     string  `json:"headRefOid"`
	BaseRefName    string  `json:"baseRefName"`
	HeadRefName    string  `json:"headRefName"`
	Repository     struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	// HeadRepository is null where the head's fork is gone.
	HeadRepository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	ReviewThreads struct {
		Nodes []struct {
			IsResolved bool `json:"isResolved"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	ReviewRequests struct {
		Nodes []struct {
			RequestedReviewer reviewer `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	TimelineItems struct {
		Nodes []struct {
			CreatedAt         time.Time `json:"createdAt"`
			RequestedReviewer reviewer  `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"timelineItems"`
}

type search struct {
	IssueCount int        `json:"issueCount"`
	Nodes      []searchPR `json:"nodes"`
}

// fetch runs query with `gh api graphql`, outside any repository, and keeps
// only the pull requests whose keys and heads have their shapes.
func fetch(ctx context.Context, now time.Time) (*GitHub, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "graphql", "-f", "query="+query)
	cmd.Dir = gh.Outside
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("gh api graphql: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var reply struct {
		Data struct {
			Viewer struct {
				Login string `json:"login"`
			} `json:"viewer"`
			Mine      search `json:"mine"`
			Requested search `json:"requested"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("gh api graphql: %w", err)
	}
	d := reply.Data
	g := &GitHub{At: now, Mine: []PR{}, MineTotal: d.Mine.IssueCount, Requests: []Request{}, RequestsTotal: d.Requested.IssueCount}
	mine := parents(d.Mine.Nodes, func(p searchPR) bool { return isKey(p.Repository.NameWithOwner, p.Number) })
	for _, p := range d.Mine.Nodes {
		if !isKey(p.Repository.NameWithOwner, p.Number) {
			continue
		}
		pr := PR{Repo: p.Repository.NameWithOwner, Number: p.Number, Title: p.Title, Draft: p.IsDraft, CI: headCI(p), Review: "none",
			Parent: mine(p)}
		if p.ReviewDecision != nil {
			pr.Review = enum(*p.ReviewDecision, "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED")
		}
		for _, t := range p.ReviewThreads.Nodes {
			if !t.IsResolved {
				pr.Unresolved++
			}
		}
		g.Mine = append(g.Mine, pr)
	}
	me := d.Viewer.Login
	isMe := func(r reviewer) bool { return r.Typename == "User" && me != "" && r.Login == me }
	listed := func(p searchPR) bool {
		return isKey(p.Repository.NameWithOwner, p.Number) && fullHead.MatchString(p.HeadRefOid)
	}
	requested := parents(d.Requested.Nodes, listed)
	for _, p := range d.Requested.Nodes {
		if !listed(p) {
			continue
		}
		r := Request{Repo: p.Repository.NameWithOwner, Number: p.Number, Title: p.Title, Draft: p.IsDraft, Head: p.HeadRefOid, CI: headCI(p), Team: true,
			Parent: requested(p)}
		for _, n := range p.ReviewRequests.Nodes {
			if isMe(n.RequestedReviewer) {
				r.Team = false
			}
		}
		for _, e := range p.TimelineItems.Nodes {
			if (r.Team && e.RequestedReviewer.Typename == "Team" || !r.Team && isMe(e.RequestedReviewer)) && e.CreatedAt.After(r.Requested) {
				r.Requested = e.CreatedAt
			}
		}
		g.Requests = append(g.Requests, r)
	}
	return g, nil
}

// parents is, for a pull request of nodes, the number of the one of nodes
// kept whose head branch, in the same repository, is its base: the pull
// request it is stacked on; or 0. A head on a fork never counts, since
// its branch name says nothing about the repository's own branches.
func parents(nodes []searchPR, kept func(searchPR) bool) func(searchPR) int {
	type branch struct{ repo, name string }
	heads := map[branch]int{}
	for _, p := range nodes {
		if kept(p) && p.HeadRefName != "" && p.HeadRepository != nil &&
			p.HeadRepository.NameWithOwner == p.Repository.NameWithOwner {
			heads[branch{p.Repository.NameWithOwner, p.HeadRefName}] = p.Number
		}
	}
	return func(p searchPR) int {
		if n := heads[branch{p.Repository.NameWithOwner, p.BaseRefName}]; n != p.Number && p.BaseRefName != "" {
			return n
		}
		return 0
	}
}

// headCI is the check rollup of p's last commit, as PR's CI.
func headCI(p searchPR) string {
	if n := p.Commits.Nodes; len(n) > 0 && n[0].Commit.StatusCheckRollup != nil {
		return enum(n[0].Commit.StatusCheckRollup.State, "SUCCESS", "FAILURE", "ERROR", "PENDING", "EXPECTED")
	}
	return "none"
}

// enum is v lower-cased, with "-" for "_", where it is one of values, and
// "unknown" otherwise.
func enum(v string, values ...string) string {
	for _, w := range values {
		if v == w {
			return strings.ReplaceAll(strings.ToLower(w), "_", "-")
		}
	}
	return "unknown"
}

var (
	// slug is a GitHub owner/name, neither part starting with a dot.
	slug     = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*/[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	fullHead = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

func isKey(repo string, number int) bool { return slug.MatchString(repo) && number > 0 }

// writeFile replaces file with data, with mode, by renaming a new file over
// it, so that a reader never sees it half written.
func writeFile(file string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), file)
}
