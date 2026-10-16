package plans

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// REVIEWS and PIPELINE are tracked: each change through agent-plan is
// logged, and what stop-check and the gates read is the log's value, so a
// hand edit of the file is drift, not an opt-out.
func TestReviewsAndPipeline(t *testing.T) {
	t.Run("set and logged", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		if err := p.SetProperty("", "REVIEWS", []string{"security"}, day); err != nil {
			t.Fatal(err)
		}
		if err := p.SetProperty("", "PIPELINE", []string{"opted-out:operator-asked"}, day); err != nil {
			t.Fatal(err)
		}
		expectList(t, "logged", propertyEvents(t, p), p.Name+" WORKTREES = /w/loop", p.Name+" BRANCHES = main..loop",
			p.Name+" REVIEWS = security", p.Name+" PIPELINE = opted-out:operator-asked")
		expectList(t, "drift", drift(t, p))
		events, _ := p.Events()
		if got := p.pipeline(events); got != "opted-out:operator-asked" {
			t.Errorf("pipeline %q", got)
		}
	})

	t.Run("a hand edit", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		setProp(t, p.File(), "PIPELINE", "opted-out:by-hand")
		expectList(t, "drift", drift(t, p), p.Name+` PIPELINE: file "opted-out:by-hand", log "full"`)
		events, _ := p.Events()
		if got := p.pipeline(events); got != "full" {
			t.Errorf("pipeline %q, want full: the file's value is drift", got)
		}
	})

	// A plan made before PIPELINE was tracked has no PIPELINE event; its
	// file's "full" is the default the log gives, so it is no drift.
	t.Run("never logged", func(t *testing.T) {
		c := testConfig(t)
		made, err := c.New(NewOptions{Title: "Old", Slug: "old"}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := c.Open(made.Plan)
		expectList(t, "drift", drift(t, p))
	})

	t.Run("refused", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		for what, set := range map[string][]string{"REVIEWS unknown": {"REVIEWS", "style"},
			"PIPELINE off": {"PIPELINE", "off"}, "PIPELINE no reason": {"PIPELINE", "opted-out:"},
			"PIPELINE two": {"PIPELINE", "full", "full"}} {
			isRefusal(t, what, p.SetProperty("", set[0], set[1:], day))
		}
	})
}

// A waiver is logged by `agent-plan waive` alone, with its route and why.
func TestWaive(t *testing.T) {
	p := newPlan(t, testConfig(t), "x")
	e, err := p.Waive("security", "only comments change", day)
	if err != nil {
		t.Fatal(err)
	}
	if e.Kind != "waived" || e.Focus != "security" || e.Evidence != "only comments change" {
		t.Errorf("event %+v", e)
	}
	isRefusal(t, "unknown route", func() error { _, err := p.Waive("style", "x", day); return err }())
	isRefusal(t, "no reason", func() error { _, err := p.Waive("security", " ", day); return err }())
	isRefusal(t, "logged by hand", func() error {
		_, err := p.Log(Event{Kind: "waived", Item: p.Name, Evidence: "x"}, day)
		return err
	}())
	for _, k := range []string{"routed", "route-receipt", "review-issued"} {
		isRefusal(t, k, func() error { _, err := p.Log(Event{Kind: k, Item: p.Name}, day); return err }())
	}
}

// routeRepo is a repository whose origin/main is its first commit, with
// branch loop checked out on two commits above it, the second adding
// risky.go, and its commits oldest first.
func routeRepo(t *testing.T) (string, []string) {
	t.Helper()
	repo := stopRepo(t, false)
	gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	gitIn(t, repo, "checkout", "-q", "-b", "loop")
	commitFile(t, repo, "a.txt", "Add a")
	commitFile(t, repo, "risky.go", "Add risky")
	return repo, strings.Fields(gitIn(t, repo, "rev-list", "--reverse", "origin/main..loop"))
}

func head(t *testing.T, repo string) string {
	t.Helper()
	return strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
}

