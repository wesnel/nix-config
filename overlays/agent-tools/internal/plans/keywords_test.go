package plans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The time at which Org wrote emacs-closed.org.
var orgClosed = time.Date(2026, 10, 14, 16, 29, 0, 0, time.Local)

// A step's or part's heading keyword follows its log, as log writes it, in
// the form Org itself writes.
func TestStepKeywords(t *testing.T) {
	// logged makes a plan from open.org and logs what Org's fixture shows:
	// S1 and P1 done, S2 verified, S3 abandoned.
	logged := func(t *testing.T) Plan {
		t.Helper()
		p := fromFixture(t, "open.org")
		for _, e := range []Event{{Kind: "done", Item: "S1"}, {Kind: "done", Item: "S2"}, {Kind: "verified", Item: "S2"},
			{Kind: "abandoned", Item: "S3"}, {Kind: "done", Item: "P1"}} {
			if _, err := p.Log(e, orgClosed); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}

	t.Run("as Org writes them", func(t *testing.T) {
		p := logged(t)
		got, _ := os.ReadFile(p.file(""))
		want, _ := os.ReadFile("testdata/emacs-closed.org")
		// F1, a follow-up, is no step: its keyword is open.org's.
		wanted := strings.Replace(string(want), "*** DONE Guard accepts --draft=false\nCLOSED: [2026-10-14 Wed 16:29]\n",
			"*** TODO Guard accepts --draft=false\n", 1)
		if string(got) != wanted {
			t.Errorf("differs from Org's own output:\n%s\nwant:\n%s", got, wanted)
		}
	})

	// A keyword already right is not written again.
	t.Run("already right", func(t *testing.T) {
		p := logged(t)
		old := time.Now().Add(-time.Hour).Truncate(time.Second)
		os.Chtimes(p.file(""), old, old)
		if _, err := p.Log(Event{Kind: "reviewed", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		if info, _ := os.Stat(p.file("")); !info.ModTime().Equal(old) {
			t.Errorf("the plan was rewritten")
		}
	})

	t.Run("reopened", func(t *testing.T) {
		p := logged(t)
		if _, err := p.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, p.file(""), "S1", 1); got != "*** STARTED Add the flag\nSCHEDULED: <2026-10-20 Tue>" {
			t.Errorf("reopened:\n%s", got)
		}
		data, _ := os.ReadFile(p.file(""))
		if !strings.Contains(string(data), "** Steps [2/4]\n") {
			t.Errorf("cookie:\n%s", data)
		}
	})

	// A part's step is in the part's file, and a heading written without a
	// keyword gets one.
	t.Run("a part's step without a keyword", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		addItems(t, p.file(""), "** Distribution", "P1")
		made, err := c.New(NewOptions{Title: "Part", Parent: p.file(""), Part: "P1"}, day)
		if err != nil {
			t.Fatal(err)
		}
		insertUnder(t, made.File, "** Steps", "*** Bare step\n:PROPERTIES:\n:ID: P1.S1\n:END:\n")
		if _, err := p.Log(Event{Kind: "started", Item: "P1.S1"}, day); err != nil {
			t.Fatal(err)
		}
		if got := linesAt(t, made.File, "P1.S1", 0); got != "*** STARTED Bare step" {
			t.Errorf("heading %q", got)
		}
	})

	// Emacs has unsaved changes to the file: the event is still logged, and
	// status shows the keyword the file lacks.
	t.Run("a file Emacs is editing", func(t *testing.T) {
		p := fromFixture(t, "open.org")
		os.Symlink("wgn@box.1:1", filepath.Join(p.Dir, ".#"+filepath.Base(p.file(""))))
		e, err := p.Log(Event{Kind: "done", Item: "S1"}, day)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(e.Note, "Emacs") {
			t.Errorf("note %q", e.Note)
		}
		if events, _ := p.Events(); len(events) != 1 {
			t.Errorf("events %+v", events)
		}
		s, err := p.Status()
		if err != nil {
			t.Fatal(err)
		}
		var s1 *Drift
		for i := range s.KeywordDrift {
			if s.KeywordDrift[i].ID == "S1" {
				s1 = &s.KeywordDrift[i]
			}
		}
		if s1 == nil || s1.File != "TODO" || s1.Log != "DONE" {
			t.Errorf("drift %+v", s.KeywordDrift)
		}
		if !strings.Contains(s.Org(), "- Keyword drift: S1 (file TODO, log DONE)") {
			t.Errorf("org:\n%s", s.Org())
		}
	})
}
