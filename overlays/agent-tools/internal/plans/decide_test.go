package plans

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// otherHosts opens, as this host, a plan of another host's beside p.
func otherHosts(t *testing.T, p Plan) Plan {
	t.Helper()
	theirs := newPlan(t, Config{Dir: p.Dir, Host: "other"}, "y")
	other, _ := Config{Dir: p.Dir, Host: "box"}.Open(theirs.file(""))
	return other
}

// fromFixture makes a plan whose file is the fixture name, which is
// testdata/open.org or Org's output from it, both of plan 2026-10-14-x.box.
func fromFixture(t *testing.T, name string) Plan {
	t.Helper()
	p := newPlan(t, testConfig(t), "x")
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.file(""), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// lines is the file's lines from the heading with ID id: the heading and
// the n lines after it.
func linesAt(t *testing.T, file, id string, n int) string {
	t.Helper()
	data, _ := os.ReadFile(file)
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if l == ":ID: "+id {
			for i >= 0 && level(lines[i]) == 0 {
				i--
			}
			return strings.Join(lines[i:min(i+1+n, len(lines))], "\n")
		}
	}
	t.Fatalf("no %s in %s", id, file)
	return ""
}

// The time at which Org wrote the emacs-archive fixtures.
var orgArchived = time.Date(2026, 10, 14, 16, 33, 0, 0, time.Local)

