package loop

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/redact"
)

// BotReview is one bot's standing on a PR's head.
type BotReview struct {
	ReviewedHead       bool   `json:"reviewedHead"`
	Pending            bool   `json:"pending"`
	Verdict            string `json:"verdict,omitempty"`
	NewComments        *int   `json:"newComments,omitempty"`
	OpenThreads        int    `json:"openThreads"`
	UnansweredThreads  int    `json:"unansweredThreads"` // open, with a bot's comment last
	ResolvesOwnThreads bool   `json:"resolvesOwnThreads"`
	At                 string `json:"at,omitempty"`
}

// Review is every bot's standing on a PR, plus the people's open threads.
type Review struct {
	PR               int                   `json:"pr"`
	Branch           string                `json:"branch"`
	Draft            bool                  `json:"draft"`
	HeadOid          string                `json:"headOid"`
	Bots             map[string]*BotReview `json:"bots"`
	OpenHumanThreads int                   `json:"openHumanThreads"`
}

var generated = regexp.MustCompile(`Comments generated:\*\*\s*(\d+)`)

func reviewStatus(slug string, number int, state *State) (*Review, error) {
	owner, name, _ := strings.Cut(slug, "/")
	var data struct {
		Repository struct {
			PullRequest struct {
				IsDraft        bool
				HeadRefName    string
				HeadRefOid     string
				ReviewRequests struct {
					Nodes []struct{ RequestedReviewer *login }
				}
				Reviews struct {
					Nodes []struct {
						Author      *login
						Commit      *struct{ Oid string }
						Body        string
						SubmittedAt string
					}
				}
			}
		}
	}
	err := graphQL(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { pullRequest(number: $number) {
			isDraft headRefName headRefOid
			reviewRequests(first: 20) { nodes { requestedReviewer { ... on Bot { login } ... on User { login } } } }
			reviews(last: 50) { nodes { author { login } commit { oid } body submittedAt } }
		} }
	}`, map[string]any{"owner": owner, "name": name, "number": number}, &data)
	if err != nil {
		return nil, err
	}
	pr := data.Repository.PullRequest
	bots, err := botsFor(slug)
	if err != nil {
		return nil, err
	}
	var requested []string
	for _, r := range pr.ReviewRequests.Nodes {
		if r.RequestedReviewer != nil {
			requested = append(requested, r.RequestedReviewer.Login)
		}
	}
	open, unanswered, err := openThreads(slug, number, bots)
	if err != nil {
		return nil, err
	}
	out := &Review{PR: number, Branch: pr.HeadRefName, Draft: pr.IsDraft, HeadOid: pr.HeadRefOid, Bots: map[string]*BotReview{},
		OpenHumanThreads: open[""]}
	for _, bot := range bots {
		review := &BotReview{ResolvesOwnThreads: bot.ResolvesOwnThreads}
		var latest *struct {
			Author      *login
			Commit      *struct{ Oid string }
			Body        string
			SubmittedAt string
		}
		for i := range pr.Reviews.Nodes {
			r := &pr.Reviews.Nodes[i]
			if r.Author != nil && bot.Pattern.MatchString(r.Author.Login) {
				latest = r
			}
		}
		if latest != nil {
			review.At = latest.SubmittedAt
			review.ReviewedHead = latest.Commit != nil && latest.Commit.Oid == pr.HeadRefOid
			if len(bot.Verdicts) > 0 {
				review.Verdict = verdictOf(bot.Verdicts, latest.Body)
			}
			if m := generated.FindStringSubmatch(latest.Body); m != nil {
				var n int
				fmt.Sscan(m[1], &n)
				review.NewComments = &n
			}
		}
		if bot.Request.Reviewer != "" {
			for _, login := range requested {
				review.Pending = review.Pending || bot.Pattern.MatchString(login)
			}
		} else if state != nil {
			if t := state.Triggers[fmt.Sprintf("%d:%s", number, bot.Name)]; t != nil && t.HeadOid == pr.HeadRefOid {
				review.Pending = latest == nil || latest.SubmittedAt <= t.At
			}
		}
		review.OpenThreads, review.UnansweredThreads = open[bot.Name], unanswered[bot.Name]
		out.Bots[bot.Name] = review
	}
	return out, nil
}

// openThreads counts a PR's unresolved review threads by the bot that
// started them, as `pr-loop threads` labels them, with people's under "",
// and the bots' threads whose last comment is a bot's. It reads every page:
// a request is gated on these counts.
func openThreads(slug string, number int, bots []*Bot) (open, unanswered map[string]int, err error) {
	owner, name, _ := strings.Cut(slug, "/")
	open, unanswered = map[string]int{}, map[string]int{}
	author := func(nodes []struct{ Author *login }) string {
		if len(nodes) > 0 && nodes[0].Author != nil {
			return nodes[0].Author.Login
		}
		return ""
	}
	var cursor *string
	for {
		var data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						PageInfo struct {
							HasNextPage bool
							EndCursor   string
						}
						Nodes []struct {
							IsResolved bool
							First      struct{ Nodes []struct{ Author *login } }
							Last       struct{ Nodes []struct{ Author *login } }
						}
					}
				}
			}
		}
		err := graphQL(`query($owner: String!, $name: String!, $number: Int!, $after: String) {
			repository(owner: $owner, name: $name) { pullRequest(number: $number) {
				reviewThreads(first: 100, after: $after) {
					pageInfo { hasNextPage endCursor }
					nodes { isResolved
					first: comments(first: 1) { nodes { author { login } } }
					last: comments(last: 1) { nodes { author { login } } } }
				}
			} }
		}`, map[string]any{"owner": owner, "name": name, "number": number, "after": cursor}, &data)
		if err != nil {
			return nil, nil, err
		}
		threads := data.Repository.PullRequest.ReviewThreads
		for _, t := range threads.Nodes {
			if t.IsResolved {
				continue
			}
			reviewer := ""
			if bot := botNamed(bots, author(t.First.Nodes)); bot != nil {
				reviewer = bot.Name
			}
			open[reviewer]++
			if reviewer != "" && botNamed(bots, author(t.Last.Nodes)) != nil {
				unanswered[reviewer]++
			}
		}
		if !threads.PageInfo.HasNextPage {
			return open, unanswered, nil
		}
		next := threads.PageInfo.EndCursor
		cursor = &next
	}
}

// Status is CI, each bot's review of the head, open threads, and how much the
// PR's diff has grown since the loop began.
func Status(numbers []int) (any, error) {
	slug, numbers, state, err := loopNumbers(numbers)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, n := range numbers {
		review, err := reviewStatus(slug, n, state)
		if err != nil {
			return nil, err
		}
		ci, err := ciStatus(slug, n, "")
		if err != nil {
			return nil, err
		}
		entry := map[string]any{"pr": n, "branch": review.Branch, "draft": review.Draft, "headOid": review.HeadOid,
			"bots": review.Bots, "openHumanThreads": review.OpenHumanThreads, "ci": ci}
		if state != nil {
			if b, ok := state.prBranches()[n]; ok {
				if parent, err := state.parentRef(&Repo{Slug: state.Repo}, b); err == nil {
					if lines, err := diffLines(parent, b); err == nil {
						entry["changedLines"] = map[string]int{"baseline": state.Branches[b].BaselineLines, "now": lines}
					}
				}
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// Request asks each bot that has neither reviewed the head nor been asked,
// once CI on the head has finished without failures of the PR's own and
// every thread the bot started has an answer.
func Request(numbers []int, onlyBots, acceptFailures []string) (any, error) {
	state, repo, prs, err := context(numbers)
	if err != nil {
		return nil, err
	}
	bots, err := botsFor(repo.Slug)
	if err != nil {
		return nil, err
	}
	var log []string
	for _, b := range state.Order {
		pr, ok := prs[b]
		if !ok {
			continue
		}
		ci, err := ciStatus(repo.Slug, pr.Number, "")
		if err != nil {
			return nil, err
		}
		if !ci.finished() {
			reason := ci.State
			if len(ci.Pending) > 0 {
				reason = strings.Join(ci.Pending, ", ")
			}
			return nil, refuse("CI on #%d has not finished (%s); run `pr-loop wait ci %d` first", pr.Number, reason, pr.Number)
		}
		key := fmt.Sprintf("%d:%s", pr.Number, ci.HeadOid)
		accepted := map[string]bool{}
		for _, name := range append(acceptFailures, state.AcceptedFailures[key]...) {
			accepted[name] = true
		}
		var own []string
		for _, c := range ci.Failed {
			if c.OnBase != "failed" && !accepted[c.Name] {
				own = append(own, c.Name)
			}
		}
		if len(own) > 0 {
			return nil, refuse("CI on #%d fails in %s, which do not fail on its base. Fix that before asking for "+
				"review. If a failure is shown not to be this PR's, waiving it is the operator's call: put the evidence in "+
				"the report, with `pr-loop request --accept-failure NAME %d` for them to run", pr.Number, strings.Join(own, ", "), pr.Number)
		}
		if len(acceptFailures) > 0 {
			for name := range accepted {
				state.AcceptedFailures[key] = appendUnique(state.AcceptedFailures[key], name)
			}
		}
		review, err := reviewStatus(repo.Slug, pr.Number, state)
		if err != nil {
			return nil, err
		}
		var ask []*Bot
		var unanswered []string
		for _, bot := range bots {
			if len(onlyBots) > 0 && !contains(onlyBots, bot.Name) {
				continue
			}
			current := review.Bots[bot.Name]
			switch {
			case current.ReviewedHead || current.Pending:
				log = append(log, fmt.Sprintf("#%d: %s has already reviewed or been asked to review %.12s", pr.Number, bot.Name, ci.HeadOid))
			case current.UnansweredThreads > 0:
				unanswered = append(unanswered, fmt.Sprintf("%d from %s", current.UnansweredThreads, bot.Name))
			default:
				ask = append(ask, bot)
			}
		}
		// Only threads a bot has the last word in hold it back. People's
		// threads are the operator's, which the loop cannot answer; and a
		// thread answered but left open, deferred or left for a bot that
		// resolves its own, has been triaged.
		if len(unanswered) > 0 {
			return nil, refuse("#%d has review threads nobody has answered (%s). Triage them before asking for another "+
				"review: list them with `pr-loop threads %d`, reply to each, resolving it or fixing it first, "+
				"then run `pr-loop request` again", pr.Number, strings.Join(unanswered, ", "), pr.Number)
		}
		for _, bot := range ask {
			if bot.Request.Reviewer != "" {
				if err := requestReviewer(repo.Slug, pr.Number, bot.Request.Reviewer); err != nil {
					return nil, fmt.Errorf("requesting %s on #%d: %w", bot.Name, pr.Number, err)
				}
			} else {
				if err := redact.Outgoing(bot.Request.Comment); err != nil {
					return nil, fmt.Errorf("bot %s's request.comment: %w", bot.Name, err)
				}
				triggerKey := fmt.Sprintf("%d:%s", pr.Number, bot.Name)
				if previous := state.Triggers[triggerKey]; previous != nil {
					_ = gh.REST("DELETE", fmt.Sprintf("repos/%s/issues/comments/%d", repo.Slug, previous.CommentID), nil, nil)
				}
				var comment struct {
					ID        int64  `json:"id"`
					CreatedAt string `json:"created_at"`
				}
				err := gh.REST("POST", fmt.Sprintf("repos/%s/issues/%d/comments", repo.Slug, pr.Number),
					map[string]string{"body": bot.Request.Comment}, &comment)
				if err != nil {
					return nil, err
				}
				state.Triggers[triggerKey] = &Trigger{CommentID: comment.ID, At: comment.CreatedAt, HeadOid: ci.HeadOid}
			}
			if err := state.save(); err != nil {
				return nil, err
			}
			log = append(log, fmt.Sprintf("#%d: requested a review from %s of %.12s", pr.Number, bot.Name, ci.HeadOid))
		}
	}
	return log, state.save()
}

// requestReviewer asks reviewer to review PR number in slug. Tests that fake
// GitHub replace it.
var requestReviewer = func(slug string, number int, reviewer string) error {
	cmd := exec.Command("gh", "pr", "edit", fmt.Sprint(number), "--repo", slug, "--add-reviewer", reviewer)
	cmd.Dir = gh.Outside
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// WaitReviews blocks until requested reviews land. It reports settled=false
// at the deadline rather than failing.
func WaitReviews(numbers []int, timeout, interval time.Duration) (any, bool, error) {
	slug, numbers, _, err := loopNumbers(numbers)
	if err != nil {
		return nil, false, err
	}
	deadline := time.Now().Add(timeout)
	for {
		var statuses []any
		settled := true
		for _, n := range numbers {
			state, err := loadState(false)
			if err != nil {
				return nil, false, err
			}
			review, err := reviewStatus(slug, n, state)
			if err != nil {
				return nil, false, err
			}
			for _, b := range review.Bots {
				settled = settled && !b.Pending
			}
			statuses = append(statuses, review)
		}
		if settled || time.Now().After(deadline) {
			return statuses, settled, nil
		}
		time.Sleep(interval)
	}
}

// verdictOf prefers a verdict named in a Markdown heading, since a review's
// prose can mention another verdict's words; it falls back to the whole body
// for verdicts, such as an error, that are not headings.
func verdictOf(verdicts map[string]string, body string) string {
	names := make([]string, 0, len(verdicts))
	for name := range verdicts {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		for _, name := range names {
			if strings.Contains(line, verdicts[name]) {
				return name
			}
		}
	}
	for _, name := range names {
		if strings.Contains(body, verdicts[name]) {
			return name
		}
	}
	return "unknown"
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func appendUnique(list []string, s string) []string {
	if contains(list, s) {
		return list
	}
	return append(list, s)
}
