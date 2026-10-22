package plans

import (
	"os"
	"strings"
	"testing"
)

func TestAsk(t *testing.T) {
	// asked makes the plan openQuestions does, decides Q2, and asks Q3, after
	// Q1.
	asked := func(t *testing.T) (Plan, Asked) {
		t.Helper()
		_, p := openQuestions(t)
		if _, err := p.Decide("Q2", "no", day); err != nil {
			t.Fatal(err)
		}
		got, err := p.Ask(AskOptions{Question: "Keep the old flag?", Recommendation: "no, drop it",
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
		if got.ID != "Q3" || got.File != p.file("") {
			t.Errorf("asked %+v", got)
		}
		qs, err := p.Questions(false)
		if err != nil {
			t.Fatal(err)
		}
		var read *Question
		for i := range qs {
			if qs[i].ID == "Q3" {
				read = &qs[i]
			}
		}
		if read == nil || read.Question != "Keep the old flag?" || read.Recommendation != "no, drop it" ||
			read.Context != "Nothing reads it." || strings.Join(read.Alternatives, "|") != "keep it|deprecate it" ||
			strings.Join(read.Waiting, " ") != "Q1" {
			t.Errorf("read back %+v from %+v", read, qs)
		}
		data, _ := os.ReadFile(p.file(""))
		text := string(data)
		if open := text[strings.Index(text, "** Open questions\n"):strings.Index(text, "** Verification\n")]; !strings.HasSuffix(open,
			"*** Keep the old flag?\n:PROPERTIES:\n:ID: Q3\n:RECOMMENDATION: no, drop it\n:AFTER: Q1\n:END:\nNothing reads it.\n- keep it\n- deprecate it\n") {
			t.Errorf("Open questions:\n%s", open)
		}
		events, _ := p.Events()
		if last := events[len(events)-1]; last.Kind != "asked" || last.Item != "Q3" || last.Summary != "Keep the old flag?" {
			t.Errorf("logged %+v", last)
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
