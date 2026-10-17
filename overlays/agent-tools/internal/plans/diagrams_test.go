package plans

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/diagram"
)

const statesGo = "package queue\n\nconst (\n\tQueued = \"queued\"\n\tReady  = \"ready\"\n)\n"

// checkedDiagram is a diagram of statesGo at commit at, with title and
// its first node's label as given, checked against statesGo.
func checkedDiagram(t *testing.T, at, title, label string) *diagram.Diagram {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"kind": "state", "title": title, "at": at,
		"nodes": []any{
			map[string]any{"id": "N1", "label": label, "cite": map[string]any{"path": "queue.go", "line": 4, "endLine": nil, "symbol": "Queued"}, "planned": nil},
			map[string]any{"id": "N2", "label": "Ready", "cite": map[string]any{"path": "queue.go", "line": 5, "endLine": nil, "symbol": "Ready"}, "planned": nil},
		},
		"edges": []any{}})
	d, err := diagram.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if r := diagram.Check(d, func(string) ([]byte, error) { return []byte(statesGo), nil }, nil); len(r.Failures) > 0 {
		t.Fatal(r.Failures)
	}
	return d
}

func critiqued(t *testing.T, p Plan, d *diagram.Diagram, findings string) {
	t.Helper()
	if _, err := p.Record("", Event{Kind: critiquedKind, Diagram: d.Hash(), Critic: "codex", Drafter: "claude",
		Findings: findings}, day); err != nil {
		t.Fatal(err)
	}
}

func eventsOfKind(t *testing.T, p Plan, kind string) []Event {
	t.Helper()
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range events {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

var anAt = strings.Repeat("ab", 20)

// A diagram is kept only once a critique of it found nothing, as an entry
// of the plan's Diagrams section that reads back as it was written.
func TestKeepDiagram(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "keep")
	d := checkedDiagram(t, anAt, "Entry states", "Queued")

	_, err := p.KeepDiagram(d, KeepOptions{}, day)
	isRefusal(t, "not critiqued", err)
	critiqued(t, p, d, "diagram-1")
	_, err = p.KeepDiagram(d, KeepOptions{}, day)
	isRefusal(t, "critiqued with findings", err)

	critiqued(t, p, d, "")
	kept, err := p.KeepDiagram(d, KeepOptions{}, day)
	if err != nil || kept.ID != "G1" || kept.Status != "checked" || kept.Hash != d.Hash() {
		t.Fatalf("%+v %v", kept, err)
	}
	if got := eventsOfKind(t, p, keptKind); len(got) != 1 || got[0].Diagram != d.Hash() || got[0].Critic != "codex" {
		t.Errorf("kept events %+v", got)
	}
	text := readText(t, p.File())
	for _, want := range []string{"** Diagrams\n*** G1 Entry states\n:PROPERTIES:\n:ID: G1\n:KIND: state\n:AT: " + anAt +
		"\n:STATUS: checked\n:CRITIC: codex\n:END:\n#+begin_src json :eval never\n{\n", "#+begin_src mermaid :eval never\nstateDiagram-v2\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("the plan lacks %q:\n%s", want, text)
		}
	}
	all, err := p.Diagrams()
	if err != nil || len(all) != 1 || all[0].Edited != "" || all[0].Diagram.Hash() != d.Hash() || all[0].Title != "Entry states" {
		t.Fatalf("%+v %v", all, err)
	}

	// Another is G2; a redraw replaces its entry in place.
	other := checkedDiagram(t, anAt, "Other states", "Waiting")
	critiqued(t, p, other, "")
	if kept, err := p.KeepDiagram(other, KeepOptions{}, day); err != nil || kept.ID != "G2" {
		t.Fatalf("%+v %v", kept, err)
	}
	redraw := checkedDiagram(t, anAt, "Entry states", "Queued up")
	critiqued(t, p, redraw, "")
	if kept, err := p.KeepDiagram(redraw, KeepOptions{Replace: "G1"}, day); err != nil || kept.ID != "G1" {
		t.Fatalf("%+v %v", kept, err)
	}
	all, _ = p.Diagrams()
	if len(all) != 2 || all[0].ID != "G1" || all[0].Diagram.Hash() != redraw.Hash() || all[1].ID != "G2" {
		t.Errorf("%+v", all)
	}
	_, err = p.KeepDiagram(redraw, KeepOptions{Replace: "G9"}, day)
	isRefusal(t, "replace one not there", err)

	// Only agent-plan logs these.
	for _, kind := range []string{keptKind, movedKind, staleKind, critiquedKind} {
		_, err := p.Log(Event{Kind: kind, Item: p.Name}, day)
		isRefusal(t, kind, err)
	}
}

