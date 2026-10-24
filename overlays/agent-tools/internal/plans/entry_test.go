package plans

import (
	"os"
	"strings"
	"testing"
)

// entryPlan makes a plan with steps S1 and S2, part P1 with its file, and
// question Q1.
func entryPlan(t *testing.T) (Config, Plan) {
	t.Helper()
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, p.file(""), "** Steps", "S1", "S2")
	addItems(t, p.file(""), "** Distribution", "P1")
	if _, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Ask(AskOptions{Question: "Keep it?", Recommendation: "yes"}, day); err != nil {
		t.Fatal(err)
	}
	return c, p
}

func fileText(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A step or a part is added through agent-plan, under the next free ID of
// its kind, in the form the plan's readers take.
func TestAddEntry(t *testing.T) {
	t.Run("a step", func(t *testing.T) {
		_, p := entryPlan(t)
		got, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "Tag it", Text: "Done when the tag is pushed.\nCI passes on it.",
			After: []string{"S1", "Q1"}}, day)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != "S3" || got.File != p.file("") || got.Event.Kind != "planned" || got.Event.Item != "S3" ||
			got.Event.Summary != "Tag it" {
			t.Errorf("added %+v", got)
		}
		want := "*** TODO Step S2\n:PROPERTIES:\n:CUSTOM_ID: S2\n:END:\n*** TODO Tag it\n:PROPERTIES:\n:CUSTOM_ID: S3\n:AFTER: S1 Q1\n:END:\n" +
			"Done when the tag is pushed.\nCI passes on it.\n** Distribution\n"
		if text := fileText(t, p.file("")); !strings.Contains(text, want) {
			t.Errorf("plan:\n%s", text)
		}
		s, err := p.Status()
		if err != nil || len(s.StructureDrift) != 0 {
			t.Errorf("status %v, drift %v", err, s.StructureDrift)
		}
	})

	t.Run("an ID the log named is not handed out again", func(t *testing.T) {
		_, p := entryPlan(t)
		addItems(t, p.file(""), "** Steps", "S7")
		if _, err := p.Log(Event{Kind: "abandoned", Item: "S7"}, day); err != nil {
			t.Fatal(err)
		}
		text := fileText(t, p.file(""))
		os.WriteFile(p.file(""), []byte(strings.Replace(text, "*** ABANDONED Step S7\nCLOSED: [2026-10-14 Wed 13:05]\n:PROPERTIES:\n:CUSTOM_ID: S7\n:END:\n", "", 1)), 0o644)
		got, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "Tag it"}, day)
		if err != nil || got.ID != "S8" {
			t.Errorf("added %+v, %v", got, err)
		}
	})

	t.Run("a part's step", func(t *testing.T) {
		_, p := entryPlan(t)
		got, err := p.AddEntry(EntryOptions{Section: "Steps", Part: "P1", Title: "Add the flag"}, day)
		if err != nil || got.ID != "P1.S1" || got.File != p.file("P1") {
			t.Fatalf("added %+v, %v", got, err)
		}
		if text := fileText(t, p.file("P1")); !strings.Contains(text, "** Steps\n*** TODO Add the flag\n:PROPERTIES:\n:CUSTOM_ID: P1.S1\n:END:\n** Questions\n") {
			t.Errorf("part:\n%s", text)
		}
	})

	t.Run("a part", func(t *testing.T) {
		c, p := entryPlan(t)
		got, err := p.AddEntry(EntryOptions{Section: "Distribution", Title: "plan-write-path", Text: "Commands for every edit.",
			After: []string{"P1"}, Repo: "nix-config", Branch: "main..plan-write-path", Worktree: "/w/plan-write-path"}, day)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != "P2" || got.Event.Kind != "planned" {
			t.Errorf("added %+v", got)
		}
		want := "*** TODO plan-write-path\n:PROPERTIES:\n:CUSTOM_ID: P2\n:AFTER: P1\n:END:\n" +
			"nix-config: plan-write-path on main, worktree /w/plan-write-path.\nCommands for every edit.\n** Questions\n"
		if text := fileText(t, p.file("")); !strings.Contains(text, want) {
			t.Errorf("plan:\n%s", text)
		}
		if _, err := c.New(NewOptions{Title: "Write path", Parent: p.file(""), Part: "P2"}, day); err != nil {
			t.Errorf("the new part's file: %v", err)
		}
	})

	t.Run("a part with a file but no entry keeps its ID", func(t *testing.T) {
		_, p := entryPlan(t)
		os.WriteFile(p.file("P4"), []byte(fileText(t, p.file("P1"))), 0o644)
		got, err := p.AddEntry(EntryOptions{Section: "Distribution", Title: "next"}, day)
		if err != nil || got.ID != "P5" {
			t.Errorf("added %+v, %v", got, err)
		}
	})

	for what, o := range map[string]EntryOptions{
		"no title":                    {Section: "Steps"},
		"a title on two lines":        {Section: "Steps", Title: "x\n* forged"},
		"a title Org reads as done":   {Section: "Steps", Title: "DONE already"},
		"a title with a priority":     {Section: "Steps", Title: "[#A] first"},
		"a title with tags":           {Section: "Steps", Title: "first :ARCHIVE:"},
		"a heading in the text":       {Section: "Steps", Title: "x", Text: "fine\n*** TODO forged"},
		"a drawer in the text":        {Section: "Steps", Title: "x", Text: ":LOGBOOK:\nx\n:END:"},
		"a keyword line in the text":  {Section: "Steps", Title: "x", Text: "#+TODO: A | B"},
		"an ID line in the text":      {Section: "Steps", Title: "x", Text: "fine\n:CUSTOM_ID: S1"},
		"an old ID line in the text":  {Section: "Steps", Title: "x", Text: "fine\n:ID: S1"},
		"a property line in the text": {Section: "Steps", Title: "x", Text: "fine\n  :AFTER: S1"},
		"a COMMENT title":             {Section: "Steps", Title: "COMMENT later"},
		"an AFTER twice":              {Section: "Steps", Title: "x", After: []string{"S1", "S1"}},
		"an unclosed block":           {Section: "Steps", Title: "x", Text: "#+begin_src sh\necho"},
		"an unknown AFTER":            {Section: "Steps", Title: "x", After: []string{"S9"}},
		"an AFTER with a space":       {Section: "Steps", Title: "x", After: []string{"S1 S2"}},
		"another section":             {Section: "Questions", Title: "x"},
		"a part in a part's file":     {Section: "Distribution", Part: "P1", Title: "x"},
		"a part with no file":         {Section: "Steps", Part: "P7", Title: "x"},
		"a step with a repository":    {Section: "Steps", Title: "x", Repo: "nix-config"},
		"a branch without a base":     {Section: "Distribution", Title: "x", Repo: "nix-config", Branch: "loop"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			_, p := entryPlan(t)
			before := fileText(t, p.file(""))
			_, err := p.AddEntry(o, day)
			isRefusal(t, what, err)
			if after := fileText(t, p.file("")); after != before {
				t.Errorf("%s: the plan changed:\n%s", what, after)
			}
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		_, p := entryPlan(t)
		_, err := otherHosts(t, p).AddEntry(EntryOptions{Section: "Steps", Title: "x"}, day)
		isRefusal(t, "another host's plan", err)
	})
}

