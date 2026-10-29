package plans

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// commitRepo makes a repository whose origin is GitHub's o/r and whose
// origin/main is its first commit, as a session commits in.
func commitRepo(t *testing.T) string {
	t.Helper()
	for _, v := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(v, "T")
	}
	for _, v := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(v, "t@e")
	}
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
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

	t.Run("a plan names the linked worktree", func(t *testing.T) {
		c := testConfig(t)
		_, linked := checkouts(t, 1)
		made, err := c.New(NewOptions{Title: "Add x", Slug: "x", Worktrees: []string{linked[0]}}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, created, err := c.RecordCommit("s1", linked[0], Event{Commit: "Add x", Summary: "Add x"}, day)
		if err != nil || created || p.File() != made.Plan {
			t.Errorf("recorded in %s, created %v, %v; want %s", p.File(), created, err, made.Plan)
		}
	})

	// The main checkout is named by many plans and is no one plan's: a
	// commit there by a session no plan names is unplanned work.
	t.Run("a plan names only the main checkout", func(t *testing.T) {
		c := testConfig(t)
		repo, _ := checkouts(t, 0)
		made, err := c.New(NewOptions{Title: "Add x", Slug: "x", Worktrees: []string{repo}, Sessions: []string{"other"}}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, created, err := c.RecordCommit("s1", repo, Event{Commit: "Add x", Summary: "Add x"}, day)
		if err != nil || !created || p.File() == made.Plan {
			t.Errorf("recorded in %s, created %v, %v; want a new plan, not %s", p.File(), created, err, made.Plan)
		}
		if got := commitsOf(t, p); strings.Join(got, "\n") != "Add x by s1" {
			t.Errorf("commits %q", got)
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

// checkouts makes a repository, as commitRepo does, and n linked worktrees
// of it, and returns its main checkout and the linked worktrees.
func checkouts(t *testing.T, n int) (main string, linked []string) {
	t.Helper()
	main = commitRepo(t)
	for i := range n {
		dir, _ := filepath.EvalSymlinks(t.TempDir())
		gitIn(t, main, "worktree", "add", "-q", "-b", "w"+strconv.Itoa(i), dir)
		linked = append(linked, dir)
	}
	return main, linked
}

// locatePlan makes a plan, made at, whose own file names worktrees and
// sessions, and returns its file.
func locatePlan(t *testing.T, c Config, slug string, at time.Time, worktrees, sessions []string) string {
	t.Helper()
	made, err := c.New(NewOptions{Title: "Add " + slug, Slug: slug, Worktrees: worktrees, Sessions: sessions}, at)
	if err != nil {
		t.Fatal(err)
	}
	return made.Plan
}

// locatePart gives plan a part id whose file names worktrees and sessions.
func locatePart(t *testing.T, c Config, plan, id string, worktrees, sessions []string) {
	t.Helper()
	text, err := os.ReadFile(plan)
	if err != nil {
		t.Fatal(err)
	}
	withPart := strings.Replace(string(text), "** Distribution\n",
		"** Distribution\n*** The part\n:PROPERTIES:\n:ID: "+id+"\n:END:\n", 1)
	if withPart == string(text) {
		t.Fatalf("%s has no Distribution heading to add %s under", plan, id)
	}
	if err := os.WriteFile(plan, []byte(withPart), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := c.New(NewOptions{Title: "The part", Parent: plan, Part: id, Worktrees: worktrees,
		Sessions: sessions}, day); err != nil {
		t.Fatal(err)
	}
}

func TestLocate(t *testing.T) {
	later := day.AddDate(0, 0, 1)
	locate := func(t *testing.T, c Config, session, worktree, wantPlan, wantPart string) {
		t.Helper()
		p, part, ok := c.Locate(session, worktree)
		if !ok || p.File() != wantPlan || part != wantPart {
			t.Errorf("Locate(%q, %q) = %s part %q, %v; want %s part %q", session, worktree, p.File(), part, ok,
				wantPlan, wantPart)
		}
	}

	none := func(t *testing.T, c Config, session, worktree string) {
		t.Helper()
		if p, part, ok := c.Locate(session, worktree); ok {
			t.Errorf("Locate(%q, %q) = %s part %q; want none", session, worktree, p.File(), part)
		}
	}

	t.Run("the session's plan over an older plan of the worktree", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 2)
		locatePlan(t, c, "old", day, []string{w[0]}, []string{"claude:other"})
		mine := locatePlan(t, c, "mine", later, []string{w[1]}, []string{"claude:s1"})
		locate(t, c, "s1", w[0], mine, "")
	})

	t.Run("the session's older plan that names the worktree", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 2)
		older := locatePlan(t, c, "older", day, []string{w[0]}, []string{"claude:s1"})
		locatePlan(t, c, "newer", later, []string{w[1]}, []string{"claude:s1"})
		locate(t, c, "s1", w[0], older, "")
	})

	t.Run("the linked worktree's plan when no plan names the session", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 1)
		plan := locatePlan(t, c, "x", day, []string{w[0]}, []string{"claude:other"})
		locate(t, c, "s1", w[0], plan, "")
		locate(t, c, "", w[0], plan, "")
	})

	// Many plans name the main checkout, and none of them is the plan of a
	// session they do not name.
	t.Run("no plan for the main checkout when no plan names the session", func(t *testing.T) {
		c := testConfig(t)
		main, _ := checkouts(t, 0)
		locatePlan(t, c, "older", day, []string{main}, []string{"claude:other"})
		locatePlan(t, c, "newer", later, []string{main}, []string{"claude:another"})
		none(t, c, "s1", main)
		none(t, c, "", main)
	})

	// The session's older plan, such as an unplanned plan made for a
	// commit in the main checkout, is not its plan for naming it too.
	t.Run("the session's newest plan over its older plan of the main checkout", func(t *testing.T) {
		c := testConfig(t)
		main, w := checkouts(t, 1)
		locatePlan(t, c, "older", day, []string{main}, []string{"claude:s1"})
		newer := locatePlan(t, c, "newer", later, []string{w[0]}, []string{"claude:s1"})
		locate(t, c, "s1", main, newer, "")
	})

	t.Run("a part naming the worktree over its own file naming it", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 1)
		plan := locatePlan(t, c, "x", day, []string{w[0]}, []string{"claude:s1"})
		locatePart(t, c, plan, "P1", []string{w[0]}, []string{"claude:s1"})
		locate(t, c, "s1", w[0], plan, "P1")
	})

	t.Run("a part naming the worktree but no session", func(t *testing.T) {
		c := testConfig(t)
		main, w := checkouts(t, 1)
		plan := locatePlan(t, c, "x", day, []string{main}, []string{"claude:s1"})
		locatePart(t, c, plan, "P1", []string{w[0]}, nil)
		locate(t, c, "s1", w[0], plan, "P1")
	})

	// After a /clear only the plan's own file names the new session.
	t.Run("a part naming an earlier session", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 1)
		plan := locatePlan(t, c, "x", day, nil, []string{"claude:s1", "claude:s2"})
		locatePart(t, c, plan, "P1", []string{w[0]}, []string{"claude:s1"})
		locate(t, c, "s2", w[0], plan, "P1")
	})

	t.Run("the own file when no file names the worktree", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 2)
		plan := locatePlan(t, c, "x", day, nil, []string{"claude:s1"})
		locatePart(t, c, plan, "P1", []string{w[0]}, []string{"claude:s1"})
		locate(t, c, "s1", w[1], plan, "")
	})

	t.Run("the own file in the main checkout a part names", func(t *testing.T) {
		c := testConfig(t)
		main, _ := checkouts(t, 0)
		plan := locatePlan(t, c, "x", day, nil, []string{"claude:s1"})
		locatePart(t, c, plan, "P1", []string{main}, []string{"claude:s1"})
		locate(t, c, "s1", main, plan, "")
	})

	t.Run("the first part when only parts name the session", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 3)
		plan := locatePlan(t, c, "x", day, nil, []string{"claude:lead"})
		locatePart(t, c, plan, "P1", []string{w[0]}, []string{"claude:s1"})
		locatePart(t, c, plan, "P2", []string{w[1]}, []string{"claude:s1"})
		locate(t, c, "s1", w[2], plan, "P1")
	})

	t.Run("the newest of equally named plans", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 1)
		locatePlan(t, c, "older", day, []string{w[0]}, []string{"claude:s1"})
		newer := locatePlan(t, c, "newer", later, []string{w[0]}, []string{"claude:s1"})
		locate(t, c, "s1", w[0], newer, "")
	})

	t.Run("one key", func(t *testing.T) {
		c := testConfig(t)
		_, w := checkouts(t, 2)
		bySession := locatePlan(t, c, "a", day, []string{w[0]}, []string{"claude:s1"})
		byWorktree := locatePlan(t, c, "b", later, []string{w[1]}, []string{"claude:other"})
		locate(t, c, "s1", "", bySession, "")
		locate(t, c, "", w[1], byWorktree, "")
	})
}

// What tools record is theirs alone to log: an agent's `agent-plan log` of
// one of those kinds is refused, saying what logs it.
func TestToolKindsNotLoggable(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"committed", "pushed", "sign-requested", "signed", "sign-refused", "sign-failed", "sign-delivered",
		"loop-began", "round", "replied", "resolved", "ci-waited", "branch-changed", "property"} {
		t.Run(kind, func(t *testing.T) {
			p := newPlan(t, testConfig(t), "x")
			_, err := p.Log(Event{Kind: kind, Item: p.Name}, day)
			isRefusal(t, kind, err)
		})
	}
}
