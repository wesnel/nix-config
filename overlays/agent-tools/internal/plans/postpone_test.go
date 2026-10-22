package plans

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// A step or part postponed until a time drops out of the ready lists until
// then, and comes back by itself.
func TestPostpone(t *testing.T) {
	// plan makes a plan with steps S1 and S2 and a part P1.
	plan := func(t *testing.T) Plan {
		t.Helper()
		p := newPlan(t, testConfig(t), "x")
		addItems(t, p.file(""), "** Steps", "S1", "S2")
		addItems(t, p.file(""), "** Distribution", "P1")
		return p
	}
	ready := func(t *testing.T, p Plan, at time.Time) (steps, parts []string) {
		t.Helper()
		s, err := p.StatusAt(at)
		if err != nil {
			t.Fatal(err)
		}
		return s.ReadySteps, s.Ready
	}

	t.Run("until a day", func(t *testing.T) {
		p := plan(t)
		e, err := p.Postpone("S1", "2026-10-20", day)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind != "postponed" || e.Item != "S1" || e.Summary != "until <2026-10-20 Tue>" {
			t.Errorf("event %+v", e)
		}
		if got := linesAt(t, p.file(""), "S1", 1); got != "*** TODO Step S1\nSCHEDULED: <2026-10-20 Tue>" {
			t.Errorf("heading:\n%s", got)
		}
		if steps, _ := ready(t, p, day); slices.Contains(steps, "S1") {
			t.Errorf("ready before the day: %v", steps)
		}
		if steps, _ := ready(t, p, time.Date(2026, 10, 20, 0, 1, 0, 0, time.Local)); !slices.Contains(steps, "S1") {
			t.Errorf("not ready on the day: %v", steps)
		}
	})

	// A schedule may name a time of day.
	t.Run("until a time", func(t *testing.T) {
		p := plan(t)
		if _, err := p.Postpone("S2", "2026-10-14 18:30", day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "S2", 1); got != "*** TODO Step S2\nSCHEDULED: <2026-10-14 Wed 18:30>" {
			t.Errorf("heading:\n%s", got)
		}
		if steps, _ := ready(t, p, time.Date(2026, 10, 14, 18, 29, 0, 0, time.Local)); slices.Contains(steps, "S2") {
			t.Errorf("ready before the time: %v", steps)
		}
		if steps, _ := ready(t, p, time.Date(2026, 10, 14, 18, 30, 0, 0, time.Local)); !slices.Contains(steps, "S2") {
			t.Errorf("not ready at the time: %v", steps)
		}
	})

	t.Run("a part", func(t *testing.T) {
		p := plan(t)
		if _, err := p.Postpone("P1", "2026-10-15", day); err != nil {
			t.Fatal(err)
		}
		if _, parts := ready(t, p, day); slices.Contains(parts, "P1") {
			t.Errorf("part ready before the day: %v", parts)
		}
		if _, parts := ready(t, p, day.Add(24*time.Hour)); !slices.Contains(parts, "P1") {
			t.Errorf("part not ready on the day: %v", parts)
		}
	})

	// A new time replaces the old one and keeps the rest of the line, and
	// clearing removes it.
	t.Run("again, then cleared", func(t *testing.T) {
		p := fromFixture(t, "emacs-closed.org")
		if _, err := p.Postpone("S1", "2026-11-02", day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "S1", 1); got != "*** DONE Add the flag\nCLOSED: [2026-10-14 Wed 16:29] SCHEDULED: <2026-11-02 Mon>" {
			t.Errorf("rescheduled:\n%s", got)
		}
		e, err := p.Postpone("S1", "", day)
		if err != nil {
			t.Fatal(err)
		}
		if e.Summary != "cleared" {
			t.Errorf("event %+v", e)
		}
		if got := linesAt(t, p.file(""), "S1", 1); got != "*** DONE Add the flag\nCLOSED: [2026-10-14 Wed 16:29]" {
			t.Errorf("cleared:\n%s", got)
		}
		if _, err := p.Postpone("S2", "", day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "S2", 1); got != "*** VERIFIED Wire it in\nCLOSED: [2026-10-14 Wed 16:29]" {
			t.Errorf("cleared without a schedule:\n%s", got)
		}
	})

	t.Run("cleared alone", func(t *testing.T) {
		p := plan(t)
		if _, err := p.Postpone("S1", "2026-10-20", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Postpone("S1", "", day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "S1", 1); !strings.HasPrefix(got, "*** TODO Step S1\n:PROPERTIES:") {
			t.Errorf("cleared:\n%s", got)
		}
	})

	for what, c := range map[string][2]string{
		"a question":         {"Q1", "2026-10-20"},
		"an unknown ID":      {"S9", "2026-10-20"},
		"a bad time":         {"S1", "next week"},
		"a time with no day": {"S1", "18:30"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			p := plan(t)
			insertUnder(t, p.file(""), "** Questions", question("Q1", "Keep it?", ""))
			_, err := p.Postpone(c[0], c[1], day)
			isRefusal(t, what, err)
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		p := plan(t)
		_, err := otherHosts(t, p).Postpone("S1", "2026-10-20", day)
		isRefusal(t, "another host's plan", err)
	})
}
