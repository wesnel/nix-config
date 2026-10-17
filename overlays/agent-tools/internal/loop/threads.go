package loop

import (
	"fmt"
	"os"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/relay"
)

type login struct {
	Login string `json:"login"`
}

// ThreadComment is one comment in a review thread.
type ThreadComment struct {
	Author string `json:"author"`
	Body   string `json:"body"`
	URL    string `json:"url"`
	At     string `json:"at"`
}

// Thread is a review thread, labelled with the bot that started it.
type Thread struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Line     *int   `json:"line"`
	Outdated bool   `json:"outdated"`
	Resolved bool   `json:"resolved"`
	Reviewer string `json:"reviewer"`
	// In a loop, OnChangedLines says whether the thread sits on a line the
	// PR changed, as the loop last pushed it, and OnLoopFixup whether a
	// fixup changed that line last. Either is absent where it cannot be
	// told, as on an outdated thread.
	OnChangedLines *bool           `json:"onChangedLines,omitempty"`
	OnLoopFixup    *bool           `json:"onLoopFixup,omitempty"`
	Comments       []ThreadComment `json:"comments"`
	// side is the diff's side the line is on: RIGHT, the head, or LEFT,
	// the base.
	side string
}

func slugOf(state *State) (string, error) {
	if state != nil {
		return state.Repo, nil
	}
	repo, err := repository()
	if err != nil {
		return "", err
	}
	return repo.Slug, nil
}

// Threads lists a PR's unresolved review threads (all of them with all).
func Threads(number int, all bool) (any, error) {
	state, err := loadState(false)
	if err != nil {
		return nil, err
	}
	slug, err := slugOf(state)
	if err != nil {
		return nil, err
	}
	bots, err := botsFor(slug)
	if err != nil {
		return nil, err
	}
	owner, name, _ := strings.Cut(slug, "/")
	out := []Thread{}
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
							DiffSide   string
							Comments   struct {
								Nodes []struct {
									Author    *login
									Body      string
									URL       string
									CreatedAt string
								}
							}
						}
					}
				}
			}
		}
		err := graphQL(`query($owner: String!, $name: String!, $number: Int!, $after: String) {
			repository(owner: $owner, name: $name) { pullRequest(number: $number) {
				reviewThreads(first: 50, after: $after) {
					pageInfo { hasNextPage endCursor }
					nodes { id isResolved isOutdated path line diffSide
						comments(first: 100) { nodes { author { login } body url createdAt } } }
				}
			} }
		}`, map[string]any{"owner": owner, "name": name, "number": number, "after": cursor}, &data)
		if err != nil {
			return nil, err
		}
		threads := data.Repository.PullRequest.ReviewThreads
		for _, t := range threads.Nodes {
			if t.IsResolved && !all {
				continue
			}
			thread := Thread{ID: t.ID, Path: t.Path, Line: t.Line, Outdated: t.IsOutdated, Resolved: t.IsResolved,
				Reviewer: "human", Comments: []ThreadComment{}, side: t.DiffSide}
			for _, c := range t.Comments.Nodes {
				author := ""
				if c.Author != nil {
					author = c.Author.Login
				}
				thread.Comments = append(thread.Comments, ThreadComment{Author: author, Body: c.Body, URL: c.URL, At: c.CreatedAt})
			}
			if len(thread.Comments) > 0 {
				if bot := botNamed(bots, thread.Comments[0].Author); bot != nil {
					thread.Reviewer = bot.Name
				}
			}
			out = append(out, thread)
		}
		if !threads.PageInfo.HasNextPage {
			break
		}
		next := threads.PageInfo.EndCursor
		cursor = &next
	}
	if state != nil && state.Repo == slug {
		markThreads(state, number, out)
	}
	return out, nil
}

