package export_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/export"
)

// pageJS runs the page's script, as a snapshot argv[2] holds it, against
// a document of its own that keeps elements as a tree, and prints the
// outline of what it drew: of the Today section and of the whole page.
// Each element is a line, indented by its depth, of its tag, its classes,
// its href in brackets and its own text in JSON.
const pageJS = `"use strict";
const fs = require("fs");
const page = fs.readFileSync(process.argv[2], "utf8");
const block = (id) => { const m = new RegExp('<script[^>]*id="' + id + '">([\\s\\S]*?)</script>').exec(page); return m ? m[1] : null; };
const code = page.split("\n<script>\n")[1].split("\n</script>")[0];
class Text { constructor(s) { this.textContent = s; } }
class El {
  constructor(tag) { this.tag = tag; this.kids = []; this.attrs = {}; this.className = ""; this.listeners = {}; }
  get open() { return Object.hasOwn(this.attrs, "open"); }
  set open(v) { if (v) this.attrs.open = ""; else delete this.attrs.open; }
  append(...ks) { this.kids.push(...ks); }
  replaceChildren(...ks) { this.kids = ks; }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return Object.hasOwn(this.attrs, k) ? this.attrs[k] : null; }
  removeAttribute(k) { delete this.attrs[k]; }
  addEventListener(type, f) { (this.listeners[type] ||= []).push(f); }
  focus() {}
  scrollIntoView() {}
  remove() {}
  get textContent() { return this.kids.map((k) => k.textContent).join(""); }
  set textContent(v) { this.kids = [new Text(String(v))]; }
}
const els = new Map();
const document = {
  createElement: (tag) => new El(tag),
  createTextNode: (s) => new Text(s),
  getElementById: (id) => {
    const b = block(id);
    if (b !== null) return { textContent: b };
    if (!els.has(id)) els.set(id, new El("div"));
    return els.get(id);
  },
  documentElement: new El("html"),
  body: new El("body"),
  activeElement: null,
  hidden: false,
};
const window = { matchMedia: () => ({ matches: false, addEventListener() {} }), addEventListener() {} };
new Function("document", "window", "location", "MutationObserver", "setInterval", "requestAnimationFrame", code)(
  document, window, { hash: "" }, class { observe() {} }, () => 0, (f) => f());
const outline = (n) => {
  const lines = [];
  const walk = (n, depth) => {
    if (n instanceof Text) return;
    const own = n.kids.filter((k) => k instanceof Text).map((k) => k.textContent).join("");
    let s = n.tag + (n.className ? "." + n.className.split(" ").join(".") : "") + (n.attrs.href ? "[" + n.attrs.href + "]" : "");
    if (own) s += " " + JSON.stringify(own);
    lines.push("  ".repeat(depth) + s);
    for (const k of n.kids) walk(k, depth + 1);
  };
  walk(n, 0);
  return lines;
};
const app = els.get("app");
const today = app.kids.find((k) => k.attrs && k.attrs.id === "today");
// Each of Today's diagrams is opened, as a viewer's click opens it.
const open = (n) => {
  if (n instanceof Text) return;
  if (n.tag === "details" && n.className === "diagram") {
    n.open = true;
    for (const f of n.listeners.toggle || []) f();
  }
  n.kids.forEach(open);
};
if (today) open(today);
process.stdout.write(JSON.stringify({ today: today ? outline(today) : [], app: outline(app) }));
`

// drawn is what the page's script draws of the documents under dir, as a
// snapshot carries them: the outline of its Today section, and of the
// whole page, each line trimmed.
func drawn(t *testing.T, dir string) (today, all []string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	html, err := export.Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	runner, pageFile := filepath.Join(work, "run.js"), filepath.Join(work, "page.html")
	for file, content := range map[string][]byte{runner: []byte(pageJS), pageFile: html} {
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, runner, pageFile)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	var got struct{ Today, App []string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node's outline %s: %v", out, err)
	}
	trim := func(lines []string) []string {
		for i, l := range lines {
			lines[i] = strings.TrimSpace(l)
		}
		return lines
	}
	return trim(got.Today), trim(got.App)
}