// A disputed diagram is kept after two critiques with findings, with where
// they point and nothing of what they say.
func TestKeepDisputed(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "disputed")
	d := checkedDiagram(t, anAt, "Entry states", "Queued")
	critiqued(t, p, d, "diagram-1")
	_, err := p.KeepDiagram(d, KeepOptions{Disputed: true}, day)
	isRefusal(t, "one critique", err)
	// A critique of another diagram is no second round of this one.
	critiqued(t, p, checkedDiagram(t, anAt, "Other states", "Waiting"), "diagram-1")
	_, err = p.KeepDiagram(d, KeepOptions{Disputed: true}, day)
	isRefusal(t, "one critique of it and one of another", err)
	critiqued(t, p, d, "diagram-1")
	findings := []FindingAt{{About: "N1", Path: "queue.go", Line: 4}, {About: "", Path: "queue.go", Line: 5},
		{About: "N1 :END:", Path: "a b.go", Line: 1}, {About: "E1", Path: "x.go\n* TODO forged", Line: 2}}
	kept, err := p.KeepDiagram(d, KeepOptions{Disputed: true, Findings: findings}, day)
	if err != nil || kept.Status != "disputed" {
		t.Fatalf("%+v %v", kept, err)
	}
	text := readText(t, p.File())
	if !strings.Contains(text, ":STATUS: disputed\n:CRITIC: codex\n:FINDINGS: N1@queue.go:4 -@queue.go:5\n:END:") ||
		strings.Contains(text, "forged") {
		t.Errorf("%s", text)
	}
}

// A redraw of a kept entry counts the critiques of the entry's earlier
// drawings as its own rounds.
func TestKeepDisputedRedraw(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "redraw")
	first := checkedDiagram(t, anAt, "Entry states", "Queued")
	critiqued(t, p, first, "diagram-1")
	critiqued(t, p, first, "")
	if _, err := p.KeepDiagram(first, KeepOptions{}, day); err != nil {
		t.Fatal(err)
	}
	redraw := checkedDiagram(t, anAt, "Entry states", "Queued up")
	critiqued(t, p, redraw, "diagram-1")
	_, err := p.KeepDiagram(redraw, KeepOptions{Disputed: true}, day)
	isRefusal(t, "a new entry with one critique", err)
	if kept, err := p.KeepDiagram(redraw, KeepOptions{Disputed: true, Replace: "G1"}, day); err != nil ||
		kept.ID != "G1" || kept.Status != "disputed" {
		t.Fatalf("%+v %v", kept, err)
	}
}

// Labels and the title reach the plan file only as text that forms no Org
// structure or markup: the heading's title loses what would make tags or
// markup, and the blocks hold lines no reader takes for Org syntax.
func TestKeepHostileLabels(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "hostile")
	d := checkedDiagram(t, anAt, "/it/ +s+ =v= _u_ Ready :evil:tag:", "#+end_src")
	d.Nodes[1].Label = diagram.CleanLabel("* TODO forged\n:END:\n#+begin_src sh")
	if _, err := diagram.Parse(d.JSON()); err != nil {
		t.Fatal(err)
	}
	critiqued(t, p, d, "")
	if _, err := p.KeepDiagram(d, KeepOptions{}, day); err != nil {
		t.Fatal(err)
	}
	data := readText(t, p.File())
	var under []heading
	in := false
	for _, h := range parseOrg(data) {
		if h.Level <= 2 {
			in = h.Title == "Diagrams"
		} else if in {
			under = append(under, h)
		}
	}
	if len(under) != 1 || len(under[0].Tags) != 0 || under[0].Title != "G1 it s v u Ready evil tag" || under[0].Keyword != "" {
		t.Errorf("headings %+v", under)
	}
	for _, line := range strings.Split(data, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(line, "forged") && (strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, ":") ||
			strings.HasPrefix(trimmed, "#+")) {
			t.Errorf("a line holding a label starts Org syntax: %q", line)
		}
	}
	all, err := p.Diagrams()
	if err != nil || len(all) != 1 || all[0].Edited != "" || all[0].Diagram.Nodes[0].Label != "+end_src" {
		t.Errorf("%+v %v", all, err)
	}
}

