package plans

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Every command that adds an entry gives it its ID in CUSTOM_ID, which
// org-id and org-roam leave alone, and never in ID, which they take as
// global: S1 is S1 in every plan.
func TestWritersCustomID(t *testing.T) {
	p := newPlan(t, testConfig(t), "x")
	if _, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "Add the loop"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.AddEntry(EntryOptions{Section: "Distribution", Title: "The loop"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Ask(AskOptions{Question: "Keep the flag?", Recommendation: "no"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Followup(FollowupOptions{Title: "Rename it", FoundIn: "S1"}, day); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p.file(""))
	for _, id := range []string{"S1", "P1", "Q1", "F1"} {
		if !strings.Contains(string(data), "\n:CUSTOM_ID: "+id+"\n") || strings.Contains(string(data), "\n:ID: "+id+"\n") {
			t.Errorf("%s is not in CUSTOM_ID alone:\n%s", id, data)
		}
	}
	if ids, err := p.IDs(); err != nil || strings.Join(ids, " ") != "S1 P1 Q1 F1" {
		t.Errorf("IDs %v, %v", ids, err)
	}
}

// A part's file is no org-roam node: it has no file-level ID, and its own
// ID, like its entries', is in CUSTOM_ID.
func TestPartCustomID(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, p.file(""), "** Distribution", "P1")
	made, err := c.New(NewOptions{Title: "The part", Parent: p.file(""), Part: "P1"}, day)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(made.File)
	if !strings.HasPrefix(string(data), "#+title: The part\n") || !strings.Contains(string(data), "* Plan\n:PROPERTIES:\n:CUSTOM_ID: P1\n") ||
		strings.Contains(string(data), ":ID:") {
		t.Errorf("the part's file:\n%s", data)
	}
}

// Plans in either form are read alike: an item's ID is its CUSTOM_ID, or
// else its ID, so an org-id :ID: beside a CUSTOM_ID names nothing.
func TestItemIDForms(t *testing.T) {
	p := newPlan(t, testConfig(t), "x")
	addItemsIn(t, "ID", p.file(""), "** Steps", "S1")
	data, _ := os.ReadFile(p.file(""))
	text := strings.Replace(string(data), "** Distribution\n", "*** TODO Step S2\n:PROPERTIES:\n:CUSTOM_ID: S2\n:END:\n"+
		"*** TODO Step S3\n:PROPERTIES:\n:ID: 4f1d2c3b-0a9e-4d8c-b7a6-123456789abc\n:CUSTOM_ID: S3\n:END:\n** Distribution\n", 1)
	os.WriteFile(p.file(""), []byte(text), 0o644)
	if ids, err := p.IDs(); err != nil || strings.Join(ids, " ") != "S1 S2 S3" {
		t.Errorf("IDs %v, %v", ids, err)
	}
	for _, id := range []string{"S1", "S2", "S3"} {
		title := "Edited " + id
		if _, err := p.EditEntry(id, EditOptions{Title: &title}, day); err != nil {
			t.Errorf("edit %s: %v", id, err)
		}
	}
	data, _ = os.ReadFile(p.file(""))
	for _, id := range []string{"S1", "S2", "S3"} {
		if !strings.Contains(string(data), "*** TODO Edited "+id+"\n") {
			t.Errorf("%s was not edited:\n%s", id, data)
		}
	}
	title := "x"
	_, err := p.EditEntry("4f1d2c3b-0a9e-4d8c-b7a6-123456789abc", EditOptions{Title: &title}, day)
	isRefusal(t, "an org-id ID", err)
	s, err := p.StatusAt(day)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, st := range s.Steps {
		steps = append(steps, st.ID)
	}
	if !slices.Equal(steps, []string{"S1", "S2", "S3"}) {
		t.Errorf("status steps %v", steps)
	}
}

// A file in the old form, item IDs in ID or a plan's file without a
// file-level ID, is one drift entry that says it stays readable, and a
// write to it is not refused for it.
func TestOldForm(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	if drift, err := p.structureDrift(); err != nil || len(drift) != 0 {
		t.Fatalf("a new plan drifts: %+v, %v", drift, err)
	}
	data, _ := os.ReadFile(p.file(""))
	old := regexp.MustCompile(`\A:PROPERTIES:\n:ID: \S+\n:END:\n`).ReplaceAllString(string(data), "")
	old = strings.Replace(old, ":CUSTOM_ID: "+p.Name+"\n", ":ID: "+p.Name+"\n", 1)
	os.WriteFile(p.file(""), []byte(old), 0o644)
	addItemsIn(t, "ID", p.file(""), "** Steps", "S1")
	drift, err := p.structureDrift()
	if err != nil || len(drift) != 1 || !strings.Contains(drift[0].Problem, "old form") ||
		!strings.Contains(drift[0].Problem, "readable") {
		t.Errorf("the old form: %+v, %v", drift, err)
	}
	if _, err := p.AddEntry(EntryOptions{Section: "Steps", Title: "Another"}, day); err != nil {
		t.Errorf("a write to an old-form plan: %v", err)
	}
	if drift, _ := p.structureDrift(); len(drift) != 1 {
		t.Errorf("after a write: %+v", drift)
	}
}