// markThreads sets each thread's OnChangedLines and OnLoopFixup from blame
// over the PR's commits, up to the tip the loop last pushed of PR number's
// branch: a line is the PR's when the commit that last changed it is one
// of the PR's, and a loop fixup's when that commit is a fixup! the branch
// did not have when the loop began. A thread on the base's side of the
// diff is on a line the PR removed. Whatever git cannot answer is left
// unmarked.
func markThreads(state *State, number int, threads []Thread) {
	branch, ok := state.prBranches()[number]
	if !ok || state.Branches[branch].RemoteOid == "" {
		return
	}
	tip := state.Branches[branch].RemoteOid
	parent, err := state.parentRef(&Repo{Slug: state.Repo}, branch)
	if err != nil {
		return
	}
	base, err := git.Run("merge-base", parent, tip)
	if err != nil {
		return
	}
	list, err := git.Run("rev-list", base+".."+tip)
	if err != nil {
		return
	}
	own := map[string]bool{}
	for _, oid := range strings.Fields(list) {
		own[oid] = true
	}
	began := map[Commit]bool{}
	for _, c := range state.Branches[branch].Commits {
		began[c] = true
	}
	yes, no := true, false
	for i := range threads {
		t := &threads[i]
		if t.Outdated || t.Line == nil || t.Path == "" {
			continue
		}
		if t.side == "LEFT" {
			t.OnChangedLines = &yes
			continue
		}
		// Blamed over base..tip, a line older than the PR goes to the
		// boundary, base, without walking the base's history.
		blame, err := git.Run("blame", "--porcelain", "-L", fmt.Sprintf("%d,%d", *t.Line, *t.Line), base+".."+tip, "--", t.Path)
		if err != nil {
			continue
		}
		oid, _, _ := strings.Cut(blame, " ")
		if !own[oid] {
			t.OnChangedLines, t.OnLoopFixup = &no, &no
			continue
		}
		t.OnChangedLines, t.OnLoopFixup = &yes, &no
		if commits, err := history.Log("-1", oid); err == nil && len(commits) == 1 {
			if kind, _ := fold(commits[0].Message); kind == history.Fixup && !began[commits[0]] {
				t.OnLoopFixup = &yes
			}
		}
	}
}

// sessionEnv is set by every review-loop launcher.
const sessionEnv = "PR_LOOP_SESSION"

// inLoopSession reports whether an unattended review loop is the caller.
// Every loop session reaches pr-loop through sandbox-relay, which sets
// relay.RelayEnv on the host, out of the sandbox's reach. No argument can
// turn this off: a loop must never answer people's threads.
func inLoopSession() bool {
	return os.Getenv(relay.RelayEnv) == "1" || os.Getenv(sessionEnv) == "1"
}

// threadPolicy decides which review threads may be answered. In a loop
// session, and on a PR registered with begin, only threads a review bot
// started on the loop's PRs; people's threads there are the operator's.
// Otherwise any thread on an open PR by the authenticated user in this
// repository.
type threadPolicy struct {
	repo    *Repo
	state   *State // nil when no loop is registered here
	session bool
	bots    []*Bot
	botsErr error
	loaded  bool
}

func newThreadPolicy() (*threadPolicy, error) {
	session := inLoopSession()
	state, err := loadState(session)
	if err != nil {
		return nil, err
	}
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	return &threadPolicy{repo: repo, state: state, session: session}, nil
}

// threadCheck is a thread the policy lets the caller answer.
type threadCheck struct {
	pr         int
	resolved   bool
	lastAuthor string
}

