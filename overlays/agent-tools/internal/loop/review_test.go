package loop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
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
	reviews   []fakeReviewNode
}

// fakeReviewNode is a review a bot submitted on a commit.
type fakeReviewNode struct{ login, oid, body string }

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
		reviews := []any{}
		for i, r := range f.reviews {
			reviews = append(reviews, map[string]any{"author": map[string]any{"login": r.login},
				"commit": map[string]any{"oid": r.oid}, "body": r.body, "submittedAt": "2026-01-01T00:00:0" + strconv.Itoa(i) + "Z"})
		}
		data = map[string]any{"repository": map[string]any{"pullRequest": map[string]any{
			"isDraft": true, "headRefName": pr.head, "headRefOid": pr.headOid, "reviews": map[string]any{"nodes": reviews}}}}
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

// The loop is done with a PR only when every bot has reviewed its head and
// no bot's thread awaits an answer, whatever one bot's verdict says.
func TestStatusDone(t *testing.T) {
	const config = `{"bots": {
		"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"},
			"verdicts": {"approval": "Approval recommended", "closer-look": "closer look"}},
		"qodo": {"login": "^qodo-code-review", "request": {"comment": "/agentic_review"}}}}`
	done := func(t *testing.T) (bool, string) {
		t.Helper()
		registerLoop(t)
		out, err := Status(nil)
		if err != nil {
			t.Fatal(err)
		}
		entry := out.([]map[string]any)[0]
		ok, _ := entry["done"].(bool)
		waiting, _ := json.Marshal(entry["waitingOn"])
		return ok, string(waiting)
	}
	reviewed := []fakeReviewNode{{copilotLogin, "a1", "Copilot wants a closer look"}, {qodoLogin, "a1", "Review"}}

	t.Run("one bot clean, another's threads open", func(t *testing.T) {
		f := reviewRepo(t, config, fakeReviewThread{first: qodoLogin, last: qodoLogin})
		f.reviews = reviewed
		if ok, waiting := done(t); ok || !strings.Contains(waiting, "qodo") || strings.Contains(waiting, "copilot") {
			t.Errorf("done %v, waiting on %s", ok, waiting)
		}
	})

	t.Run("a bot has not reviewed the head", func(t *testing.T) {
		f := reviewRepo(t, config)
		f.reviews = []fakeReviewNode{{copilotLogin, "a1", "Approval recommended"}, {qodoLogin, "a0", "Review"}}
		if ok, waiting := done(t); ok || !strings.Contains(waiting, "qodo") {
			t.Errorf("done %v, waiting on %s", ok, waiting)
		}
	})

	t.Run("every bot reviewed, every thread answered", func(t *testing.T) {
		f := reviewRepo(t, config,
			fakeReviewThread{first: qodoLogin, last: "me"},
			fakeReviewThread{first: copilotLogin, last: "me"},
			fakeReviewThread{first: "josh", last: "josh"})
		f.reviews = reviewed
		if ok, waiting := done(t); !ok || waiting != "[]" {
			t.Errorf("done %v, waiting on %s", ok, waiting)
		}
	})

	t.Run("CI failing on the head", func(t *testing.T) {
		f := reviewRepo(t, config)
		f.reviews = reviewed
		f.checks["a1"] = []fakeCheck{{name: "build", status: "COMPLETED", conclusion: "FAILURE", job: 1}}
		if ok, waiting := done(t); ok || !strings.Contains(waiting, "ci") {
			t.Errorf("done %v, waiting on %s", ok, waiting)
		}
	})

	// GitHub has not worked out whether the PR merges: not done yet.
	t.Run("merge state unknown", func(t *testing.T) {
		f := reviewRepo(t, config)
		f.reviews = reviewed
		f.prs[1].mergeable = "UNKNOWN"
		if ok, waiting := done(t); ok || !strings.Contains(waiting, "base") {
			t.Errorf("done %v, waiting on %s", ok, waiting)
		}
	})
}

