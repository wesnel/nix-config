// Package status collects the operator's status, model-free: their open
// pull requests, the reviews asked of them, and this host's plans. It writes
// status.org, which holds no text anyone else wrote: only keys, states,
// counts and the links it builds from them, each checked against its shape
// before it is written, so that a pull request's title, branch or author can
// form no Org headline, keyword or link there.
package status

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/prreview"
	"github.com/wesnel/agent-tools/internal/routes"
)

// cacheFor is how long a fetch from GitHub serves before the next: one
// search query costs a point of GitHub's 5000 an hour.
const cacheFor = 60 * time.Second

// PR is one of the operator's open pull requests.
type PR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	// Title is the author's text, for the escaped page alone: Org never
	// writes it.
	Title string `json:"title"`
	Draft bool   `json:"draft"`
	// CI and Review are the head's check rollup and the review decision,
	// lower-case, "none" where GitHub has none and "unknown" where it gave
	// a value outside its schema.
	CI         string `json:"ci"`
	Review     string `json:"review"`
	Unresolved int    `json:"unresolved"`
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
	// Record is "yes" or "no" for whether pr-loop holds a review of Head,
	// or "unknown" without a review directory.
	Record string `json:"-"`
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

// Plan is one of this host's plans that is not halted.
type Plan struct {
	Name  string `json:"name"`
	File  string `json:"file"`
	State string `json:"state"`
	// Ready counts the steps ready now, and Questions the open questions
	// the operator can answer now; Owed are the review routes owed.
	Ready     int      `json:"ready"`
	Questions int      `json:"questions"`
	Owed      []string `json:"owed"`
}

// Status is one collect. GitHub is nil when no fetch has ever succeeded,
// and Stale when it is the last good result because this fetch failed.
type Status struct {
	At     time.Time `json:"at"`
	GitHub *GitHub   `json:"github"`
	Stale  bool      `json:"stale"`
	Plans  []Plan    `json:"plans"`
}

// Collect reads GitHub, through cache, a file only Collect writes, and c's
// plans of this host as at now. Its error is what failed; the Status still
// holds what did not.
func Collect(ctx context.Context, c plans.Config, cache string, now time.Time) (Status, error) {
	s := Status{At: now}
	var ghErr, plansErr error
	s.GitHub, s.Stale, ghErr = github(ctx, cache, now)
	if s.GitHub != nil {
		root, err := prreview.Root()
		for i := range s.GitHub.Requests {
			r := &s.GitHub.Requests[i]
			r.Record = "unknown"
			if err != nil {
				continue
			}
			if file, err := prreview.RecordFile(root, r.Repo, r.Number, r.Head); err == nil {
				r.Record = "no"
				if _, err := os.Stat(file); err == nil {
					r.Record = "yes"
				}
			}
		}
	}
	s.Plans, plansErr = Plans(c, now)
	return s, errors.Join(ghErr, plansErr)
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
// title, which only the page shows; never a body, branch or comment.
const query = `query {
  viewer { login }
  mine: search(query: "is:pr is:open archived:false author:@me sort:updated-desc", type: ISSUE, first: 100) {
    issueCount
    nodes { ... on PullRequest {
      number title isDraft reviewDecision repository { nameWithOwner }
      commits(last: 1) { nodes { commit { statusCheckRollup { state } } } }
      reviewThreads(first: 100) { nodes { isResolved } }
    } }
  }
  requested: search(query: "is:pr is:open archived:false review-requested:@me sort:updated-desc", type: ISSUE, first: 100) {
    issueCount
    nodes { ... on PullRequest {
      number title isDraft headRefOid repository { nameWithOwner }
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
	Repository     struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
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
	for _, p := range d.Mine.Nodes {
		if !isKey(p.Repository.NameWithOwner, p.Number) {
			continue
		}
		pr := PR{Repo: p.Repository.NameWithOwner, Number: p.Number, Title: p.Title, Draft: p.IsDraft, CI: headCI(p), Review: "none"}
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
	for _, p := range d.Requested.Nodes {
		if !isKey(p.Repository.NameWithOwner, p.Number) || !fullHead.MatchString(p.HeadRefOid) {
			continue
		}
		r := Request{Repo: p.Repository.NameWithOwner, Number: p.Number, Title: p.Title, Draft: p.IsDraft, Head: p.HeadRefOid, CI: headCI(p), Team: true}
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
	// The shapes of a plan's name, its file's path and its STATUS.
	planName  = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}-[a-z0-9]+(-[a-z0-9]+)*\.[A-Za-z0-9][A-Za-z0-9_-]*$`)
	planPath  = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)
	planState = regexp.MustCompile(`^[a-z][a-z-]*$`)
)

func isKey(repo string, number int) bool { return slug.MatchString(repo) && number > 0 }

