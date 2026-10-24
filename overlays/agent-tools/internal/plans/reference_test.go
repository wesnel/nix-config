package plans

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// References the operator gives a session stay in its plan, with what each was
// used for.
func TestReference(t *testing.T) {
	const guide = "https://github.com/cursor/plugins/blob/main/pstack/docs/guide/08-principles.md"

	t.Run("added, then noted", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		e, err := p.Reference(guide, "", day)
		if err != nil {
			t.Fatal(err)
		}
		if e.Kind != "referenced" || e.Item != p.Name || e.Summary != guide {
			t.Errorf("event %+v", e)
		}
		if _, err := p.Reference(guide, "Principles for the design review; fed Q3 and Q4.", day); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(p.file(""))
		want := "** References\n*** " + guide + "\n:PROPERTIES:\n:URL: " + guide + "\n:END:\n" +
			"Principles for the design review; fed Q3 and Q4.\n** Related\n** Verification\n"
		if !strings.HasSuffix(string(data), want) {
			t.Errorf("plan:\n%s", data)
		}
		events := eventsBut(t, p, "property")
		if len(events) != 2 || events[1].Kind != "referenced" {
			t.Errorf("events %+v", events)
		}
	})

	// The same reference again, with no note, changes nothing.
	t.Run("again without a note", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		if _, err := p.Reference(guide, "Used for the review.", day); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(p.file(""))
		e, err := p.Reference(guide, "", day)
		if err != nil || e.Kind != "" {
			t.Errorf("again: %+v, %v", e, err)
		}
		if after, _ := os.ReadFile(p.file("")); string(after) != string(before) {
			t.Errorf("plan changed:\n%s", after)
		}
		if events := eventsBut(t, p, "property"); len(events) != 1 {
			t.Errorf("events %+v", events)
		}
	})

	for what, c := range map[string][2]string{
		"not a URL":            {"pstack principles", ""},
		"a URL with a space":   {"https://example.com/a b", ""},
		"a note with newlines": {guide, "x\n* forged"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			p := newPlan(t, testConfig(t), "x")
			_, err := p.Reference(c[0], c[1], day)
			isRefusal(t, what, err)
		})
	}

	t.Run("refused/another host's plan", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		_, err := otherHosts(t, p).Reference(guide, "", day)
		isRefusal(t, "another host's plan", err)
	})
}

// Every http(s) URL in the operator's own prompt is kept as a reference of
// the session's plan, quietly; a prompt that carries tool or agent output
// is not theirs.
func TestPromptReferences(t *testing.T) {
	prompt := func(text string) []byte {
		data, _ := json.Marshal(map[string]any{"session_id": "abc", "transcript_path": "/h/.claude/projects/p/abc.jsonl",
			"cwd": "/nowhere", "permission_mode": "default", "hook_event_name": "UserPromptSubmit", "prompt": text})
		return data
	}
	references := func(t *testing.T, p Plan) []string {
		t.Helper()
		var out []string
		data, _ := os.ReadFile(p.file(""))
		for _, line := range strings.Split(string(data), "\n") {
			if url, ok := strings.CutPrefix(line, ":URL: "); ok {
				out = append(out, url)
			}
		}
		return out
	}

	t.Run("the operator's prompt", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		out := c.Remind(prompt("Read https://github.com/cursor/plugins/blob/main/pstack/docs/guide/08-principles.md " +
			"and its 24 principle skills (https://github.com/cursor/plugins/tree/main/pstack/skills), then grill me on " +
			"which belong in our skills. See also http://example.com/a?b=1&c=2. Same guide again: " +
			"https://github.com/cursor/plugins/blob/main/pstack/docs/guide/08-principles.md"))
		if len(out) != 0 {
			t.Errorf("printed %s", out)
		}
		want := "https://github.com/cursor/plugins/blob/main/pstack/docs/guide/08-principles.md " +
			"https://github.com/cursor/plugins/tree/main/pstack/skills http://example.com/a?b=1&c=2"
		if got := strings.Join(references(t, p), " "); got != want {
			t.Errorf("references %q, want %q", got, want)
		}
	})

	// As Claude Code 2.1.300 sent a background sub-agent's result.
	t.Run("a sub-agent's result", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		c.Remind(prompt("<task-notification>\n<task-id>a1</task-id>\n<status>completed</status>\n" +
			"<summary>Agent \"Look it up\" finished</summary>\n<result>See https://example.com/found.</result>\n</task-notification>"))
		if refs := references(t, p); len(refs) != 0 {
			t.Errorf("references %v", refs)
		}
	})

	// Codex's UserPromptSubmit input, as its schema has it; a sub-agent's
	// turn carries agent_id and is not the operator's.
	codex := func(text string, agent bool) []byte {
		fields := map[string]any{"session_id": "abc", "transcript_path": "/h/.codex/sessions/2026/10/14/rollout-abc.jsonl",
			"cwd": "/nowhere", "model": "gpt-5.5", "permission_mode": "default", "hook_event_name": "UserPromptSubmit",
			"prompt": text, "turn_id": "t1"}
		if agent {
			fields["agent_id"], fields["agent_type"] = "a1", "worker"
		}
		data, _ := json.Marshal(fields)
		return data
	}
	for name, c2 := range map[string]struct {
		input []byte
		want  int
	}{
		"the operator's prompt in Codex": {codex("Read https://example.com/x", false), 1},
		"a Codex sub-agent's turn":       {codex("Read https://example.com/x", true), 0},
	} {
		t.Run(name, func(t *testing.T) {
			c := testConfig(t)
			p := newPlan(t, c, "x")
			if out := c.Remind(c2.input); len(out) != 0 {
				t.Errorf("printed %s", out)
			}
			if refs := references(t, p); len(refs) != c2.want {
				t.Errorf("references %v, want %d", refs, c2.want)
			}
		})
	}

	t.Run("a session with no plan", func(t *testing.T) {
		c := testConfig(t)
		newPlan(t, c, "x")
		data, _ := json.Marshal(map[string]any{"session_id": "zzz", "hook_event_name": "UserPromptSubmit",
			"prompt": "Read https://example.com/x"})
		if out := c.Remind(data); len(out) != 0 {
			t.Errorf("printed %s", out)
		}
	})
}