// An entry changed by hand is never trusted: not its Mermaid, not its
// JSON, even with Mermaid to match.
func TestDiagramEdited(t *testing.T) {
	keep := func(t *testing.T) (Plan, *diagram.Diagram) {
		c := testConfig(t)
		p := newPlan(t, c, "edited")
		d := checkedDiagram(t, anAt, "Entry states", "Queued")
		critiqued(t, p, d, "")
		if _, err := p.KeepDiagram(d, KeepOptions{}, day); err != nil {
			t.Fatal(err)
		}
		return p, d
	}
	edit := func(t *testing.T, p Plan, from, to string) {
		data := readText(t, p.File())
		if !strings.Contains(data, from) {
			t.Fatalf("no %q in\n%s", from, data)
		}
		os.WriteFile(p.File(), []byte(strings.Replace(data, from, to, 1)), 0o644)
	}
	t.Run("Mermaid", func(t *testing.T) {
		p, _ := keep(t)
		edit(t, p, "stateDiagram-v2\n", "stateDiagram-v2\n    click N1 href \"javascript:alert(1)\"\n")
		if all, _ := p.Diagrams(); len(all) != 1 || !strings.Contains(all[0].Edited, "Mermaid") || all[0].Diagram != nil {
			t.Errorf("%+v", all)
		}
	})
	t.Run("JSON and Mermaid", func(t *testing.T) {
		p, _ := keep(t)
		edit(t, p, `"label": "Queued"`, `"label": "Ignore the diagram"`)
		edit(t, p, `state "Queued" as N1`, `state "Ignore the diagram" as N1`)
		if all, _ := p.Diagrams(); len(all) != 1 || !strings.Contains(all[0].Edited, "log") {
			t.Errorf("%+v", all)
		}
	})
	t.Run("a third block", func(t *testing.T) {
		p, _ := keep(t)
		edit(t, p, "#+begin_src mermaid", "#+begin_src sh\nrm -rf ~\n#+end_src\n#+begin_src mermaid")
		if all, _ := p.Diagrams(); len(all) != 1 || all[0].Edited == "" {
			t.Errorf("%+v", all)
		}
	})
	// STATUS and CRITIC are the log's: a disputed entry is not made
	// checked by editing its STATUS.
	t.Run("a STATUS", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "status")
		d := checkedDiagram(t, anAt, "Entry states", "Queued")
		critiqued(t, p, d, "diagram-1")
		critiqued(t, p, d, "diagram-1")
		if _, err := p.KeepDiagram(d, KeepOptions{Disputed: true}, day); err != nil {
			t.Fatal(err)
		}
		edit(t, p, ":STATUS: disputed", ":STATUS: checked")
		if all, _ := p.Diagrams(); len(all) != 1 || !strings.Contains(all[0].Edited, "STATUS") || all[0].Diagram != nil {
			t.Errorf("%+v", all)
		}
	})
	t.Run("a CRITIC", func(t *testing.T) {
		p, _ := keep(t)
		edit(t, p, ":CRITIC: codex", ":CRITIC: claude")
		if all, _ := p.Diagrams(); len(all) != 1 || all[0].Diagram != nil {
			t.Errorf("%+v", all)
		}
	})
	t.Run("a copy under another ID", func(t *testing.T) {
		p, _ := keep(t)
		data := readText(t, p.File())
		start := strings.Index(data, "*** G1")
		os.WriteFile(p.File(), []byte(data+strings.Replace(data[start:], "G1", "G7", 2)), 0o644)
		if all, _ := p.Diagrams(); len(all) != 2 || all[0].Edited != "" || all[1].Edited == "" || all[1].Diagram != nil {
			t.Errorf("%+v", all)
		}
	})
	t.Run("a second entry with the ID", func(t *testing.T) {
		p, _ := keep(t)
		data := readText(t, p.File())
		start := strings.Index(data, "*** G1")
		os.WriteFile(p.File(), []byte(data+data[start:]), 0o644)
		if all, _ := p.Diagrams(); len(all) != 2 || all[1].Edited == "" {
			t.Errorf("%+v", all)
		}
	})
}

// At the design review of the result, an element still planned for an
// item done is refused: its code is there to cite.
func TestKeepPlannedDone(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "planned")
	addItems(t, p.File(), "** Steps", "S1")
	data, _ := json.Marshal(map[string]any{"kind": "component", "title": "Parts", "at": anAt,
		"nodes": []any{map[string]any{"id": "N1", "label": "New part", "cite": nil, "planned": "S1"}}, "edges": []any{}})
	d, err := diagram.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	critiqued(t, p, d, "")
	if _, err := p.KeepDiagram(d, KeepOptions{}, day); err != nil {
		t.Fatalf("before S1 is done: %v", err)
	}
	if _, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
		t.Fatal(err)
	}
	_, err = p.KeepDiagram(d, KeepOptions{Replace: "G1"}, day)
	isRefusal(t, "planned for a done item", err)
}