// A loop that does not converge pauses: after six rounds, or once a PR's
// changed lines have grown more than half again over the loop's start.
func TestStatusPaused(t *testing.T) {
	const config = `{"bots": {"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}}}}`
	status := func(t *testing.T, round, baseline int) map[string]any {
		t.Helper()
		state := &State{Repo: "o/r", Order: []string{"feat"}, Round: round,
			Branches: map[string]*Branch{"feat": {PR: 1, Base: "main", BaselineLines: baseline}}}
		if err := state.save(); err != nil {
			t.Fatal(err)
		}
		out, err := Status(nil)
		if err != nil {
			t.Fatal(err)
		}
		return out.([]map[string]any)[0]
	}
	// grow adds ten changed lines to feat.
	grow := func(t *testing.T) {
		t.Helper()
		os.WriteFile(filepath.Join(git.Dir, "ten"), []byte(strings.Repeat("line\n", 10)), 0o644)
		gitIn(t, git.Dir, "add", "ten")
		gitIn(t, git.Dir, "commit", "-qm", "Add ten lines")
	}

	t.Run("still converging", func(t *testing.T) {
		reviewRepo(t, config, fakeReviewThread{first: copilotLogin, last: copilotLogin})
		grow(t)
		if s := status(t, 5, 7); s["paused"] != false || s["round"] != 5 {
			t.Errorf("round 5, 10 lines from 7: %v", s)
		}
	})

	t.Run("six rounds", func(t *testing.T) {
		reviewRepo(t, config, fakeReviewThread{first: copilotLogin, last: copilotLogin})
		if s := status(t, 6, 0); s["paused"] != true || !strings.Contains(fmt.Sprint(s["pausedBecause"]), "6 rounds") {
			t.Errorf("after six rounds: %v", s)
		}
	})

	t.Run("growth over half", func(t *testing.T) {
		reviewRepo(t, config, fakeReviewThread{first: copilotLogin, last: copilotLogin})
		grow(t)
		if s := status(t, 1, 6); s["paused"] != true || !strings.Contains(fmt.Sprint(s["pausedBecause"]), "6 to 10") {
			t.Errorf("10 lines from 6: %v", s)
		}
	})

	// A bot asked for a review of the head that has not given one within
	// the budget holds the loop up without a push to count as a round.
	t.Run("a bot that does not review", func(t *testing.T) {
		reviewRepo(t, config)
		ask := func(ago time.Duration, head string) {
			state := &State{Repo: "o/r", Order: []string{"feat"}, Round: 1,
				Branches: map[string]*Branch{"feat": {PR: 1, Base: "main"}},
				Asked:    map[string]*Ask{"1:copilot": {At: now().Add(-ago).Format(time.RFC3339), HeadOid: head}}}
			if err := state.save(); err != nil {
				t.Fatal(err)
			}
		}
		get := func() map[string]any {
			out, err := Status(nil)
			if err != nil {
				t.Fatal(err)
			}
			return out.([]map[string]any)[0]
		}
		ask(2*time.Hour, "a1")
		if s := get(); s["paused"] != true || !strings.Contains(fmt.Sprint(s["pausedBecause"]), "copilot") {
			t.Errorf("asked two hours ago: %v", s)
		}
		ask(10*time.Minute, "a1")
		if s := get(); s["paused"] != false {
			t.Errorf("asked ten minutes ago: %v", s)
		}
		ask(2*time.Hour, "a0")
		if s := get(); s["paused"] != false {
			t.Errorf("asked about an earlier head: %v", s)
		}
	})

	// A loop that is done finishes, however long it took.
	t.Run("done outranks paused", func(t *testing.T) {
		f := reviewRepo(t, config)
		f.reviews = []fakeReviewNode{{copilotLogin, "a1", "Review"}}
		if s := status(t, 9, 0); s["done"] != true || s["paused"] != false {
			t.Errorf("done after nine rounds: %v", s)
		}
	})
}

func TestRequest(t *testing.T) {
	const config = `{"bots": {"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}}}}`

	// A thread nobody has answered must be triaged before the bot is asked
	// again, or its findings go unread while it adds more.
	t.Run("unanswered bot thread", func(t *testing.T) {
		f := reviewRepo(t, config,
			fakeReviewThread{first: copilotLogin, last: "me"},
			fakeReviewThread{first: copilotLogin, last: copilotLogin})
		registerLoop(t)
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
	// bot back: people's are the operator's, and the loop cannot answer them.
	t.Run("answered and people's threads", func(t *testing.T) {
		f := reviewRepo(t, config,
			fakeReviewThread{first: copilotLogin, last: "me"},
			fakeReviewThread{first: copilotLogin, last: copilotLogin, resolved: true},
			fakeReviewThread{first: "josh", last: "josh"})
		registerLoop(t)
		if _, err := Request(nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if len(f.requested) != 1 || f.requested[0] != "@copilot" {
			t.Errorf("requested %v, want [@copilot]", f.requested)
		}
		// The ask is recorded with the head it was for, which status times.
		state, _ := loadState(true)
		if a := state.Asked["1:copilot"]; a == nil || a.HeadOid != "a1" || a.At == "" {
			t.Errorf("the ask recorded: %+v", a)
		}
	})
}