// A note is text like any other: no line of it is Org structure, and an
// entry is found by its own drawer's URL, never by a line of a note.
func TestReferenceNote(t *testing.T) {
	const d, e = "https://example.com/d", "https://example.com/e"
	for what, note := range map[string]string{
		"a keyword line":    "#+TODO: | ASK",
		"a URL line":        ":URL: " + e,
		"an ID line":        ":ID: S1",
		"a drawer":          ":PROPERTIES:",
		"a heading":         "fine\n*** forged",
		"a keyword further": "fine\n#+STARTUP: overview",
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			_, p := entryPlan(t)
			if _, err := p.Reference(d, "", day); err != nil {
				t.Fatal(err)
			}
			before := fileText(t, p.file(""))
			_, err := p.Reference(d, note, day)
			isRefusal(t, what, err)
			if after := fileText(t, p.file("")); after != before {
				t.Errorf("%s: the plan changed:\n%s", what, after)
			}
			if qs, _ := p.Questions(false); len(qs) != 1 {
				t.Errorf("%s: questions %+v", what, qs)
			}
		})
	}

	t.Run("a URL line in another entry's note", func(t *testing.T) {
		_, p := entryPlan(t)
		for _, u := range []string{d, e} {
			if _, err := p.Reference(u, "", day); err != nil {
				t.Fatal(err)
			}
		}
		insertUnder(t, p.file(""), ":URL: "+d+"\n:END:", "See also\n:URL: "+e+"\n")
		if _, err := p.Reference(e, "The e notes.", day); err != nil {
			t.Fatal(err)
		}
		plan := fileText(t, p.file(""))
		if !strings.Contains(plan, ":URL: "+d+"\n:END:\nSee also\n:URL: "+e+"\n*** "+e+"\n") ||
			!strings.Contains(plan, "*** "+e+"\n:PROPERTIES:\n:URL: "+e+"\n:END:\nThe e notes.\n") {
			t.Errorf("plan:\n%s", plan)
		}
	})

	t.Run("a note on two lines", func(t *testing.T) {
		_, p := entryPlan(t)
		if _, err := p.Reference(d, "The d notes;\nfed Q1.", day); err != nil {
			t.Fatal(err)
		}
		if plan := fileText(t, p.file("")); !strings.Contains(plan, ":URL: "+d+"\n:END:\nThe d notes;\nfed Q1.\n") {
			t.Errorf("plan:\n%s", plan)
		}
	})
}
