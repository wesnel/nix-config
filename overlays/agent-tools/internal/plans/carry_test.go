package plans

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// carryFixture is a plan whose S1 is verified, S2 started, S3 waits on S1
// and S2, and S4 verified; F1 is open and F2 done; Q1 is open and Q2
// decided.
func carryFixture(t *testing.T) (Config, Plan) {
	t.Helper()
	c := testConfig(t)
	p := newPlan(t, c, "loop")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, s := range []EntryOptions{
		{Title: "Add the loop"},
		{Title: "Add the flag"},
		{Title: "Wire them", After: []string{"S1", "S2"}},
		{Title: "Document it"},
	} {
		s.Section, s.Text = "Steps", "Done when "+strings.ToLower(s.Title)+"."
		_, err := p.AddEntry(s, day)
		must(err)
	}
	for _, title := range []string{"Guard accepts --draft=false", "Rename the alias"} {
		_, err := p.Followup(FollowupOptions{Title: title, FoundIn: "S1"}, day)
		must(err)
	}
	for _, q := range []string{"Keep the old flag?", "Rename it?"} {
		_, err := p.Ask(AskOptions{Question: q, Recommendation: "yes"}, day)
		must(err)
	}
	_, err := p.Decide("Q2", DecideOptions{Answer: "yes"}, day)
	must(err)
	_, err = p.SetSection("", "Goal", "Add the run loop.", day)
	must(err)
	must(p.SetProperty("", "PULL_REQUESTS", []string{"https://github.com/o/r/pull/7"}, day))
	must(p.SetProperty("", "SKILLS", []string{"design"}, day))
	for _, e := range []Event{{Kind: "done", Item: "S1"}, {Kind: "verified", Item: "S1"}, {Kind: "started", Item: "S2"},
		{Kind: "done", Item: "S4"}, {Kind: "verified", Item: "S4"}, {Kind: "done", Item: "F2"}} {
		_, err := p.Log(e, day)
		must(err)
	}
	return c, p
}

// itemKeywords are the ID and keyword of each item of p's own file, as
// "S2 TODO", in file order.
func itemKeywords(t *testing.T, p Plan) string {
	t.Helper()
	list, err := p.itemList()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range list {
		if it.File == p.file("") && it.ID != p.Name {
			out = append(out, it.ID+" "+it.Keyword)
		}
	}
	return strings.Join(out, ", ")
}

// planFiles are the contents of p's file and log, to compare runs by.
func planFiles(t *testing.T, p Plan) string {
	t.Helper()
	plan, err := os.ReadFile(p.file(""))
	if err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(p.log())
	if err != nil {
		t.Fatal(err)
	}
	return string(plan) + string(log)
}

