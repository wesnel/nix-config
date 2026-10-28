package export_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/export"
)

func TestSnapshotViewer(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the snapshot viewer runtime check")
	}
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "full"}[full], func(t *testing.T) {
			dir := t.TempDir()
			e := export.Export{Listed: true, Sync: export.Sync{Updated: "2026-10-27T12:00:00Z", Host: "box"}, GitHub: export.GitHub{At: "2026-10-27T11:00:00Z"}}
			if full {
				e.Plans = []export.Plan{{ID: "2026-10-27-demo.box", Title: "</script><script>throw 'unsafe'</script>", Host: "box", Status: "working", Goal: "Keep everything", Steps: []export.Step{{ID: "S1", Title: "One step", Keyword: "TODO", Ready: true}}}}
				e.GitHub.Mine = []export.PR{{Repo: "owner/repo", Number: 7, Title: "Change", RecordDiagrams: []export.ReviewDiagram{{ID: "D1", Kind: "component", Source: "flowchart TD\nN1[\"Visible source\"]\n"}}}}
			}
			if err := export.Write(filepath.Join(dir, "out"), e); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "snapshot.html")
			if err := export.WriteSnapshot(filepath.Join(dir, "out"), file); err != nil {
				t.Fatal(err)
			}
			runner := filepath.Join(dir, "run.cjs")
			if err := os.WriteFile(runner, []byte(snapshotViewerJS), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), node, runner, file)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("viewer: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "Snapshot") || !strings.Contains(string(out), "Collected") || strings.Contains(string(out), "Live") {
				t.Fatalf("snapshot freshness: %s", out)
			}
			if full && (!strings.Contains(string(out), "Keep everything") || !strings.Contains(string(out), "Visible source") || !strings.Contains(string(out), "</script><script>throw 'unsafe'</script>")) {
				t.Fatalf("viewer lost full data or source fallback: %s", out)
			}
			if !full && !strings.Contains(string(out), "No plans yet") {
				t.Fatalf("empty viewer: %s", out)
			}
		})
	}
}

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
const blocked = () => { throw Error("snapshot attempted external capability or network"); };
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
