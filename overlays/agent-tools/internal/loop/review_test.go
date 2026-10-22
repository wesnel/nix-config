package loop

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type fakeReviewThread struct {
	resolved    bool
	first, last string // the authors of its first and last comments
}

// fakeSubmitted is a review submitted on PR 1.
type fakeSubmitted struct{ login, commit, at string }

// fakeIssueComment is a top-level comment on PR 1.
type fakeIssueComment struct {
	id          int64
	login, body string
	at          string
}

// fakeReview adds to fakeCI the GraphQL that status and request make: the
// open PRs' search, PR 1's reviews and top-level comments, and its review
// threads two to a page; and the REST calls that post and delete top-level
// comments.
type fakeReview struct {
	*fakeCI
	threads   []fakeReviewThread
	reviews   []fakeSubmitted
	comments  []fakeIssueComment
	requested []string
	posted    []string
	deleted   []int64
}

func (f *fakeReview) graphQL(query string, vars map[string]any, out any) error {
	var data any
	switch {
	case strings.Contains(query, "search(query"):
		nodes := []any{}
		for n, pr := range f.prs {
			if pr.state == "OPEN" {
				nodes = append(nodes, map[string]any{"id": "PR" + strconv.Itoa(n), "number": n, "isCrossRepository": pr.cross,
					"author": map[string]any{"login": pr.author}, "headRefName": pr.head, "headRefOid": pr.headOid, "baseRefName": "main"})
			}
		}
		data = map[string]any{"search": map[string]any{"nodes": nodes}}
	case strings.Contains(query, "reviewThreads"):
		start := 0
		if after, ok := vars["after"].(*string); ok && after != nil {
			start, _ = strconv.Atoi(*after)
		}
		end := min(start+2, len(f.threads))
		nodes := []any{}
		for _, t := range f.threads[start:end] {
			author := func(login string) any {
				return map[string]any{"nodes": []any{map[string]any{"author": map[string]any{"login": login}}}}
			}
			nodes = append(nodes, map[string]any{"isResolved": t.resolved, "first": author(t.first), "last": author(t.last)})
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"reviewThreads": map[string]any{"nodes": nodes,
				"pageInfo": map[string]any{"hasNextPage": end < len(f.threads), "endCursor": strconv.Itoa(end)}}}}}
	case strings.Contains(query, "comments(first: 100, after: $after)"):
		nodes := []any{}
		for _, c := range f.comments {
			nodes = append(nodes, map[string]any{"databaseId": c.id, "body": c.body, "createdAt": c.at,
				"author": map[string]any{"login": c.login}})
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"comments": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false}}}}}
	case strings.Contains(query, "reviewRequests"):
		pr := f.prs[vars["number"].(int)]
		reviews := []any{}
		for _, r := range f.reviews {
			reviews = append(reviews, map[string]any{"author": map[string]any{"login": r.login},
				"commit": map[string]any{"oid": r.commit}, "body": "", "submittedAt": r.at})
		}
		requested := []any{}
		for _, login := range f.requested {
			requested = append(requested, map[string]any{"requestedReviewer": map[string]any{"login": strings.TrimPrefix(login, "@")}})
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"isDraft": true, "headRefName": pr.head, "headRefOid": pr.headOid,
			"reviews": map[string]any{"nodes": reviews}, "reviewRequests": map[string]any{"nodes": requested}}}}
	default:
		return f.fakeCI.graphQL(query, vars, out)
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

func (f *fakeReview) rest(method, path string, body, out any) error {
	switch {
	case method == "POST" && path == "repos/o/r/issues/1/comments":
		text := body.(map[string]string)["body"]
		f.posted = append(f.posted, text)
		f.comments = append(f.comments, fakeIssueComment{id: int64(100 + len(f.posted)), login: "me", body: text, at: "2026-10-20T12:00:00Z"})
	case method == "DELETE" && strings.HasPrefix(path, "repos/o/r/issues/comments/"):
		id, _ := strconv.ParseInt(strings.TrimPrefix(path, "repos/o/r/issues/comments/"), 10, 64)
		f.deleted = append(f.deleted, id)
		for i, c := range f.comments {
			if c.id == id {
				f.comments = append(f.comments[:i], f.comments[i+1:]...)
				break
			}
		}
	default:
		return errors.New("unexpected call: " + method + " " + path)
	}
	return nil
}

