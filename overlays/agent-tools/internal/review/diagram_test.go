package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/record"
)

// codeDiagram is a component diagram of setUp's code.txt at tip, its
// second node citing line.
func codeDiagram(tip string, line int) map[string]any {
	cite := func(line int, symbol string) map[string]any {
		return map[string]any{"path": "code.txt", "line": line, "endLine": nil, "symbol": symbol}
	}
	return map[string]any{"kind": "component", "title": "The code", "at": tip,
		"nodes": []map[string]any{
			{"id": "N1", "label": "b", "cite": cite(2, "b"), "planned": nil},
			{"id": "N2", "label": "c", "cite": cite(line, "c"), "planned": nil},
		},
		"edges": []map[string]any{{"id": "E1", "from": "N1", "to": "N2", "label": "then", "cite": cite(3, "c"), "planned": nil}},
	}
}

func diagramsReply(ds ...map[string]any) string {
	return claudeResult(map[string]any{"diagrams": ds, "unverified": []string{}}, "Drawn.")
}

// A diagram review draws with Claude, holds each diagram to the code at
// TIP, has Codex critique it, and keeps the JSON of those that hold, which
// show renders as Mermaid; one citing a line that is not there is dropped.
func TestDiagrams(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "critique")
	t.Setenv("FAKE_CHECKED", `["N1", "N2", "E1"]`)
	f.claudeSays(t, "diagrammer", diagramsReply(codeDiagram(f.tip, 3), codeDiagram(f.tip, 9)))
	code, out, stderr := runDelegate(t, "--reviewer", "diagram", "--plan", f.plan, f.base+"..HEAD")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	rec := must(record.ReadRange(out.Record))
	if len(rec.Diagrams) != 1 || rec.Diagrams[0].ID != "D1" || rec.Diagrams[0].Verdict != "holds" ||
		rec.Diagrams[0].DrawnBy != "claude" || rec.Diagrams[0].CritiquedBy != "codex" || rec.Diagrams[0].Title != "The code" {
		t.Fatalf("diagrams %+v", rec.Diagrams)
	}
	round := rec.Rounds[0]
	if round.Reviewer != "diagram" || len(round.Dropped) != 1 || !strings.Contains(round.Dropped[0], "does not hold") {
		t.Errorf("round %+v", round)
	}
	if prompt := string(must(readLog(f, "prompt"))); !strings.Contains(prompt, "## Diagram") || !strings.Contains(prompt, "- N1: ") {
		t.Errorf("the critique's prompt:\n%s", prompt)
	}
	if !strings.HasPrefix(claudeArgs(t, f, "diagrammer", 1)[len(claudeArgs(t, f, "diagrammer", 1))-1], drawContract) {
		t.Error("the diagrammer was not given its contract")
	}

	code, stdout, stderr := delegateCmd(t, "show", f.base+"..HEAD")
	if code != 0 {
		t.Fatalf("show: exit %d\n%s", code, stderr)
	}
	var doc any
	must(0, json.Unmarshal([]byte(stdout), &doc))
	want := must(diagramMermaid(rec.Diagrams[0].JSON))
	if found := diagrams(doc); len(found) != 1 || found[0]["id"] != "D1" || found[0]["mermaid"] != want ||
		!strings.Contains(want, "flowchart") && !strings.Contains(want, "graph") {
		t.Errorf("show's diagrams %v, want D1 with\n%s", found, want)
	}
	f.checkCleanedUp(t)
}

// A diagram the critic faults is redrawn once, checked and critiqued
// again, and the redraw kept as corrected when that critique passes.
func TestDiagramRedrawn(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "fault-once")
	t.Setenv("FAKE_CHECKED", `["N1", "N2", "E1"]`)
	redrawn := codeDiagram(f.tip, 3)
	redrawn["title"] = "The code, redrawn"
	f.claudeSays(t, "diagrammer", diagramsReply(codeDiagram(f.tip, 3)), diagramsReply(redrawn))
	code, out, stderr := runDelegate(t, "--reviewer", "diagram", "--plan", f.plan, f.base+"..HEAD")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	rec := must(record.ReadRange(out.Record))
	if len(rec.Diagrams) != 1 || rec.Diagrams[0].Verdict != "corrected" || rec.Diagrams[0].Title != "The code, redrawn" {
		t.Fatalf("diagrams %+v", rec.Diagrams)
	}
	if len(rec.Findings) != 0 {
		t.Errorf("a corrected diagram's critique was kept as findings: %+v", rec.Findings)
	}
	redraw := claudeArgs(t, f, "diagrammer", 2)
	if text := redraw[len(redraw)-1]; !strings.Contains(text, "## Redraw") || !strings.Contains(text, `"about": "N1"`) {
		t.Errorf("the redraw was not given the critic's findings:\n%s", text)
	}
	if prompt := string(must(readLog(f, "prompt"))); !strings.Contains(prompt, "The code, redrawn") {
		t.Errorf("the redraw was not critiqued:\n%s", prompt)
	}
}