// Plans are c's plans of this host as at now, newest first, but those
// halted and those handed back with nothing left for the operator.
func Plans(c plans.Config, now time.Time) ([]Plan, error) {
	own, err := c.OwnPlans()
	if err != nil {
		return nil, err
	}
	out := []Plan{}
	var errs []error
	for _, p := range own {
		st, err := p.StatusAt(now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		qs, err := p.Questions(true)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		plan := Plan{Name: p.Name, File: p.File(), State: st.State, Ready: len(st.ReadySteps),
			Questions: len(plans.Askable(qs)), Owed: p.RoutesOwed()}
		if plan.State == "halted" || plan.State == "handed-back" && plan.Questions == 0 && len(plan.Owed) == 0 {
			continue
		}
		out = append(out, plan)
	}
	return out, errors.Join(errs...)
}

// Org is status.org. Every value it writes is checked against its shape
// here, the cache's included: one that fails is left out, or written as
// "unknown".
func (s Status) Org() string {
	var b strings.Builder
	b.WriteString("#+title: Agent status\n# Written by plan-dash collect, which replaces it on each run.\n\n")
	fmt.Fprintf(&b, "Collected %s. ", stamp(s.At))
	g := s.GitHub
	switch {
	case g == nil:
		b.WriteString("GitHub: no data, the fetch failed.\n")
	case s.Stale:
		fmt.Fprintf(&b, "GitHub as of %s, stale: the last fetch failed.\n", stamp(g.At))
	default:
		fmt.Fprintf(&b, "GitHub as of %s.\n", stamp(g.At))
	}
	if g != nil {
		var rows [][]string
		for _, p := range g.Mine {
			if !isKey(p.Repo, p.Number) {
				continue
			}
			draft := "ready"
			if p.Draft {
				draft = "draft"
			}
			rows = append(rows, []string{prLink(p.Repo, p.Number), draft, orgCI(p.CI),
				enum(strings.ToUpper(strings.ReplaceAll(p.Review, "-", "_")), "NONE", "APPROVED", "CHANGES_REQUESTED", "REVIEW_REQUIRED"),
				fmt.Sprint(p.Unresolved)})
		}
		table(&b, "My pull requests", []string{"Pull request", "Draft", "CI", "Review", "Unresolved"}, rows, g.MineTotal)

		rows = nil
		for _, r := range g.Requests {
			if !isKey(r.Repo, r.Number) || !fullHead.MatchString(r.Head) {
				continue
			}
			who, when := "me", ""
			if r.Team {
				who = "team"
			}
			if !r.Requested.IsZero() {
				when = stamp(r.Requested)
			}
			rows = append(rows, []string{prLink(r.Repo, r.Number), who, when, r.Head[:12], orgCI(r.CI),
				enum(strings.ToUpper(r.Record), "YES", "NO", "UNKNOWN")})
		}
		table(&b, "Review requests", []string{"Pull request", "For", "Requested", "Head", "CI", "Record"}, rows, g.RequestsTotal)
	}

	var rows [][]string
	for _, p := range s.Plans {
		if !planName.MatchString(p.Name) {
			continue
		}
		name := p.Name
		if planPath.MatchString(p.File) {
			name = "[[file:" + p.File + "][" + p.Name + "]]"
		}
		state := p.State
		if !planState.MatchString(state) {
			state = "unknown"
		}
		var owed []string
		for _, r := range p.Owed {
			if routes.IsKnown(r) {
				owed = append(owed, r)
			}
		}
		rows = append(rows, []string{name, state, fmt.Sprint(p.Ready), fmt.Sprint(p.Questions), strings.Join(owed, " ")})
	}
	table(&b, "Plans", []string{"Plan", "Status", "Ready", "Questions", "Routes owed"}, rows, len(rows))
	return b.String()
}

// orgCI is a CI value of PR's or Request's where it is one, else "unknown".
func orgCI(ci string) string {
	return enum(strings.ToUpper(strings.ReplaceAll(ci, "-", "_")), "NONE", "SUCCESS", "FAILURE", "ERROR", "PENDING", "EXPECTED")
}

// table writes a heading and its rows as an Org table, which Org aligns
// when it is edited, and how many of total it shows where that is fewer.
func table(b *strings.Builder, heading string, header []string, rows [][]string, total int) {
	fmt.Fprintf(b, "\n* %s\n", heading)
	if len(rows) == 0 {
		b.WriteString("None.\n")
		return
	}
	fmt.Fprintf(b, "| %s |\n|-\n", strings.Join(header, " | "))
	for _, r := range rows {
		fmt.Fprintf(b, "| %s |\n", strings.Join(r, " | "))
	}
	if len(rows) < total {
		fmt.Fprintf(b, "%d of %d shown.\n", len(rows), total)
	}
}

func prLink(repo string, number int) string {
	return fmt.Sprintf("[[https://github.com/%s/pull/%d][%s#%d]]", repo, number, repo, number)
}

// stamp is t as an inactive Org timestamp in local time.
func stamp(t time.Time) string { return t.Local().Format("[2006-01-02 Mon 15:04]") }

// Write replaces file with text, read-only, by renaming a new file over it,
// so that a reader never sees it half written.
func Write(file, text string) error { return writeFile(file, []byte(text), 0o444) }

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
