package plans

import (
	"os"
	"strings"
	"testing"
)

// byID is the heading of headings whose ID is id.
func byID(t *testing.T, headings []heading, id string) heading {
	t.Helper()
	for _, h := range headings {
		if h.Props["ID"] == id {
			return h
		}
	}
	t.Fatalf("no heading with ID %s", id)
	return heading{}
}

func readOrg(t *testing.T, name string) []heading {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return parseOrg(string(data))
}

// parseOrg reads a heading's state from the keywords its own file
// defines, as Org does, and keeps drawers other than the property drawer
// out of the body.
func TestParseOrg(t *testing.T) {
	// emacs-closed.org and emacs-note.org are Org's own output: open.org,
	// with states changed in Emacs and saved.
	t.Run("the file's own keywords", func(t *testing.T) {
		hs := readOrg(t, "emacs-closed.org")
		for id, want := range map[string][2]string{
			"S1": {"DONE", "Add the flag"}, "S2": {"VERIFIED", "Wire it in"}, "S3": {"ABANDONED", "Drop the old flag"},
			"S4": {"TODO", "Leave it"}, "Q1": {"ASK", "Keep the old flag?"},
		} {
			if h := byID(t, hs, id); h.Keyword != want[0] || h.Title != want[1] {
				t.Errorf("%s: keyword %q, title %q; want %q", id, h.Keyword, h.Title, want)
			}
		}
		for id, done := range map[string]bool{"S1": true, "S2": true, "S3": true, "S4": false, "Q1": false, "P1": true} {
			if h := byID(t, hs, id); h.Done != done {
				t.Errorf("%s (%s): done %v, want %v", id, h.Keyword, h.Done, done)
			}
		}
		if h := byID(t, hs, "Q1"); h.Priority != "A" {
			t.Errorf("Q1's priority %q", h.Priority)
		}
	})

	t.Run("Org's default keywords", func(t *testing.T) {
		hs := parseOrg("* STARTED Not a keyword here\n* DONE Done\n* TODO Open\n")
		if hs[0].Keyword != "" || hs[0].Title != "STARTED Not a keyword here" {
			t.Errorf("STARTED in a file without it: %+v", hs[0])
		}
		if hs[1].Keyword != "DONE" || !hs[1].Done || hs[2].Keyword != "TODO" || hs[2].Done {
			t.Errorf("TODO and DONE: %+v", hs[1:])
		}
	})

	t.Run("a sequence without a bar", func(t *testing.T) {
		hs := parseOrg("#+SEQ_TODO: OPEN(o) SHUT(s!)\n* OPEN a\n* SHUT b\n")
		if hs[0].Keyword != "OPEN" || hs[0].Done || hs[1].Keyword != "SHUT" || !hs[1].Done {
			t.Errorf("headings %+v", hs)
		}
	})

	t.Run("priority and tags", func(t *testing.T) {
		hs := parseOrg("#+TODO: ASK | DECIDED\n*** DECIDED [#B] Keep it?                              :ARCHIVE:x_y:\n*** Plain :not a tag:\n")
		if h := hs[0]; h.Keyword != "DECIDED" || h.Priority != "B" || h.Title != "Keep it?" ||
			strings.Join(h.Tags, " ") != "ARCHIVE x_y" {
			t.Errorf("heading %+v", h)
		}
		if h := hs[1]; h.Title != "Plain :not a tag:" || len(h.Tags) != 0 {
			t.Errorf("heading %+v", h)
		}
	})

	// A note Wesley adds when he settles a question in Emacs lands in a
	// LOGBOOK drawer, which is neither the question's context nor one of
	// its alternatives.
	t.Run("a LOGBOOK note", func(t *testing.T) {
		q := byID(t, readOrg(t, "emacs-note.org"), "Q1")
		if body := strings.Join(q.Body, "\n"); body != "Nothing reads it.\n- keep it" {
			t.Errorf("body %q", body)
		}
		if notes := strings.Join(q.Notes, "\n"); !strings.Contains(notes, `- State "DECIDED"    from "ASK"`) ||
			!strings.Contains(notes, "  The new flag covers it.") {
			t.Errorf("notes %q", notes)
		}
		if q.Keyword != "DECIDED" || !q.Done {
			t.Errorf("Q1 %+v", q)
		}
	})
}
