package loop

import (
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
)

// BotReview is one bot's standing on a PR's head.
type BotReview struct {
	ReviewedHead       bool   `json:"reviewedHead"`
	Pending            bool   `json:"pending"`
	Verdict            string `json:"verdict,omitempty"`
	NewComments        *int   `json:"newComments,omitempty"`
	OpenThreads        int    `json:"openThreads"`
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
				ReviewThreads struct {
					Nodes []struct {
						IsResolved bool
						Comments   struct{ Nodes []struct{ Author *login } }
					}
				}
			}
		}
	}
	err := gh.GraphQL(`query($owner: String!, $name: String!, $number: Int!) {
		repository(owner: $owner, name: $name) { pullRequest(number: $number) {
			isDraft headRefName headRefOid
			reviewRequests(first: 20) { nodes { requestedReviewer { ... on Bot { login } ... on User { login } } } }
			reviews(last: 50) { nodes { author { login } commit { oid } body submittedAt } }
			reviewThreads(first: 100) { nodes { isResolved comments(first: 1) { nodes { author { login } } } } }
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
	openBy := map[string]int{}
	for _, t := range pr.ReviewThreads.Nodes {
		if !t.IsResolved && len(t.Comments.Nodes) > 0 && t.Comments.Nodes[0].Author != nil {
			openBy[t.Comments.Nodes[0].Author.Login]++
		}
	}
	out := &Review{PR: number, Branch: pr.HeadRefName, Draft: pr.IsDraft, HeadOid: pr.HeadRefOid, Bots: map[string]*BotReview{}}
	for author, n := range openBy {
		if botNamed(bots, author) == nil {
			out.OpenHumanThreads += n
		}
	}
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
		for author, n := range openBy {
			if bot.Pattern.MatchString(author) {
				review.OpenThreads += n
			}
		}
		out.Bots[bot.Name] = review
	}
	return out, nil
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
// once CI on the head has finished without failures of the PR's own.
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
		for _, bot := range bots {
			if len(onlyBots) > 0 && !contains(onlyBots, bot.Name) {
				continue
			}
			current := review.Bots[bot.Name]
			if current.ReviewedHead || current.Pending {
				log = append(log, fmt.Sprintf("#%d: %s has already reviewed or been asked to review %.12s", pr.Number, bot.Name, ci.HeadOid))
				continue
			}
			if bot.Request.Reviewer != "" {
				cmd := exec.Command("gh", "pr", "edit", fmt.Sprint(pr.Number), "--repo", repo.Slug,
					"--add-reviewer", bot.Request.Reviewer)
				cmd.Dir = gh.Outside
				if out, err := cmd.CombinedOutput(); err != nil {
					return nil, fmt.Errorf("requesting %s on #%d: %w: %s", bot.Name, pr.Number, err, out)
				}
			} else {
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
