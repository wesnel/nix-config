package loop

import (
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
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
	ID       string          `json:"id"`
	Path     string          `json:"path"`
	Line     *int            `json:"line"`
	Outdated bool            `json:"outdated"`
	Resolved bool            `json:"resolved"`
	Reviewer string          `json:"reviewer"`
	Comments []ThreadComment `json:"comments"`
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
		err := gh.GraphQL(`query($owner: String!, $name: String!, $number: Int!, $after: String) {
			repository(owner: $owner, name: $name) { pullRequest(number: $number) {
				reviewThreads(first: 50, after: $after) {
					pageInfo { hasNextPage endCursor }
					nodes { id isResolved isOutdated path line
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
				Reviewer: "human", Comments: []ThreadComment{}}
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
	return out, nil
}

type threadCheck struct {
	repo       *Repo
	lastAuthor string
}

// checkedThread applies the policy for answering a thread: a bot started it,
// on an open PR of the user's that this loop registered.
func checkedThread(id string) (*threadCheck, error) {
	state, err := loadState(true)
	if err != nil {
		return nil, err
	}
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	var data struct {
		Node *struct {
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
	err = gh.GraphQL(`query($id: ID!) { node(id: $id) { ... on PullRequestReviewThread {
		pullRequest { number state headRefName author { login } repository { nameWithOwner } }
		first: comments(first: 1) { nodes { author { login } } }
		last: comments(last: 1) { nodes { author { login } } }
	} } }`, map[string]any{"id": id}, &data)
	if err != nil {
		return nil, err
	}
	if data.Node == nil || len(data.Node.First.Nodes) == 0 {
		return nil, refuse("%s is not a review thread", id)
	}
	pr := data.Node.PullRequest
	switch {
	case pr.Repository.NameWithOwner != state.Repo:
		return nil, refuse("%s is in %s, not %s", id, pr.Repository.NameWithOwner, state.Repo)
	case pr.State != "OPEN" || pr.Author.Login != repo.Viewer:
		return nil, refuse("#%d is not an open pull request by %s", pr.Number, repo.Viewer)
	case state.Branches[pr.HeadRefName] == nil:
		return nil, refuse("#%d is not part of this loop", pr.Number)
	}
	bots, err := botsFor(state.Repo)
	if err != nil {
		return nil, err
	}
	starter := ""
	if a := data.Node.First.Nodes[0].Author; a != nil {
		starter = a.Login
	}
	if botNamed(bots, starter) == nil {
		return nil, refuse("%s was started by %s, not a review bot; those are left to Wesley", id, starter)
	}
	last := ""
	if n := data.Node.Last.Nodes; len(n) > 0 && n[0].Author != nil {
		last = n[0].Author.Login
	}
	return &threadCheck{repo: repo, lastAuthor: last}, nil
}

// Reply posts body inside a bot's review thread.
func Reply(id, body string) (any, error) {
	if strings.TrimSpace(body) == "" {
		return nil, refuse("the reply is empty")
	}
	if _, err := checkedThread(id); err != nil {
		return nil, err
	}
	var data struct {
		AddPullRequestReviewThreadReply struct{ Comment struct{ URL string } }
	}
	err := gh.GraphQL(`mutation($input: AddPullRequestReviewThreadReplyInput!) {
		addPullRequestReviewThreadReply(input: $input) { comment { url } } }`,
		map[string]any{"input": map[string]any{"pullRequestReviewThreadId": id, "body": body}}, &data)
	if err != nil {
		return nil, err
	}
	return map[string]string{"url": data.AddPullRequestReviewThreadReply.Comment.URL}, nil
}

// Resolve resolves a bot's thread whose last comment is the user's reply.
func Resolve(id string) (any, error) {
	check, err := checkedThread(id)
	if err != nil {
		return nil, err
	}
	if check.lastAuthor != check.repo.Viewer {
		return nil, refuse("the last comment in %s is from %s; reply before resolving", id, check.lastAuthor)
	}
	err = gh.GraphQL(`mutation($id: ID!) { resolveReviewThread(input: { threadId: $id }) { thread { isResolved } } }`,
		map[string]any{"id": id}, nil)
	if err != nil {
		return nil, err
	}
	return map[string]string{"resolved": id}, nil
}