// routeReceipt logs a route receipt of base..tip in p as a review in repo
// would, with the patch ID of the change as it stands.
func routeReceipt(t *testing.T, p Plan, repo, focus, base, tip, branch string, findings ...string) {
	t.Helper()
	if _, err := p.Receipt("", Receipt{Range: base + ".." + tip, Tip: head(t, repo), PatchID: patchIDOf(t, repo, base),
		Reviewer: "codex", Focus: focus, Branch: branch, FindingIDs: findings, Findings: len(findings)}, day); err != nil {
		t.Fatal(err)
	}
}

func due(t *testing.T, c Config, p Plan, repo string) []string {
	t.Helper()
	s, err := c.RouteState(p, repo, "loop", head(t, repo))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range s.Due {
		short := []string{}
		for _, u := range d.Uncovered {
			short = append(short, u[:7])
		}
		out = append(out, d.Route+" uncovered "+strings.Join(short, ",")+" unsettled "+strings.Join(d.Unsettled, ","))
	}
	return out
}

func short(commits ...string) string {
	var out []string
	for _, c := range commits {
		out = append(out, c[:7])
	}
	return strings.Join(out, ",")
}

// A route is owed from a routed event, or from REVIEWS, until every commit
// that needs it lies inside a route receipt of its focus whose patch ID
// still matches, and the latest such receipt's findings are settled.
func TestRouteState(t *testing.T) {
	routed := func(t *testing.T, p Plan, repo string, commits ...string) {
		t.Helper()
		if _, err := p.Routed("", Routed{Branch: "loop", Base: strings.TrimSpace(gitIn(t, repo, "rev-parse", "origin/main")),
			Tip: head(t, repo), Routes: map[string][]string{"security": commits}}, day); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("routed, then met", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits[1])
		expectList(t, "routed", due(t, c, p, repo), "security uncovered "+short(commits[1])+" unsettled ")
		// A general review and a review of another focus meet nothing.
		if _, err := p.Receipt("", Receipt{Range: "origin/main..loop", Tip: head(t, repo), PatchID: patchID(t, repo)}, day); err != nil {
			t.Fatal(err)
		}
		routeReceipt(t, p, repo, "compat", "origin/main", "loop", "loop")
		expectList(t, "general and other focus", due(t, c, p, repo), "security uncovered "+short(commits[1])+" unsettled ")
		routeReceipt(t, p, repo, "security", "origin/main", "loop", "loop")
		expectList(t, "met", due(t, c, p, repo))
	})

	// A receipt covers the commits it reviewed, at the tip it recorded,
	// while that range keeps its patch ID; a commit routed after it is
	// owed on its own.
	t.Run("a commit routed after the receipt", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits[1])
		routeReceipt(t, p, repo, "security", "origin/main", "HEAD", "loop")
		commitFile(t, repo, "b.go", "Add b")
		expectList(t, "not routed", due(t, c, p, repo))
		routed(t, p, repo, commits[1], head(t, repo))
		expectList(t, "routed", due(t, c, p, repo), "security uncovered "+short(head(t, repo))+" unsettled ")
	})

	t.Run("REVIEWS owes every commit", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		if err := p.SetProperty("", "REVIEWS", []string{"security"}, day); err != nil {
			t.Fatal(err)
		}
		// Taking it out again owes it still: the union of every value.
		if err := p.SetProperty("", "REVIEWS", nil, day); err != nil {
			t.Fatal(err)
		}
		expectList(t, "reviews", due(t, c, p, repo), "security uncovered "+short(commits...)+" unsettled ")
	})

	t.Run("waived", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits[1])
		if _, err := p.Waive("security", "operator said so", day); err != nil {
			t.Fatal(err)
		}
		s, err := c.RouteState(p, repo, "loop", head(t, repo))
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Due) != 0 || len(s.Waivers) != 1 || s.Waivers[0].Route != "security" || s.Waivers[0].Why != "operator said so" {
			t.Errorf("state %+v", s)
		}
	})

	t.Run("findings unsettled", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits[1])
		routeReceipt(t, p, repo, "security", "origin/main", "loop", "loop", "security-1", "security-2")
		expectList(t, "unsettled", due(t, c, p, repo), "security uncovered  unsettled 1.security-1,1.security-2")
		for _, id := range []string{"1.security-1", "1.security-2"} {
			if _, err := p.Log(Event{Kind: "finding-settled", Item: p.Name, Review: id, Outcome: "fix"}, day); err != nil {
				t.Fatal(err)
			}
		}
		expectList(t, "settled", due(t, c, p, repo))
		// The next round's receipt is the latest, and its findings are its own.
		routeReceipt(t, p, repo, "security", "origin/main", "loop", "loop", "security-1")
		expectList(t, "round 2", due(t, c, p, repo), "security uncovered  unsettled 2.security-1")
	})

	// After a rewrite that keeps the change, such as a signing pass, the
	// commits routed are gone; the whole range is then owed, and a receipt
	// whose range, named, still has its patch ID covers it.
	t.Run("rewritten", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits[1])
		gitIn(t, repo, "commit", "-q", "--amend", "-m", "Add risky, reworded")
		now := strings.Fields(gitIn(t, repo, "rev-list", "--reverse", "origin/main..loop"))
		expectList(t, "rewritten", due(t, c, p, repo), "security uncovered "+short(now...)+" unsettled ")
		routeReceipt(t, p, repo, "security", "origin/main", "loop", "loop")
		gitIn(t, repo, "commit", "-q", "--amend", "-m", "Add risky, reworded again")
		expectList(t, "receipt by name", due(t, c, p, repo))
	})

	// A receipt's findings count only in its own plan, and only for the
	// branch it was made for: another branch's unsettled finding, in this
	// plan or another, owes this branch nothing.
	t.Run("another branch's findings", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits[1])
		routeReceipt(t, p, repo, "security", "origin/main", "loop", "loop")
		gitIn(t, repo, "checkout", "-q", "-b", "other", "origin/main")
		commitFile(t, repo, "other.go", "Add other")
		q := newPlan(t, c, "other")
		routeReceipt(t, q, repo, "security", "origin/main", "other", "other", "security-1")
		routeReceipt(t, p, repo, "security", "origin/main", "other", "other", "security-2")
		gitIn(t, repo, "checkout", "-q", "loop")
		expectList(t, "another branch's findings", due(t, c, p, repo))
	})

	// A routed event of a branch of the same name whose base and commits
	// this repository lacks, as in another repository, owes nothing here.
	t.Run("routed in another repository", func(t *testing.T) {
		c := testConfig(t)
		repo, _ := routeRepo(t)
		p := newPlan(t, c, "x")
		missing := strings.Repeat("ab", 20)
		if _, err := p.Routed("", Routed{Branch: "loop", Base: strings.Repeat("cd", 20), Tip: missing,
			Routes: map[string][]string{"security": {missing}}}, day); err != nil {
			t.Fatal(err)
		}
		expectList(t, "another repository", due(t, c, p, repo))
	})

	// Commits routed and since merged are no longer the change: a branch
	// reusing the name owes nothing for them.
	t.Run("routed, then merged", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits[1])
		gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "loop")
		commitFile(t, repo, "c.txt", "Add c")
		expectList(t, "merged", due(t, c, p, repo))
	})

	// In a stack, the parent branch's receipt, in its own plan, covers the
	// commits below the child's.
	t.Run("stack", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		gitIn(t, repo, "branch", "parent", commits[0])
		parentPlan := newPlan(t, c, "parent")
		gitIn(t, repo, "checkout", "-q", "parent")
		routeReceipt(t, parentPlan, repo, "security", "origin/main", "parent", "parent")
		gitIn(t, repo, "checkout", "-q", "loop")
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits...)
		expectList(t, "child unreviewed", due(t, c, p, repo), "security uncovered "+short(commits[1])+" unsettled ")
		routeReceipt(t, p, repo, "security", "parent", "loop", "loop")
		expectList(t, "both", due(t, c, p, repo))
	})

	// A receipt made on one branch for another branch's range, as a loop
	// working on its own branch could make for anyone's, covers nothing of
	// that other branch.
	t.Run("made on another branch", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		gitIn(t, repo, "branch", "other", "origin/main")
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits...)
		routeReceipt(t, newPlan(t, c, "other"), repo, "security", "origin/main", "loop", "other")
		expectList(t, "made on other", due(t, c, p, repo), "security uncovered "+short(commits...)+" unsettled ")
	})

	// A log an older agent-plan wrote holds review receipts alone, which
	// meet no route.
	t.Run("older log", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := newPlan(t, c, "x")
		routed(t, p, repo, commits[1])
		f, err := os.OpenFile(filepath.Join(p.Dir, p.Name+".log.org"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("* review-receipt " + p.Name + " — origin/main..loop: 0 findings\n:PROPERTIES:\n:EVENT: e1\n:ITEM: " +
			p.Name + "\n:KIND: review-receipt\n:RANGE: origin/main..loop\n:PATCH_ID: " + patchID(t, repo) + "\n:END:\n[2026-10-14 Wed 13:05]\n")
		f.Close()
		expectList(t, "older", due(t, c, p, repo), "security uncovered "+short(commits[1])+" unsettled ")
	})
}

