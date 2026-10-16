package loop

import (
	"encoding/json"
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

// fakeReview adds to fakeCI the GraphQL that status and request make: the
// open PRs' search, each PR's reviews, and its review threads two to a page.
type fakeReview struct {
	*fakeCI
	threads   []fakeReviewThread
	requested []string
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
	case strings.Contains(query, "reviewRequests"):
		pr := f.prs[vars["number"].(int)]
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"isDraft": true, "headRefName": pr.head, "headRefOid": pr.headOid}}}
	default:
		return f.fakeCI.graphQL(query, vars, out)
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

// reviewRepo is ciRepo with bots configured as config gives them, and CI
// passing on PR 1's head.
func reviewRepo(t *testing.T, config string, threads ...fakeReviewThread) *fakeReview {
	t.Helper()
	f := &fakeReview{fakeCI: ciRepo(t), threads: threads}
	f.checks["a1"] = []fakeCheck{{name: "build", status: "COMPLETED", conclusion: "SUCCESS"}}
	graphQL = f.graphQL
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_LOOP_CONFIG", path)
	old := requestReviewer
	requestReviewer = func(slug string, number int, reviewer string) error {
		f.requested = append(f.requested, reviewer)
		return nil
	}
	t.Cleanup(func() { requestReviewer = old })
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
	review, err := reviewStatus("o/r", 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]int{"copilot": 2, "pilot": 0, "qodo": 1} {
		if got := review.Bots[name].OpenThreads; got != want {
			t.Errorf("%s has %d open threads, want %d", name, got, want)
		}
	}
	if review.OpenHumanThreads != 1 {
		t.Errorf("%d open human threads, want 1", review.OpenHumanThreads)
	}
}
