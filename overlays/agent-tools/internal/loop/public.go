package loop

import (
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/publictext"
)

// checkPublic refuses text, what a command would post to GitHub as what,
// where it names what publictext finds, knowing threads as the review
// threads its author has seen. then is what to do once it is rewritten.
func checkPublic(what, text string, threads []string, then string) error {
	ctx := publictext.In(git.Dir)
	ctx.Threads = append(ctx.Threads, threads...)
	if reason := publictext.Refusal(what, publictext.Check(text, ctx), then); reason != "" {
		return refuse("%s", reason)
	}
	return nil
}

// checkReplies refuses the replies of plan, checks being their threads',
// unless every one passes checkPublic, naming each that does not.
func checkReplies(slug string, plan []PlannedReply, checks []*threadCheck) error {
	threads := map[int][]string{}
	for i, r := range plan {
		if _, ok := threads[checks[i].pr]; !ok {
			ids, err := prThreads(slug, checks[i].pr)
			if err != nil {
				return err
			}
			threads[checks[i].pr] = ids
		}
		threads[checks[i].pr] = append(threads[checks[i].pr], r.Thread)
	}
	var reasons []string
	for i, r := range plan {
		if err := checkPublic("the reply to "+r.Thread, r.Body, threads[checks[i].pr], "run `pr-loop reply --from` again"); err != nil {
			reasons = append(reasons, err.Error())
		}
	}
	if len(reasons) > 0 {
		return refuse("nothing was posted: %s", strings.Join(reasons, "\n"))
	}
	return nil
}

// prThreads are the IDs of every review thread on PR number in slug.
func prThreads(slug string, number int) ([]string, error) {
	owner, name, _ := strings.Cut(slug, "/")
	var ids []string
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
						Nodes []struct{ ID string }
					}
				}
			}
		}
		err := graphQL(`query($owner: String!, $name: String!, $number: Int!, $after: String) {
			repository(owner: $owner, name: $name) { pullRequest(number: $number) {
				reviewThreads(first: 100, after: $after) { pageInfo { hasNextPage endCursor } nodes { id } }
			} }
		}`, map[string]any{"owner": owner, "name": name, "number": number, "after": cursor}, &data)
		if err != nil {
			return nil, err
		}
		threads := data.Repository.PullRequest.ReviewThreads
		for _, n := range threads.Nodes {
			ids = append(ids, n.ID)
		}
		if !threads.PageInfo.HasNextPage {
			return ids, nil
		}
		next := threads.PageInfo.EndCursor
		cursor = &next
	}
}
