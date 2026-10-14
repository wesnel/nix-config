package plans

import (
	"os"
	"strings"
	"testing"
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
	text := "*** " + title + "\n:PROPERTIES:\n:ID: " + id + "\n:RECOMMENDATION: yes\n"
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

// A question waits until every ID in its AFTER is settled: a question once
// decided, a part or step once logged done. An ID the plan does not settle
// keeps it waiting.
func TestQuestionsAfter(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	plan := p.file("")
	addItems(t, plan, "** Distribution", "P1")
	addItems(t, plan, "** Steps", "S1")
	insertUnder(t, plan, "** Open questions", question("Q1", "Keep the flag?", "")+
		question("Q2", "Name the flag --wait?", "Q1")+
		question("Q3", "Stack the part on main?", "P1 S1")+
		question("Q4", "Rename it?", "Q9"))

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

	if _, err := p.Decide("Q1", "yes", day); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Event{{Kind: "done", Item: "P1"}, {Kind: "started", Item: "S1"}} {
		if _, err := p.Log(e, day); err != nil {
			t.Fatal(err)
		}
	}
	all, _ = p.Questions(true)
	if got := ids(Askable(all)); got != "Q2" || waits(all)["Q3"] != "S1" {
		t.Errorf("after Q1 decided and P1 done: askable %q, waiting %v", got, waits(all))
	}
	if _, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
		t.Fatal(err)
	}
	all, _ = p.Questions(true)
	if got := ids(Askable(all)); got != "Q2 Q3" {
		t.Errorf("after S1 done: askable %q", got)
	}

	org := QuestionsOrg(all)
	if !strings.Contains(org, "- Q4 :: Rename it? Recommended: yes. Waits for: Q9.") {
		t.Errorf("org does not say what Q4 waits for:\n%s", org)
	}

	// A waiting question can still be decided.
	if _, err := p.Decide("Q4", "no", day); err != nil {
		t.Errorf("deciding a waiting question: %v", err)
	}
}

// The status line counts only the questions Wesley can answer now.
func TestStatusLineAfter(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	insertUnder(t, p.file(""), "** Open questions", question("Q1", "Keep it?", "")+question("Q2", "Rename it?", "Q1"))
	if got := c.StatusLine([]byte(`{"session_id":"abc"}`)); got != "1 open question · /questions" {
		t.Errorf("status line %q", got)
	}
	insertUnder(t, p.file(""), "** Open questions", question("Q3", "Move it?", "Q2"))
	if _, err := p.Decide("Q1", "yes", day); err != nil {
		t.Fatal(err)
	}
	if got := c.StatusLine([]byte(`{"session_id":"abc"}`)); got != "1 open question · /questions" {
		t.Errorf("status line after Q1 %q", got)
	}
}

// Status lists the parts ready to start: neither started nor waiting.
func TestStatusReadyParts(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	plan := p.file("")
	insertUnder(t, plan, "** Distribution", "*** Part one\n:PROPERTIES:\n:ID: P1\n:END:\n"+
		"*** Part two\n:PROPERTIES:\n:ID: P2\n:AFTER: P1\n:END:\n"+
		"**** TODO Its first step\n:PROPERTIES:\n:ID: P2.S1\n:END:\n"+
		"*** Part three\n:PROPERTIES:\n:ID: P3\n:AFTER: Q1\n:END:\n")
	insertUnder(t, plan, "** Open questions", question("Q1", "Keep it?", "")+question("Q2", "Rename it?", "Q1"))

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

	if _, err := p.Log(Event{Kind: "started", Item: "P1"}, day); err != nil {
		t.Fatal(err)
	}
	if s, _ = p.Status(); len(s.Ready) != 0 {
		t.Errorf("ready after P1 started: %v", s.Ready)
	}
	if !strings.Contains(s.Org(), "- Parts ready to start: none") {
		t.Errorf("org status with none ready:\n%s", s.Org())
	}
	if _, err := p.Log(Event{Kind: "done", Item: "P1"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Decide("Q1", "yes", day); err != nil {
		t.Fatal(err)
	}
	if s, _ = p.Status(); strings.Join(s.Ready, " ") != "P2 P3" || len(s.Waiting) != 0 {
		t.Errorf("after P1 done and Q1 decided: ready %v, waiting %+v", s.Ready, s.Waiting)
	}

	// A part whose own steps have begun has started.
	if _, err := p.Log(Event{Kind: "started", Item: "P2.S1"}, day); err != nil {
		t.Fatal(err)
	}
	if s, _ = p.Status(); strings.Join(s.Ready, " ") != "P3" {
		t.Errorf("after P2.S1 started: ready %v", s.Ready)
	}
}
