package plans

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func structureDrift(t *testing.T, p Plan) []string {
	t.Helper()
	s, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range s.StructureDrift {
		out = append(out, fmt.Sprintf("%s: %s: %s", filepath.Base(d.File), d.Heading, d.Problem))
	}
	return out
}

func appendTo(t *testing.T, file, text string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, append(data, text...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// planOrg is a plan file whose `* Plan` holds body.
func planOrg(body string) string {
	return "#+title: T\n" + todoLines() + "\n* Plan\n:PROPERTIES:\n:ID: x\n:END:\n" + body
}

func entry(keyword, title, props string) string {
	if keyword != "" {
		keyword += " "
	}
	return "*** " + keyword + title + "\n:PROPERTIES:\n" + props + ":END:\n"
}

// A plan file holds what is intended and decided now, in the sections
// agent-plan new writes: structureOf reports a heading that holds anything
// else, such as the history the log holds.
func TestStructureOf(t *testing.T) {
	question := "a question without a keyword ASK, DECIDED, DROPPED or SUPERSEDED"
	for name, c := range map[string]struct {
		text string
		want []string
	}{
		"what agent-plan and Org write": {planOrg("** Goal\nAdd the loop.\n" +
			"** Steps [2/5]\n" +
			"*** TODO In order\n:PROPERTIES:\n:ORDERED: t\n:END:\n" +
			"**** TODO First\n:PROPERTIES:\n:ID: S1\n:END:\n***** A note on the first\n" +
			"**** Nested\n:PROPERTIES:\n:ORDERED: t\n:END:\n***** TODO Deep\n:PROPERTIES:\n:ID: S2\n:END:\n" +
			"**** TODO With its own steps\n:PROPERTIES:\n:ID: S3\n:ORDERED: t\n:END:\n***** DONE Inner\n:PROPERTIES:\n:ID: S4\n:END:\n" +
			"*** Archive :ARCHIVE:\n**** DONE Archived\n:PROPERTIES:\n:ID: S5\n:END:\n" +
			"** Distribution [50%]\n*** Parts\n:PROPERTIES:\n:ORDERED: t\n:END:\n" +
			"**** TODO One\n:PROPERTIES:\n:ID: P1\n:END:\n**** TODO Two\n:PROPERTIES:\n:ID: P2\n:END:\n" +
			"** Questions\n" + entry("ASK", "Keep it?", ":ID: Q1\n") + entry("DECIDED [#A]", "Drop it? :ARCHIVE:", ":ID: Q2\n") +
			"** Follow-ups\n" + entry("TODO", "Fix the guard", ":ID: F1\n") +
			"** References\n" + entry("", "https://example.com", ":URL: https://example.com\n") +
			"** Verification\n#+begin_example\n,* not a heading\n#+end_example\n*\tnot a heading either\n"), nil},
		"a journal": {planOrg("** Goal\n** Hand-off\nRead this first.\n" +
			"** Decisions (2026-10-17)\n*** D1 Branch built and pushed\n" +
			"** Steps\n*** Status of the branches\n" +
			entry("", "Not a group", ":ORDERED: nil\n") +
			entry("TODO", "A group with a bad ID", ":ID: X1\n:ORDERED: t\n") +
			entry("", "Branch status", ":ORDERED: t\n") + "Everything is pushed.\n" + "**** TODO Push\n:PROPERTIES:\n:ID: S1\n:END:\n" +
			"*** Archive :ARCHIVE:\n2026-10-17 pushed everything\n" +
			entry("", "An empty group", ":ORDERED: t\n") +
			"*** Archive\n" +
			entry("TODO", "A step with steps", ":ID: S2\n:ORDERED: t\n") + "**** Not a step\n" +
			"** Distribution\n*** TODO The flag\n" +
			"** Questions\n" + entry("", "Keep the flag?", ":ID: Q1\n") + "*** ASK Rename it?\n" + entry("TODO", "Drop it?", ":ID: Q2\n") +
			entry("ASK", "Ordered?", ":ORDERED: t\n") +
			"** Follow-ups\n" + entry("TODO", "Fix the guard", ":ID: D3\n") +
			"* Notes\n** Steps\n*** Not checked\n"), []string{
			"** Hand-off: not a plan section",
			"** Decisions (2026-10-17): not a plan section",
			"*** Status of the branches: a Steps entry without an ID S<n>",
			"*** Not a group: a Steps entry without an ID S<n>",
			"*** A group with a bad ID: a Steps entry without an ID S<n>",
			"*** Branch status: a group with text of its own",
			"*** Archive: a group with text of its own",
			"*** An empty group: a group with no entries under it",
			"*** Archive: a Steps entry without an ID S<n>",
			"**** Not a step: a Steps entry without an ID S<n>",
			"*** The flag: a Distribution entry without an ID P<n>",
			"*** Keep the flag?: " + question,
			"*** Rename it?: a Questions entry without an ID Q<n>",
			"*** Drop it?: " + question,
			"*** Ordered?: a Questions entry without an ID Q<n>",
			"*** Fix the guard: a Follow-ups entry without an ID F<n>",
			"* Notes: a top-level heading other than `* Plan`"}},
		"headings out of place": {"** Before the plan\n" + planOrg("** Goal\n*** Why\n** Steps\n**** Too deep\n** Steps\n*** Not checked\n"+
			"** References\n*** A link\n** Verification\n*** How\n* Plan\n** Goal\n"), []string{
			"** Before the plan: a level-2 heading with no level-1 heading above it",
			"*** Why: a heading under Goal, which holds text",
			"**** Too deep: a level-4 heading with no level-3 heading above it",
			"** Steps: a second `** Steps`",
			"*** A link: a References entry without a URL",
			"*** How: a heading under Verification, which holds text",
			"* Plan: a second `* Plan`"}},
		// Org reads a block with no end as text, and the headings after it
		// as headings.
		"headings in blocks": {planOrg("** Verification\n#+begin_example\n* Hand-off\n#+end_example\n#+begin_quote\n" +
			"#+begin_src sh\n** Decisions\n#+end_example\n** Steps\n"), []string{
			"* Hand-off: a heading line inside a block, which Org reads as a heading; Org escapes it with a comma",
			"#+begin_quote: a block with no end, which Org reads as text",
			"#+begin_src sh: a block with no end, which Org reads as text",
			"** Decisions: not a plan section"}},
	} {
		t.Run(name, func(t *testing.T) {
			var got []string
			for _, d := range structureOf(c.text) {
				got = append(got, d.heading+": "+d.problem)
			}
			expectList(t, "structure", got, c.want...)
		})
	}

	// The fixtures are Org's own output, statistics cookies and all.
	t.Run("Org's files", func(t *testing.T) {
		files, err := filepath.Glob("testdata/*.org")
		if err != nil || len(files) == 0 {
			t.Fatal(files, err)
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "** Steps [") {
				t.Errorf("%s has no cookie", f)
			}
			if got := structureOf(string(data)); len(got) > 0 {
				t.Errorf("%s: %v", f, got)
			}
		}
	})
}

// Every command finds a section by the title the structure check reads,
// whatever statistics cookies or tags Org keeps on it.
func TestSectionsWithCookies(t *testing.T) {
	p := newPlan(t, testConfig(t), "x")
	data, err := os.ReadFile(p.File())
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for from, to := range map[string]string{"* Plan\n": "* Plan :work:\n", "** Goal\n": "** Goal [0%]\nAdd the loop.\n",
		"** Questions\n": "** Questions [1/2]\n", "** Follow-ups\n": "** Follow-ups [0/1]\n",
		"** References\n": "** References [0/0] :refs:\n"} {
		if !strings.Contains(text, from) {
			t.Fatalf("no %q", from)
		}
		text = strings.Replace(text, from, to, 1)
	}
	if err := os.WriteFile(p.File(), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, o := range []AskOptions{{Question: "Keep the flag?", Recommendation: "no"},
		{Question: "Rename it?", Recommendation: "yes"}} {
		if _, err := p.Ask(o, day); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.Decide("Q1", DecideOptions{Answer: "no, drop it"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Followup(FollowupOptions{Title: "Fix the guard", FoundIn: "Q1"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Reference("https://example.com/a", "", day); err != nil {
		t.Fatal(err)
	}
	if err := p.SetProperty("", "BRANCHES", []string{"main..flag"}, day); err != nil {
		t.Fatal(err)
	}
	inQuestions, err := p.InSection("Questions")
	if err != nil {
		t.Fatal(err)
	}
	if !inQuestions["Q1"] || !inQuestions["Q2"] || inQuestions["F1"] {
		t.Errorf("in Questions: %v", inQuestions)
	}
	s, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.StructureDrift) > 0 || len(s.PropertyDrift) > 0 || strings.Join(s.Followups, " ") != "F1" {
		t.Errorf("structure %v, properties %v, follow-ups %v", s.StructureDrift, s.PropertyDrift, s.Followups)
	}
	if qs, err := p.Questions(true); err != nil || ids(qs) != "Q2" {
		t.Errorf("questions %v, %v", qs, err)
	}
	brief, err := p.Brief(day)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(brief, "Goal:\nAdd the loop.\nDecided:\n- Q1 :: Keep the flag? no, drop it\n") ||
		!strings.Contains(brief, "Questions for the operator: Q2\n") || !strings.Contains(brief, "Follow-ups open: 1\n") {
		t.Errorf("brief:\n%s", brief)
	}
	data, _ = os.ReadFile(p.File())
	for _, want := range []string{"* Plan :work:\n:PROPERTIES:\n", ":BRANCHES: main..flag\n", "** Questions [1/2]\n*** ",
		"** Follow-ups [0/1]\n*** TODO Fix the guard", "** References [0/0] :refs:\n*** "} {
		if !strings.Contains(string(data), want) {
			t.Errorf("plan has no %q:\n%s", want, data)
		}
	}
}

// A section is the plan's only under its first `* Plan`: commands neither
// write to nor read from a section of the same title elsewhere.
func TestSectionsUnderPlan(t *testing.T) {
	p := newPlan(t, testConfig(t), "x")
	stray := "* Notes\n** Questions\n" + entry("ASK", "Stray?", ":ID: Q7\n") + "** Goal\nNot the goal.\n"
	setGoal(t, p.File(), "Add the loop.")
	insertUnder(t, p.File(), "#+FILETAGS: :r:box:\n", stray)
	if _, err := p.Ask(AskOptions{Question: "Keep the flag?", Recommendation: "no"}, day); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p.File())
	if !strings.Contains(string(data), stray) || !strings.Contains(string(data), "** Questions\n*** ASK Keep the flag?\n") {
		t.Errorf("plan:\n%s", data)
	}
	if qs, err := p.Questions(true); err != nil || ids(qs) != "Q8" {
		t.Errorf("questions %v, %v", qs, err)
	}
	if goal, err := p.Goal(""); err != nil || goal != "Add the loop.\n" {
		t.Errorf("goal %q, %v", goal, err)
	}
	if in, err := p.InSection("Questions"); err != nil || in["Q7"] || !in["Q8"] {
		t.Errorf("in Questions %v, %v", in, err)
	}
}

// Status, its JSON and the briefs report the structure drift of every
// file of the plan.
func TestStructureDrift(t *testing.T) {
	t.Run("a new plan", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		expectList(t, "structure drift", structureDrift(t, p))
		s, err := p.Status()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"structureDrift":[]`) {
			t.Errorf("json: %s", data)
		}
		if strings.Contains(s.Org(), "Structure drift") {
			t.Errorf("org:\n%s", s.Org())
		}
		for _, brief := range []func() (string, error){func() (string, error) { return p.Brief(day) },
			func() (string, error) { return p.ResumeBrief(day, ResumeLimit) }} {
			text, err := brief()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, "Structure drift") || !strings.Contains(text, "Follow-ups open: 0\n") ||
				strings.Contains(text, "\n\n") {
				t.Errorf("brief:\n%s", text)
			}
		}
	})

	t.Run("a journal", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		made, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day)
		if err != nil {
			t.Fatal(err)
		}
		long := "D1 " + strings.Repeat("branch built, ", 20)
		insertUnder(t, p.file(""), "** Steps", "*** "+long+"\n")
		appendTo(t, p.file(""), "** Hand-off\n** Decisions (2026-10-17)\n** Progress\n")
		appendTo(t, made.File, "** Progress\n")
		plan, part := filepath.Base(p.file("")), filepath.Base(made.File)
		expectList(t, "structure drift", structureDrift(t, p),
			plan+": *** "+strings.TrimSpace(long)+": a Steps entry without an ID S<n>",
			plan+": ** Hand-off: not a plan section",
			plan+": ** Decisions (2026-10-17): not a plan section",
			plan+": ** Progress: not a plan section",
			part+": ** Progress: not a plan section")
		s, err := p.Status()
		if err != nil {
			t.Fatal(err)
		}
		shown := "*** D1 " + strings.Repeat("branch built, ", 5) + "…"
		if org := s.Org(); !strings.Contains(org, "\n- Structure drift: "+plan+" `"+shown+"` (a Steps entry without an ID S<n>); "+
			plan+" `** Hand-off` (not a plan section); ") || strings.Contains(org, long) {
			t.Errorf("org:\n%s", org)
		}
		line := "\nStructure drift in 5 headings, which `agent-plan status " + p.file("") + "` lists: `" + shown +
			"`, `** Hand-off`, `** Decisions (2026-10-17)`, and 2 more\n"
		brief, err := p.Brief(day)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(brief, line) {
			t.Errorf("brief:\n%s", brief)
		}
		resume, err := p.ResumeBrief(day, ResumeLimit)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(resume, line) {
			t.Errorf("resume brief:\n%s", resume)
		}
	})
}

// Every write agent-plan makes to a plan file keeps its structure: one
// that would add drift is refused before anything is written, and drift
// already in the file, which is the operator's to settle, refuses nothing.
func TestWriteKeepsStructure(t *testing.T) {
	edit := func(p Plan, file string, fn func(lines []string) ([]string, error)) error {
		return p.withLock(func(l *lockedPlan) error { return l.edit(file, fn) })
	}
	insert := func(under string, add ...string) func(lines []string) ([]string, error) {
		return func(lines []string) ([]string, error) {
			_, end, ok := section(lines, under)
			if !ok {
				return nil, fmt.Errorf("no %s", under)
			}
			if end == len(lines) && lines[end-1] == "" {
				end--
			}
			return append(append(append([]string{}, lines[:end]...), add...), lines[end:]...), nil
		}
	}
	for what, c := range map[string]struct {
		fn    func(lines []string) ([]string, error)
		shown string
	}{
		"a second section":        {insert("Steps", "** Questions"), "** Questions"},
		"an entry without its ID": {insert("Steps", "*** TODO Tag it"), "*** Tag it"},
		"a heading under Goal":    {insert("Goal", "*** Why"), "*** Why"},
		"a top-level heading":     {insert("Verification", "* Journal"), "* Journal"},
		"a block with no end":     {insert("Verification", "#+begin_src sh"), "#+begin_src sh"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			cfg := testConfig(t)
			p := newPlan(t, cfg, "x")
			file := p.file("")
			before, _ := os.ReadFile(file)
			log, _ := os.ReadFile(p.log())
			err := edit(p, file, c.fn)
			isRefusal(t, what, err)
			if err != nil && !strings.Contains(err.Error(), c.shown) {
				t.Errorf("%s: the refusal does not name %q: %v", what, c.shown, err)
			}
			if after, _ := os.ReadFile(file); string(after) != string(before) {
				t.Errorf("%s: the file changed:\n%s", what, after)
			}
			if after, _ := os.ReadFile(p.log()); string(after) != string(log) {
				t.Errorf("%s: the log changed", what)
			}
		})
	}

	t.Run("drift already there", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Steps", "S1")
		appendTo(t, p.file(""), "** Hand-off\n* Journal\n")
		if _, err := p.Followup(FollowupOptions{Title: "Split the guard", FoundIn: "S1"}, day); err != nil {
			t.Fatalf("a follow-up in a drifting plan: %v", err)
		}
		if err := edit(p, p.file(""), insert("Steps", "*** TODO Also this")); err == nil {
			t.Error("a write adding drift to a drifting plan went through")
		}
	})

	// A keyword the log does not give is drift too; the log's own
	// projection of one is not.
	t.Run("keyword drift", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Steps", "S1", "S2")
		done := func(id string) func(lines []string) ([]string, error) {
			return func(lines []string) ([]string, error) {
				return setKeyword(lines, headingWithID(lines, id), kwDone, planKeywords, day), nil
			}
		}
		before := fileText(t, p.file(""))
		err := edit(p, p.file(""), done("S1"))
		isRefusal(t, "a keyword the log does not give", err)
		if err != nil && !strings.Contains(err.Error(), "S1") {
			t.Errorf("the refusal does not name S1: %v", err)
		}
		if after := fileText(t, p.file("")); after != before {
			t.Errorf("the file changed:\n%s", after)
		}
		if _, err := p.Log(Event{Kind: "done", Item: "S2"}, day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "S2", 0); got != "*** DONE Step S2" {
			t.Errorf("the log's keyword was not written: %q", got)
		}
		// S1 drifting already does not stop a write elsewhere.
		data := strings.Replace(fileText(t, p.file("")), "*** TODO Step S1", "*** DONE Step S1", 1)
		os.WriteFile(p.file(""), []byte(data), 0o644)
		if _, err := p.Followup(FollowupOptions{Title: "Later", FoundIn: "S1"}, day); err != nil {
			t.Errorf("a follow-up beside drift already there: %v", err)
		}
	})
}