// reviewRepo is ciRepo in a bots-mode loop, with bots configured as config
// gives them, and CI passing on PR 1's head, a1.
func reviewRepo(t *testing.T, config string, threads ...fakeReviewThread) *fakeReview {
	t.Helper()
	f := &fakeReview{fakeCI: ciRepo(t), threads: threads}
	t.Setenv(ModeEnv, "bots")
	f.checks["a1"] = []fakeCheck{{name: "build", status: "COMPLETED", conclusion: "SUCCESS"}}
	graphQL = f.graphQL
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_LOOP_CONFIG", path)
	oldRequest, oldREST := requestReviewer, commentsREST
	requestReviewer = func(slug string, number int, reviewer string) error {
		f.requested = append(f.requested, reviewer)
		return nil
	}
	commentsREST = f.rest
	t.Cleanup(func() { requestReviewer, commentsREST = oldRequest, oldREST })
	return f
}

const (
	copilotLogin = "copilot-pull-request-reviewer"
	qodoLogin    = "qodo-code-review"
)

func TestReviewStatusThreads(t *testing.T) {
	// pilot's login pattern also matches Copilot's: a thread is counted
	// once, for the first bot by name that matches its author, as `pr-loop
	// threads` labels it.
	reviewRepo(t, `{"bots": {
		"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}},
		"pilot": {"login": "pilot", "request": {"comment": "/pilot"}},
		"qodo": {"login": "^qodo-code-review", "request": {"comment": "/agentic_review"}, "resolvesOwnThreads": true}}}`,
		fakeReviewThread{first: copilotLogin, last: copilotLogin},
		fakeReviewThread{first: copilotLogin, last: "me"},
		fakeReviewThread{first: copilotLogin, last: "me", resolved: true},
		fakeReviewThread{first: "josh", last: "josh"},
		fakeReviewThread{first: qodoLogin, last: qodoLogin},
	)
	review, err := reviewStatus("o/r", 1, "me")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][2]int{"copilot": {2, 1}, "pilot": {0, 0}, "qodo": {1, 1}} {
		got := review.Bots[name]
		if got.OpenThreads != want[0] || got.UnansweredThreads != want[1] {
			t.Errorf("%s has %d open threads, %d unanswered, want %d and %d",
				name, got.OpenThreads, got.UnansweredThreads, want[0], want[1])
		}
	}
	if review.OpenHumanThreads != 1 {
		t.Errorf("%d open human threads, want 1", review.OpenHumanThreads)
	}
}

