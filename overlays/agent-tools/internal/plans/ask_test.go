package plans

import (
	"os"
	"strings"
	"testing"
)

func TestAsk(t *testing.T) {
	// asked makes the plan openQuestions does, decides Q2, and asks Q4, after
	// Q1.
	asked := func(t *testing.T) (Plan, Asked) {
		t.Helper()
		_, p := openQuestions(t)
		if _, err := p.Decide("Q2", DecideOptions{Answer: "no"}, day); err != nil {
			t.Fatal(err)
		}
		got, err := p.Ask(AskOptions{Question: "Keep the old flag?", Recommendation: "no, drop it", Priority: "A",
			Context: "Nothing reads it.", Alternatives: []string{"keep it", "deprecate it"}, After: []string{"Q1"}}, day)
		if err != nil {
			t.Fatal(err)
		}
		return p, got
	}

	// A question asked through Ask is one Questions reads back whole, under
	// the next free ID of its file, and the log records it.
	t.Run("question", func(t *testing.T) {
		p, got := asked(t)
		if got.ID != "Q4" || got.File != p.file("") {
			t.Errorf("asked %+v", got)
		}
		qs, err := p.Questions(false)
		if err != nil {
			t.Fatal(err)
		}
		var read *Question
		for i := range qs {
			if qs[i].ID == "Q4" {
				read = &qs[i]
			}
		}
		if read == nil || read.Question != "Keep the old flag?" || read.Recommendation != "no, drop it" || read.Priority != "A" ||
			read.Context != "Nothing reads it." || strings.Join(read.Alternatives, "|") != "keep it|deprecate it" ||
			strings.Join(read.Waiting, " ") != "Q1" {
			t.Errorf("read back %+v from %+v", read, qs)
		}
		data, _ := os.ReadFile(p.file(""))
		text := string(data)
		if section := text[strings.Index(text, "** Questions\n"):strings.Index(text, "** Follow-ups\n")]; !strings.HasSuffix(section,
			"*** ASK [#A] Keep the old flag?\n:PROPERTIES:\n:ID: Q4\n:RECOMMENDATION: no, drop it\n:AFTER: Q1\n:END:\nNothing reads it.\n- keep it\n- deprecate it\n") {
			t.Errorf("Questions:\n%s", section)
		}
		events, _ := p.Events()
		if last := events[len(events)-1]; last.Kind != "asked" || last.Item != "Q4" || last.Summary != "Keep the old flag?" {
			t.Errorf("logged %+v", last)
		}
	})

	// An agent's own decision, on a constraint that already holds, is a
	// question settled from the start: never put to Wesley, and read back
	// as a decision.
	t.Run("already answered", func(t *testing.T) {
		c, p := openQuestions(t)
		got, err := p.Ask(AskOptions{Question: "Keep tests off the network?", Recommendation: "yes",
			Answer: "yes: the status line must never touch the network", Role: "single"}, day)
		if err != nil {
			t.Fatal(err)
		}
		entry := linesAt(t, p.file(""), got.ID, 7)
		want := "*** DECIDED Keep tests off the network? :ARCHIVE:\nCLOSED: [2026-10-14 Wed 13:05]\n:PROPERTIES:\n:ID: Q4\n" +
			":RECOMMENDATION: yes\n:ANSWER: yes: the status line must never touch the network\n:DECIDED_BY: agent\n:END:"
		if entry != want {
			t.Errorf("entry:\n%s\nwant:\n%s", entry, want)
		}
		events, _ := p.Events()
		if len(events) != 2 || events[0].Kind != "asked" || events[1].Kind != "decided" || events[1].Item != "Q4" ||
			events[1].Role != "single" || events[1].Summary != "yes: the status line must never touch the network" {
			t.Errorf("events %+v", events)
		}
		if qs, _ := p.Questions(true); ids(qs) != "Q1 Q2 P1.Q1" {
			t.Errorf("open %s", ids(qs))
		}
		if line := c.StatusLine([]byte(`{"session_id":"abc"}`)); line != "3 open questions · /questions" {
			t.Errorf("status line %q", line)
		}
		if ds, _ := p.Decisions(false); decisionIDs(ds) != "Q3 Q4" {
			t.Errorf("decisions %s", decisionIDs(ds))
		}
	})

	t.Run("part's question", func(t *testing.T) {
		p, _ := asked(t)
		part, err := p.Ask(AskOptions{Part: "P1", Question: "Split the file?", Recommendation: "yes"}, day)
		if err != nil || part.ID != "P1.Q2" || part.File != p.file("P1") {
			t.Errorf("a part's question: %+v, %v", part, err)
		}
		if all, _ := p.Questions(true); len(all) != 4 || all[3].ID != "P1.Q2" || all[3].Part != "P1" {
			t.Errorf("with the parts: %+v", all)
		}
	})

	for what, o := range map[string]AskOptions{
		"no question":           {Recommendation: "yes"},
		"no recommendation":     {Question: "Why?"},
		"a forged heading":      {Question: "Why?\n* forged", Recommendation: "yes"},
		"a forged alternative":  {Question: "Why?", Recommendation: "yes", Alternatives: []string{"a\n** Steps"}},
		"an unknown AFTER ID":   {Question: "Why?", Recommendation: "yes", After: []string{"Q9"}},
		"an AFTER with a space": {Question: "Why?", Recommendation: "yes", After: []string{"Q1 Q2"}},
		"a part with no file":   {Part: "P7", Question: "Why?", Recommendation: "yes"},
		"a part ID that is not": {Part: "x", Question: "Why?", Recommendation: "yes"},
		"an unknown priority":   {Question: "Why?", Recommendation: "yes", Priority: "D"},
		"a forged answer":       {Question: "Why?", Recommendation: "yes", Answer: "yes\n* forged"},
		"a structural context":  {Question: "Why?", Recommendation: "yes", Context: "- a list item"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			p, _ := asked(t)
			_, err := p.Ask(o, day)
			isRefusal(t, what, err)
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		p, _ := asked(t)
		_, err := otherHosts(t, p).Ask(AskOptions{Question: "Why?", Recommendation: "yes"}, day)
		isRefusal(t, "another host's plan", err)
	})
}