func (p *threadPolicy) check(id string) (*threadCheck, error) {
	var data struct {
		Node *struct {
			IsResolved  bool
			PullRequest struct {
				Number      int
				State       string
				HeadRefName string
				Author      login
				Repository  struct{ NameWithOwner string }
			}
			First struct{ Nodes []struct{ Author *login } }
			Last  struct{ Nodes []struct{ Author *login } }
		}
	}
	err := graphQL(`query($id: ID!) { node(id: $id) { ... on PullRequestReviewThread {
		isResolved
		pullRequest { number state headRefName author { login } repository { nameWithOwner } }
		first: comments(first: 1) { nodes { author { login } } }
		last: comments(last: 1) { nodes { author { login } } }
	} } }`, map[string]any{"id": id}, &data)
	if err != nil {
		return nil, err
	}
	if data.Node == nil || len(data.Node.First.Nodes) == 0 {
		return nil, refuse("%s is not a review thread; take thread IDs from `pr-loop threads PR`", id)
	}
	pr := data.Node.PullRequest
	switch {
	case pr.Repository.NameWithOwner != p.repo.Slug:
		return nil, refuse("%s is in %s, not %s; take thread IDs from `pr-loop threads PR`", id, pr.Repository.NameWithOwner, p.repo.Slug)
	case pr.State != "OPEN" || pr.Author.Login != p.repo.Viewer:
		return nil, refuse("#%d is not an open pull request by %s, which pr-loop never writes to; leave it to the operator", pr.Number, p.repo.Viewer)
	}
	registered := p.state != nil && p.state.Repo == p.repo.Slug && p.state.Branches[pr.HeadRefName] != nil
	if p.session || registered {
		switch {
		case p.state.Repo != p.repo.Slug:
			return nil, refuse("this checkout is %s, but the loop was started for %s; run pr-loop from a checkout of %s",
				p.repo.Slug, p.state.Repo, p.state.Repo)
		case !registered:
			return nil, refuse(notInLoop, pr.Number)
		}
		if !p.loaded {
			p.loaded = true
			p.bots, p.botsErr = botsFor(p.state.Repo)
		}
		if p.botsErr != nil {
			return nil, p.botsErr
		}
		starter := ""
		if a := data.Node.First.Nodes[0].Author; a != nil {
			starter = a.Login
		}
		if botNamed(p.bots, starter) == nil {
			return nil, refuse("%s on #%d was started by %s, not a review bot, and in a review loop those are the operator's to answer; "+
				"put what you would reply in the report", id, pr.Number, starter)
		}
	}
	last := ""
	if n := data.Node.Last.Nodes; len(n) > 0 && n[0].Author != nil {
		last = n[0].Author.Login
	}
	return &threadCheck{pr: pr.Number, resolved: data.Node.IsResolved, lastAuthor: last}, nil
}

// postReply replies in thread id, on PR pr, and logs the reply.
func postReply(state *State, pr int, id, body string) (string, error) {
	var data struct {
		AddPullRequestReviewThreadReply struct{ Comment struct{ URL string } }
	}
	err := graphQL(`mutation($input: AddPullRequestReviewThreadReplyInput!) {
		addPullRequestReviewThreadReply(input: $input) { comment { url } } }`,
		map[string]any{"input": map[string]any{"pullRequestReviewThreadId": id, "body": body}}, &data)
	if err == nil {
		record(state, plans.Event{Kind: "replied", Summary: fmt.Sprintf("in thread %s on #%d", id, pr)})
	}
	return data.AddPullRequestReviewThreadReply.Comment.URL, err
}

// resolveThread resolves thread id, on PR pr, and logs it.
func resolveThread(state *State, pr int, id string) error {
	err := graphQL(`mutation($id: ID!) { resolveReviewThread(input: { threadId: $id }) { thread { isResolved } } }`,
		map[string]any{"id": id}, nil)
	if err == nil {
		record(state, plans.Event{Kind: "resolved", Summary: fmt.Sprintf("thread %s on #%d", id, pr)})
	}
	return err
}

// Reply posts body inside a review thread the policy allows.
func Reply(id, body string) (any, error) {
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
	url, err := postReply(policy.state, check.pr, id, body)
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
	if err := resolveThread(policy.state, check.pr, id); err != nil {
		return nil, err
	}
	return map[string]string{"resolved": id}, nil
}
