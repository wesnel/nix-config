package plans

import (
	"os"
	"strings"
	"testing"
)

// A decided question leaves Open questions for Decisions, as the next
// decision, with its answer, and the log records it.
func TestDecide(t *testing.T) {
	_, p := openQuestions(t)
	got, err := p.Decide("Q1", "release", day)
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision != "D2" || got.Question != "Q1" {
		t.Errorf("decided %+v", got)
	}
	data, _ := os.ReadFile(p.file(""))
	text := string(data)
	decisions := text[strings.Index(text, "** Decisions\n"):strings.Index(text, "** Steps\n")]
	open := text[strings.Index(text, "** Open questions\n"):]
	want := "*** Which branch is the base?\n:PROPERTIES:\n:ID: D2\n:QUESTION: Q1\n:RECOMMENDATION: main\n:ANSWER: release\n:END:\n" +
		"The release branch is frozen.\n- release\n- a branch of its own\n"
	if !strings.Contains(decisions, "*** Keep the flag\n") || !strings.HasSuffix(decisions, want) {
		t.Errorf("Decisions:\n%s", decisions)
	}
	if strings.Contains(open, "Q1") || !strings.Contains(open, ":ID: Q2\n") {
		t.Errorf("Open questions:\n%s", open)
	}
	events, _ := p.Events()
	if len(events) != 1 || events[0].Kind != "decided" || events[0].Item != "D2" || events[0].Summary != "Q1: release" {
		t.Errorf("events %+v", events)
	}
	part, err := p.Decide("P1.Q1", "yes", day)
	if err != nil || part.Decision != "P1.D1" {
		t.Errorf("a part's question: %+v, %v", part, err)
	}
	if qs, _ := p.Questions(true); len(qs) != 1 || qs[0].ID != "Q2" {
		t.Errorf("left open: %+v", qs)
	}
	for what, args := range map[string][2]string{
		"an unknown question":     {"Q9", "x"},
		"a decided one":           {"Q1", "x"},
		"an answer with newlines": {"Q2", "x\n* forged"},
		"an empty answer":         {"Q2", " "},
	} {
		_, err := p.Decide(args[0], args[1], day)
		isRefusal(t, what, err)
	}
	theirs := newPlan(t, Config{Dir: p.Dir, Host: "other"}, "y")
	other, _ := Config{Dir: p.Dir, Host: "box"}.Open(theirs.file(""))
	_, err = other.Decide("Q1", "x", day)
	isRefusal(t, "another host's plan", err)
}

// The status line names the session's open questions, and nothing else.
func TestStatusLine(t *testing.T) {
	c, _ := openQuestions(t)
	if got := c.StatusLine([]byte(`{"session_id":"abc","cwd":"/nowhere","workspace":{"current_dir":"/nowhere"}}`)); got != "3 open questions · /questions" {
		t.Errorf("status line %q", got)
	}
	for _, in := range []string{`{"session_id":"zzz","cwd":"/nowhere"}`, `not json`, ``} {
		if got := c.StatusLine([]byte(in)); got != "" {
			t.Errorf("%q: status line %q", in, got)
		}
	}
}
