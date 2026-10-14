package plans

import (
	"os"
	"strings"
	"testing"
)

// openQuestions writes two open questions into a plan's file and one into
// its part's, and a decision already taken.
func openQuestions(t *testing.T) (Config, Plan) {
	t.Helper()
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, p.file(""), "** Distribution", "P1")
	if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day); err != nil {
		t.Fatal(err)
	}
	edit := func(file, under, text string) {
		data, _ := os.ReadFile(file)
		os.WriteFile(file, []byte(strings.Replace(string(data), under+"\n", under+"\n"+text, 1)), 0o644)
	}
	edit(p.file(""), "** Decisions", "*** Keep the flag\n:PROPERTIES:\n:ID: D1\n:END:\n")
	edit(p.file(""), "** Open questions", "*** Which branch is the base?\n:PROPERTIES:\n:ID: Q1\n:RECOMMENDATION: main\n:END:\n"+
		"The release branch is frozen.\n- release\n- a branch of its own\n"+
		"*** Rename the command?\n:PROPERTIES:\n:ID: Q2\n:RECOMMENDATION: no\n:END:\n")
	edit(p.file("P1"), "** Open questions", "*** Split the test?\n:PROPERTIES:\n:ID: P1.Q1\n:RECOMMENDATION: yes\n:END:\n")
	return c, p
}

func TestQuestions(t *testing.T) {
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
	if QuestionsOrg(nil) != "No open questions.\n" {
		t.Errorf("none: %q", QuestionsOrg(nil))
	}
}
