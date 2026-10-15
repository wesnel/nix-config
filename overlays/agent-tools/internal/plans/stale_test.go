package plans

import (
	"strings"
	"testing"
	"time"
)

// status points at work that went quiet and at commits no step accounts
// for.
func TestStaleAndUnrecorded(t *testing.T) {
	// worked makes a plan whose S1 was started at day, and S2 started and
	// done then, with these commits logged.
	worked := func(t *testing.T, commits ...string) Plan {
		t.Helper()
		p := newPlan(t, testConfig(t), "x")
		addItems(t, p.file(""), "** Steps", "S1", "S2")
		events := []Event{{Kind: "started", Item: "S1"}, {Kind: "started", Item: "S2"},
			{Kind: "done", Item: "S2", Commit: "Add the run loop"}}
		for _, c := range commits {
			events = append(events, Event{Kind: "committed", Item: p.Name, Commit: c, Summary: c})
		}
		for _, e := range events {
			if _, err := p.Record(e.Item, e, day); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}

	t.Run("a step started a day ago", func(t *testing.T) {
		p := worked(t)
		s, err := p.StatusAt(day.Add(24*time.Hour + time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(s.Stale, " ") != "S1" || !strings.Contains(s.Org(), "- Stale, started with nothing logged for a day: S1\n") {
			t.Errorf("stale %v:\n%s", s.Stale, s.Org())
		}
	})

	t.Run("a step started today", func(t *testing.T) {
		p := worked(t)
		if s, _ := p.StatusAt(day.Add(23 * time.Hour)); len(s.Stale) != 0 {
			t.Errorf("stale %v", s.Stale)
		}
	})

	// A fixup, amend or squash commit belongs to the commit it names.
	t.Run("commits no step records", func(t *testing.T) {
		p := worked(t, "Add the run loop", "fixup! Add the run loop", "amend! fixup! Add the run loop",
			"Rename the flag", "squash! Rename the flag")
		s, err := p.StatusAt(day)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(s.Unrecorded, "; "); got != "Rename the flag; squash! Rename the flag" {
			t.Errorf("unrecorded %q", got)
		}
		if !strings.Contains(s.Org(), "- Commits no step records: Rename the flag; squash! Rename the flag\n") {
			t.Errorf("org:\n%s", s.Org())
		}
	})
}
