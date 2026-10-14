package plans

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// otherHosts opens, as this host, a plan of another host's beside p.
func otherHosts(t *testing.T, p Plan) Plan {
	t.Helper()
	theirs := newPlan(t, Config{Dir: p.Dir, Host: "other"}, "y")
	other, _ := Config{Dir: p.Dir, Host: "box"}.Open(theirs.file(""))
	return other
}

func TestDecide(t *testing.T) {
	// A decided question leaves Open questions for Decisions, as the next
	// decision, with its answer, and the log records it.
	t.Run("question", func(t *testing.T) {
		_, p := openQuestions(t)
		got, err := p.Decide("Q1", "release", day)
		if err != nil {
			t.Fatal(err)
		}
		if got.Decision != "D2" || got.Question != "Q1" {
			t.Errorf("decided %+v", got)
		}
		data, _ := os.ReadFile(p.file(""))
		text := string(data)
		decisions := text[strings.Index(text, "** Decisions\n"):strings.Index(text, "** Steps\n")]
		open := text[strings.Index(text, "** Open questions\n"):]
		want := "*** Which branch is the base?\n:PROPERTIES:\n:ID: D2\n:QUESTION: Q1\n:RECOMMENDATION: main\n:ANSWER: release\n:END:\n" +
			"The release branch is frozen.\n- release\n- a branch of its own\n"
		if !strings.Contains(decisions, "*** Keep the flag\n") || !strings.HasSuffix(decisions, want) {
			t.Errorf("Decisions:\n%s", decisions)
		}
		if strings.Contains(open, "Q1") || !strings.Contains(open, ":ID: Q2\n") {
			t.Errorf("Open questions:\n%s", open)
		}
		events, _ := p.Events()
		if len(events) != 1 || events[0].Kind != "decided" || events[0].Item != "D2" || events[0].Summary != "Q1: release" {
			t.Errorf("events %+v", events)
		}
	})

	t.Run("part's question", func(t *testing.T) {
		_, p := openQuestions(t)
		part, err := p.Decide("P1.Q1", "yes", day)
		if err != nil || part.Decision != "P1.D1" {
			t.Errorf("a part's question: %+v, %v", part, err)
		}
	})

	t.Run("left open", func(t *testing.T) {
		_, p := openQuestions(t)
		for _, id := range []string{"Q1", "P1.Q1"} {
			if _, err := p.Decide(id, "yes", day); err != nil {
				t.Fatal(err)
			}
		}
		if qs, _ := p.Questions(true); len(qs) != 1 || qs[0].ID != "Q2" {
			t.Errorf("left open: %+v", qs)
		}
	})

	for _, c := range []struct {
		what, id, answer string
		// decidedQ1 is whether Q1 is decided first.
		decidedQ1 bool
	}{
		{"an unknown question", "Q9", "x", false},
		{"a decided one", "Q1", "x", true},
		{"an answer with newlines", "Q2", "x\n* forged", false},
		{"an empty answer", "Q2", " ", false},
	} {
		t.Run("refused/"+c.what, func(t *testing.T) {
			_, p := openQuestions(t)
			if c.decidedQ1 {
				if _, err := p.Decide("Q1", "release", day); err != nil {
					t.Fatal(err)
				}
			}
			_, err := p.Decide(c.id, c.answer, day)
			isRefusal(t, c.what, err)
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		_, p := openQuestions(t)
		_, err := otherHosts(t, p).Decide("Q1", "x", day)
		isRefusal(t, "another host's plan", err)
	})
}

// The status line names the session's open questions, and nothing else.
func TestStatusLine(t *testing.T) {
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
		insertUnder(t, made.File, "** Open questions", question("Q1", "Keep it?", ""))
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