// An ID line in an entry's text, as the operator may write one, names no
// heading: a command finds an entry by the drawer Org reads as its own.
func TestIDInText(t *testing.T) {
	text := func(s string) *string { return &s }
	t.Run("edit", func(t *testing.T) {
		_, p := entryPlan(t)
		insertUnder(t, p.file(""), ":CUSTOM_ID: S1\n:END:", "Before S2:\n:CUSTOM_ID: S2\n")
		if _, err := p.EditEntry("S2", EditOptions{Title: text("The second")}, day); err != nil {
			t.Fatal(err)
		}
		plan := fileText(t, p.file(""))
		if !strings.Contains(plan, "*** TODO Step S1\n") || !strings.Contains(plan, "*** TODO The second\n:PROPERTIES:\n:CUSTOM_ID: S2\n") {
			t.Errorf("plan:\n%s", plan)
		}
	})
	t.Run("decide", func(t *testing.T) {
		_, p := entryPlan(t)
		if _, err := p.Ask(AskOptions{Question: "Keep that too?", Recommendation: "no"}, day); err != nil {
			t.Fatal(err)
		}
		insertUnder(t, p.file(""), ":RECOMMENDATION: yes\n:END:", "See\n:CUSTOM_ID: Q2\n")
		if _, err := p.Decide("Q2", DecideOptions{Answer: "no"}, day); err != nil {
			t.Fatal(err)
		}
		if plan := fileText(t, p.file("")); !strings.Contains(plan, "*** ASK Keep it?\n") || !strings.Contains(plan, "*** DECIDED Keep that too?") {
			t.Errorf("plan:\n%s", plan)
		}
	})
}

