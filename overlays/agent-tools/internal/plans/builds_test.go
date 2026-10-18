package plans

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// linkDrifts are p's status's link drift, as "FOLLOWUP ENTRY,ENTRY: problem".
func linkDrifts(t *testing.T, p Plan) []string {
	t.Helper()
	s, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range s.LinkDrift {
		out = append(out, fmt.Sprintf("%s %s: %s", d.Followup, strings.Join(d.Entries, ","), d.Problem))
	}
	return out
}

// A step or part names the follow-up it builds through the log, its BUILDS
// follows the log, and the follow-up closes as the entry does.
func TestBuilds(t *testing.T) {
	// plan makes a plan with step S1, parts P1 and P2 in its Distribution,
	// and follow-up F1, found in S1; P1 and P2 have no files yet.
	plan := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Steps", "S1")
		addItems(t, p.file(""), "** Distribution", "P1", "P2")
		if _, err := p.Followup(FollowupOptions{Title: "Split the guard", FoundIn: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		return c, p
	}
	newPart := func(t *testing.T, c Config, p Plan, part, from string) (Created, error) {
		t.Helper()
		return c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: part, From: from}, day)
	}
	refused := func(t *testing.T, what string, err error, says string) {
		t.Helper()
		isRefusal(t, what, err)
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Errorf("%s: %v, want it to say %q", what, err, says)
		}
	}

	t.Run("a new part, closed with it", func(t *testing.T) {
		c, p := plan(t)
		made, err := newPart(t, c, p, "P1", "F1")
		if err != nil || made.Builds != "F1" || made.Note != "" {
			t.Fatalf("new --from: %+v, %v", made, err)
		}
		if got := linesAt(t, p.file(""), "P1", 4); got != "*** TODO Step P1\n:PROPERTIES:\n:ID: P1\n:BUILDS: F1\n:END:" {
			t.Errorf("P1:\n%s", got)
		}
		expectList(t, "property drift", drift(t, p))
		e, err := p.Log(Event{Kind: "done", Item: "P1", Role: "parent", Evidence: "tests pass"}, day)
		if err != nil || e.Note != "logged done for F1, which P1 builds" {
			t.Fatalf("done: %+v, %v", e, err)
		}
		if got := linesAt(t, p.file(""), "F1", 1); got != "*** DONE Split the guard\nCLOSED: [2026-10-14 Wed 13:05]" {
			t.Errorf("F1 after done:\n%s", got)
		}
		events, _ := p.Events()
		last := events[len(events)-1]
		if last.Kind != "done" || last.Item != "F1" || last.Summary != "built by P1" || last.Role != "parent" ||
			last.Evidence != "tests pass" {
			t.Errorf("follow-up's event %+v", last)
		}
		if _, err := p.Log(Event{Kind: "verified", Item: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** VERIFIED Split the guard" {
			t.Errorf("F1 after verified: %s", got)
		}
		// Done again, after a fix, leaves the follow-up verified.
		if e, err := p.Log(Event{Kind: "done", Item: "P1"}, day); err != nil || e.Note != "" {
			t.Fatalf("done again: %+v, %v", e, err)
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** VERIFIED Split the guard" {
			t.Errorf("F1 after done again: %s", got)
		}
		s, _ := p.Status()
		if len(s.Followups) != 0 || len(s.KeywordDrift) != 0 {
			t.Errorf("status follow-ups %v, keyword drift %v", s.Followups, s.KeywordDrift)
		}
	})

	t.Run("a step, to a part's follow-up", func(t *testing.T) {
		c, p := plan(t)
		if _, err := newPart(t, c, p, "P1", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Followup(FollowupOptions{Title: "Rename the flag", FoundIn: "P1", Part: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		e, err := p.Link("S1", "P1.F1", day)
		if err != nil || e.Kind != buildsKind || e.Item != "S1" || e.Builds != "P1.F1" || e.Summary != "builds P1.F1" {
			t.Fatalf("link: %+v, %v", e, err)
		}
		if _, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file("P1"), "P1.F1", 0); got != "*** DONE Rename the flag" {
			t.Errorf("P1.F1: %s", got)
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** TODO Split the guard" {
			t.Errorf("F1: %s", got)
		}
	})

	t.Run("abandoned, released", func(t *testing.T) {
		c, p := plan(t)
		if _, err := newPart(t, c, p, "P1", "F1"); err != nil {
			t.Fatal(err)
		}
		_, err := p.Link("P2", "F1", day)
		refused(t, "a second builder", err, "F1 is already built by P1")
		if _, err := p.Log(Event{Kind: "abandoned", Item: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** TODO Split the guard" {
			t.Errorf("F1 after P1 abandoned: %s", got)
		}
		// The release is logged, and the abandoned heading loses BUILDS.
		events, _ := p.Events()
		if last := events[len(events)-1]; last.Kind != buildsKind || last.Item != "P1" || last.Builds != "" {
			t.Errorf("release %+v", last)
		}
		if got := linesAt(t, p.file(""), "P1", 4); strings.Contains(got, "BUILDS") {
			t.Errorf("P1 after abandoned:\n%s", got)
		}
		expectList(t, "property drift", drift(t, p))
		_, err = p.Link("P1", "F1", day)
		refused(t, "an abandoned entry", err, "P1 is abandoned")
		if _, err := p.Link("P2", "F1", day); err != nil {
			t.Fatalf("P2 takes F1 over: %v", err)
		}
		// P1 taken up again and done builds nothing: its link stays released.
		for _, kind := range []string{"started", "done"} {
			if e, err := p.Log(Event{Kind: kind, Item: "P1"}, day); err != nil || e.Note != "" {
				t.Fatalf("%s: %+v, %v", kind, e, err)
			}
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** TODO Split the guard" {
			t.Errorf("F1 after P1 done: %s", got)
		}
		expectList(t, "link drift", linkDrifts(t, p))
		_, err = p.Link("P1", "", day)
		refused(t, "clearing no link", err, "P1 builds no follow-up")
		if _, err := p.Link("P2", "", day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "P2", 4); strings.Contains(got, "BUILDS") {
			t.Errorf("P2 after clearing:\n%s", got)
		}
		expectList(t, "property drift", drift(t, p))
	})

	t.Run("two builders, from an older log", func(t *testing.T) {
		_, p := plan(t)
		if _, err := p.Link("P1", "F1", day); err != nil {
			t.Fatal(err)
		}
		logRaw(t, p, Event{Kind: buildsKind, Item: "P2", Builds: "F1"})
		expectList(t, "link drift", linkDrifts(t, p), "F1 P1,P2: built by more than one live entry")
		s, _ := p.Status()
		if !strings.Contains(s.Org(), "- Link drift: F1 built by P1, P2 (built by more than one live entry)\n") {
			t.Errorf("status:\n%s", s.Org())
		}
		// Neither closes a follow-up whose builder is in doubt.
		if e, err := p.Log(Event{Kind: "done", Item: "P1"}, day); err != nil || e.Note != "" {
			t.Errorf("done: %+v, %v", e, err)
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** TODO Split the guard" {
			t.Errorf("F1: %s", got)
		}
		// An abandoned event an older build logged releases the link too.
		logRaw(t, p, Event{Kind: "abandoned", Item: "P2"})
		logRaw(t, p, Event{Kind: "started", Item: "P2"})
		expectList(t, "link drift after P2 abandoned", linkDrifts(t, p), "F1 P1: open, but P1 is DONE")
	})

	t.Run("an entry already closed", func(t *testing.T) {
		_, p := plan(t)
		if _, err := p.Log(Event{Kind: "verified", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		e, err := p.Link("S1", "F1", day)
		if err != nil || e.Note != "logged verified for F1, which S1 builds" {
			t.Fatalf("link: %+v, %v", e, err)
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** VERIFIED Split the guard" {
			t.Errorf("F1: %s", got)
		}
		events, _ := p.Events()
		if last := events[len(events)-1]; last.BuiltBy != "S1" || last.Kind != "verified" {
			t.Errorf("follow-up's event %+v", last)
		}
	})

	t.Run("closed by an older build", func(t *testing.T) {
		_, p := plan(t)
		if _, err := p.Link("P1", "F1", day); err != nil {
			t.Fatal(err)
		}
		logRaw(t, p, Event{Kind: "done", Item: "P1"})
		expectList(t, "link drift", linkDrifts(t, p), "F1 P1: open, but P1 is DONE")
		before, _ := p.Events()
		e, err := p.Link("P1", "F1", day)
		if err != nil || e.ID != "" || e.Note != "P1 already builds F1, so no link is logged; logged done for F1, which P1 builds" {
			t.Fatalf("link again: %+v, %v", e, err)
		}
		after, _ := p.Events()
		if len(after) != len(before)+1 || after[len(after)-1].Kind != "done" {
			t.Errorf("link again logged %d events, want the follow-up's done alone", len(after)-len(before))
		}
		expectList(t, "link drift", linkDrifts(t, p))
		// Once more, with nothing to settle, it logs nothing and says so.
		e, err = p.Link("P1", "F1", day)
		if err != nil || e.ID != "" || e.Note != "P1 already builds F1, so no link is logged" {
			t.Fatalf("link a third time: %+v, %v", e, err)
		}
		// A follow-up done whose builder an older build verified.
		logRaw(t, p, Event{Kind: "verified", Item: "P1"})
		expectList(t, "link drift", linkDrifts(t, p), "F1 P1: DONE, but P1 is VERIFIED")
		if _, err := p.Link("P1", "F1", day); err != nil {
			t.Fatalf("settling a verified builder: %v", err)
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** VERIFIED Split the guard" {
			t.Errorf("F1: %s", got)
		}
		expectList(t, "link drift", linkDrifts(t, p))
	})

	t.Run("a follow-up abandoned", func(t *testing.T) {
		_, p := plan(t)
		if _, err := p.Link("S1", "F1", day); err != nil {
			t.Fatal(err)
		}
		e, err := p.Log(Event{Kind: "abandoned", Item: "F1"}, day)
		if err != nil || e.Note != "released S1's link to F1, which is abandoned" {
			t.Fatalf("abandoned: %+v, %v", e, err)
		}
		if got := linesAt(t, p.file(""), "S1", 4); strings.Contains(got, "BUILDS") {
			t.Errorf("S1 after F1 abandoned:\n%s", got)
		}
		if _, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "F1", 0); got != "*** ABANDONED Split the guard" {
			t.Errorf("F1 after S1 done: %s", got)
		}
		expectList(t, "property drift", drift(t, p))
		expectList(t, "link drift", linkDrifts(t, p))
	})

	t.Run("a follow-up abandoned by an older build", func(t *testing.T) {
		_, p := plan(t)
		if _, err := p.Link("S1", "F1", day); err != nil {
			t.Fatal(err)
		}
		logRaw(t, p, Event{Kind: "abandoned", Item: "F1"})
		expectList(t, "link drift", linkDrifts(t, p), "F1 S1: ABANDONED, but S1 builds it")
		if e, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil || e.Note != "" {
			t.Errorf("done: %+v, %v", e, err)
		}
		if _, err := p.Link("S1", "F1", day); err != nil {
			t.Errorf("link again: %v", err)
		}
		if events, _ := p.Events(); stepState(events, "F1") != kwAbandoned {
			t.Errorf("F1's log gives it %s", stepState(events, "F1"))
		}
		expectList(t, "link drift", linkDrifts(t, p), "F1 S1: ABANDONED, but S1 builds it")
	})

	t.Run("a closed entry keeps its link", func(t *testing.T) {
		_, p := plan(t)
		if _, err := p.Followup(FollowupOptions{Title: "Other", FoundIn: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Link("S1", "F1", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Log(Event{Kind: "done", Item: "S1", Role: "parent", Agent: "a1", Evidence: "tests pass"}, day); err != nil {
			t.Fatal(err)
		}
		_, err := p.Link("S1", "F2", day)
		refused(t, "relinking a done entry", err, "S1 is DONE and builds F1")
		if got := linesAt(t, p.file(""), "F2", 0); got != "*** TODO Other" {
			t.Errorf("F2: %s", got)
		}
		// Cleared first, it may take another, which closes at once with the
		// entry's own evidence.
		if _, err := p.Link("S1", "", day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Link("S1", "F2", day); err != nil {
			t.Fatal(err)
		}
		events, _ := p.Events()
		if last := events[len(events)-1]; last.Item != "F2" || last.Kind != "done" || last.Role != "parent" ||
			last.Agent != "a1" || last.Evidence != "tests pass" || last.BuiltBy != "S1" {
			t.Errorf("F2's event %+v", last)
		}
	})

	t.Run("an ID is never reused", func(t *testing.T) {
		_, p := plan(t)
		if _, err := p.Link("S1", "F1", day); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p.file(""))
		text := strings.Replace(string(data), "*** TODO Split the guard\n:PROPERTIES:\n:ID: F1\n:FOUND_IN: S1\n:END:\n", "", 1)
		os.WriteFile(p.file(""), []byte(text), 0o644)
		got, err := p.Followup(FollowupOptions{Title: "Unrelated", FoundIn: "S1"}, day)
		if err != nil || got.ID != "F2" {
			t.Fatalf("followup: %+v, %v", got, err)
		}
		expectList(t, "link drift", linkDrifts(t, p), "F1 S1: no follow-up of the plan")
		if e, err := p.Log(Event{Kind: "verified", Item: "S1"}, day); err != nil || e.Note != "" {
			t.Errorf("verified: %+v, %v", e, err)
		}
		if got := linesAt(t, p.file(""), "F2", 0); got != "*** TODO Unrelated" {
			t.Errorf("F2: %s", got)
		}
	})

	t.Run("on a part file's Plan heading", func(t *testing.T) {
		c, p := plan(t)
		if _, err := newPart(t, c, p, "P1", ""); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p.file("P1"))
		os.WriteFile(p.file("P1"), []byte(strings.Replace(string(data), ":ID: P1\n", ":ID: P1\n:BUILDS: F1\n", 1)), 0o644)
		s, _ := p.Status()
		if len(s.StructureDrift) != 1 || s.StructureDrift[0].Problem !=
			"a BUILDS property, which belongs on the part's Distribution entry, where `agent-plan link` writes it" {
			t.Errorf("structure drift %+v", s.StructureDrift)
		}
	})

	t.Run("hand edits drift", func(t *testing.T) {
		c, p := plan(t)
		if _, err := newPart(t, c, p, "P1", "F1"); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p.file(""))
		text := strings.Replace(string(data), ":ID: P2\n", ":ID: P2\n:BUILDS: F1\n", 1)
		text = strings.Replace(text, ":ID: F1\n", ":ID: F9\n", 1)
		os.WriteFile(p.file(""), []byte(text), 0o644)
		expectList(t, "property drift", drift(t, p), `P2 BUILDS: file "F1", log ""`)
		expectList(t, "link drift", linkDrifts(t, p), "F1 P1: no follow-up of the plan")
		// P1 done closes nothing the plan no longer holds.
		if e, err := p.Log(Event{Kind: "done", Item: "P1"}, day); err != nil || e.Note != "" {
			t.Errorf("done: %+v, %v", e, err)
		}
	})

	t.Run("refused", func(t *testing.T) {
		c, p := plan(t)
		_, err := p.Link("S9", "F1", day)
		refused(t, "an unknown entry", err, `"S9" is not a step or part`)
		_, err = p.Link("F1", "F1", day)
		refused(t, "a follow-up as the builder", err, `"F1" is not a step or part`)
		_, err = p.Link("S1", "F2", day)
		refused(t, "an unknown follow-up", err, `"F2" is not a follow-up`)
		_, err = p.Link("S1", "P2", day)
		refused(t, "a part as the follow-up", err, `"P2" is not a follow-up`)
		_, err = newPart(t, c, p, "P1", "F2")
		refused(t, "new --from an unknown follow-up", err, `"F2" is not a follow-up`)
		if _, err := os.Stat(p.file("P1")); err == nil {
			t.Error("a refused new --from made the part's file")
		}
		_, err = c.New(NewOptions{Title: "Plan", Slug: "y", From: "F1"}, day)
		refused(t, "new --from without a part", err, "--from names the follow-up a part builds")
		_, err = p.Log(Event{Kind: buildsKind, Item: "S1", Builds: "F1"}, day)
		refused(t, "logging builds", err, "agent-plan link PLAN ID FOLLOWUP")
		if _, err := p.Log(Event{Kind: "done", Item: "F1"}, day); err != nil {
			t.Fatal(err)
		}
		_, err = p.Link("S1", "F1", day)
		refused(t, "a done follow-up", err, "F1 is DONE")
		if _, err := p.Followup(FollowupOptions{Title: "Dropped", FoundIn: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Log(Event{Kind: "abandoned", Item: "F2"}, day); err != nil {
			t.Fatal(err)
		}
		_, err = p.Link("S1", "F2", day)
		refused(t, "an abandoned follow-up", err, "F2 is ABANDONED")
		_, err = p.Link("S1", "", day)
		refused(t, "clearing no link", err, "S1 builds no follow-up")
	})

	// A part made from a follow-up another entry took meanwhile is refused
	// before its file is made or anything is logged of it.
	t.Run("new --from refused leaves nothing", func(t *testing.T) {
		c, p := plan(t)
		if _, err := p.Link("P2", "F1", day); err != nil {
			t.Fatal(err)
		}
		before, _ := p.Events()
		_, err := newPart(t, c, p, "P1", "F1")
		refused(t, "a taken follow-up", err, "F1 is already built by P2")
		if _, err := os.Stat(p.file("P1")); err == nil {
			t.Error("the refused part's file was made")
		}
		if after, _ := p.Events(); len(after) != len(before) {
			t.Errorf("%d events logged", len(after)-len(before))
		}
	})
}

// logRaw logs e as an older build of agent-plan did: without closing a
// follow-up or releasing a link.
func logRaw(t *testing.T, p Plan, e Event) {
	t.Helper()
	if err := p.withLock(func(l *lockedPlan) error { _, err := l.log(e, day); return err }); err != nil {
		t.Fatal(err)
	}
}
