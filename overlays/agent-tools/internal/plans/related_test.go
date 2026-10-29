package plans

import (
	"path/filepath"
	"strings"
	"testing"
)

// shortName is p's name without its host, as a plan's text names another.
func shortName(p Plan) string { return strings.TrimSuffix(p.Name, "."+p.Host) }

// linksTo reports the reason p's Related gives for its link to other, or
// "" where it has none.
func linksTo(t *testing.T, p, other Plan) string {
	t.Helper()
	id := fileID(fileText(t, other.file("")))
	for _, l := range strings.Split(fileText(t, p.file("")), "\n") {
		if _, why, ok := strings.Cut(l, "[[id:"+id+"]"); ok {
			_, why, _ = strings.Cut(why, "]: ")
			return why
		}
	}
	return ""
}

// mainCheckout is a repository's main checkout, and linked a worktree
// added to it.
func mainCheckout(t *testing.T) (main, linked string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	main, linked = filepath.Join(root, "repo"), filepath.Join(root, "repo-loop")
	gitIn(t, root, "init", "-q", "-b", "main", main)
	gitIn(t, main, "-c", "commit.gpgsign=false", "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-q", "--allow-empty", "-m", "Start")
	gitIn(t, main, "worktree", "add", "-q", "-b", "loop", linked)
	return main, linked
}