// A diagram whose redraw the critic faults too is kept as first drawn,
// unresolved, with the first critique's findings about it.
func TestDiagramUnresolved(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "fault")
	t.Setenv("FAKE_CHECKED", `["N1", "N2", "E1"]`)
	redrawn := codeDiagram(f.tip, 3)
	redrawn["title"] = "The code, redrawn"
	f.claudeSays(t, "diagrammer", diagramsReply(codeDiagram(f.tip, 3)), diagramsReply(redrawn))
	code, out, stderr := runDelegate(t, "--reviewer", "diagram", "--plan", f.plan, f.base+"..HEAD")
	if code != 3 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	rec := must(record.ReadRange(out.Record))
	if len(rec.Diagrams) != 1 || rec.Diagrams[0].Verdict != "unresolved" || rec.Diagrams[0].Title != "The code" {
		t.Fatalf("diagrams %+v", rec.Diagrams)
	}
	if len(rec.Findings) != 1 || rec.Findings[0].Kind != "untrue-doc" ||
		!strings.Contains(rec.Findings[0].Evidence, `About: N1 of the diagram "The code"`) {
		t.Errorf("findings %+v", rec.Findings)
	}
	round := rec.Rounds[0]
	if !strings.Contains(strings.Join(round.Dropped, "\n"), "redraw untrue too") ||
		!strings.Contains(strings.Join(round.Unverified, "\n"), `"The code" is unresolved`) {
		t.Errorf("round %+v", round)
	}
}

// A critique that faults nothing but leaves an element unverified, or
// whose every finding is dropped, does not let a diagram hold.
func TestDiagramInconclusive(t *testing.T) {
	for mode, why := range map[string]string{"unsure": "could not verify", "misfault": "was dropped"} {
		t.Run(mode, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv("FAKE_CODEX", mode)
			t.Setenv("FAKE_CHECKED", `["N1", "N2", "E1"]`)
			f.claudeSays(t, "diagrammer", diagramsReply(codeDiagram(f.tip, 3)))
			code, out, stderr := runDelegate(t, "--reviewer", "diagram", "--plan", f.plan, f.base+"..HEAD")
			if code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			rec := must(record.ReadRange(out.Record))
			if len(rec.Diagrams) != 1 || rec.Diagrams[0].Verdict != "unresolved" {
				t.Fatalf("diagrams %+v", rec.Diagrams)
			}
			if u := strings.Join(rec.Rounds[0].Unverified, "\n"); !strings.Contains(u, why) {
				t.Errorf("unverified %q, want %q", u, why)
			}
		})
	}
}

// Rerunning the diagram type replaces the record's diagrams, one with the
// same title keeping its ID.
func TestDiagramRerun(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "critique")
	t.Setenv("FAKE_CHECKED", `["N1", "N2", "E1"]`)
	other := codeDiagram(f.tip, 3)
	other["title"] = "Another view"
	f.claudeSays(t, "diagrammer", diagramsReply(codeDiagram(f.tip, 3)), diagramsReply(other, codeDiagram(f.tip, 3)))
	var rec *record.Range
	for range 2 {
		code, out, stderr := runDelegate(t, "--reviewer", "diagram", "--plan", f.plan, f.base+"..HEAD")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		rec = must(record.ReadRange(out.Record))
	}
	var got []string
	for _, d := range rec.Diagrams {
		got = append(got, d.ID+" "+d.Title)
	}
	if strings.Join(got, ", ") != "D2 Another view, D1 The code" {
		t.Errorf("diagrams after a rerun: %v", got)
	}
}

func TestReplaceDiagrams(t *testing.T) {
	old := []record.Diagram{{ID: "D1", Title: "a"}, {ID: "D3", Title: "b"}}
	got := replaceDiagrams(old, []record.Diagram{{Title: "c"}, {Title: "b"}, {Title: "d"}})
	var ids []string
	for _, d := range got {
		ids = append(ids, d.ID+" "+d.Title)
	}
	if strings.Join(ids, ", ") != "D4 c, D3 b, D5 d" {
		t.Errorf("replaced: %v", ids)
	}
}

// A diagram review needs Codex for its critic.
func TestDiagramsWithoutCodex(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("PR_LOOP_SESSION", "1")
	code, out, stderr := runDelegate(t, "--reviewer", "diagram", "--plan", f.plan, f.base+"..HEAD")
	if code != 4 || len(out.NotRun) != 1 || out.NotRun[0]["reviewer"] != "diagram" {
		t.Errorf("exit %d, %+v\n%s", code, out, stderr)
	}
}

func readLog(f *fixture, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(f.log, name))
}
