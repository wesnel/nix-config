package loop

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/record"
)

type login struct {
	Login string `json:"login"`
}

// author is a comment's author as GraphQL gives it: a login and its type,
// Bot for an app's account.
type author struct {
	Login    string
	Typename string `json:"__typename"`
}

// ThreadComment is one comment in a review thread.
type ThreadComment struct {
	Author string `json:"author"`
	Type   string `json:"type"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	At     string `json:"at"`
}

// Thread is a review thread, labelled with the bot that started it.
type Thread struct {
	ID       string          `json:"id"`
	Path     string          `json:"path"`
	Line     *int            `json:"line"`
	Outdated bool            `json:"outdated"`
	Resolved bool            `json:"resolved"`
	Reviewer string          `json:"reviewer"`
	Comments []ThreadComment `json:"comments"`
}

// commentPage is one page of a thread's comments.
type commentPage struct {
	TotalCount int
	PageInfo   struct {
		HasNextPage bool
		EndCursor   string
	}
	Nodes []struct {
		Author    *author
		Body      string
		URL       string
		CreatedAt string
	}
}

const commentFields = `totalCount pageInfo { hasNextPage endCursor } nodes { author { login __typename } body url createdAt }`

// comments reads every comment of thread id, from first, its first page,
// on: a thread read in part could hide a person's comment, so a page that
// fails, or a count that differs from GitHub's total, is an error.
func comments(id string, first commentPage) ([]ThreadComment, error) {
	var out []ThreadComment
	page := first
	for {
		for _, c := range page.Nodes {
			tc := ThreadComment{Author: "ghost", Type: "User", Body: c.Body, URL: c.URL, At: c.CreatedAt}
			if c.Author != nil {
				tc.Author, tc.Type = c.Author.Login, c.Author.Typename
			}
			out = append(out, tc)
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		var data struct {
			Node *struct{ Comments commentPage }
		}
		err := graphQL(`query($id: ID!, $after: String) { node(id: $id) { ... on PullRequestReviewThread {
			comments(first: 100, after: $after) { `+commentFields+` } } } }`,
			map[string]any{"id": id, "after": page.PageInfo.EndCursor}, &data)
		if err != nil {
			return nil, fmt.Errorf("reading the comments of %s: %w", id, err)
		}
		if data.Node == nil {
			return nil, fmt.Errorf("reading the comments of %s: GitHub returned no thread", id)
		}
		page = data.Node.Comments
	}
	if len(out) != first.TotalCount {
		return nil, fmt.Errorf("%s has %d comments on GitHub, and %d were read", id, first.TotalCount, len(out))
	}
	return out, nil
}

// isBot reports whether c is a configured review bot's: its login matches
// the bot's pattern, and GitHub types the account a bot.
func isBot(bots []*Bot, c ThreadComment) bool {
	return c.Type == "Bot" && botNamed(bots, c.Author) != nil
}

// Threads lists a PR's unresolved review threads (all of them with all),
// and writes every thread, resolved or not, with every comment, to the PR's
// review record (internal/record), keeping the drafts the record holds.
func Threads(number int, all bool) (any, error) {
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	bots, err := botsFor(repo.Slug)
	if err != nil {
		return nil, err
	}
	owner, name, _ := strings.Cut(repo.Slug, "/")
	var threads []Thread
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
							ID         string
							IsResolved bool
							IsOutdated bool
							Path       string
							Line       *int
							Comments   commentPage
						}
					}
				}
			}
		}
		err := graphQL(`query($owner: String!, $name: String!, $number: Int!, $after: String) {
			repository(owner: $owner, name: $name) { pullRequest(number: $number) {
				reviewThreads(first: 50, after: $after) {
					pageInfo { hasNextPage endCursor }
					nodes { id isResolved isOutdated path line comments(first: 100) { `+commentFields+` } }
				}
			} }
		}`, map[string]any{"owner": owner, "name": name, "number": number, "after": cursor}, &data)
		if err != nil {
			return nil, err
		}
		page := data.Repository.PullRequest.ReviewThreads
		for _, t := range page.Nodes {
			thread := Thread{ID: t.ID, Path: t.Path, Line: t.Line, Outdated: t.IsOutdated, Resolved: t.IsResolved, Reviewer: "human"}
			if thread.Comments, err = comments(t.ID, t.Comments); err != nil {
				return nil, err
			}
			if len(thread.Comments) > 0 && isBot(bots, thread.Comments[0]) {
				thread.Reviewer = botNamed(bots, thread.Comments[0].Author).Name
			}
			threads = append(threads, thread)
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		next := page.PageInfo.EndCursor
		cursor = &next
	}
	if err := writeThreads(repo, number, threads); err != nil {
		return nil, err
	}
	out := []Thread{}
	for _, t := range threads {
		if all || !t.Resolved {
			out = append(out, t)
		}
	}
	return out, nil
}

// unsafeLogin is what a record's AUTHORS cannot hold of a login.
var unsafeLogin = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// writeThreads replaces the threads in PR number's record with threads,
// keeping each one's draft.
func writeThreads(repo *Repo, number int, threads []Thread) error {
	path, err := prFile(repo, number)
	if err != nil {
		return err
	}
	return record.Update(path, func(p *record.PR) error {
		drafts := map[string]*record.Draft{}
		for _, t := range p.Threads {
			drafts[t.ID] = t.Draft
		}
		p.Repo, p.Number, p.Updated = repo.Slug, number, time.Now()
		p.Threads = nil
		for _, t := range threads {
			rt := record.Thread{ID: t.ID, Resolved: t.Resolved, Path: t.Path, Draft: drafts[t.ID]}
			if t.Line != nil {
				rt.Line = *t.Line
			}
			for _, c := range t.Comments {
				kind := c.Type
				if kind == "" {
					kind = "User"
				}
				rt.Comments = append(rt.Comments, record.Comment{Author: unsafeLogin.ReplaceAllString(c.Author, "-"),
					Type: unsafeLogin.ReplaceAllString(kind, ""), Body: c.Body})
			}
			p.Threads = append(p.Threads, rt)
		}
		return nil
	})
}

func prFile(repo *Repo, number int) (string, error) {
	owner, name, _ := strings.Cut(repo.Slug, "/")
	return record.PRFile(owner, name, number)
}

// threadPolicy decides which review threads may be answered: only those on
// an open PR by the authenticated user in this repository whose every
// comment is a configured bot's or the user's. A person's thread is the
// operator's to answer, through a people-mode loop's drafts and
// `pr-loop post`.
type threadPolicy struct {
	repo    *Repo
	bots    []*Bot
	botsErr error
	loaded  bool
}

func newThreadPolicy() (*threadPolicy, error) {
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	return &threadPolicy{repo: repo}, nil
}

// threadCheck is what the policy read of a thread.
type threadCheck struct {
	pr         int
	resolved   bool
	lastAuthor string
	comments   []ThreadComment
}

// read reads thread id with every comment, refusing one that is not on an
// open PR by the authenticated user in this repository.
func (p *threadPolicy) read(id string) (*threadCheck, error) {
	var data struct {
		Node *struct {
			IsResolved  bool
			PullRequest struct {
				Number     int
				State      string
				Author     login
				Repository struct{ NameWithOwner string }
			}
			Comments commentPage
		}
	}
	err := graphQL(`query($id: ID!) { node(id: $id) { ... on PullRequestReviewThread {
		isResolved
		pullRequest { number state author { login } repository { nameWithOwner } }
		comments(first: 100) { `+commentFields+` }
	} } }`, map[string]any{"id": id}, &data)
	if err != nil {
		return nil, err
	}
	if data.Node == nil || data.Node.Comments.TotalCount == 0 {
		return nil, refuse("%s is not a review thread; take thread IDs from `pr-loop threads PR`", id)
	}
	pr := data.Node.PullRequest
	switch {
	case pr.Repository.NameWithOwner != p.repo.Slug:
		return nil, refuse("%s is in %s, not %s; take thread IDs from `pr-loop threads PR`", id, pr.Repository.NameWithOwner, p.repo.Slug)
	case pr.State != "OPEN" || pr.Author.Login != p.repo.Viewer:
		return nil, refuse("#%d is not an open pull request by %s, which pr-loop never writes to; leave it to the operator", pr.Number, p.repo.Viewer)
	}
	all, err := comments(id, data.Node.Comments)
	if err != nil {
		return nil, err
	}
	return &threadCheck{pr: pr.Number, resolved: data.Node.IsResolved, lastAuthor: all[len(all)-1].Author, comments: all}, nil
}

// check is read, refusing a thread with a comment by anyone but a
// configured bot and the authenticated user.
func (p *threadPolicy) check(id string) (*threadCheck, error) {
	thread, err := p.read(id)
	if err != nil {
		return nil, err
	}
	if !p.loaded {
		p.loaded = true
		p.bots, p.botsErr = botsFor(p.repo.Slug)
	}
	if p.botsErr != nil {
		return nil, p.botsErr
	}
	for _, c := range thread.comments {
		if c.Author != p.repo.Viewer && !isBot(p.bots, c) {
			return nil, refuse("%s on #%d has a comment by %s, who is neither a configured review bot nor %s, and a person's "+
				"thread is the operator's to answer; in a people-mode loop draft the answer with `pr-loop draft`, and "+
				"otherwise put it in the report", id, thread.pr, c.Author, p.repo.Viewer)
		}
	}
	return thread, nil
}

// postReply replies in thread id, on PR pr, and logs the reply.
func postReply(pr int, id, body string) (string, error) {
	var data struct {
		AddPullRequestReviewThreadReply struct{ Comment struct{ URL string } }
	}
	err := graphQL(`mutation($input: AddPullRequestReviewThreadReplyInput!) {
		addPullRequestReviewThreadReply(input: $input) { comment { url } } }`,
		map[string]any{"input": map[string]any{"pullRequestReviewThreadId": id, "body": body}}, &data)
	if err == nil {
		logEvent(plans.Event{Kind: "replied", Summary: fmt.Sprintf("in thread %s on #%d", id, pr)})
	}
	return data.AddPullRequestReviewThreadReply.Comment.URL, err
}

// resolveThread resolves thread id, on PR pr, and logs it.
func resolveThread(pr int, id string) error {
	err := graphQL(`mutation($id: ID!) { resolveReviewThread(input: { threadId: $id }) { thread { isResolved } } }`,
		map[string]any{"id": id}, nil)
	if err == nil {
		logEvent(plans.Event{Kind: "resolved", Summary: fmt.Sprintf("thread %s on #%d", id, pr)})
	}
	return err
}

// Reply posts body inside a review thread the policy allows, in a
// bots-mode loop.
func Reply(id, body string) (any, error) {
	if err := requireMode("reply", "bots"); err != nil {
		return nil, err
	}
	if strings.TrimSpace(body) == "" {
		return nil, refuse("the reply is empty; pass its text with `--body-file FILE`, or on stdin with `--body-file -`")
	}
	policy, err := newThreadPolicy()
	if err != nil {
		return nil, err
	}
	check, err := policy.check(id)
	if err != nil {
		return nil, err
	}
	url, err := postReply(check.pr, id, body)
	if err != nil {
		return nil, err
	}
	return map[string]string{"url": url}, nil
}

// Resolve resolves a thread the policy allows, once its last comment is the
// user's reply, so no thread is closed without an answer.
func Resolve(id string) (any, error) {
	policy, err := newThreadPolicy()
	if err != nil {
		return nil, err
	}
	check, err := policy.check(id)
	if err != nil {
		return nil, err
	}
	if check.lastAuthor != policy.repo.Viewer {
		return nil, refuse("the last comment in %s is from %s, so it has no answer yet; reply with `pr-loop reply %s` before resolving",
			id, check.lastAuthor, id)
	}
	if err := resolveThread(check.pr, id); err != nil {
		return nil, err
	}
	return map[string]string{"resolved": id}, nil
}