func TestRequest(t *testing.T) {
	const config = `{"bots": {"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}}}}`
	const qodo = `{"bots": {"qodo": {"login": "^qodo-code-review", "request": {"comment": "/agentic_review"}, "resolvesOwnThreads": true}}}`

	t.Run("outside a bots-mode loop", func(t *testing.T) {
		for _, mode := range []string{"", "people"} {
			f := reviewRepo(t, config)
			t.Setenv(ModeEnv, mode)
			if _, err := Request(nil, nil, nil); !refusedWell(err) || len(f.requested) != 0 {
				t.Errorf("mode %q: %v, requested %v", mode, err, f.requested)
			}
		}
	})

	t.Run("copilot as a reviewer", func(t *testing.T) {
		f := reviewRepo(t, config)
		if _, err := Request(nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(f.requested) != "[@copilot]" || len(f.posted) != 0 {
			t.Errorf("requested %v, posted %v", f.requested, f.posted)
		}
	})

	// A draft, where qodo does not review on its own, gets the trigger
	// comment, and the earlier identical one is deleted first.
	t.Run("qodo on a draft", func(t *testing.T) {
		f := reviewRepo(t, qodo)
		f.comments = []fakeIssueComment{{id: 7, login: "me", body: "/agentic_review", at: "2026-10-19T09:00:00Z"},
			{id: 8, login: "josh", body: "/agentic_review", at: "2026-10-19T09:00:00Z"}}
		f.reviews = []fakeSubmitted{{login: qodoLogin, commit: "a0", at: "2026-10-19T10:00:00Z"}}
		if _, err := Request(nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(f.posted) != "[/agentic_review]" || fmt.Sprint(f.deleted) != "[7]" {
			t.Errorf("posted %v, deleted %v", f.posted, f.deleted)
		}
	})

	t.Run("qodo has reviewed this head", func(t *testing.T) {
		f := reviewRepo(t, qodo)
		f.reviews = []fakeSubmitted{{login: qodoLogin, commit: "a1", at: "2026-10-19T10:00:00Z"}}
		if _, err := Request(nil, nil, nil); err != nil || len(f.posted) != 0 {
			t.Errorf("asked again for a reviewed head: %v, posted %v", err, f.posted)
		}
	})

	// After a push, qodo, which never reviews on push, is asked again: its
	// last review is of the earlier head, after the earlier trigger.
	t.Run("qodo after a push", func(t *testing.T) {
		f := reviewRepo(t, qodo)
		f.comments = []fakeIssueComment{{id: 7, login: "me", body: "/agentic_review", at: "2026-10-19T09:00:00Z"}}
		f.reviews = []fakeSubmitted{{login: qodoLogin, commit: "a0", at: "2026-10-19T10:00:00Z"}}
		if _, err := Request(nil, nil, nil); err != nil || len(f.posted) != 1 {
			t.Fatalf("not asked again after a push: %v, posted %v", err, f.posted)
		}
		// Asked, and not yet answered: pending, so not asked twice.
		if _, err := Request(nil, nil, nil); err != nil || len(f.posted) != 1 {
			t.Errorf("asked twice: %v, posted %v", err, f.posted)
		}
	})

	// A thread nobody has answered must be triaged before the bot is asked
	// again, or its findings go unread while it adds more.
	t.Run("unanswered bot thread", func(t *testing.T) {
		f := reviewRepo(t, config,
			fakeReviewThread{first: copilotLogin, last: "me"},
			fakeReviewThread{first: copilotLogin, last: copilotLogin})
		_, err := Request(nil, nil, nil)
		if !refused(err) || !strings.Contains(err.Error(), "pr-loop threads 1") {
			t.Errorf("an unanswered thread: %v", err)
		}
		if len(f.requested) > 0 {
			t.Errorf("requested %v", f.requested)
		}
	})

	// Answered threads left open (deferred, awaiting the operator, or left
	// for a bot that resolves its own) and people's threads do not hold the
	// bot back: people's are the operator's.
	t.Run("answered and people's threads", func(t *testing.T) {
		f := reviewRepo(t, config,
			fakeReviewThread{first: copilotLogin, last: "me"},
			fakeReviewThread{first: copilotLogin, last: copilotLogin, resolved: true},
			fakeReviewThread{first: "josh", last: "josh"})
		if _, err := Request(nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if len(f.requested) != 1 || f.requested[0] != "@copilot" {
			t.Errorf("requested %v, want [@copilot]", f.requested)
		}
	})

	t.Run("CI failing", func(t *testing.T) {
		f := reviewRepo(t, config)
		f.checks["a1"] = []fakeCheck{{name: "build", status: "COMPLETED", conclusion: "FAILURE"}}
		if _, err := Request(nil, nil, nil); !refused(err) || len(f.requested) != 0 {
			t.Errorf("CI failing: %v", err)
		}
		if _, err := Request(nil, nil, []string{"build"}); err != nil || len(f.requested) != 1 {
			t.Errorf("an accepted failure: %v, %v", err, f.requested)
		}
	})
}

// end deletes the viewer's trigger comments alone.
func TestEnd(t *testing.T) {
	f := reviewRepo(t, `{"bots": {"qodo": {"login": "^qodo-code-review", "request": {"comment": "/agentic_review"}}}}`)
	t.Setenv(ModeEnv, "")
	f.comments = []fakeIssueComment{{id: 7, login: "me", body: "/agentic_review"}, {id: 8, login: "josh", body: "/agentic_review"},
		{id: 9, login: "me", body: "Thanks"}, {id: 10, login: "me", body: "/agentic_review"}}
	got, err := End(nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(f.deleted) != "[7 10]" || fmt.Sprint(got) != "map[deletedTriggers:map[#1:2]]" {
		t.Errorf("deleted %v, got %v", f.deleted, got)
	}
}