func TestDecide(t *testing.T) {
	// A decided question stays where it is, settled as Org settles it, with
	// the operator's answer, and the log records it.
	t.Run("in place", func(t *testing.T) {
		p := fromFixture(t, "open.org")
		got, err := p.Decide("Q1", DecideOptions{Answer: "drop it"}, orgArchived)
		if err != nil {
			t.Fatal(err)
		}
		if got.Question != "Q1" || got.State != "DECIDED" || got.File != p.file("") {
			t.Errorf("decided %+v", got)
		}
		if got, want := linesAt(t, p.file(""), "Q1", 1), linesAt(t, "testdata/emacs-archive-decided.org", "Q1", 1); got != want {
			t.Errorf("heading:\n%s\nwant Org's:\n%s", got, want)
		}
		if entry := linesAt(t, p.file(""), "Q1", 7); !strings.Contains(entry, ":RECOMMENDATION: no, drop it\n:ANSWER: drop it\n:END:\nNothing reads it.") {
			t.Errorf("entry:\n%s", entry)
		}
		events := eventsBut(t, p, "property")
		if len(events) != 1 || events[0].Kind != "decided" || events[0].Item != "Q1" || events[0].Summary != "drop it" {
			t.Errorf("events %+v", events)
		}
		if qs, _ := p.Questions(true); len(qs) != 0 {
			t.Errorf("still open: %+v", qs)
		}
	})

	t.Run("part's question", func(t *testing.T) {
		_, p := openQuestions(t)
		got, err := p.Decide("P1.Q1", DecideOptions{Answer: "yes"}, day)
		if err != nil || got.File != p.file("P1") {
			t.Errorf("a part's question: %+v, %v", got, err)
		}
		if qs, _ := p.Questions(true); ids(qs) != "Q1 Q2" {
			t.Errorf("left open: %s", ids(qs))
		}
	})

	t.Run("dropped", func(t *testing.T) {
		_, p := openQuestions(t)
		got, err := p.Decide("Q2", DecideOptions{Answer: "moot after Q1", Drop: true}, day)
		if err != nil || got.State != "DROPPED" {
			t.Fatalf("dropped %+v, %v", got, err)
		}
		if entry := linesAt(t, p.file(""), "Q2", 1); entry != "*** DROPPED Rename the command? :ARCHIVE:\nCLOSED: [2026-10-14 Wed 13:05]" {
			t.Errorf("entry:\n%s", entry)
		}
		if qs, _ := p.Questions(false); ids(qs) != "Q1" {
			t.Errorf("left open: %s", ids(qs))
		}
	})

	// A new decision replaces a settled one, which Org then shows as
	// SUPERSEDED, keeping when it was closed.
	t.Run("superseding", func(t *testing.T) {
		p := fromFixture(t, "open.org")
		if _, err := p.Decide("Q1", DecideOptions{Answer: "drop it"}, orgArchived); err != nil {
			t.Fatal(err)
		}
		asked, err := p.Ask(AskOptions{Question: "Keep the old flag after all?", Recommendation: "yes"}, day)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Decide(asked.ID, DecideOptions{Answer: "yes", Supersedes: "Q1"}, day); err != nil {
			t.Fatal(err)
		}
		if got, want := linesAt(t, p.file(""), "Q1", 1), linesAt(t, "testdata/emacs-archive.org", "Q1", 1); got != want {
			t.Errorf("superseded:\n%s\nwant Org's:\n%s", got, want)
		}
		if entry := linesAt(t, p.file(""), asked.ID, 7); !strings.Contains(entry, ":ANSWER: yes\n:SUPERSEDES: Q1\n:END:") {
			t.Errorf("the new decision:\n%s", entry)
		}
		if ds, _ := p.Decisions(true); decisionIDs(ds) != asked.ID {
			t.Errorf("decisions %s", decisionIDs(ds))
		}
	})

	t.Run("refused/superseding an open question", func(t *testing.T) {
		_, p := openQuestions(t)
		before, _ := os.ReadFile(p.file(""))
		_, err := p.Decide("Q1", DecideOptions{Answer: "x", Supersedes: "Q2"}, day)
		isRefusal(t, "superseding an open question", err)
		if after, _ := os.ReadFile(p.file("")); string(after) != string(before) {
			t.Errorf("the plan changed:\n%s", after)
		}
	})

	for _, c := range []struct {
		what, id string
		o        DecideOptions
	}{
		{"an unknown question", "Q9", DecideOptions{Answer: "x"}},
		{"a decided one", "Q3", DecideOptions{Answer: "x"}},
		{"a step", "P1", DecideOptions{Answer: "x"}},
		{"an answer with newlines", "Q2", DecideOptions{Answer: "x\n* forged"}},
		{"an empty answer", "Q2", DecideOptions{Answer: " "}},
		{"superseding an unknown question", "Q2", DecideOptions{Answer: "x", Supersedes: "Q9"}},
		{"dropping and superseding", "Q2", DecideOptions{Answer: "x", Drop: true, Supersedes: "Q3"}},
	} {
		t.Run("refused/"+c.what, func(t *testing.T) {
			_, p := openQuestions(t)
			_, err := p.Decide(c.id, c.o, day)
			isRefusal(t, c.what, err)
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		_, p := openQuestions(t)
		_, err := otherHosts(t, p).Decide("Q1", DecideOptions{Answer: "x"}, day)
		isRefusal(t, "another host's plan", err)
	})
}

// The status line names the session's open questions, and a review the
// session owes, and nothing else.
func TestStatusLine(t *testing.T) {
	// A session owes a review from its commit until the next review
	// receipt; another session's commits are not its debt.
	t.Run("review owed", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		line := func() string {
			t.Helper()
			return c.StatusLine([]byte(`{"session_id":"abc","cwd":"/nowhere"}`))
		}
		steps := []struct {
			name string
			step func()
			want string
		}{
			{"nothing committed", func() {}, ""},
			{"committed", func() {
				p.Record("", Event{Kind: "committed", Commit: "Add x", Session: "abc"}, day)
			}, "review owed"},
			{"reviewed", func() {
				p.Receipt("", Receipt{Range: "origin/main..HEAD", Tip: "1234", PatchID: "abcd", Reviewer: "codex"}, day)
			}, ""},
			{"another session committed", func() {
				p.Record("", Event{Kind: "committed", Commit: "Add y", Session: "zzz"}, day)
			}, ""},
			{"committed again", func() {
				p.Record("", Event{Kind: "committed", Commit: "Add z", Session: "abc"}, day)
				insertUnder(t, p.File(), "** Questions", question("Q1", "Keep it?", ""))
			}, "review owed · 1 open question · /questions"},
		}
		for _, s := range steps {
			s.step()
			if got := line(); got != s.want {
				t.Errorf("%s: status line %q, want %q", s.name, got, s.want)
			}
		}
	})

	// A route the log names is owed until a route receipt of it follows,
	// or a waiver; the line reads the log alone.
	t.Run("route review owed", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		line := func() string {
			t.Helper()
			return c.StatusLine([]byte(`{"session_id":"abc","cwd":"/nowhere"}`))
		}
		steps := []struct {
			name string
			step func()
			want string
		}{
			{"routed", func() {
				p.Routed("", Routed{Branch: "loop", Base: "a", Tip: "b", Routes: map[string][]string{"security": {"b"}}}, day)
			}, "security review owed"},
			{"a general receipt", func() {
				p.Receipt("", Receipt{Range: "origin/main..HEAD", Tip: "1234", PatchID: "abcd", Reviewer: "codex"}, day)
			}, "security review owed"},
			{"a route receipt", func() {
				p.Receipt("", Receipt{Range: "origin/main..HEAD", Tip: "1234", PatchID: "abcd", Focus: "security"}, day)
			}, ""},
			{"routed again", func() {
				p.Routed("", Routed{Branch: "loop", Base: "a", Tip: "c", Routes: map[string][]string{"security": {"c"}}}, day)
			}, "security review owed"},
			{"waived", func() { p.Waive("security", "comments only", day) }, ""},
		}
		for _, s := range steps {
			s.step()
			if got := line(); got != s.want {
				t.Errorf("%s: status line %q, want %q", s.name, got, s.want)
			}
		}
	})

	t.Run("the session's open questions", func(t *testing.T) {
		c, _ := openQuestions(t)
		if got := c.StatusLine([]byte(`{"session_id":"abc","cwd":"/nowhere","workspace":{"current_dir":"/nowhere"}}`)); got != "3 open questions · /questions" {
			t.Errorf("status line %q", got)
		}
	})

	for name, in := range map[string]string{
		"another session": `{"session_id":"zzz","cwd":"/nowhere"}`,
		"not JSON":        `not json`,
		"no input":        ``,
	} {
		t.Run(name, func(t *testing.T) {
			c, _ := openQuestions(t)
			if got := c.StatusLine([]byte(in)); got != "" {
				t.Errorf("%q: status line %q", in, got)
			}
		})
	}

	// Emacs's mode line sends only the directory of the buffer's file.
	t.Run("a worktree's plan, given only a directory in it", func(t *testing.T) {
		c := testConfig(t)
		top, _ := filepath.EvalSymlinks(t.TempDir())
		gitIn(t, top, "init", "-q", "-b", "main")
		sub := filepath.Join(top, "lisp")
		os.MkdirAll(sub, 0o755)
		made, err := c.New(NewOptions{Title: "T", Slug: "x", Worktrees: []string{top}}, day)
		if err != nil {
			t.Fatal(err)
		}
		insertUnder(t, made.File, "** Questions", question("Q1", "Keep it?", ""))
		in, _ := json.Marshal(map[string]string{"cwd": sub})
		if got := c.StatusLine(in); got != "1 open question · /questions" {
			t.Errorf("status line %q", got)
		}
		elsewhere, _ := json.Marshal(map[string]string{"cwd": t.TempDir()})
		if got := c.StatusLine(elsewhere); got != "" {
			t.Errorf("status line outside the worktree %q", got)
		}
	})

	t.Run("after", testStatusLineAfter)
}
