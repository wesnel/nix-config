package plans

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// later is two days after day: the time of the items a --since between
// them keeps.
var later = day.AddDate(0, 0, 2)

// openFixture makes this host's plan with an item of every kind, some at
// day and some at later, and items that are not open; a halted plan of
// this host's with a follow-up at later; and another host's plan with a
// follow-up. It returns the config and the two plans of this host.
func openFixture(t *testing.T) (Config, Plan, Plan) {
	t.Helper()
	c := testConfig(t)
	p := newPlan(t, c, "open")
	insertUnder(t, p.file(""), "** Steps", "*** TODO Write the loop\n:PROPERTIES:\n:CUSTOM_ID: S1\n:END:\n"+
		"*** TODO Test the loop\n:PROPERTIES:\n:CUSTOM_ID: S2\n:AFTER: S1\n:END:\n")
	addItems(t, p.file(""), "** Distribution", "P1")
	if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day); err != nil {
		t.Fatal(err)
	}
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(p.Ask(AskOptions{Question: "Keep the flag?", Recommendation: "no"}, day))
	must(p.Ask(AskOptions{Question: "Deny git add -A?", Recommendation: "a guard rule", Kind: "retro"}, later))
	must(p.Ask(AskOptions{Question: "Rename it after the loop?", Recommendation: "no", After: []string{"S1"}}, day))
	must(p.Ask(AskOptions{Question: "Already settled?", Recommendation: "yes", Answer: "yes"}, later))
	must(p.Ask(AskOptions{Part: "P1", Question: "Split the test?", Recommendation: "yes"}, day))
	must(p.Followup(FollowupOptions{Title: "Guard accepts --draft=false", FoundIn: "S1"}, later))
	must(p.Followup(FollowupOptions{Part: "P1", Title: "Cache the parse", FoundIn: "S1"}, day))
	must(p.Followup(FollowupOptions{Title: "Done already", FoundIn: "S1"}, day))
	must(p.Log(Event{Kind: "done", Item: "F2"}, day))
	must(p.Log(Event{Kind: "handed-back", Item: "P1", Summary: "ready to sign", Command: "git sign-pass", Dir: "/w/loop"}, later))
	must(c.RequestSign(SignRequest{Session: "abc", Branches: []string{"loop"}}, later))
	signed, err := c.RequestSign(SignRequest{Session: "abc", Branches: []string{"old"}}, day)
	must(nil, err)
	must(c.RecordSign(signed, SignResult{Exit: 0, Summary: "1 signed"}, day))

	made, err := c.New(NewOptions{Title: "Parked work", Slug: "parked", Status: "halted"}, day)
	must(nil, err)
	halted, err := c.Open(made.Plan)
	must(nil, err)
	addItems(t, halted.file(""), "** Steps", "S1")
	must(halted.Log(Event{Kind: "abandoned", Item: "S1"}, day))
	must(halted.Followup(FollowupOptions{Title: "Forgotten idea", FoundIn: "S1"}, later))

	mac := Config{Dir: c.Dir, Host: "mac"}
	made, err = mac.New(NewOptions{Title: "Their plan", Slug: "theirs"}, day)
	must(nil, err)
	theirs, err := mac.Open(made.Plan)
	must(nil, err)
	addItems(t, theirs.file(""), "** Steps", "S1")
	must(theirs.Followup(FollowupOptions{Title: "Their follow-up", FoundIn: "S1"}, later))
	return c, p, halted
}

// shown is each item as "plan kind item", in order.
func shown(items []OpenItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Plan+" "+it.Kind+" "+it.Item)
	}
	return out
}