// The gates refuse a branch whose plans owe a route, or a route review's
// finding not settled, and show the waivers and opt-outs they let pass.
func TestRouteGate(t *testing.T) {
	gate := func(t *testing.T, c Config, repo string) Gate {
		t.Helper()
		g, err := c.RouteGate("", repo, repo, "loop", head(t, repo))
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	planned := func(t *testing.T, c Config, branches ...string) Plan {
		t.Helper()
		made, err := c.New(NewOptions{Title: "Add the loop", Slug: "loop", Branches: branches}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := c.Open(made.Plan)
		return p
	}

	t.Run("owed, then reviewed", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := planned(t, c, "main..loop")
		if g := gate(t, c, repo); g.Refusal != "" {
			t.Errorf("nothing owed: %+v", g)
		}
		p.SetProperty("", "REVIEWS", []string{"security"}, day)
		g := gate(t, c, repo)
		if !strings.Contains(g.Refusal, "security") || !strings.Contains(g.Refusal, commits[0][:12]) ||
			!strings.Contains(g.Refusal, "git fresh-review --focus security --goal FILE origin/HEAD..loop") ||
			!strings.Contains(g.Refusal, "below every commit listed") || !strings.Contains(g.Refusal, "agent-plan waive") {
			t.Errorf("owed: %+v", g)
		}
		routeReceipt(t, p, repo, "security", "origin/main", "loop", "loop", "security-1")
		if g := gate(t, c, repo); !strings.Contains(g.Refusal, "1.security-1") {
			t.Errorf("unsettled: %+v", g)
		}
		p.Log(Event{Kind: "finding-settled", Item: p.Name, Review: "1.security-1", Outcome: "fix"}, day)
		if g := gate(t, c, repo); g.Refusal != "" {
			t.Errorf("settled: %+v", g)
		}
	})

	t.Run("waived", func(t *testing.T) {
		c := testConfig(t)
		repo, _ := routeRepo(t)
		p := planned(t, c, "main..loop")
		p.SetProperty("", "REVIEWS", []string{"security"}, day)
		p.Waive("security", "the operator read it", day)
		if g := gate(t, c, repo); g.Refusal != "" || len(g.Notes) != 1 || !strings.Contains(g.Notes[0], "the operator read it") {
			t.Errorf("waived: %+v", g)
		}
	})

	t.Run("opted out", func(t *testing.T) {
		c := testConfig(t)
		repo, _ := routeRepo(t)
		p := planned(t, c, "main..loop")
		p.SetProperty("", "REVIEWS", []string{"security"}, day)
		p.SetProperty("", "PIPELINE", []string{"opted-out:spike"}, day)
		if g := gate(t, c, repo); g.Refusal != "" || len(g.Notes) != 1 || !strings.Contains(g.Notes[0], "opted-out:spike") {
			t.Errorf("opted out: %+v", g)
		}
	})

	// A plan of another branch owes this one nothing.
	t.Run("another branch's plan", func(t *testing.T) {
		c := testConfig(t)
		repo, _ := routeRepo(t)
		p := planned(t, c, "main..other")
		p.SetProperty("", "REVIEWS", []string{"security"}, day)
		if g := gate(t, c, repo); g.Refusal != "" {
			t.Errorf("another branch: %+v", g)
		}
	})

	// A plan of a branch of the same name in another repository owes this
	// one nothing; one naming this repository, in any case, does.
	t.Run("another repository's plan", func(t *testing.T) {
		c := testConfig(t)
		repo, _ := routeRepo(t)
		gitIn(t, repo, "remote", "add", "origin", "https://github.com/o/here.git")
		for _, repos := range []string{"o/elsewhere", "O/Here"} {
			made, err := c.New(NewOptions{Title: "Add the loop", Slug: "loop", Repos: []string{repos},
				Branches: []string{"main..loop"}}, day)
			if err != nil {
				t.Fatal(err)
			}
			p, _ := c.Open(made.Plan)
			p.SetProperty("", "REVIEWS", []string{"security"}, day)
			g := gate(t, c, repo)
			if owes := strings.Contains(g.Refusal, p.File()); owes != (repos == "O/Here") {
				t.Errorf("REPOSITORIES %s: %+v", repos, g)
			}
		}
	})

	// A halted plan, or one signed with nothing committed, pushed or routed
	// since, is over: a branch reusing its name owes it nothing.
	t.Run("finished plans", func(t *testing.T) {
		c := testConfig(t)
		repo, _ := routeRepo(t)
		p := planned(t, c, "main..loop")
		p.SetProperty("", "REVIEWS", []string{"security"}, day)
		setProp(t, p.File(), "STATUS", "halted")
		if g := gate(t, c, repo); g.Refusal != "" {
			t.Errorf("halted: %+v", g)
		}
		setProp(t, p.File(), "STATUS", "handed-back")
		if _, err := p.record(Event{Kind: "signed", Item: p.Name, Summary: "signed"}, day); err != nil {
			t.Fatal(err)
		}
		if g := gate(t, c, repo); g.Refusal != "" {
			t.Errorf("signed: %+v", g)
		}
		// A review routed afterwards, as a loop's relayed review logs in
		// the plan naming its branch, is no new work in it.
		if _, err := p.Routed("", Routed{Branch: "loop", Base: "a", Tip: "b", Routes: map[string][]string{"security": nil}}, day); err != nil {
			t.Fatal(err)
		}
		if g := gate(t, c, repo); g.Refusal != "" {
			t.Errorf("routed after signing: %+v", g)
		}
		if _, err := p.record(Event{Kind: "committed", Item: p.Name, Summary: "Add more"}, day); err != nil {
			t.Fatal(err)
		}
		if g := gate(t, c, repo); g.Refusal == "" {
			t.Errorf("committed after signing: %+v", g)
		}
	})

	// A plan each of whose branches was merged into the default branch,
	// as the plan last saw it, or deleted, is over too, signed or not: a
	// branch reusing a name it holds owes it nothing.
	t.Run("branches merged or deleted", func(t *testing.T) {
		c := testConfig(t)
		repo, commits := routeRepo(t)
		p := planned(t, c, "main..loop", "main..gone")
		p.SetProperty("", "REVIEWS", []string{"security"}, day)
		if _, err := p.record(Event{Kind: "committed", Item: p.Name, Branch: "loop", Commit: commits[1][:7], Summary: "Add risky"}, day); err != nil {
			t.Fatal(err)
		}
		if g := gate(t, c, repo); g.Refusal == "" {
			t.Errorf("unmerged: %+v", g)
		}
		gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "loop")
		gitIn(t, repo, "checkout", "-q", "-B", "loop", "origin/main")
		commitFile(t, repo, "b.txt", "Add b")
		if g := gate(t, c, repo); g.Refusal != "" {
			t.Errorf("merged, and the name reused: %+v", g)
		}
	})
}

func TestRoutesInStatus(t *testing.T) {
	c := testConfig(t)
	_, commits := routeRepo(t)
	p := newPlan(t, c, "x")
	if _, err := p.Routed("", Routed{Branch: "loop", Base: commits[0], Tip: commits[1],
		Routes: map[string][]string{"security": {commits[1]}}}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Waive("security", "comments only", day); err != nil {
		t.Fatal(err)
	}
	s, err := p.StatusAt(day.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.Routes, []string{"security"}) || len(s.Waivers) != 1 || s.Pipeline != "full" {
		t.Errorf("status %+v", s)
	}
	if !strings.Contains(s.Org(), "- Review routes: security; waived: security (comments only)") {
		t.Errorf("org:\n%s", s.Org())
	}
}
