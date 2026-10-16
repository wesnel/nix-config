package plans

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// openQuestions writes two open questions and a decided one into a plan's
// file, and an open one into its part's.
func openQuestions(t *testing.T) (Config, Plan) {
	t.Helper()
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, p.file(""), "** Distribution", "P1")
	if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day); err != nil {
		t.Fatal(err)
	}
	insertUnder(t, p.file(""), "** Questions", "*** ASK Which branch is the base?\n:PROPERTIES:\n:ID: Q1\n:RECOMMENDATION: main\n:END:\n"+
		"The release branch is frozen.\n- release\n- a branch of its own\n"+
		"*** ASK Rename the command?\n:PROPERTIES:\n:ID: Q2\n:RECOMMENDATION: no\n:END:\n"+
		"*** DECIDED Keep the flag? :ARCHIVE:\nCLOSED: [2026-10-13 Tue 10:00]\n:PROPERTIES:\n:ID: Q3\n:RECOMMENDATION: yes\n:ANSWER: yes\n:END:\n")
	insertUnder(t, p.file("P1"), "** Questions", "*** ASK Split the test?\n:PROPERTIES:\n:ID: P1.Q1\n:RECOMMENDATION: yes\n:END:\n")
	return c, p
}

func TestQuestions(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		_, p := openQuestions(t)
		qs, err := p.Questions(false)
		if err != nil {
			t.Fatal(err)
		}
		if len(qs) != 2 || qs[0].ID != "Q1" || qs[0].Question != "Which branch is the base?" || qs[0].Recommendation != "main" ||
			strings.Join(qs[0].Alternatives, "|") != "release|a branch of its own" || qs[0].Context != "The release branch is frozen." ||
			qs[1].ID != "Q2" || len(qs[1].Alternatives) != 0 {
			t.Errorf("questions %+v", qs)
		}
		all, err := p.Questions(true)
		if err != nil || len(all) != 3 || all[2].ID != "P1.Q1" || all[2].Part != "P1" {
			t.Errorf("with the parts: %+v, %v", all, err)
		}
		org := QuestionsOrg(qs)
		for _, s := range []string{"- Q1 :: Which branch is the base? Recommended: main. Or: release; a branch of its own.",
			"- Q2 :: Rename the command? Recommended: no."} {
			if !strings.Contains(org, s) {
				t.Errorf("org lacks %q:\n%s", s, org)
			}
		}
	})

	// Org reads a heading without a priority as [#B].
	t.Run("by priority, then file order", func(t *testing.T) {
		_, p := openQuestions(t)
		insertUnder(t, p.file(""), "** Questions", "*** ASK [#C] Later?\n:PROPERTIES:\n:ID: Q4\n:RECOMMENDATION: no\n:END:\n"+
			"*** ASK [#A] First?\n:PROPERTIES:\n:ID: Q5\n:RECOMMENDATION: yes\n:END:\n")
		qs, err := p.Questions(true)
		if err != nil {
			t.Fatal(err)
		}
		if got := ids(qs); got != "Q5 Q1 Q2 P1.Q1 Q4" {
			t.Errorf("order %q", got)
		}
		data, _ := json.Marshal(qs[0])
		if !strings.Contains(string(data), `"priority":"A"`) {
			t.Errorf("JSON %s", data)
		}
		if org := QuestionsOrg(qs[:1]); !strings.HasPrefix(org, "- Q5 [#A] :: First?") {
			t.Errorf("org %q", org)
		}
	})

	t.Run("none", func(t *testing.T) {
		if QuestionsOrg(nil) != "No open questions.\n" {
			t.Errorf("none: %q", QuestionsOrg(nil))
		}
	})

	t.Run("after", testQuestionsAfter)
}

func TestDecisions(t *testing.T) {
	t.Run("from the answer", func(t *testing.T) {
		_, p := openQuestions(t)
		if _, err := p.Decide("P1.Q1", DecideOptions{Answer: "no"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Decide("Q2", DecideOptions{Answer: "moot", Drop: true}, day); err != nil {
			t.Fatal(err)
		}
		ds, err := p.Decisions(false)
		if err != nil || len(ds) != 1 || ds[0].ID != "Q3" || ds[0].Question != "Keep the flag?" || ds[0].Answer != "yes" {
			t.Errorf("decisions %+v, %v", ds, err)
		}
		all, _ := p.Decisions(true)
		if ids := decisionIDs(all); ids != "Q3 P1.Q1" {
			t.Errorf("with the parts: %s", ids)
		}
		if org := DecisionsOrg(all); !strings.Contains(org, "- Q3 :: Keep the flag? yes\n") ||
			!strings.Contains(org, "- P1.Q1 :: Split the test? no\n") {
			t.Errorf("org:\n%s", org)
		}
	})

	// The operator settles a question in Emacs with a note, which Org puts in the
	// LOGBOOK drawer.
	t.Run("from Emacs's note", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		fixture, _ := os.ReadFile("testdata/emacs-note.org")
		os.WriteFile(p.file(""), fixture, 0o644)
		ds, err := p.Decisions(false)
		if err != nil || len(ds) != 1 || ds[0].Answer != "Drop it: nothing reads it. The new flag covers it." {
			t.Errorf("decisions %+v, %v", ds, err)
		}
	})
}

func decisionIDs(ds []Decision) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return strings.Join(out, " ")
}
