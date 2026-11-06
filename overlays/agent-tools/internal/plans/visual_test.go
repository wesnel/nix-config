package plans

import (
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/record"
)

// stateDiagram is a diagram's JSON as the diagram pass keeps it, and
// stateMermaid the Mermaid it renders to, written out.
const (
	stateDiagram = `{"kind": "state", "title": "Queue entry states", "at": "0123456789abcdef0123456789abcdef01234567",
 "nodes": [
  {"id": "N1", "label": "Queued", "cite": {"path": "queue/queue.go", "line": 5, "endLine": null, "symbol": "Queued"}, "planned": null},
  {"id": "N2", "label": "Ready", "cite": {"path": "queue/queue.go", "line": 6, "endLine": null, "symbol": "Ready"}, "planned": null}],
 "edges": [
  {"id": "E1", "from": "N1", "to": "N2", "label": "review exit 0",
   "cite": {"path": "queue/run.go", "line": 9, "endLine": null, "symbol": "Ready"}, "planned": null}]}`
	stateMermaid = "#+begin_src mermaid\nstateDiagram-v2\n    state \"Queued\" as N1\n    state \"Ready\" as N2\n" +
		"    N1 --> N2: review exit 0\n#+end_src\n"
)

// reviewedRepo is a repository whose origin is o/r on GitHub, with two
// commits, and a review record of HEAD~1..HEAD under a state directory of
// the test's own, holding diagrams. It returns the repository and the
// range as full SHAs.
func reviewedRepo(t *testing.T, diagrams ...record.Diagram) (string, string) {
	t.Helper()
	repo := commitRepo(t)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "tip")
	base := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD~1"))
	head := strings.TrimSpace(gitIn(t, repo, "rev-parse", "HEAD"))
	hostpaths.Bake(t, t.TempDir(), t.TempDir())
	path, err := record.RangeFile("o", "r", base, head)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Update(path, func(r *record.Range) error {
		*r = record.Range{Repo: "o/r", Base: base, Head: head, Title: "Queue", Updated: day, Diagrams: diagrams}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return repo, base + ".." + head
}

func keptDiagram(id, verdict string) record.Diagram {
	return record.Diagram{ID: id, Title: "Queue entry states", DrawnBy: "claude", CritiquedBy: "codex", Verdict: verdict,
		JSON: stateDiagram}
}

// A change's visuals live in its plan's Visuals, each keyed by its branch:
// a diagram copied from the review that kept it, a tape's source, or an
// attachment's URL, with a caption.
func TestVisual(t *testing.T) {
	t.Run("a kept diagram", func(t *testing.T) {
		repo, span := reviewedRepo(t, keptDiagram("D1", "holds"))
		p := newPlan(t, testConfig(t), "x")
		d, err := ReadDiagram(repo, "HEAD~1..HEAD", "D1")
		if err != nil {
			t.Fatal(err)
		}
		r, err := p.Visual(VisualOptions{Branch: "loop", Diagram: &d}, day)
		if err != nil {
			t.Fatal(err)
		}
		if r.ID != "V1" || r.Event.Kind != "planned" {
			t.Errorf("recorded %+v", r)
		}
		want := "** Follow-ups\n** Visuals\n*** Queue entry states\n:PROPERTIES:\n:CUSTOM_ID: V1\n:BRANCH: loop\n:DIAGRAM: D1\n" +
			":RANGE: " + span + "\n:VERDICT: holds\n:END:\n" + stateMermaid + "** References\n"
		if text := fileText(t, p.file("")); !strings.Contains(text, want) {
			t.Errorf("plan:\n%s\nwant within it:\n%s", text, want)
		}
		if drift := structureDrift(t, p); len(drift) != 0 {
			t.Errorf("drift %v", drift)
		}
	})

	t.Run("a corrected diagram, captioned", func(t *testing.T) {
		repo, _ := reviewedRepo(t, keptDiagram("D1", "holds"), keptDiagram("D2", "corrected"))
		p := newPlan(t, testConfig(t), "x")
		for _, id := range []string{"D1", "D2"} {
			d, err := ReadDiagram(repo, "HEAD~1..HEAD", id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Visual(VisualOptions{Branch: "loop", Diagram: &d, Caption: "How an entry gets ready"}, day); err != nil {
				t.Fatal(err)
			}
		}
		text := fileText(t, p.file(""))
		if !strings.Contains(text, "*** How an entry gets ready\n:PROPERTIES:\n:CUSTOM_ID: V2\n:BRANCH: loop\n:DIAGRAM: D2\n") ||
			!strings.Contains(text, ":VERDICT: corrected\n") {
			t.Errorf("plan:\n%s", text)
		}
	})

	t.Run("refused/an unresolved diagram", func(t *testing.T) {
		repo, _ := reviewedRepo(t, keptDiagram("D1", "unresolved"))
		p := newPlan(t, testConfig(t), "x")
		before := fileText(t, p.file(""))
		d, err := ReadDiagram(repo, "HEAD~1..HEAD", "D1")
		if err != nil || d.Verdict != "unresolved" {
			t.Fatalf("read %+v, %v", d, err)
		}
		_, err = p.Visual(VisualOptions{Branch: "loop", Diagram: &d}, day)
		isRefusal(t, "an unresolved diagram", err)
		if err == nil || !strings.Contains(err.Error(), "unresolved") {
			t.Errorf("refusal %v does not name the verdict", err)
		}
		if after := fileText(t, p.file("")); after != before {
			t.Errorf("plan changed:\n%s", after)
		}
	})

	t.Run("refused/a diagram the review does not hold", func(t *testing.T) {
		repo, _ := reviewedRepo(t, keptDiagram("D1", "holds"))
		_, err := ReadDiagram(repo, "HEAD~1..HEAD", "D7")
		isRefusal(t, "an unknown diagram", err)
	})

	t.Run("refused/a range never reviewed", func(t *testing.T) {
		repo, _ := reviewedRepo(t, keptDiagram("D1", "holds"))
		_, err := ReadDiagram(repo, "HEAD..HEAD", "D1")
		isRefusal(t, "a range never reviewed", err)
	})

	// The same diagram of the same range again changes nothing, and logs
	// nothing.
	t.Run("again changes nothing", func(t *testing.T) {
		repo, _ := reviewedRepo(t, keptDiagram("D1", "holds"))
		p := newPlan(t, testConfig(t), "x")
		d, err := ReadDiagram(repo, "HEAD~1..HEAD", "D1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Visual(VisualOptions{Branch: "loop", Diagram: &d, Caption: "The queue"}, day); err != nil {
			t.Fatal(err)
		}
		before := fileText(t, p.file(""))
		events := len(eventsBut(t, p, "property"))
		for _, caption := range []string{"", "The queue"} {
			r, err := p.Visual(VisualOptions{Branch: "loop", Diagram: &d, Caption: caption}, day)
			if err != nil || r.ID != "V1" || r.Event.Kind != "" {
				t.Errorf("again with caption %q: %+v, %v", caption, r, err)
			}
		}
		if after := fileText(t, p.file("")); after != before {
			t.Errorf("plan changed:\n%s", after)
		}
		if n := len(eventsBut(t, p, "property")); n != events {
			t.Errorf("%d events, had %d", n, events)
		}
	})

	const tape = "Output demo.gif\nType \"agent-plan status\"\nEnter\nSleep 2s\n"

	// A tape's source and its recording's URL, added to the entry once the
	// recording is attached to the pull request.
	t.Run("a tape, then its recording", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		r, err := p.Visual(VisualOptions{Branch: "loop", Tape: tape, Caption: "The status of a plan"}, day)
		if err != nil || r.ID != "V1" {
			t.Fatalf("tape: %+v, %v", r, err)
		}
		const url = "https://github.com/user-attachments/assets/0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0"
		if r, err := p.Visual(VisualOptions{Entry: "V1", URL: url}, day); err != nil || r.ID != "V1" || r.Event.Kind != "planned" {
			t.Fatalf("url: %+v, %v", r, err)
		}
		want := "** Visuals\n*** The status of a plan\n:PROPERTIES:\n:CUSTOM_ID: V1\n:BRANCH: loop\n:URL: " + url + "\n:END:\n" +
			"#+begin_src tape\n" + tape + "#+end_src\n** References\n"
		if text := fileText(t, p.file("")); !strings.Contains(text, want) {
			t.Errorf("plan:\n%s\nwant within it:\n%s", text, want)
		}
		// The same tape again keeps its URL and changes nothing.
		before := fileText(t, p.file(""))
		if r, err := p.Visual(VisualOptions{Branch: "loop", Tape: tape, Caption: "The status of a plan"}, day); err != nil ||
			r.ID != "V1" || r.Event.Kind != "" {
			t.Errorf("tape again: %+v, %v", r, err)
		}
		if after := fileText(t, p.file("")); after != before {
			t.Errorf("plan changed:\n%s", after)
		}
	})

	t.Run("a screenshot", func(t *testing.T) {
		p := newPlan(t, testConfig(t), "x")
		const url = "https://github.com/user-attachments/assets/1a2b"
		if _, err := p.Visual(VisualOptions{Branch: "loop", URL: url, Caption: "The page"}, day); err != nil {
			t.Fatal(err)
		}
		want := "*** The page\n:PROPERTIES:\n:CUSTOM_ID: V1\n:BRANCH: loop\n:URL: " + url + "\n:END:\n** References\n"
		if text := fileText(t, p.file("")); !strings.Contains(text, want) {
			t.Errorf("plan:\n%s", text)
		}
	})

	for what, tape := range map[string]string{
		"a heading line":     "Type \"x\"\n* Output\n",
		"a drawer line":      ":PROPERTIES:\n",
		"a planning line":    "SCHEDULED: <2026-11-06 Fri>\n",
		"a keyword line":     "#+title: x\n",
		"the end of a block": "Type \"x\"\n#+end_src\n* Forged\n",
		"a block of its own": "#+begin_example\nx\n#+end_example\n",
		"a carriage return":  "Type \"x\"\r\n",
		"no source at all":   "\n\n",
	} {
		t.Run("refused/a tape with "+what, func(t *testing.T) {
			p := newPlan(t, testConfig(t), "x")
			before := fileText(t, p.file(""))
			_, err := p.Visual(VisualOptions{Branch: "loop", Tape: tape, Caption: "Demo"}, day)
			isRefusal(t, what, err)
			if after := fileText(t, p.file("")); after != before {
				t.Errorf("plan changed:\n%s", after)
			}
		})
	}

	for what, url := range map[string]string{
		"http":             "http://github.com/user-attachments/assets/1a2b",
		"another host":     "https://example.com/demo.gif",
		"no path":          "https://github.com",
		"a space":          "https://github.com/user-attachments/assets/1a 2b",
		"no scheme at all": "github.com/user-attachments/assets/1a2b",
	} {
		t.Run("refused/a URL over "+what, func(t *testing.T) {
			p := newPlan(t, testConfig(t), "x")
			if _, err := p.Visual(VisualOptions{Branch: "loop", Tape: tape, Caption: "Demo"}, day); err != nil {
				t.Fatal(err)
			}
			before := fileText(t, p.file(""))
			_, err := p.Visual(VisualOptions{Entry: "V1", URL: url}, day)
			isRefusal(t, what, err)
			if after := fileText(t, p.file("")); after != before {
				t.Errorf("plan changed:\n%s", after)
			}
		})
	}

	for what, o := range map[string]VisualOptions{
		"no branch":                {Tape: tape, Caption: "Demo"},
		"a tape with no caption":   {Branch: "loop", Tape: tape},
		"a tape and a diagram":     {Branch: "loop", Tape: tape, Caption: "Demo", Diagram: &KeptDiagram{}},
		"no visual":                {Branch: "loop", Caption: "Demo"},
		"an entry that is not one": {Entry: "V9", URL: "https://github.com/user-attachments/assets/1a2b"},
		"a branch with a space":    {Branch: "a loop", Tape: tape, Caption: "Demo"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			p := newPlan(t, testConfig(t), "x")
			_, err := p.Visual(o, day)
			isRefusal(t, what, err)
		})
	}
}
