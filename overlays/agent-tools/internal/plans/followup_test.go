package plans

import (
	"os"
	"strings"
	"testing"
)

// Work found out of scope is recorded where it was found, as a TODO of its
// own, and status keeps it in view until it is done.
func TestFollowup(t *testing.T) {
	// plan makes a plan with a step S1 and a part P1 with its file.
	plan := func(t *testing.T) Plan {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Steps", "S1")
		addItems(t, p.file(""), "** Distribution", "P1")
		if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("recorded", func(t *testing.T) {
		p := plan(t)
		got, err := p.Followup(FollowupOptions{Title: "Guard accepts --draft=false", FoundIn: "S1",
			Context: "The guard reads the flag's presence only."}, day)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != "F1" || got.File != p.file("") || got.Event.Kind != "discovered" || got.Event.Item != "F1" ||
			got.Event.Summary != "Guard accepts --draft=false" {
			t.Errorf("followup %+v", got)
		}
		data, _ := os.ReadFile(p.file(""))
		want := "** Follow-ups\n*** TODO Guard accepts --draft=false\n:PROPERTIES:\n:ID: F1\n:FOUND_IN: S1\n:END:\n" +
			"The guard reads the flag's presence only.\n** References\n** Verification\n"
		if !strings.HasSuffix(string(data), want) {
			t.Errorf("plan:\n%s", data)
		}
		s, _ := p.Status()
		if strings.Join(s.Followups, " ") != "F1" || !strings.Contains(s.Org(), "- Follow-ups open: F1\n") {
			t.Errorf("status follow-ups %v:\n%s", s.Followups, s.Org())
		}
	})

	t.Run("a part's, then done", func(t *testing.T) {
		p := plan(t)
		if _, err := p.Followup(FollowupOptions{Title: "First", FoundIn: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		got, err := p.Followup(FollowupOptions{Title: "Split the test", FoundIn: "P1", Part: "P1"}, day)
		if err != nil || got.ID != "P1.F1" || got.File != p.file("P1") {
			t.Fatalf("a part's follow-up: %+v, %v", got, err)
		}
		if _, err := p.Log(Event{Kind: "done", Item: "F1"}, day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "F1", 1); got != "*** DONE First\nCLOSED: [2026-10-14 Wed 13:05]" {
			t.Errorf("done:\n%s", got)
		}
		if s, _ := p.Status(); strings.Join(s.Followups, " ") != "P1.F1" {
			t.Errorf("open follow-ups %v", s.Followups)
		}
	})

	for what, o := range map[string]FollowupOptions{
		"no title":             {FoundIn: "S1"},
		"a forged title":       {Title: "x\n* forged", FoundIn: "S1"},
		"an unknown found-in":  {Title: "x", FoundIn: "S9"},
		"no found-in":          {Title: "x"},
		"a part with no file":  {Title: "x", FoundIn: "S1", Part: "P7"},
		"a structural context": {Title: "x", FoundIn: "S1", Context: "* forged"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			p := plan(t)
			_, err := p.Followup(o, day)
			isRefusal(t, what, err)
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		p := plan(t)
		_, err := otherHosts(t, p).Followup(FollowupOptions{Title: "x", FoundIn: "S1"}, day)
		isRefusal(t, "another host's plan", err)
	})
}
