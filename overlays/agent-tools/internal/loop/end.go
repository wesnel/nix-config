package loop

import (
	"fmt"
	"strings"
)

// issueComment is one of the authenticated user's top-level comments on a
// pull request.
type issueComment struct {
	ID   int64
	Body string
	At   string
}

// viewerComments lists viewer's top-level comments on PR number, oldest
// first, reading every page.
func viewerComments(slug string, number int, viewer string) ([]issueComment, error) {
	owner, name, _ := strings.Cut(slug, "/")
	var out []issueComment
	var cursor *string
	for {
		var data struct {
			Repository struct {
				PullRequest struct {
					Comments struct {
						PageInfo struct {
							HasNextPage bool
							EndCursor   string
						}
						Nodes []struct {
							DatabaseID int64
							Body       string
							CreatedAt  string
							Author     *login
						}
					}
				}
			}
		}
		err := graphQL(`query($owner: String!, $name: String!, $number: Int!, $after: String) {
			repository(owner: $owner, name: $name) { pullRequest(number: $number) {
				comments(first: 100, after: $after) {
					pageInfo { hasNextPage endCursor }
					nodes { databaseId body createdAt author { login } }
				}
			} }
		}`, map[string]any{"owner": owner, "name": name, "number": number, "after": cursor}, &data)
		if err != nil {
			return nil, err
		}
		comments := data.Repository.PullRequest.Comments
		for _, c := range comments.Nodes {
			if c.Author != nil && c.Author.Login == viewer {
				out = append(out, issueComment{ID: c.DatabaseID, Body: c.Body, At: c.CreatedAt})
			}
		}
		if !comments.PageInfo.HasNextPage {
			return out, nil
		}
		next := comments.PageInfo.EndCursor
		cursor = &next
	}
}

// lastTrigger is the newest of comments whose body is trigger, or nil.
func lastTrigger(comments []issueComment, trigger string) *issueComment {
	var last *issueComment
	for i := range comments {
		if comments[i].Body == trigger {
			last = &comments[i]
		}
	}
	return last
}

// deleteTriggers deletes the authenticated user's top-level comments on PR
// number whose body is one of triggers, and returns how many it deleted.
func deleteTriggers(repo *Repo, number int, triggers []string) (int, error) {
	comments, err := viewerComments(repo.Slug, number, repo.Viewer)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, c := range comments {
		if !contains(triggers, c.Body) {
			continue
		}
		if err := commentsREST("DELETE", fmt.Sprintf("repos/%s/issues/comments/%d", repo.Slug, c.ID), nil, nil); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

// End deletes the authenticated user's trigger comments, those whose body
// is a configured bot's request.comment, on the PRs ciTargets resolves.
// request deletes its own earlier one too, so a loop that never ends
// leaves one comment per bot, not one per round.
func End(args []string) (any, error) {
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	numbers, err := targetsIn(repo, args)
	if err != nil {
		return nil, err
	}
	bots, err := botsFor(repo.Slug)
	if err != nil {
		return nil, err
	}
	var triggers []string
	for _, b := range bots {
		if b.Request.Comment != "" {
			triggers = append(triggers, b.Request.Comment)
		}
	}
	out := map[string]int{}
	for _, n := range numbers {
		deleted, err := deleteTriggers(repo, n, triggers)
		if err != nil {
			return nil, err
		}
		out[fmt.Sprintf("#%d", n)] = deleted
	}
	return map[string]any{"deletedTriggers": out}, nil
}