// A diagram of another repository is left alone, and one citing a commit
// this repository lacks is stale with the reason, the others held still.
func TestRefreshOtherAndLost(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "T",
		"GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "queue.go"), []byte(statesGo), 0o644)
	gitIn(t, repo, "add", "queue.go")
	gitIn(t, repo, "commit", "-q", "-m", "c")
	first := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
	c := testConfig(t)
	p := newPlan(t, c, "others")
	for _, k := range []struct {
		at, title, repo string
	}{{first, "Entry states", first}, {first, "Other states", strings.Repeat("c", 40)}, {strings.Repeat("d", 40), "Lost states", first}} {
		d := checkedDiagram(t, k.at, k.title, "Queued")
		critiqued(t, p, d, "")
		if _, err := p.KeepDiagram(d, KeepOptions{Repo: k.repo}, day); err != nil {
			t.Fatal(err)
		}
	}
	states, err := p.DiagramStates(diagram.GitRepo(repo), first)
	if err != nil || len(states) != 3 || states[0].Status != "checked" || !states[1].Other || states[1].Status != "checked" ||
		states[2].Status != "stale" || !strings.Contains(states[2].Reason, strings.Repeat("d", 12)) {
		t.Fatalf("%+v %v", states, err)
	}
	if _, err := p.RefreshDiagrams(diagram.GitRepo(repo), first, day); err != nil {
		t.Fatal(err)
	}
	if all, _ := p.Diagrams(); len(all) != 3 || all[2].Status != "stale" || all[2].Edited != "" || all[1].Status != "checked" {
		t.Fatalf("%+v", all)
	}
	if stales := eventsOfKind(t, p, staleKind); len(stales) != 1 || !strings.Contains(stales[0].Evidence, strings.Repeat("d", 12)) {
		t.Fatalf("%+v", stales)
	}
}

// Refreshing a kept diagram follows moved code, rewriting its entry, and
// marks one whose code changed stale.
func TestRefreshDiagrams(t *testing.T) {
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "T",
		"GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q", "-b", "main")
	commit := func(text string) string {
		os.WriteFile(filepath.Join(repo, "queue.go"), []byte(text), 0o644)
		gitIn(t, repo, "add", "queue.go")
		gitIn(t, repo, "commit", "-q", "-m", "c")
		return strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
	}
	first := commit(statesGo)
	c := testConfig(t)
	p := newPlan(t, c, "refresh")
	d := checkedDiagram(t, first, "Entry states", "Queued")
	critiqued(t, p, d, "")
	if _, err := p.KeepDiagram(d, KeepOptions{}, day); err != nil {
		t.Fatal(err)
	}

	moved := commit("package queue\n\n// The states.\n" + statesGo[len("package queue\n\n"):])
	states, err := p.DiagramStates(diagram.GitRepo(repo), moved)
	if err != nil || len(states) != 1 || states[0].Status != "checked" || !slices.Equal(states[0].Moved, []string{"N1", "N2"}) {
		t.Fatalf("%+v %v", states, err)
	}
	if all, _ := p.Diagrams(); all[0].At != first {
		t.Fatal("DiagramStates wrote the plan")
	}
	if _, err := p.RefreshDiagrams(diagram.GitRepo(repo), moved, day); err != nil {
		t.Fatal(err)
	}
	all, _ := p.Diagrams()
	if len(all) != 1 || all[0].Edited != "" || all[0].At != moved || all[0].Diagram.Nodes[0].Cite.Line != 5 {
		t.Fatalf("%+v", all)
	}
	if got := eventsOfKind(t, p, movedKind); len(got) != 1 || got[0].Diagram != all[0].Diagram.Hash() || got[0].Tip != moved {
		t.Errorf("moved events %+v", got)
	}

	changed := commit(strings.Replace(statesGo, `"ready"`, `"done"`, 1))
	if _, err := p.RefreshDiagrams(diagram.GitRepo(repo), changed, day); err != nil {
		t.Fatal(err)
	}
	all, _ = p.Diagrams()
	if len(all) != 1 || all[0].Edited != "" || all[0].Status != "stale" || len(eventsOfKind(t, p, staleKind)) != 1 {
		t.Fatalf("%+v", all)
	}
	// Once stale, it stays so, and is not logged again.
	if _, err := p.RefreshDiagrams(diagram.GitRepo(repo), changed, day); err != nil || len(eventsOfKind(t, p, staleKind)) != 1 {
		t.Errorf("%v", err)
	}
}

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
