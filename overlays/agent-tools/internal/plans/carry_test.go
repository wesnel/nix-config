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

// /clear hands the session it starts the plan the old one's work was
// carried to; a compaction leaves the old session with its own plan,
// though both plans name the worktree it runs in.
func TestCarryHandover(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c, p := carryFixture(t)
	_, linked := mainCheckout(t)
	if err := p.SetProperty("", "WORKTREES", []string{linked}, day); err != nil {
		t.Fatal(err)
	}
	got, err := c.Carry(p.File(), CarryOptions{}, day.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	out, err := c.SessionHook([]byte(`{"session_id":"abc","source":"compact","cwd":"`+linked+`"}`), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if context := hookContext(t, out, "SessionStart"); !strings.Contains(context, "plan "+p.File()) {
		t.Errorf("after a compaction the old session is not given its own plan: %q", context)
	}
	end := `{"session_id":"abc","hook_event_name":"SessionEnd","reason":"clear","cwd":"/nowhere"}`
	if err := c.SessionEndHook([]byte(end), time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err = c.SessionHook([]byte(`{"session_id":"def","source":"clear","cwd":"/nowhere"}`), "claude")
	if err != nil {
		t.Fatal(err)
	}
	if context := hookContext(t, out, "SessionStart"); !strings.Contains(context, "plan "+got.Plan) {
		t.Errorf("after /clear the new session is not given the carried plan: %q", context)
	}
	if found, err := c.Find(FindOptions{Session: "claude:def"}); err != nil || len(found) != 1 || found[0].Plan != got.Plan {
		t.Errorf("the new session's plans: %+v %v", found, err)
	}
}

// Each brief says where the plan's work came from or went, and the new
// plan's brief carries the decisions made before the carry.
func TestCarryBriefs(t *testing.T) {
	t.Parallel()
	c, p := carryFixture(t)
	got, err := c.Carry(p.File(), CarryOptions{}, day.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := c.Open(got.Plan)
	briefs := map[string]func(Plan) (string, error){
		"brief":        func(p Plan) (string, error) { return p.Brief(day) },
		"resume brief": func(p Plan) (string, error) { return p.ResumeBrief(day, ResumeLimit) },
	}
	for name, brief := range briefs {
		old, err := brief(p)
		if err != nil {
			t.Fatal(err)
		}
		if want := "Carried to: " + n.Name + "; pick it up with `agent-plan pickup " + n.File() + "`\n"; !strings.Contains(old, want) {
			t.Errorf("old plan's %s lacks %q:\n%s", name, want, old)
		}
		next, err := brief(n)
		if err != nil {
			t.Fatal(err)
		}
		if want := "Carried from: " + p.Name + "\n"; !strings.Contains(next, want) {
			t.Errorf("new plan's %s lacks %q:\n%s", name, want, next)
		}
	}
	old, _ := p.ResumeBrief(day, ResumeLimit)
	if !strings.Contains(old, "In progress: none\n") {
		t.Errorf("the old plan's resume brief shows a carried step in progress:\n%s", old)
	}
	next, _ := n.Brief(day)
	if want := "Decided in " + p.Name + ":\n- Q2 :: Rename it? yes\n"; !strings.Contains(next, want) {
		t.Errorf("new plan's brief lacks %q:\n%s", want, next)
	}
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
	// A plan along the chain that cannot be read could hold the next ID,
	// so none is handed out.
	t.Run("no new ID past a plan that cannot be read", func(t *testing.T) {
		c, p := carryFixture(t)
		got, err := c.Carry(p.File(), CarryOptions{}, later)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := c.Open(got.Plan)
		if err := os.Chmod(p.file(""), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p.file(""), 0o644) })
		if step, err := n.AddEntry(EntryOptions{Section: "Steps", Title: "Next"}, later); err == nil {
			t.Errorf("a step was added as %s, an ID the unread plan may use", step.ID)
		}
	})

	// A template gives the new plan its review stages afresh, under IDs no
	// plan along the carry used, waiting on each other as in the template.
	t.Run("a template's review stages under fresh IDs", func(t *testing.T) {
		c, p := carryFixture(t)
		c.Templates = t.TempDir()
		review := "** Steps\n*** TODO Write the code\n:PROPERTIES:\n:CUSTOM_ID: S1\n:END:\n" +
			"*** TODO Fresh review loop\n:PROPERTIES:\n:CUSTOM_ID: S2\n:AFTER: S1\n:STAGE: review\n:END:\n" +
			"*** TODO Hand back\n:PROPERTIES:\n:CUSTOM_ID: S3\n:AFTER: S2\n:STAGE: review\n:END:\n"
		if err := os.WriteFile(c.Templates+"/review.org", []byte(review), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := c.Carry(p.File(), CarryOptions{Template: "review"}, later)
		if err != nil {
			t.Fatal(err)
		}
		n, _ := c.Open(got.Plan)
		if got := itemKeywords(t, n); got != "S5 TODO, S6 TODO, S2 TODO, S3 TODO, Q1 ASK, F1 TODO" {
			t.Errorf("new plan's items: %s", got)
		}
		items, _ := n.items()
		if after := items["S5"].Props["AFTER"]; after != "S2 S3" {
			t.Errorf("the first review stage waits on %q, want the carried S2 S3", after)
		}
		if after := items["S6"].Props["AFTER"]; after != "S5" {
			t.Errorf("the template's second stage waits on %q, want S5", after)
		}
		if s, _ := n.Status(); strings.Join(s.ReadySteps, " ") != "S2" {
			t.Errorf("ready steps %v, want the carried S2 alone", s.ReadySteps)
		}
		before := planFiles(t, n)
		if _, err := c.Carry(p.File(), CarryOptions{Template: "review"}, later); err != nil {
			t.Fatal(err)
		}
		if planFiles(t, n) != before {
			t.Error("a re-run added the template's steps again")
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

	// A step done but not verified stays in the old plan, where it is
	// verified, and the next plan's brief names it.
	t.Run("a step done but not verified stays", func(t *testing.T) {
		c, p := carryFixture(t)
		if _, err := p.Log(Event{Kind: "done", Item: "S2"}, day); err != nil {
			t.Fatal(err)
		}
		got, err := c.Carry(p.File(), CarryOptions{}, later)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got.Steps, " ") != "S3" {
			t.Errorf("carried steps %v, want S3", got.Steps)
		}
		n, _ := c.Open(got.Plan)
		brief, err := n.Brief(later)
		if err != nil {
			t.Fatal(err)
		}
		if want := "Done, not verified in " + p.Name + ": S2\n"; !strings.Contains(brief, want) {
			t.Errorf("the next plan's brief lacks %q:\n%s", want, brief)
		}
		if _, err := p.Log(Event{Kind: "verified", Item: "S2"}, later); err != nil {
			t.Errorf("verifying it in the old plan: %v", err)
		}
		if brief, _ := n.Brief(later); strings.Contains(brief, "Done, not verified in") {
			t.Errorf("the next plan's brief still names it once verified:\n%s", brief)
		}
	})

	// Nothing open is left behind in a plan that is closed and off the
	// plans page: what carry does not move, it refuses to leave.
	for what, leave := range map[string]func(t *testing.T, c Config, p Plan){
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