// writeDoc puts v as the document file under dir.
func writeDoc(t *testing.T, dir, file string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, file)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The Today panel draws a parsed digest's changes, each with its pull
// request linked, its steps and results as code, its diagrams as the
// review diagrams are drawn, under their captions, each opened showing
// its own source as a snapshot shows one, and its https links
// as links, and lists what the parse dropped; a digest with no changes is
// its text. It draws from the digest alone: no plan's text and no pull
// request's status, which the rest of the page shows, reach it. The
// package build has node; elsewhere the test needs it on PATH.
func TestPageDigest(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "plans/2026-10-13-x.box.json", export.Plan{Schema: 6, ID: "2026-10-13-x.box", Title: "PRIVATE-plan-title",
		Status: "working", Host: "box"})
	writeDoc(t, dir, "github/status.json", export.GitHub{Schema: 6, At: "2026-10-13T09:00:00Z",
		Mine: []export.PR{{Repo: "wesnel/nix-config", Number: 250, Title: "PRIVATE-pr-title", CI: "failure", Review: "none"}}})
	writeDoc(t, dir, "meta/sync.json", export.Sync{Schema: 6, Updated: "2026-10-13T09:00:00Z", Host: "box"})
	structured := export.Digest{Kind: "demo", Text: "SHOWN-ONLY-AS-RAW", Summary: "Two changes reached main.",
		Changes: []export.Change{{Title: "Draw the digest", Repo: "wesnel/nix-config", Number: 250,
			Summary: "The Today panel draws the digest.",
			Steps: []export.DemoStep{{Text: "Export the documents:", Command: "plan-dash export", Result: `{"documents": 3}`},
				{Command: "plan-dash page"}},
			Diagrams: []export.DigestDiagram{{Caption: "How the digest reaches the page",
				Source: "flowchart LR\n    N1[\"export\"]\n    N2[\"page\"]\n    N1 -->|\"today/digest\"| N2\n"}},
			Links: []export.DigestLink{{URL: "https://github.com/user-attachments/assets/1234", Caption: "the panel drawn"},
				{URL: "javascript:alert(1)", Caption: "not a link"}}}},
		Dropped: []export.Dropped{{Line: 24, What: "link", Why: "not an https URL"}}}
	raw := export.Digest{Kind: "demo", Text: "* Yesterday\n- Shipped.", Changes: []export.Change{}, Dropped: []export.Dropped{}}
	writeDoc(t, dir, "today/digest.json", export.Today{Schema: 6, Date: "2026-10-13", Digests: []export.Digest{structured, raw}})

	today, all := drawn(t, dir)
	for _, want := range []string{
		`p.digest-text "Two changes reached main."`,
		`h3 "Draw the digest "`,
		`a.mono[https://github.com/wesnel/nix-config/pull/250] "wesnel/nix-config#250"`,
		`p.digest-text "The Today panel draws the digest."`,
		`p.digest-text "Export the documents:"`,
		`pre.demo-command "plan-dash export"`,
		`pre.demo-result "{\"documents\": 3}"`,
		`pre.demo-command "plan-dash page"`,
		`p.small.muted "No result recorded."`,
		`details.diagram`,
		`span "How the digest reaches the page"`,
		`p.small.muted "Diagram source; this snapshot loads nothing to draw it."`,
		`pre.diagram-source "flowchart LR\n    N1[\"export\"]\n    N2[\"page\"]\n    N1 -->|\"today/digest\"| N2\n"`,
		`a[https://github.com/user-attachments/assets/1234] "the panel drawn"`,
		`li "Line 24, link: not an https URL"`,
		`pre.digest-raw "* Yesterday\n- Shipped."`,
	} {
		if !slices.Contains(today, want) {
			t.Errorf("Today lacks %s:\n%s", want, strings.Join(today, "\n"))
		}
	}
	text := strings.Join(today, "\n")
	for _, bad := range []string{"javascript:", "not a link", "SHOWN-ONLY-AS-RAW", "PRIVATE", "failure"} {
		if strings.Contains(text, bad) {
			t.Errorf("Today shows %q:\n%s", bad, text)
		}
	}
	// The rest of the page shows the plan and the pull request's status.
	page := strings.Join(all, "\n")
	for _, want := range []string{"PRIVATE-plan-title", "PRIVATE-pr-title"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q, so its absence from Today shows nothing", want)
		}
	}
}
