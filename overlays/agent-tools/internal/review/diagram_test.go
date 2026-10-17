package review

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/diagram"
	"github.com/wesnel/agent-tools/internal/plans"
)

const queueSource = `package queue

type State string

const (
	Queued State = "queued"
	Ready  State = "ready"
)

type Entry struct{ State State }

func Run(e *Entry) {
	if review(e) == 0 {
		e.State = Ready
	}
}

func review(e *Entry) int { return 0 }
`

// diagramFixture is focusFixture's repository, with or without its plan,
// and a commit adding queue.go on top, and a state diagram of it, as edit
// changes it, written beside the repository.
func diagramFixture(t *testing.T, withPlan bool, edit func(m map[string]any)) (*fixture, plans.Plan, string) {
	t.Helper()
	return drawnFixture(t, withPlan, "claude", edit)
}

// drawnFixture is diagramFixture, the diagram recorded as drawn by family
// where there is a plan.
func drawnFixture(t *testing.T, withPlan bool, family string, edit func(m map[string]any)) (*fixture, plans.Plan, string) {
	t.Helper()
	var f *fixture
	var p plans.Plan
	if withPlan {
		f, p = focusFixture(t)
	} else {
		f = setUp(t, true)
	}
	os.WriteFile(filepath.Join(f.repo, "queue.go"), []byte(queueSource), 0o644)
	run(t, f.repo, "git", "add", "queue.go")
	run(t, f.repo, "git", "commit", "-q", "-m", "Add the queue")
	f.tip = run(t, f.repo, "git", "rev-parse", "HEAD")
	var m map[string]any
	json.Unmarshal([]byte(`{"kind": "state", "title": "Entry states", "at": "`+f.tip+`",
 "nodes": [
  {"id": "N1", "label": "Queued", "cite": {"path": "queue.go", "line": 6, "endLine": null, "symbol": "Queued"}, "planned": null},
  {"id": "N2", "label": "Ready", "cite": {"path": "queue.go", "line": 7, "endLine": null, "symbol": "Ready"}, "planned": null}],
 "edges": [
  {"id": "E1", "from": "N1", "to": "N2", "label": "review exit 0",
   "cite": {"path": "queue.go", "line": 14, "endLine": null, "symbol": "Ready"}, "planned": null}]}`), &m)
	if edit != nil {
		edit(m)
	}
	path := filepath.Join(f.repo, ".git", "diagram.json")
	os.WriteFile(path, must(json.Marshal(m)), 0o644)
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SANDBOX"} {
		t.Setenv(v, "")
	}
	if withPlan {
		drawnBy(t, f, p, path, family)
	}
	return f, p, path
}

