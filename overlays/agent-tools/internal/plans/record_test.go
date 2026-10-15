package plans

import (
	"strings"
	"sync"
	"testing"
)

// commitRepo makes a repository whose origin is GitHub's o/r, as a session
// commits in.
func commitRepo(t *testing.T) string {
	t.Helper()
	repo := stopRepo(t, false)
	gitIn(t, repo, "remote", "add", "origin", "https://github.com/o/r.git")
	return repo
}

// commitsOf are the summaries of the committed events in p's log, each with
// its session.
func commitsOf(t *testing.T, p Plan) []string {
	t.Helper()
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		if e.Kind == "committed" {
			out = append(out, e.Summary+" by "+e.Session)
		}
	}
	return out
}

// Every session that commits has a plan: a commit no plan takes makes one,
// named for the repository, which names the session and the worktree.
func TestRecordCommit(t *testing.T) {
	t.Run("no plan", func(t *testing.T) {
		c := testConfig(t)
		repo := commitRepo(t)
		p, created, err := c.RecordCommit("s1", repo, Event{Commit: "Add x", Summary: "Add x", Branch: "main"}, day)
		if err != nil || !created {
			t.Fatalf("created %v, %v", created, err)
		}
		if got := strings.Join(commitsOf(t, p), "\n"); got != "Add x by s1" {
			t.Errorf("commits %q", got)
		}
		items, _ := p.items()
		props := items[p.Name].Props
		for name, want := range map[string]string{"SESSIONS": "s1", "WORKTREES": repo, "REPOSITORIES": "o/r",
			"STATUS": "working", "PIPELINE": "full"} {
			if props[name] != want {
				t.Errorf("%s %q, want %q", name, props[name], want)
			}
		}
		if goal, err := p.Goal(""); err != nil || goal != "Unplanned work in o/r.\n" {
			t.Errorf("goal %q, %v", goal, err)
		}
		again, created, err := c.RecordCommit("s1", repo, Event{Commit: "Add y", Summary: "Add y"}, day)
		if err != nil || created || again.File() != p.File() {
			t.Errorf("second commit: %s, created %v, %v", again.File(), created, err)
		}
	})

	t.Run("a plan names the worktree", func(t *testing.T) {
		c := testConfig(t)
		repo := commitRepo(t)
		made, err := c.New(NewOptions{Title: "Add x", Slug: "x", Worktrees: []string{repo}}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, created, err := c.RecordCommit("s1", repo, Event{Commit: "Add x", Summary: "Add x"}, day)
		if err != nil || created || p.File() != made.Plan {
			t.Errorf("recorded in %s, created %v, %v; want %s", p.File(), created, err, made.Plan)
		}
	})

	// Hooks of the same session running at once make one plan between them.
	t.Run("concurrent commits", func(t *testing.T) {
		c := testConfig(t)
		repo := commitRepo(t)
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				subject := "Commit " + string(rune('a'+i))
				if _, _, err := c.RecordCommit("s1", repo, Event{Commit: subject, Summary: subject}, day); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		own, err := c.ownPlans()
		if err != nil || len(own) != 1 {
			t.Fatalf("%d plans, %v", len(own), err)
		}
		if got := commitsOf(t, own[0]); len(got) != 8 {
			t.Errorf("commits %q", got)
		}
	})
}

// What tools record is theirs alone to log: an agent's `agent-plan log` of
// one of those kinds is refused, saying what logs it.
func TestToolKindsNotLoggable(t *testing.T) {
	for _, kind := range []string{"committed", "pushed", "sign-requested", "signed", "sign-refused", "sign-failed", "sign-delivered",
		"review-receipt", "loop-began", "round", "replied", "resolved", "ci-waited", "loop-signed", "stop-blocked", "branch-changed"} {
		t.Run(kind, func(t *testing.T) {
			p := newPlan(t, testConfig(t), "x")
			_, err := p.Log(Event{Kind: kind, Item: p.Name}, day)
			isRefusal(t, kind, err)
		})
	}
}