// A step's, part's or follow-up's title and text change in place, keeping
// everything Org and the log keep on its heading.
func TestEditEntry(t *testing.T) {
	text := func(s string) *string { return &s }

	t.Run("title and text", func(t *testing.T) {
		_, p := entryPlan(t)
		if _, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		insertUnder(t, p.file(""), ":CUSTOM_ID: S1\n:END:", "Old text.\n:LOGBOOK:\n- Note taken\n:END:\nMore old text.\n**** A child\nkept\n")
		data := strings.Replace(fileText(t, p.file("")), "*** DONE Step S1\n", "*** DONE Step S1 :later:\n", 1)
		os.WriteFile(p.file(""), []byte(data), 0o644)
		got, err := p.EditEntry("S1", EditOptions{Title: text("Add the run loop"), Text: text("Done when it runs.")}, day)
		if err != nil {
			t.Fatal(err)
		}
		if got.Event.Kind != "planned" || got.Event.Item != "S1" || got.Event.Summary != "title and text edited" {
			t.Errorf("event %+v", got.Event)
		}
		want := "*** DONE Add the run loop :later:\nCLOSED: [2026-10-14 Wed 13:05]\n:PROPERTIES:\n:CUSTOM_ID: S1\n:END:\n" +
			"Done when it runs.\n:LOGBOOK:\n- Note taken\n:END:\n**** A child\nkept\n*** TODO Step S2\n"
		if text := fileText(t, p.file("")); !strings.Contains(text, want) {
			t.Errorf("plan:\n%s", text)
		}
	})

	t.Run("text cleared, title kept", func(t *testing.T) {
		_, p := entryPlan(t)
		insertUnder(t, p.file(""), ":CUSTOM_ID: P1\n:END:", "nix-config: loop on main.\n")
		if _, err := p.EditEntry("P1", EditOptions{Text: text("")}, day); err != nil {
			t.Fatal(err)
		}
		if text := fileText(t, p.file("")); !strings.Contains(text, "*** TODO Step P1\n:PROPERTIES:\n:CUSTOM_ID: P1\n:END:\n** Questions\n") {
			t.Errorf("plan:\n%s", text)
		}
	})

	t.Run("a part's step, last in its file", func(t *testing.T) {
		_, p := entryPlan(t)
		if _, err := p.Followup(FollowupOptions{Title: "Later", FoundIn: "P1", Part: "P1"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.EditEntry("P1.F1", EditOptions{Text: text("Found in the flag's tests.")}, day); err != nil {
			t.Fatal(err)
		}
		if text := fileText(t, p.file("P1")); !strings.Contains(text, ":FOUND_IN: P1\n:END:\nFound in the flag's tests.\n** References\n") {
			t.Errorf("part:\n%s", text)
		}
	})

	for what, c := range map[string]struct {
		id string
		o  EditOptions
	}{
		"nothing to change":  {"S1", EditOptions{}},
		"a question":         {"Q1", EditOptions{Title: text("Keep it all?")}},
		"an unknown ID":      {"S9", EditOptions{Title: text("x")}},
		"an empty title":     {"S1", EditOptions{Title: text(" ")}},
		"a title as keyword": {"S1", EditOptions{Title: text("VERIFIED x")}},
		"a heading in text":  {"S1", EditOptions{Text: text("* forged")}},
		"the plan's own ID":  {"2026-10-14-x.box", EditOptions{Text: text("x")}},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			_, p := entryPlan(t)
			before := fileText(t, p.file(""))
			_, err := p.EditEntry(c.id, c.o, day)
			isRefusal(t, what, err)
			if after := fileText(t, p.file("")); after != before {
				t.Errorf("%s: the plan changed:\n%s", what, after)
			}
		})
	}
}