// Hard evidence that two plans are one piece of work links the plan that
// gains it to the other, through Link, with the evidence as the reason:
// a branch both work on or one stacks on, a pull request both name, an
// item one takes over from the other, and a commit both log.
func TestLinkEvidence(t *testing.T) {
	t.Run("shared branch", func(t *testing.T) {
		c := testConfig(t)
		a := planOn(t, c, "a", "", "main..loop")
		b := planOn(t, c, "b", "", "main..loop")
		stacked := planOn(t, c, "stacked", "", "loop..flag")
		if why := linksTo(t, b, a); why != "Both work on branch loop." {
			t.Errorf("b's link to a: %q", why)
		}
		if why := linksTo(t, stacked, a); why != "Stacks on its branch loop." {
			t.Errorf("stacked's link to a: %q", why)
		}
		if why := linksTo(t, a, b); why != "" {
			t.Errorf("a, made first, links b: %q", why)
		}
	})

	t.Run("shared pull request", func(t *testing.T) {
		c := testConfig(t)
		a, b := planOn(t, c, "a", ""), planOn(t, c, "b", "")
		url := "https://github.com/o/r/pull/7"
		for _, p := range []Plan{a, b} {
			if _, err := p.AddToProperty("", "PULL_REQUESTS", url, day); err != nil {
				t.Fatal(err)
			}
		}
		linked, err := c.LinkEvidence(b.file(""), day)
		if err != nil || len(linked) != 1 || linked[0].Plan != a.Name {
			t.Fatalf("linked %+v, %v", linked, err)
		}
		if events := eventsBut(t, b, "property"); len(events) != 1 || events[0].Kind != "linked" {
			t.Errorf("events %+v", events)
		}
		if why := linksTo(t, b, a); why != "Both name pull request "+url+"." {
			t.Errorf("b's link to a: %q", why)
		}
	})

	// An item of a plan named, with the plan, on one line of another's text
	// has been taken over by it, as the parts and follow-ups a later plan
	// picks up are.
	t.Run("item taken over", func(t *testing.T) {
		c := testConfig(t)
		a, b := planOn(t, c, "a", ""), planOn(t, c, "b", "")
		addItems(t, a.file(""), "** Distribution", "P3")
		addItems(t, a.file(""), "** Follow-ups", "F2")
		if _, err := b.SetSection("", "Goal", "Finish follow-up F2 and part P3 of plan "+shortName(a)+", but not F9.", day); err != nil {
			t.Fatal(err)
		}
		if _, err := c.LinkEvidence(b.file(""), day); err != nil {
			t.Fatal(err)
		}
		if why := linksTo(t, b, a); why != "Takes over its F2, P3." {
			t.Errorf("b's link to a: %q", why)
		}
		// The plan whose item was taken links the plan that took it too.
		if _, err := c.LinkEvidence(a.file(""), day); err != nil {
			t.Fatal(err)
		}
		if why := linksTo(t, a, b); why != "Took over this plan's F2, P3." {
			t.Errorf("a's link to b: %q", why)
		}
	})

	t.Run("commit both name", func(t *testing.T) {
		c := testConfig(t)
		a, b := planOn(t, c, "a", ""), planOn(t, c, "b", "")
		for _, p := range []Plan{a, b} {
			if _, err := p.Record("", Event{Kind: "committed", Branch: "loop-fix", Commit: "Fix the loop", Summary: "Fix the loop"}, day); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := c.LinkEvidence(b.file(""), day); err != nil {
			t.Fatal(err)
		}
		if why := linksTo(t, b, a); why != "Both log the commit 'Fix the loop' on loop-fix." {
			t.Errorf("b's link to a: %q", why)
		}
	})

	// What every plan of a repository may share, its default branch and
	// its main checkout, links nothing: the commit hook once filed other
	// sessions' commits into the plan that named the main checkout.
	t.Run("not through the main checkout", func(t *testing.T) {
		c := testConfig(t)
		main, _ := mainCheckout(t)
		var ps []Plan
		for _, slug := range []string{"a", "b"} {
			p := planOn(t, c, slug, "", "main")
			if _, err := p.AddToProperty("", "WORKTREES", main, day); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Record("", Event{Kind: "committed", Branch: "main", Commit: "Bump the lock", Summary: "Bump the lock"}, day); err != nil {
				t.Fatal(err)
			}
			ps = append(ps, p)
		}
		events, err := c.LinkEvidence(ps[1].file(""), day)
		if err != nil || len(events) != 0 {
			t.Errorf("events %+v, %v", events, err)
		}
		rs, err := c.Related(ps[1].file(""))
		if err != nil || len(rs) != 0 {
			t.Errorf("related %+v, %v", rs, err)
		}
	})

	t.Run("again", func(t *testing.T) {
		c := testConfig(t)
		a := planOn(t, c, "a", "", "main..loop")
		b := planOn(t, c, "b", "", "main..loop")
		before := fileText(t, b.file(""))
		events, err := c.LinkEvidence(b.file(""), day)
		if err != nil || len(events) != 0 || fileText(t, b.file("")) != before || linksTo(t, b, a) == "" {
			t.Errorf("again: %+v, %v\n%s", events, err, fileText(t, b.file("")))
		}
	})
}

// Softer signs, a session or linked worktree both name or a mention of
// the other plan with none of its items, are listed by Related as
// candidates, and never linked.
func TestRelated(t *testing.T) {
	c := testConfig(t)
	_, linked := mainCheckout(t)
	a := planOn(t, c, "a", "", "main..loop")
	made, err := c.New(NewOptions{Title: "Plan b", Slug: "b", Repos: []string{"o/r"}, Branches: []string{"main..other"},
		Worktrees: []string{linked}, Sessions: []string{"claude:s-1"}}, day)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := c.Open(made.Plan)
	if _, err := a.AddToProperty("", "WORKTREES", linked, day); err != nil {
		t.Fatal(err)
	}
	if err := a.SetProperty("", "SESSIONS", []string{"claude:s-1"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SetSection("", "Goal", "Like plan "+shortName(a)+", but for the guard.", day); err != nil {
		t.Fatal(err)
	}
	stacked := planOn(t, c, "stacked", "", "other..flag")
	before := fileText(t, b.file(""))
	rs, err := c.Related(b.file(""))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rs {
		got = append(got, r.Plan+" "+map[bool]string{true: "hard", false: "soft"}[r.Hard]+
			map[bool]string{true: " linked", false: ""}[r.Linked]+": "+strings.Join(r.Evidence, " "))
	}
	want := []string{
		stacked.Name + " hard: Its branch flag stacks on this plan's other.",
		a.Name + " soft: Both name session claude:s-1. Both name worktree " + linked + ". This plan's text names it.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("related:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if after := fileText(t, b.file("")); after != before {
		t.Errorf("Related wrote b:\n%s", after)
	}
	events, err := c.LinkEvidence(b.file(""), day)
	if err != nil || len(events) != 1 || linksTo(t, b, stacked) == "" || linksTo(t, b, a) != "" {
		t.Errorf("LinkEvidence %+v, %v: b links a with %q", events, err, linksTo(t, b, a))
	}
	rs, _ = c.Related(b.file(""))
	if len(rs) != 2 || !rs[0].Linked || rs[1].Linked {
		t.Errorf("related after linking %+v", rs)
	}
}