func TestOpenItems(t *testing.T) {
	// Every open item of this host's plans and their parts is listed under
	// its kind; what is settled, waits on another item or is another
	// host's is not.
	t.Run("every kind", func(t *testing.T) {
		c, p, halted := openFixture(t)
		items, err := c.OpenItems(OpenFilter{}, later)
		if err != nil {
			t.Fatal(err)
		}
		got := shown(items)
		want := []string{
			p.Name + " waiting P1", p.Name + " waiting " + p.Name,
			p.Name + " question Q1", p.Name + " retro Q2", p.Name + " question P1.Q1",
			p.Name + " step S1",
			p.Name + " follow-up F1", p.Name + " follow-up P1.F1",
			halted.Name + " follow-up F1",
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("listed\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		for _, it := range items {
			if it.Halted != (it.Plan == halted.Name) {
				t.Errorf("%s %s: halted %v", it.Plan, it.Item, it.Halted)
			}
			switch it.Plan + " " + it.Item {
			case p.Name + " F1":
				if it.Title != "Guard accepts --draft=false" || it.Time != "[2026-10-16 Fri 13:05]" || it.PlanTitle != "Add the loop" {
					t.Errorf("follow-up %+v", it)
				}
			case p.Name + " P1.F1":
				if it.Part != "P1" || it.Time != "[2026-10-14 Wed 13:05]" {
					t.Errorf("a part's follow-up %+v", it)
				}
			case p.Name + " P1":
				if it.Command != "git sign-pass" || it.Dir != "/w/loop" || it.Title != "ready to sign" || it.Time != "[2026-10-16 Fri 13:05]" {
					t.Errorf("hand-back %+v", it)
				}
			case p.Name + " " + p.Name:
				if !strings.HasPrefix(it.Sign, p.Name+".") || it.Branch != "loop" || it.Time != "[2026-10-16 Fri 13:05]" {
					t.Errorf("pending sign-pass %+v", it)
				}
			case p.Name + " Q2":
				if it.Title != "Deny git add -A?" || it.Time != "[2026-10-16 Fri 13:05]" {
					t.Errorf("retro %+v", it)
				}
			}
		}
	})

	t.Run("by kind", func(t *testing.T) {
		c, p, halted := openFixture(t)
		items, err := c.OpenItems(OpenFilter{Kind: "follow-up"}, later)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(shown(items), "; "); got != p.Name+" follow-up F1; "+p.Name+" follow-up P1.F1; "+halted.Name+" follow-up F1" &&
			got != halted.Name+" follow-up F1; "+p.Name+" follow-up F1; "+p.Name+" follow-up P1.F1" {
			t.Errorf("follow-ups %s", got)
		}
		items, _ = c.OpenItems(OpenFilter{Kind: "retro"}, later)
		if got := strings.Join(shown(items), "; "); got != p.Name+" retro Q2" {
			t.Errorf("retro %s", got)
		}
	})

	// Since keeps the items asked, discovered or handed back at or after
	// it; a step's time is its first event, else its plan's.
	t.Run("since", func(t *testing.T) {
		c, p, halted := openFixture(t)
		items, err := c.OpenItems(OpenFilter{Since: day.Add(24 * time.Hour)}, later)
		if err != nil {
			t.Fatal(err)
		}
		got := shown(items)
		want := []string{p.Name + " waiting P1", p.Name + " waiting " + p.Name, p.Name + " retro Q2",
			p.Name + " follow-up F1", halted.Name + " follow-up F1"}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("since\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	// A plan that cannot be read is named in the error, and the others are
	// still listed.
	t.Run("a plan that cannot be read", func(t *testing.T) {
		c, p, halted := openFixture(t)
		if err := os.Chmod(p.file("P1"), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(p.file("P1"), 0o644) })
		items, err := c.OpenItems(OpenFilter{}, later)
		if err == nil || !strings.Contains(err.Error(), p.Name) {
			t.Errorf("error %v", err)
		}
		if got := strings.Join(shown(items), "; "); got != halted.Name+" follow-up F1" {
			t.Errorf("listed %s", got)
		}
	})

	t.Run("refused/an unknown kind", func(t *testing.T) {
		c, _, _ := openFixture(t)
		_, err := c.OpenItems(OpenFilter{Kind: "bug"}, later)
		isRefusal(t, "an unknown kind", err)
	})

	// The Org listing puts each plan's items under a line naming it, and
	// marks a halted plan's.
	t.Run("org", func(t *testing.T) {
		c, p, halted := openFixture(t)
		items, _ := c.OpenItems(OpenFilter{}, later)
		org := OpenItemsOrg(items)
		for _, s := range []string{
			"* Add the loop (" + p.Name + ")\n",
			"* Parked work (" + halted.Name + ") halted\n",
			"- follow-up F1 [2026-10-16 Fri 13:05] :: Guard accepts --draft=false\n",
			"- retro Q2 [2026-10-16 Fri 13:05] :: Deny git add -A?\n",
			"- waiting P1 [2026-10-16 Fri 13:05] :: ready to sign: `git sign-pass` in /w/loop\n",
		} {
			if !strings.Contains(org, s) {
				t.Errorf("org lacks %q:\n%s", s, org)
			}
		}
		if strings.Contains(org, "("+p.Name+") halted") {
			t.Errorf("a plan not halted is marked:\n%s", org)
		}
		if OpenItemsOrg(nil) != "No open items.\n" {
			t.Errorf("none: %q", OpenItemsOrg(nil))
		}
		data, _ := json.Marshal(items[0])
		if !strings.Contains(string(data), `"kind":`) || !strings.Contains(string(data), `"plan":`) {
			t.Errorf("JSON %s", data)
		}
	})
}