// An entry's AFTER is set, replaced and cleared through agent-plan, and
// status waits on it.
func TestSetAfter(t *testing.T) {
	_, p := entryPlan(t)
	if _, err := p.SetAfter("S2", []string{"S1"}, day); err != nil {
		t.Fatal(err)
	}
	if got := linesAt(t, p.file(""), "S2", 3); got != "*** TODO Step S2\n:PROPERTIES:\n:CUSTOM_ID: S2\n:AFTER: S1" {
		t.Errorf("after set:\n%s", got)
	}
	if s, _ := p.Status(); strings.Join(s.ReadySteps, " ") != "S1" {
		t.Errorf("ready steps %v", s.ReadySteps)
	}
	e, err := p.SetAfter("S2", []string{"S1", "Q1"}, day)
	if err != nil || e.Kind != "planned" || e.Item != "S2" || e.Summary != "AFTER S1 Q1" {
		t.Errorf("replaced: %+v, %v", e, err)
	}
	if _, err := p.SetAfter("Q1", []string{"P1"}, day); err != nil {
		t.Errorf("a question's AFTER: %v", err)
	}
	e, err = p.SetAfter("S2", nil, day)
	if err != nil || e.Summary != "AFTER cleared" {
		t.Errorf("cleared: %+v, %v", e, err)
	}
	if got := linesAt(t, p.file(""), "S2", 3); got != "*** TODO Step S2\n:PROPERTIES:\n:CUSTOM_ID: S2\n:END:" {
		t.Errorf("after cleared:\n%s", got)
	}

	for what, c := range map[string]struct {
		id    string
		after []string
	}{
		"itself":           {"S2", []string{"S2"}},
		"an unknown ID":    {"S2", []string{"S9"}},
		"an unknown entry": {"S9", []string{"S1"}},
		"a follow-up":      {"F1", []string{"S1"}},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			_, p := entryPlan(t)
			p.Followup(FollowupOptions{Title: "Later", FoundIn: "S1"}, day)
			_, err := p.SetAfter(c.id, c.after, day)
			isRefusal(t, what, err)
		})
	}
}

// Goal and Verification, the sections that hold text, are written through
// agent-plan as a whole.
func TestSetSection(t *testing.T) {
	_, p := entryPlan(t)
	setGoal(t, p.file(""), "Old goal.")
	e, err := p.SetSection("", "Goal", "Add the loop.\n\nLikely next changes: its flag.", day)
	if err != nil || e.Kind != "planned" || e.Item != p.Name || e.Summary != "Goal written" {
		t.Fatalf("goal: %+v, %v", e, err)
	}
	if goal, _ := p.Goal(""); goal != "Add the loop.\n\nLikely next changes: its flag.\n" {
		t.Errorf("goal %q", goal)
	}
	if _, err := p.SetSection("P1", "Verification", "- go test ./...", day); err != nil {
		t.Fatal(err)
	}
	if text := fileText(t, p.file("P1")); !strings.HasSuffix(text, "** Verification\n- go test ./...\n") {
		t.Errorf("part:\n%s", text)
	}
	if e, err := p.SetSection("P1", "Goal", "The flag.", day); err != nil || e.Item != "P1" {
		t.Errorf("a part's goal: %+v, %v", e, err)
	}

	for _, c := range [][3]string{
		{"", "Steps", "x"},
		{"", "Goal", "*** heading"},
		{"", "Goal", " "},
		{"P7", "Goal", "x"},
	} {
		t.Run("refused/"+strings.Join(c[:], " "), func(t *testing.T) {
			_, err := p.SetSection(c[0], c[1], c[2], day)
			isRefusal(t, strings.Join(c[:], " "), err)
		})
	}

	t.Run("refused/headings under Verification", func(t *testing.T) {
		_, p := entryPlan(t)
		appendTo(t, p.file(""), "- old check\n*** The operator's notes\nkept\n")
		before := fileText(t, p.file(""))
		_, err := p.SetSection("", "Verification", "- go test ./...", day)
		isRefusal(t, "headings under Verification", err)
		if after := fileText(t, p.file("")); after != before {
			t.Errorf("the plan changed:\n%s", after)
		}
	})
}

// The `* Plan` properties no log tracks are set through agent-plan too.
func TestSetPlainProperty(t *testing.T) {
	_, p := entryPlan(t)
	for name, values := range map[string][]string{"STATUS": {"halted"}, "SKILLS": {"coordinate-subagents", "review-loop"},
		"REPOSITORIES": {"o/r", "o/s"}, "HARNESS": {"codex"}} {
		if err := p.SetProperty("", name, values, day); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if text := fileText(t, p.file("")); !strings.Contains(text, ":"+name+": "+strings.Join(values, " ")+"\n") {
			t.Errorf("%s:\n%s", name, text)
		}
	}
	if err := p.SetProperty("P1", "STATUS", []string{"working"}, day); err != nil {
		t.Errorf("a part's STATUS: %v", err)
	}
	if s, _ := p.Status(); len(s.PropertyDrift) != 0 {
		t.Errorf("property drift %v", s.PropertyDrift)
	}
	for what, c := range map[string]struct {
		name   string
		values []string
	}{
		"an unknown STATUS": {"STATUS", []string{"done"}},
		"two STATUS values": {"STATUS", []string{"working", "halted"}},
		"two harnesses":     {"HARNESS", []string{"claude", "codex"}},
		"the plan's ID":     {"ID", []string{"x"}},
		"the plan's host":   {"HOST", []string{"other"}},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			isRefusal(t, what, p.SetProperty("", c.name, c.values, day))
		})
	}
}