// Carry moves what a plan leaves open to a new plan, so the next session
// starts from only the work left, and settles it in the old plan.
func TestCarry(t *testing.T) {
	t.Parallel()
	later := day.Add(time.Hour)

	t.Run("moves what is left", func(t *testing.T) {
		c, p := carryFixture(t)
		got, err := c.Carry(p.File(), CarryOptions{}, later)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got.Steps, " ") != "S2 S3" || strings.Join(got.Followups, " ") != "F1" ||
			strings.Join(got.Questions, " ") != "Q1" || got.From != p.Name {
			t.Errorf("carried %+v", got)
		}
		n, err := c.Open(got.Plan)
		if err != nil {
			t.Fatal(err)
		}
		if n.Name == p.Name {
			t.Fatalf("carried into itself")
		}
		if got := itemKeywords(t, n); got != "S2 TODO, S3 TODO, Q1 ASK, F1 TODO" {
			t.Errorf("new plan's items: %s", got)
		}
		items, err := n.items()
		if err != nil {
			t.Fatal(err)
		}
		if after := items["S3"].Props["AFTER"]; after != "S2" {
			t.Errorf("S3 waits on %q, want S2 alone: S1 is settled in the old plan", after)
		}
		props, err := planHeading(n.file(""))
		if err != nil {
			t.Fatal(err)
		}
		if props["CARRIED_FROM"] != p.Name || props["PULL_REQUESTS"] != "https://github.com/o/r/pull/7" ||
			props["SKILLS"] != "design" || props["STATUS"] != "planning" {
			t.Errorf("new plan's properties %v", props)
		}
		if goal, _ := n.Goal(""); goal != "Add the run loop.\n" {
			t.Errorf("new plan's goal %q", goal)
		}
		if data, _ := os.ReadFile(n.file("")); !strings.Contains(string(data), "#+title: Add the loop\n") {
			t.Errorf("new plan's title:\n%s", data)
		}

		if props, _ := planHeading(p.file("")); props["CARRIED_TO"] != n.Name {
			t.Errorf("old plan's CARRIED_TO %q, want %s", props["CARRIED_TO"], n.Name)
		}
		if got := itemKeywords(t, p); got != "S1 VERIFIED, S2 CARRIED, S3 CARRIED, S4 VERIFIED, Q1 DROPPED, Q2 DECIDED, F1 CARRIED, F2 DONE" {
			t.Errorf("old plan's items: %s", got)
		}
		s, err := p.Status()
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Unsettled) != 0 || strings.Join(s.Carried, " ") != "S2 S3" {
			t.Errorf("old plan's status: unsettled %v, carried %v", s.Unsettled, s.Carried)
		}
		if _, err := p.Log(Event{Kind: "handed-back", Item: p.Name}, later); err != nil {
			t.Errorf("hand-back after the carry: %v", err)
		}
	})

	t.Run("a re-run changes nothing", func(t *testing.T) {
		c, p := carryFixture(t)
		first, err := c.Carry(p.File(), CarryOptions{}, later)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := c.Open(first.Plan)
		// What the next session changed in the new plan stays changed.
		err = n.withLock(func(l *lockedPlan) error {
			return l.setPlanProperty("", "PIPELINE", func([]string) []string { return []string{"opted-out:spike"} }, "", later)
		})
		if err != nil {
			t.Fatal(err)
		}
		before := planFiles(t, p) + planFiles(t, n)
		again, err := c.Carry(p.File(), CarryOptions{}, later.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if again.Plan != first.Plan || len(again.Steps)+len(again.Followups)+len(again.Questions) != 0 {
			t.Errorf("re-run %+v, first %+v", again, first)
		}
		if after := planFiles(t, p) + planFiles(t, n); after != before {
			t.Errorf("the re-run changed the plans:\n%s\nwas:\n%s", after, before)
		}
	})

	// A run stopped at any stage is finished by running carry again, and
	// ends as one clean run does.
	t.Run("after a stop", func(t *testing.T) {
		stages := map[string]func(t *testing.T, c Config, p Plan){
			"once the new plan is made": func(t *testing.T, c Config, p Plan) {
				if _, err := c.carryTarget(p, CarryOptions{}, later); err != nil {
					t.Fatal(err)
				}
			},
			"once the work is in the new plan": func(t *testing.T, c Config, p Plan) {
				left, err := p.leftOver()
				if err != nil {
					t.Fatal(err)
				}
				n, err := c.carryTarget(p, CarryOptions{}, later)
				if err != nil {
					t.Fatal(err)
				}
				if err := p.markCarried(n, later); err != nil {
					t.Fatal(err)
				}
				if err := n.take(p, left, later); err != nil {
					t.Fatal(err)
				}
			},
			"once a step's carried event is logged": func(t *testing.T, c Config, p Plan) {
				left, err := p.leftOver()
				if err != nil {
					t.Fatal(err)
				}
				n, err := c.carryTarget(p, CarryOptions{}, later)
				if err != nil {
					t.Fatal(err)
				}
				if err := p.markCarried(n, later); err != nil {
					t.Fatal(err)
				}
				if err := n.take(p, left, later); err != nil {
					t.Fatal(err)
				}
				err = p.withLock(func(l *lockedPlan) error {
					_, err := l.log(Event{Kind: carriedKind, Item: "S2", Summary: "to " + n.Name}, later)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			},
		}
		for name, stop := range stages {
			t.Run(name, func(t *testing.T) {
				c, p := carryFixture(t)
				stop(t, c, p)
				got, err := c.Carry(p.File(), CarryOptions{}, later)
				if err != nil {
					t.Fatal(err)
				}
				own, err := c.ownPlans()
				if err != nil {
					t.Fatal(err)
				}
				if len(own) != 2 {
					t.Errorf("%d plans, want the old one and one new", len(own))
				}
				n, _ := c.Open(got.Plan)
				if got := itemKeywords(t, n); got != "S2 TODO, S3 TODO, Q1 ASK, F1 TODO" {
					t.Errorf("new plan's items: %s", got)
				}
				if got := itemKeywords(t, p); got != "S1 VERIFIED, S2 CARRIED, S3 CARRIED, S4 VERIFIED, Q1 DROPPED, Q2 DECIDED, F1 CARRIED, F2 DONE" {
					t.Errorf("old plan's items: %s", got)
				}
			})
		}
	})

	// An ID names one item along the plans work was carried through, so
	// the next plan never hands out one the old plan used.
	t.Run("new IDs continue past the old plan", func(t *testing.T) {
		c, p := carryFixture(t)
		got, err := c.Carry(p.File(), CarryOptions{}, later)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := c.Open(got.Plan)
		step, err := n.AddEntry(EntryOptions{Section: "Steps", Title: "Next"}, later)
		if err != nil {
			t.Fatal(err)
		}
		f, err := n.Followup(FollowupOptions{Title: "Later", FoundIn: "S2"}, later)
		if err != nil {
			t.Fatal(err)
		}
		q, err := n.Ask(AskOptions{Question: "Ship it?", Recommendation: "yes"}, later)
		if err != nil {
			t.Fatal(err)
		}
		if step.ID != "S5" || f.ID != "F3" || q.ID != "Q3" {
			t.Errorf("new IDs %s %s %s, want S5 F3 Q3", step.ID, f.ID, q.ID)
		}
	})

	// A carried plan takes no new work and no events about what it
	// carried, which belong to the next plan, but still takes its own
	// hand-back and the events about what it kept.
	t.Run("a carried plan is closed to new work", func(t *testing.T) {
		c, p := carryFixture(t)
		got, err := c.Carry(p.File(), CarryOptions{}, later)
		if err != nil {
			t.Fatal(err)
		}
		for what, write := range map[string]func() error{
			"a step":      func() error { _, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "More"}, later); return err },
			"a follow-up": func() error { _, err := p.Followup(FollowupOptions{Title: "More", FoundIn: "S1"}, later); return err },
			"a question":  func() error { _, err := p.Ask(AskOptions{Question: "More?", Recommendation: "no"}, later); return err },
			"a carried step's event": func() error {
				_, err := p.Log(Event{Kind: "done", Item: "S2"}, later)
				return err
			},
		} {
			err := write()
			isRefusal(t, what, err)
			if err != nil && !strings.Contains(err.Error(), got.Plan) {
				t.Errorf("%s: the refusal does not name the next plan: %v", what, err)
			}
		}
		if _, err := p.Log(Event{Kind: "verified", Item: "S4"}, later); err != nil {
			t.Errorf("verifying a step the plan kept: %v", err)
		}
	})

	// The Related link from the next plan is the one stage a carry leaves
	// undone, and says why, when the old plan is in the old form; a link
	// that fails for any other reason fails the carry, which a re-run
	// finishes.
	t.Run("an old plan in the old form is left unlinked", func(t *testing.T) {
		c, p := carryFixture(t)
		data := fileText(t, p.file(""))
		old := regexp.MustCompile(`\A:PROPERTIES:\n(?:.*\n)*?:END:\n`).ReplaceAllString(data, "")
		if old == data {
			t.Fatalf("the fixture has no file-level drawer:\n%s", data)
		}
		if err := os.WriteFile(p.file(""), []byte(old), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := c.Carry(p.File(), CarryOptions{}, later)
		if err != nil || !strings.Contains(got.Unlinked, "old form") {
			t.Errorf("carry: %+v, %v", got, err)
		}
	})
	t.Run("a link that fails fails the carry", func(t *testing.T) {
		c, p := carryFixture(t)
		left, err := p.leftOver()
		if err != nil {
			t.Fatal(err)
		}
		n, err := c.carryTarget(p, CarryOptions{}, later)
		if err != nil {
			t.Fatal(err)
		}
		for _, stage := range []func() error{
			func() error { return n.inherit(p, CarryOptions{}, later) },
			func() error { return p.markCarried(n, later) },
			func() error { return n.take(p, left, later) },
			func() error { return p.settleCarried(n, left, later) },
		} {
			if err := stage(); err != nil {
				t.Fatal(err)
			}
		}
		// New linked the plans through their shared branch; drop that link
		// so the carry writes its own.
		unlinked := regexp.MustCompile(`(?m)^- \[\[id:.*\n`).ReplaceAllString(fileText(t, n.file("")), "")
		if err := os.WriteFile(n.file(""), []byte(unlinked), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(c.Dir, 0o555); err != nil {
			t.Fatal(err)
		}
		got, err := c.Carry(p.File(), CarryOptions{}, later)
		if err := os.Chmod(c.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err == nil {
			t.Errorf("a carry whose link could not be written succeeded: %+v", got)
		}
		if _, err := c.Carry(p.File(), CarryOptions{}, later); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(fileText(t, n.file("")), "Carried from it") {
			t.Errorf("the re-run did not link the plans:\n%s", fileText(t, n.file("")))
		}
	})

	// The next plan is in the old plan's workstream, or in none where the
	// old plan is in none, though a newer plan on the same branch is in
	// another.
	for _, tc := range []struct {
		name     string
		inMine   bool
		removeIt bool
		wantMine bool
	}{
		{name: "its own", inMine: true, wantMine: true},
		{name: "none", inMine: false, wantMine: false},
		{name: "one whose file is gone", inMine: true, removeIt: true, wantMine: true},
	} {
		t.Run("the old plan's workstream/"+tc.name, func(t *testing.T) {
			c := testConfig(t)
			mine, err := c.NewWorkstream("Loop", "")
			if err != nil {
				t.Fatal(err)
			}
			other, err := c.NewWorkstream("Flags", "")
			if err != nil {
				t.Fatal(err)
			}
			made := func(slug, ws string, at time.Time) Plan {
				t.Helper()
				m, err := c.New(NewOptions{Title: "Plan " + slug, Slug: slug, Repos: []string{"o/r"},
					Branches: []string{"main..loop"}, Workstream: ws}, at)
				if err != nil {
					t.Fatal(err)
				}
				p, err := c.Open(m.Plan)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
			var ws string
			if tc.inMine {
				ws = mine.Name
			}
			p := made("loop", ws, day)
			if !tc.inMine {
				if got, _ := p.Workstream(); got.ID != "" {
					t.Fatalf("the old plan is in %+v", got)
				}
			}
			made("flags", other.Name, day.Add(time.Minute))
			if tc.removeIt {
				if err := os.Remove(mine.File); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "Finish it"}, day); err != nil {
				t.Fatal(err)
			}
			got, err := c.Carry(p.File(), CarryOptions{}, later)
			if err != nil {
				t.Fatal(err)
			}
			n, _ := c.Open(got.Plan)
			want := ""
			if tc.wantMine {
				want = mine.ID
			}
			if ws, err := n.Workstream(); err != nil || ws.ID != want {
				t.Errorf("the next plan's workstream %+v, %v; want %q", ws, err, want)
			}
		})
	}

	t.Run("refused/a part's steps open", func(t *testing.T) {
		c, p := carryFixture(t)
		addItems(t, p.file(""), "** Distribution", "P1")
		part, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day)
		if err != nil {
			t.Fatal(err)
		}
		addItems(t, part.File, "** Steps", "P1.S1")
		_, err = c.Carry(p.File(), CarryOptions{}, later)
		isRefusal(t, "a carry with P1.S1 open", err)
		if own, _ := c.ownPlans(); len(own) != 1 {
			t.Errorf("a refused carry made a plan: %d plans", len(own))
		}
	})

	t.Run("refused/nothing left", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Steps", "S1")
		for _, kind := range []string{"done", "verified"} {
			if _, err := p.Log(Event{Kind: kind, Item: "S1"}, day); err != nil {
				t.Fatal(err)
			}
		}
		_, err := c.Carry(p.File(), CarryOptions{}, later)
		isRefusal(t, "a carry with every item settled", err)
	})

	// Nothing open is left behind in a plan that is closed and off the
	// plans page: what carry does not move, it refuses to leave.
	for what, leave := range map[string]func(t *testing.T, c Config, p Plan){
		"a step done but not verified": func(t *testing.T, c Config, p Plan) {
			if _, err := p.Log(Event{Kind: "done", Item: "S2"}, day); err != nil {
				t.Fatal(err)
			}
		},
		"a part not started": func(t *testing.T, c Config, p Plan) {
			addItems(t, p.file(""), "** Distribution", "P1")
		},
		"a part's follow-up open": func(t *testing.T, c Config, p Plan) {
			addItems(t, p.file(""), "** Distribution", "P1")
			part, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Log(Event{Kind: "done", Item: "P1"}, day); err != nil {
				t.Fatal(err)
			}
			addItems(t, part.File, "** Follow-ups", "P1.F1")
		},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			c, p := carryFixture(t)
			leave(t, c, p)
			_, err := c.Carry(p.File(), CarryOptions{}, later)
			isRefusal(t, what, err)
			if own, _ := c.ownPlans(); len(own) != 1 {
				t.Errorf("a refused carry made a plan: %d plans", len(own))
			}
		})
	}
}
