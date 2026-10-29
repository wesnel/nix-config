package export_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/export"
)

// hostile is a title that would end the snapshot's script element were it
// embedded unescaped.
const hostile = "</script><script>throw 'unsafe'</script>"

// snapshotOf writes e as export does and returns Snapshot of it.
func snapshotOf(t *testing.T, e export.Export) []byte {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "out")
	if err := export.Write(dir, e); err != nil {
		t.Fatal(err)
	}
	page, err := export.Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func fullExport() export.Export {
	return export.Export{Listed: true, Sync: export.Sync{Updated: "2026-10-27T12:00:00Z", Host: "box"},
		GitHub: export.GitHub{At: "2026-10-27T11:00:00Z", Mine: []export.PR{{Repo: "owner/repo", Number: 7, Title: "Change",
			RecordDiagrams: []export.ReviewDiagram{{ID: "D1", Kind: "component", Source: "flowchart TD\nN1[\"Visible source\"]\n"}}}}},
		Plans: []export.Plan{{ID: "2026-10-27-demo.box", Title: hostile, Host: "box", Status: "working", Goal: "Keep everything",
			Steps: []export.Step{{ID: "S1", Title: "One step", Keyword: "TODO", Ready: true}}}}}
}

// The snapshot is the page with the export's documents in it, as JSON no
// text in them can break out of.
func TestSnapshot(t *testing.T) {
	page := snapshotOf(t, fullExport())
	open := []byte(`<script type="application/json" id="snapshot">`)
	if bytes.Count(page, open) != 1 || bytes.Contains(page, []byte(hostile)) {
		t.Fatalf("the snapshot's script element: %d, or the title unescaped", bytes.Count(page, open))
	}
	_, rest, _ := bytes.Cut(page, open)
	data, _, _ := bytes.Cut(rest, []byte("</script>"))
	var docs struct {
		Plans []export.Plan `json:"plans"`
		Meta  export.Sync   `json:"meta"`
	}
	if err := json.Unmarshal(data, &docs); err != nil || len(docs.Plans) != 1 || docs.Plans[0].Title != hostile ||
		docs.Meta.Host != "box" {
		t.Errorf("the documents: %+v %v", docs, err)
	}

	t.Run("over the Space limit", func(t *testing.T) {
		e := fullExport()
		e.Plans[0].Goal = strings.Repeat("g", 300<<10)
		dir := filepath.Join(t.TempDir(), "out")
		if err := export.Write(dir, e); err != nil {
			t.Fatal(err)
		}
		if _, err := export.Snapshot(dir); err == nil || !strings.Contains(err.Error(), "256 KiB") {
			t.Errorf("%v", err)
		}
	})
}

// The snapshot renders, run by node with no database, network or frame,
// as a snapshot of when it was collected. The package build has node;
// elsewhere the test needs it on PATH.
func TestSnapshotViewer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	for name, e := range map[string]export.Export{"empty": {Listed: true, Sync: export.Sync{Updated: "2026-10-27T12:00:00Z", Host: "box"},
		GitHub: export.GitHub{At: "2026-10-27T11:00:00Z"}}, "full": fullExport()} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			file, runner := filepath.Join(dir, "snapshot.html"), filepath.Join(dir, "run.cjs")
			if err := os.WriteFile(file, snapshotOf(t, e), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(runner, []byte(snapshotViewerJS), 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.CommandContext(t.Context(), node, runner, file).CombinedOutput()
			if err != nil {
				t.Fatalf("viewer: %v\n%s", err, out)
			}
			text := string(out)
			if !strings.Contains(text, "Snapshot") || !strings.Contains(text, "Collected") || strings.Contains(text, "Live") {
				t.Fatalf("freshness: %s", text)
			}
			if name == "full" && (!strings.Contains(text, "Keep everything") || !strings.Contains(text, "Visible source") ||
				!strings.Contains(text, hostile)) {
				t.Fatalf("the plan, the diagram's source or the title as text: %s", text)
			}
			if name == "empty" && !strings.Contains(text, "No plans yet") {
				t.Fatalf("empty: %s", text)
			}
		})
	}
}

// snapshotViewerJS runs the page's script against a stand-in document,
// opens every diagram, and prints the text of the freshness line, the sync
// line and the app. Reaching for the database, the network, a frame, a
// script or an image throws.
const snapshotViewerJS = `const fs = require("node:fs"), vm = require("node:vm");
const html = fs.readFileSync(process.argv[2], "utf8"), nodes = [], byID = new Map();
class Element {
  constructor(tag, text = "") { this.tag = tag; this.textContent = text; this.children = []; this.attributes = {}; this.events = {}; nodes.push(this); }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  setAttribute(k, v) { this.attributes[k] = v; if (k === "id") byID.set(v, this); }
  getAttribute(k) { return this.attributes[k]; }
  addEventListener(k, fn) { this.events[k] = fn; }
  focus() {}
  scrollIntoView() {}
}
for (const id of ["host-line", "fresh", "syncline", "summary", "app"]) byID.set(id, new Element("div"));
for (const match of html.matchAll(/<script[^>]* id="([^"]+)"[^>]*>([\s\S]*?)<\/script>/g)) byID.set(match[1], new Element("script-data", match[2]));
const blocked = () => { throw Error("the snapshot reached for the database, the network or a frame"); };
const document = {
  getElementById: (id) => byID.get(id), createTextNode: (text) => new Element("text", text),
  createElement: (tag) => { if (["iframe", "script", "img"].includes(tag)) blocked(); return new Element(tag); },
  documentElement: new Element("html"), body: new Element("body"), activeElement: null
};
const window = { matchMedia: () => ({ matches: false, addEventListener() {} }) };
Object.defineProperty(window, "claude", { get: blocked });
const context = { document, window, location: { hash: "" }, requestAnimationFrame: (fn) => fn(),
  setInterval() {}, MutationObserver: class { observe() {} }, getComputedStyle: blocked,
  fetch: blocked, XMLHttpRequest: blocked, WebSocket: blocked, console };
const code = html.split("\n<script>\n")[1].split("\n</script>")[0];
vm.runInNewContext(code, context, { timeout: 2000 });
for (const node of [...nodes]) if (node.tag === "details" && node.className === "diagram") { node.open = true; node.events.toggle(); }
const flatten = (node) => node.textContent + node.children.map(flatten).join(" ");
console.log(["fresh", "syncline", "app"].map((id) => flatten(byID.get(id))).join("\n"));
`
