package plans

import (
	"os"
	"strings"
	"testing"
	"time"
)

// insertUnder puts text right after the line under in file.
func insertUnder(t *testing.T, file, under, text string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(data), under+"\n", under+"\n"+text, 1)
	if updated == string(data) {
		t.Fatalf("%s has no %q", file, under)
	}
	if err := os.WriteFile(file, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func question(id, title, after string) string {
	text := "*** ASK " + title + "\n:PROPERTIES:\n:CUSTOM_ID: " + id + "\n:RECOMMENDATION: yes\n"
	if after != "" {
		text += ":AFTER: " + after + "\n"
	}
	return text + ":END:\n"
}

func ids(qs []Question) string {
	var out []string
	for _, q := range qs {
		out = append(out, q.ID)
	}
	return strings.Join(out, " ")
}

func waits(qs []Question) map[string]string {
	out := map[string]string{}
	for _, q := range qs {
		if len(q.Waiting) > 0 {
			out[q.ID] = strings.Join(q.Waiting, " ")
		}
	}
	return out
}

// testQuestionsAfter: a question waits until every ID in its AFTER is
// settled: a question once its keyword is a done one, a part or step once
// logged done, verified or abandoned. An ID the plan does not settle keeps
// it waiting.
func testQuestionsAfter(t *testing.T) {
	// waiting makes a plan with a part P1, a step S1, and questions: Q1;
	// Q2 after Q1; Q3 after P1 and S1; Q4 after Q9, which the plan does not
	// settle.
	waiting := func(t *testing.T) Plan {
		t.Helper()
		p := newPlan(t, testConfig(t), "x")
		plan := p.file("")
		addItems(t, plan, "** Distribution", "P1")
		addItems(t, plan, "** Steps", "S1")
		insertUnder(t, plan, "** Questions", question("Q1", "Keep the flag?", "")+
			question("Q2", "Name the flag --wait?", "Q1")+
			question("Q3", "Stack the part on main?", "P1 S1")+
			question("Q4", "Rename it?", "Q9"))
		return p
	}
	// settle decides Q1, logs P1 done and S1 started, and S1 done if s1.
	settle := func(t *testing.T, p Plan, s1 bool) {
		t.Helper()
		if _, err := p.Decide("Q1", DecideOptions{Answer: "yes"}, day); err != nil {
			t.Fatal(err)
		}
		events := []Event{{Kind: "done", Item: "P1"}, {Kind: "started", Item: "S1"}}
		if s1 {
			events = append(events, Event{Kind: "done", Item: "S1"})
		}
		for _, e := range events {
			if _, err := p.Log(e, day); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("nothing settled", func(t *testing.T) {
		p := waiting(t)
		all, err := p.Questions(true)
		if err != nil {
			t.Fatal(err)
		}
		if ids(all) != "Q1 Q2 Q3 Q4" || strings.Join(all[2].After, " ") != "P1 S1" {
			t.Errorf("open questions %+v", all)
		}
		want := map[string]string{"Q2": "Q1", "Q3": "P1 S1", "Q4": "Q9"}
		if got := waits(all); len(got) != len(want) || got["Q2"] != want["Q2"] || got["Q3"] != want["Q3"] || got["Q4"] != want["Q4"] {
			t.Errorf("waiting %v, want %v", got, want)
		}
		if got := ids(Askable(all)); got != "Q1" {
			t.Errorf("askable %q, want Q1", got)
		}
	})

	t.Run("Q1 decided and P1 done", func(t *testing.T) {
		p := waiting(t)
		settle(t, p, false)
		all, _ := p.Questions(true)
		if got := ids(Askable(all)); got != "Q2" || waits(all)["Q3"] != "S1" {
			t.Errorf("after Q1 decided and P1 done: askable %q, waiting %v", got, waits(all))
		}
	})

	t.Run("S1 done", func(t *testing.T) {
		p := waiting(t)
		settle(t, p, true)
		all, _ := p.Questions(true)
		if got := ids(Askable(all)); got != "Q2 Q3" {
			t.Errorf("after S1 done: askable %q", got)
		}
		org := QuestionsOrg(all)
		if !strings.Contains(org, "- Q4 :: Rename it? Recommended: yes. Waits for: Q9.") {
			t.Errorf("org does not say what Q4 waits for:\n%s", org)
		}
	})

	// Abandoning a step settles it, as Org's blocker reads ABANDONED, and
	// dropping a question settles it.
	t.Run("S1 abandoned and Q1 dropped", func(t *testing.T) {
		p := waiting(t)
		if _, err := p.Decide("Q1", DecideOptions{Answer: "moot", Drop: true}, day); err != nil {
			t.Fatal(err)
		}
		for _, e := range []Event{{Kind: "done", Item: "P1"}, {Kind: "abandoned", Item: "S1"}} {
			if _, err := p.Log(e, day); err != nil {
				t.Fatal(err)
			}
		}
		all, _ := p.Questions(true)
		if got := ids(Askable(all)); got != "Q2 Q3" {
			t.Errorf("after S1 abandoned and Q1 dropped: askable %q", got)
		}
	})

	// A waiting question can still be decided.
	t.Run("deciding a waiting question", func(t *testing.T) {
		p := waiting(t)
		if _, err := p.Decide("Q4", DecideOptions{Answer: "no"}, day); err != nil {
			t.Errorf("deciding a waiting question: %v", err)
		}
	})
}

// testStatusLineAfter: the status line counts only the questions the
// operator can answer now.
func testStatusLineAfter(t *testing.T) {
	// waiting makes a plan with Q1, and Q2 after Q1.
	waiting := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		insertUnder(t, p.file(""), "** Questions", question("Q1", "Keep it?", "")+question("Q2", "Rename it?", "Q1"))
		return c, p
	}

	t.Run("one askable of two", func(t *testing.T) {
		c, _ := waiting(t)
		if got := c.StatusLine([]byte(`{"session_id":"abc"}`)); got != "1 open question · /questions" {
			t.Errorf("status line %q", got)
		}
	})

	t.Run("after Q1 decided", func(t *testing.T) {
		c, p := waiting(t)
		insertUnder(t, p.file(""), "** Questions", question("Q3", "Move it?", "Q2"))
		if _, err := p.Decide("Q1", DecideOptions{Answer: "yes"}, day); err != nil {
			t.Fatal(err)
		}
		if got := c.StatusLine([]byte(`{"session_id":"abc"}`)); got != "1 open question · /questions" {
			t.Errorf("status line after Q1 %q", got)
		}
	})
}

// testStatusReadyParts: status lists the parts ready to start: neither
// started nor waiting.
func testStatusReadyParts(t *testing.T) {
	// parts makes a plan with parts P1; P2, after P1, with a step P2.S1;
	// and P3, after Q1; and questions Q1, and Q2 after Q1.
	parts := func(t *testing.T) Plan {
		t.Helper()
		p := newPlan(t, testConfig(t), "x")
		plan := p.file("")
		insertUnder(t, plan, "** Distribution", "*** Part one\n:PROPERTIES:\n:ID: P1\n:END:\n"+
			"*** Part two\n:PROPERTIES:\n:ID: P2\n:AFTER: P1\n:END:\n"+
			"**** TODO Its first step\n:PROPERTIES:\n:ID: P2.S1\n:END:\n"+
			"*** Part three\n:PROPERTIES:\n:ID: P3\n:AFTER: Q1\n:END:\n")
		insertUnder(t, plan, "** Questions", question("Q1", "Keep it?", "")+question("Q2", "Rename it?", "Q1"))
		return p
	}
	logAll := func(t *testing.T, p Plan, events ...Event) {
		t.Helper()
		for _, e := range events {
			if _, err := p.Log(e, day); err != nil {
				t.Fatal(err)
			}
		}
	}
	// P1 done and Q1 decided.
	settle := func(t *testing.T, p Plan) {
		t.Helper()
		logAll(t, p, Event{Kind: "started", Item: "P1"}, Event{Kind: "done", Item: "P1"})
		if _, err := p.Decide("Q1", DecideOptions{Answer: "yes"}, day); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("none started", func(t *testing.T) {
		p := parts(t)
		s, err := p.Status()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(s.Ready, " ") != "P1" {
			t.Errorf("ready %v, want P1", s.Ready)
		}
		waiting := map[string]string{}
		for _, w := range s.Waiting {
			waiting[w.ID] = strings.Join(w.On, " ")
		}
		if len(waiting) != 3 || waiting["P2"] != "P1" || waiting["P3"] != "Q1" || waiting["Q2"] != "Q1" {
			t.Errorf("waiting %+v", s.Waiting)
		}
		org := s.Org()
		for _, line := range []string{"- Parts ready to start: P1", "- Waiting: P2 for P1; P3 for Q1; Q2 for Q1"} {
			if !strings.Contains(org, line) {
				t.Errorf("org status lacks %q:\n%s", line, org)
			}
		}
	})

	t.Run("P1 started", func(t *testing.T) {
		p := parts(t)
		logAll(t, p, Event{Kind: "started", Item: "P1"})
		s, _ := p.Status()
		if len(s.Ready) != 0 {
			t.Errorf("ready after P1 started: %v", s.Ready)
		}
		if !strings.Contains(s.Org(), "- Parts ready to start: none") {
			t.Errorf("org status with none ready:\n%s", s.Org())
		}
	})

	t.Run("P1 done and Q1 decided", func(t *testing.T) {
		p := parts(t)
		settle(t, p)
		if s, _ := p.Status(); strings.Join(s.Ready, " ") != "P2 P3" || len(s.Waiting) != 0 {
			t.Errorf("after P1 done and Q1 decided: ready %v, waiting %+v", s.Ready, s.Waiting)
		}
	})

	// A part whose own steps have begun has started.
	t.Run("a part's step started", func(t *testing.T) {
		p := parts(t)
		settle(t, p)
		logAll(t, p, Event{Kind: "started", Item: "P2.S1"})
		if s, _ := p.Status(); strings.Join(s.Ready, " ") != "P3" {
			t.Errorf("after P2.S1 started: ready %v", s.Ready)
		}
	})
}

// testStatusReadySteps: status lists the open steps that can be worked
// on now, as Emacs's agenda does: none of their AFTER items open, every
// earlier sibling settled under an ORDERED parent, and not scheduled for a
// later day.
func testStatusReadySteps(t *testing.T) {
	// steps makes a plan whose S1 and S2 are ORDERED, S3 waits for P1.S1
	// in part P1's file, S4 is scheduled for 2026-10-20, and S5 waits for
	// S6.
	steps := func(t *testing.T) Plan {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		made, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day)
		if err != nil {
			t.Fatal(err)
		}
		addItems(t, made.File, "** Steps", "P1.S1")
		insertUnder(t, p.file(""), "** Steps", "*** Linear\n:PROPERTIES:\n:ORDERED: t\n:END:\n"+
			"**** TODO First\n:PROPERTIES:\n:ID: S1\n:END:\n**** TODO Second\n:PROPERTIES:\n:ID: S2\n:END:\n"+
			"*** TODO After the part's step\n:PROPERTIES:\n:ID: S3\n:AFTER: P1.S1\n:END:\n"+
			"*** TODO Later\nSCHEDULED: <2026-10-20 Tue>\n:PROPERTIES:\n:ID: S4\n:END:\n"+
			"*** TODO After S6\n:PROPERTIES:\n:ID: S5\n:AFTER: S6\n:END:\n"+
			"*** TODO Free\n:PROPERTIES:\n:ID: S6\n:END:\n")
		return p
	}

	t.Run("at the start", func(t *testing.T) {
		p := steps(t)
		s, err := p.StatusAt(day)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(s.ReadySteps, " "); got != "S1 S6 P1.S1" {
			t.Errorf("ready steps %q", got)
		}
		if !strings.Contains(s.Org(), "- Steps ready: S1, S6, P1.S1\n") {
			t.Errorf("org:\n%s", s.Org())
		}
	})

	// An abandoned step settles what waits for it, as a done one does.
	t.Run("once their blockers settle", func(t *testing.T) {
		p := steps(t)
		for _, e := range []Event{{Kind: "done", Item: "S1"}, {Kind: "done", Item: "P1.S1"}, {Kind: "abandoned", Item: "S6"}} {
			if _, err := p.Log(e, day); err != nil {
				t.Fatal(err)
			}
		}
		s, _ := p.StatusAt(day)
		if got := strings.Join(s.ReadySteps, " "); got != "S2 S3 S5" {
			t.Errorf("ready steps %q", got)
		}
		on := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
		if s, _ := p.StatusAt(on); strings.Join(s.ReadySteps, " ") != "S2 S3 S4 S5" {
			t.Errorf("ready steps on the day S4 is scheduled %q", strings.Join(s.ReadySteps, " "))
		}
	})
}