// drawnBy records family as the drafter of the diagram at path, as
// `agent-plan diagram check` does for the session that drew it.
func drawnBy(t *testing.T, f *fixture, p plans.Plan, path, family string) {
	t.Helper()
	if h := hashOf(t, f, path); h != "" {
		if _, err := p.RecordDrafter(h, family, "s0", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

// hashOf is the hash of the diagram at path once checked, or "" for one
// that does not parse.
func hashOf(t *testing.T, f *fixture, path string) string {
	t.Helper()
	d, err := diagram.Parse(must(os.ReadFile(path)))
	if err != nil {
		return ""
	}
	diagram.Check(d, func(p string) ([]byte, error) {
		out, err := exec.Command("git", "-C", f.repo, "show", f.tip+":"+p).Output()
		return out, err
	}, nil)
	return d.Hash()
}

type critique struct {
	Reviewer string           `json:"reviewer"`
	Diagram  string           `json:"diagram"`
	Focus    string           `json:"focus"`
	Findings []map[string]any `json:"findings"`
	Dropped  []map[string]any `json:"dropped"`
}

func critiqueRun(t *testing.T, args ...string) (int, critique, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := Main(t.Context(), append([]string{"diagram"}, args...), strings.NewReader(""), &stdout, &stderr)
	var out critique
	if stdout.Len() > 0 {
		if err := json.Unmarshal([]byte(stdout.String()), &out); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return code, out, stderr.String()
}

const allChecked = `["N1", "N2", "E1"]`

// Codex critiques a diagram another family drew, confined as a review,
// with the reviewer's rules, the diagram's checks, every element to name as
// checked and the mechanical check's results; the critique logs
// diagram-critiqued, never a receipt.
func TestDiagramCodex(t *testing.T) {
	t.Run("holds", func(t *testing.T) {
		f, p, path := diagramFixture(t, true, nil)
		t.Setenv("FAKE_CODEX", "reply")
		t.Setenv("FAKE_REPLY", `{"findings": [], "unverified": [], "checked": `+allChecked+`}`)
		code, out, stderr := critiqueRun(t, "--diagram", path, f.base+"..HEAD")
		if code != 0 || out.Reviewer != "codex" || out.Focus != "diagram" || out.Diagram != hashOf(t, f, path) {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		prompt := string(must(os.ReadFile(filepath.Join(f.log, "prompt"))))
		for _, want := range []string{generalRules(), "## Checklist", `"label": "review exit 0"`,
			"value Queued declared in queue.go:6-6", "assigns Ready at queue.go:14-14"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("the prompt lacks %q:\n%s", want, prompt)
			}
		}
		if strings.Contains(prompt, "## Routes") {
			t.Error("the prompt asks a critic for routes")
		}
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]json.RawMessage
		}
		json.Unmarshal(must(os.ReadFile(filepath.Join(f.log, "schema"))), &schema)
		if _, routed := schema.Properties["routes"]; routed || !strings.Contains(string(schema.Properties["checked"]), `"E1"`) ||
			!strings.Contains(string(schema.Properties["findings"]), `"enum":["untrue-doc"]`) {
			t.Errorf("schema %s", must(os.ReadFile(filepath.Join(f.log, "schema"))))
		}
		got := eventsOf(t, p, "diagram-critiqued")
		if len(got) != 1 || got[0].Diagram != out.Diagram || got[0].Drafter != "claude" || got[0].Critic != "codex" ||
			got[0].Findings != "" || got[0].Tip != f.tip || got[0].Model != "fake-model" {
			t.Errorf("critiqued %+v", got)
		}
		for _, kind := range []string{"review-receipt", "route-receipt", "routed"} {
			if len(eventsOf(t, p, kind)) != 0 {
				t.Errorf("a critique logged %s", kind)
			}
		}
		f.checkCleanedUp(t)
	})

	t.Run("findings", func(t *testing.T) {
		f, p, path := diagramFixture(t, true, nil)
		t.Setenv("FAKE_CODEX", "reply")
		t.Setenv("FAKE_REPLY", `{"findings": [
 {"id": "1", "path": "queue.go", "line": 14, "endLine": null, "kind": "untrue-doc", "claim": "c", "evidence": "e", "about": "E1", "reopens": null},
 {"id": "2", "path": "queue.go", "line": 13, "endLine": null, "kind": "untrue-doc", "claim": "c", "evidence": "e", "about": null, "reopens": null},
 {"id": "3", "path": "queue.go", "line": 13, "endLine": null, "kind": "bug", "claim": "c", "evidence": "e", "about": "E1", "reopens": null},
 {"id": "4", "path": "queue.go", "line": 13, "endLine": null, "kind": "untrue-doc", "claim": "c", "evidence": "e", "about": "N9", "reopens": null}
], "unverified": [], "checked": `+allChecked+`}`)
		code, out, stderr := critiqueRun(t, "--diagram", path, f.base+"..HEAD")
		if code != 3 || len(out.Findings) != 2 || out.Findings[0]["id"] != "diagram-1" || len(out.Dropped) != 2 {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		if got := eventsOf(t, p, "diagram-critiqued"); len(got) != 1 || got[0].Findings != "diagram-1 diagram-2" {
			t.Errorf("critiqued %+v", got)
		}
	})

	t.Run("an element not checked", func(t *testing.T) {
		f, p, path := diagramFixture(t, true, nil)
		t.Setenv("FAKE_CODEX", "reply")
		t.Setenv("FAKE_REPLY", `{"findings": [], "unverified": [], "checked": ["N1", "N2"]}`)
		code, _, stderr := critiqueRun(t, "--diagram", path, f.base+"..HEAD")
		if code != 1 || !strings.Contains(stderr, "E1") || len(eventsOf(t, p, "diagram-critiqued")) != 0 {
			t.Errorf("exit %d\n%s", code, stderr)
		}
	})

	// Codex drew it, so Codex may not critique it.
	t.Run("drawn by Codex", func(t *testing.T) {
		f, _, path := drawnFixture(t, true, "codex", nil)
		code, _, stderr := critiqueRun(t, "--diagram", path, f.base+"..HEAD")
		if code != 4 || !strings.Contains(stderr, "--print-prompt") {
			t.Errorf("exit %d\n%s", code, stderr)
		}
		if _, err := os.Stat(filepath.Join(f.log, "prompt")); err == nil {
			t.Error("Codex was asked")
		}
	})

	// Who drew it is what agent-plan recorded for the session that did,
	// never what the caller says.
	t.Run("no drafter recorded", func(t *testing.T) {
		f, p, path := diagramFixture(t, false, nil)
		_ = p
		code, _, stderr := critiqueRun(t, "--diagram", path, f.base+"..HEAD")
		refused(t, "no drafter", code, stderr)
		if !strings.Contains(stderr, "agent-plan diagram check") {
			t.Errorf("%s", stderr)
		}
	})
	t.Run("a drafter given as a flag", func(t *testing.T) {
		f, _, path := diagramFixture(t, true, nil)
		if code, _, _ := critiqueRun(t, "--drafter", "codex", "--diagram", path, f.base+"..HEAD"); code != 2 {
			t.Errorf("exit %d", code)
		}
	})
}

// A diagram that fails its schema or its mechanical check goes back to the
// drafter before any critic is asked.
func TestDiagramRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(m map[string]any)
		says string
	}{
		"a citation to the wrong span": {func(m map[string]any) {
			m["nodes"].([]any)[1].(map[string]any)["cite"].(map[string]any)["line"] = 6
		}, "N2"},
		"a transition that assigns nothing": {func(m map[string]any) {
			m["edges"].([]any)[0].(map[string]any)["cite"].(map[string]any)["line"] = 7
		}, "E1"},
		"a field the schema lacks": {func(m map[string]any) { m["click"] = "N1 call alert()" }, "click"},
		"another commit":           {func(m map[string]any) { m["at"] = strings.Repeat("a", 40) }, "aaaaaaaa"},
		"a planned element": {func(m map[string]any) {
			m["nodes"].([]any)[0].(map[string]any)["cite"], m["nodes"].([]any)[0].(map[string]any)["planned"] = nil, "P1.S9"
		}, "P1.S9"},
	} {
		t.Run(name, func(t *testing.T) {
			f, p, path := diagramFixture(t, true, tc.edit)
			t.Setenv("FAKE_CODEX", "reply")
			code, _, stderr := critiqueRun(t, "--diagram", path, f.base+"..HEAD")
			refused(t, name, code, stderr)
			if !strings.Contains(stderr, tc.says) {
				t.Errorf("the refusal does not name %q: %s", tc.says, stderr)
			}
			if _, err := os.Stat(filepath.Join(f.log, "prompt")); err == nil {
				t.Error("Codex was asked")
			}
			if len(eventsOf(t, p, "diagram-critiqued")) != 0 {
				t.Error("logged")
			}
		})
	}

	t.Run("through the relay", func(t *testing.T) {
		f, _, path := diagramFixture(t, true, nil)
		t.Setenv("SANDBOX_RELAY_URL", "http://127.0.0.1:1")
		code, _, stderr := critiqueRun(t, "--diagram", path, f.base+"..HEAD")
		refused(t, "relay", code, stderr)
	})
}

// printDiagram prints the critique's prompt and returns its nonce.
func printDiagram(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr strings.Builder
	if code := Main(t.Context(), append([]string{"diagram", "--print-prompt"}, args...), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	m := nonce.FindStringSubmatch(stdout.String())
	if m == nil {
		t.Fatalf("the prompt asks for no nonce:\n%s", stdout.String())
	}
	return m[1]
}

// A diagram Codex drew is critiqued by a Claude sub-agent: the prompt is
// issued for that diagram, and its reply is checked once, for it alone.
func TestDiagramCheck(t *testing.T) {
	// codexDrawn is a diagram Codex drew, critiqued from a Claude Code
	// session.
	codexDrawn := func(t *testing.T) (*fixture, plans.Plan, string) {
		f, p, path := drawnFixture(t, true, "codex", nil)
		t.Setenv("CLAUDE_CODE_SESSION_ID", "s1")
		return f, p, path
	}
	t.Run("issued and checked", func(t *testing.T) {
		f, p, path := codexDrawn(t)
		n := printDiagram(t, "--diagram", path, f.base+"..HEAD")
		issued := eventsOf(t, p, "review-issued")
		if len(issued) != 1 || issued[0].Focus != "diagram" || issued[0].Diagram != hashOf(t, f, path) || issued[0].Drafter != "codex" {
			t.Fatalf("issued %+v", issued)
		}
		answer := reply(t, f, `{"findings": [], "unverified": [], "checked": `+allChecked+`, "issued": "`+n+`"}`)
		code, out, stderr := critiqueRun(t, "--check", answer, "--diagram", path, f.base+"..HEAD")
		if code != 0 || out.Reviewer != "subagent" {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		got := eventsOf(t, p, "diagram-critiqued")
		if len(got) != 1 || got[0].Critic != "claude" || got[0].Drafter != "codex" || got[0].Nonce != n {
			t.Errorf("critiqued %+v", got)
		}
		code, _, stderr = critiqueRun(t, "--check", answer, "--diagram", path, f.base+"..HEAD")
		refused(t, "checked again", code, stderr)
	})

	t.Run("another diagram", func(t *testing.T) {
		f, p, path := codexDrawn(t)
		n := printDiagram(t, "--diagram", path, f.base+"..HEAD")
		var m map[string]any
		json.Unmarshal(must(os.ReadFile(path)), &m)
		m["title"] = "Other states"
		os.WriteFile(path, must(json.Marshal(m)), 0o644)
		drawnBy(t, f, p, path, "codex")
		answer := reply(t, f, `{"findings": [], "unverified": [], "checked": `+allChecked+`, "issued": "`+n+`"}`)
		code, _, stderr := critiqueRun(t, "--check", answer, "--diagram", path, f.base+"..HEAD")
		refused(t, "another diagram", code, stderr)
		if len(eventsOf(t, p, "diagram-critiqued")) != 0 {
			t.Error("logged")
		}
	})

	t.Run("no nonce", func(t *testing.T) {
		f, _, path := codexDrawn(t)
		answer := reply(t, f, `{"findings": [], "unverified": [], "checked": `+allChecked+`}`)
		code, _, stderr := critiqueRun(t, "--check", answer, "--diagram", path, f.base+"..HEAD")
		refused(t, "no nonce", code, stderr)
	})

	// A diagram's prompt is never a review's: the general check refuses
	// its reply, so it cannot stand as a review receipt.
	t.Run("checked as a review", func(t *testing.T) {
		f, p, path := codexDrawn(t)
		n := printDiagram(t, "--diagram", path, f.base+"..HEAD")
		answer := reply(t, f, `{"findings": [], "unverified": [], "checked": `+allChecked+`, "tip": "`+f.tip+`", "issued": "`+n+`"}`)
		code, _, stderr := freshReview(t, "--check", answer, f.base+"..HEAD")
		refused(t, "a review's check", code, stderr)
		if len(eventsOf(t, p, "review-receipt")) != 0 {
			t.Error("a diagram's reply was a review receipt")
		}
	})

	// Claude drew it, so a Claude sub-agent may not critique it.
	t.Run("drawn by Claude", func(t *testing.T) {
		f, _, path := diagramFixture(t, true, nil)
		t.Setenv("CLAUDE_CODE_SESSION_ID", "s1")
		var stdout, stderr strings.Builder
		code := Main(t.Context(), []string{"diagram", "--print-prompt", "--diagram", path, f.base + "..HEAD"}, nil, &stdout, &stderr)
		refused(t, "drawn by Claude", code, stderr.String())
	})

	// Codex drew it, and the session asking is Codex, whose sub-agent
	// would be Codex too.
	t.Run("asked from Codex", func(t *testing.T) {
		f, _, path := drawnFixture(t, true, "codex", nil)
		t.Setenv("CODEX_THREAD_ID", "t1")
		var stdout, stderr strings.Builder
		code := Main(t.Context(), []string{"diagram", "--print-prompt", "--diagram", path, f.base + "..HEAD"}, nil, &stdout, &stderr)
		refused(t, "asked from Codex", code, stderr.String())
	})

	t.Run("no plan", func(t *testing.T) {
		f, _, path := diagramFixture(t, false, nil)
		t.Setenv("CLAUDE_CODE_SESSION_ID", "s1")
		var stdout, stderr strings.Builder
		code := Main(t.Context(), []string{"diagram", "--print-prompt", "--diagram", path, f.base + "..HEAD"},
			nil, &stdout, &stderr)
		refused(t, "no plan", code, stderr.String())
	})
}

// At the plan's design review the change has no commits yet: its diagram
// cites the base, and plan items for what the plan adds.
func TestDiagramPlanned(t *testing.T) {
	f, p, path := diagramFixture(t, true, func(m map[string]any) {
		m["nodes"].([]any)[1].(map[string]any)["cite"] = nil
		m["nodes"].([]any)[1].(map[string]any)["planned"] = "S1"
		m["edges"].([]any)[0].(map[string]any)["cite"] = nil
		m["edges"].([]any)[0].(map[string]any)["planned"] = "S1"
	})
	addStep(t, p, "S1")
	t.Setenv("FAKE_CODEX", "reply")
	t.Setenv("FAKE_REPLY", `{"findings": [], "unverified": [], "checked": `+allChecked+`}`)
	code, out, stderr := critiqueRun(t, "--diagram", path, "HEAD..HEAD")
	if code != 0 || out.Diagram == "" {
		t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
	}
	if prompt := string(must(os.ReadFile(filepath.Join(f.log, "prompt")))); !strings.Contains(prompt, "planned as S1") {
		t.Errorf("prompt:\n%s", prompt)
	}
}

// addStep adds a step with id under the plan's Steps.
func addStep(t *testing.T, p plans.Plan, id string) {
	t.Helper()
	data := string(must(os.ReadFile(p.File())))
	data = strings.Replace(data, "** Steps\n", "** Steps\n*** TODO Add the state\n:PROPERTIES:\n:ID: "+id+"\n:END:\n", 1)
	if err := os.WriteFile(p.File(), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
