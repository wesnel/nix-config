package export_test

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/dashboard"
	"github.com/wesnel/agent-tools/internal/diagram"
	"github.com/wesnel/agent-tools/internal/export"
	"github.com/wesnel/agent-tools/internal/queue"
)

var page = string(agentplans.Page)

// script is the page's one script that runs.
func script(t *testing.T) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, "\n<script>\n")
	code, _, ok2 := strings.Cut(rest, "\n</script>")
	if !ok || !ok2 {
		t.Fatal("the page has no <script> block")
	}
	return code
}

// Nothing the page shows is parsed as markup or run as code, and it loads
// nothing from elsewhere.
func TestPageRendersText(t *testing.T) {
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
		"DOMParser", "createContextualFragment", "eval(", "Function(", "setTimeout(\"", "javascript:", "<iframe",
		"<link", "@import", "localStorage", "window.open", "allow-same-origin"} {
		if strings.Contains(page, bad) {
			t.Errorf("the page holds %q", bad)
		}
	}
	// The one frame's document is the page's own (TestPageRenderer).
	if strings.Count(page, "srcdoc") != 1 || !strings.Contains(script(t), "frame.srcdoc = FRAME_DOC;") {
		t.Error("a frame's document set other than to FRAME_DOC")
	}
	if m := regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`).FindString(page); m != "" {
		t.Errorf("an inline event handler: %s", m)
	}
	if m := regexp.MustCompile(`(?i)\.on[a-z]+\s*=[^=]`).FindString(script(t)); m != "" {
		t.Errorf("a handler set as a property: %s", m)
	}
	tags := regexp.MustCompile(`<script[^>]*>`).FindAllString(page, -1)
	if !slices.Equal(tags, []string{`<script type="application/json" id="schema">`, `<script type="application/json" id="grammar">`,
		`<script type="text/plain" id="renderer">`, `<script>`}) {
		t.Errorf("script tags %q", tags)
	}
	// The artifact publishes no files, so the page loads none: no font,
	// no image. Mermaid is the one script its frame loads
	// (TestPageRenderer).
	for _, bad := range []string{"url(", "@font-face"} {
		if strings.Contains(page, bad) {
			t.Errorf("the page loads a file: %s", bad)
		}
	}
	for _, s := range regexp.MustCompile(`"https?://[^"]*"`).FindAllString(page, -1) {
		if s != `"https://github.com/"` && s != `"`+mermaidSrc+`"` {
			t.Errorf("the page links to %s", s)
		}
	}
	// Attributes come from h alone, which sets each by a name the page's
	// own code gives; the one other assignment of markup is textContent.
	if n := strings.Count(script(t), ".setAttribute("); n != 1 {
		t.Errorf("%d calls of setAttribute, want h's one", n)
	}
}

// mermaidSrc is the one Mermaid the page loads: the version whose lexers
// the grammar block and diagram.lexerProblem are written against.
const mermaidSrc = "https://cdn.jsdelivr.net/npm/mermaid@11.17.2/dist/mermaid.min.js"

// rendererScript is the page's renderer block, the frame's script.
func rendererScript(t *testing.T) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, "<script type=\"text/plain\" id=\"renderer\">\n")
	code, _, ok2 := strings.Cut(rest, "\n</script>")
	if !ok || !ok2 {
		t.Fatal("the page has no renderer block")
	}
	return code
}

// Mermaid runs only in the renderer frame: sandboxed to scripts with an
// opaque origin, its document the page's own, loading the pinned file by
// its hash, set to strict with no HTML in labels. The page and the frame
// each heed only the other's window, the page gives it only a source the
// grammar takes, and shows what comes back only as an image.
func TestPageRenderer(t *testing.T) {
	code := script(t)
	for _, want := range []string{
		`const MERMAID_SRC = "` + mermaidSrc + `";`,
		`const MERMAID_SRI = "sha384-EOXBFmc3gx5mb+vn0vPvvGqACToJD24hhacX5Yx+8NUUQrHIle/Qi5Bg9o3zKwW2";`,
		`'<scr' + 'ipt src="' + MERMAID_SRC + '" integrity="' + MERMAID_SRI + '" crossorigin="anonymous"></scr' + 'ipt>'`,
		`'<scr' + 'ipt>' + document.getElementById("renderer").textContent + '</scr' + 'ipt>';`,
		`h("iframe", { sandbox: "allow-scripts", class: "renderer",`,
		`if (ev.source !== frame.contentWindow || ev.origin !== "null") return;`,
		`r.frame.contentWindow.postMessage({ type: "draw", id, source, theme }, "*");`,
		`if (!validSource(d.source)) {`,
		`h("img", { class: "diagram-img", src: "data:image/svg+xml;charset=utf-8," + encodeURIComponent(svg),`,
	} {
		if strings.Count(code, want) != 1 {
			t.Errorf("the script holds %q %d times, want once", want, strings.Count(code, want))
		}
	}
	// The page itself never runs Mermaid nor makes a script, and the source
	// is checked before it is sent.
	if m := regexp.MustCompile(`\bmermaid\.[A-Za-z]+\(|window\.mermaid|"script"|\.postMessage\(`).FindAllString(code, -1); len(m) != 1 || m[0] != ".postMessage(" {
		t.Errorf("the page runs Mermaid, makes a script or posts elsewhere: %q", m)
	}
	if strings.Index(code, "if (!validSource(d.source)) {") > strings.Index(code, "renderSVG(d.source, theme())") {
		t.Error("a source is drawn before it is checked")
	}
	frame := rendererScript(t)
	for _, want := range []string{
		`startOnLoad: false,`, `securityLevel: "strict",`, `htmlLabels: false,`, `flowchart: { htmlLabels: false },`,
		`if (ev.source !== parent ||`, `if (typeof m.source !== "string" || m.source.length > MAX_SOURCE) {`,
		`target.textContent = m.source;`, `const say = (m) => parent.postMessage(m, "*");`,
	} {
		if strings.Count(frame, want) != 1 {
			t.Errorf("the renderer holds %q %d times, want once", want, strings.Count(frame, want))
		}
	}
	if strings.Contains(frame, "</") {
		t.Error("the renderer block holds a closing tag")
	}
	// Mermaid's config comes from the renderer alone; a message gives
	// only colors and a face, each checked.
	calls := regexp.MustCompile(`mermaid\.[a-zA-Z]+\(`).FindAllString(frame, -1)
	if !slices.Equal(calls, []string{"mermaid.initialize(", "mermaid.run("}) ||
		!strings.Contains(frame, `mermaid.initialize(config(m.theme && typeof m.theme === "object" ? m.theme : {}));`) {
		t.Errorf("Mermaid called as %q", calls)
	}
}

// grammar is the page's grammar block.
type grammar struct {
	Headers map[string]struct{ Kind, Node, Edge string } `json:"headers"`
	Misread []struct {
		Pattern, Flags string
		Kinds, On      []string
	} `json:"misread"`
}

// valid is the page's validSource, in Go, over the grammar block.
func (g grammar) valid(t *testing.T, src string) bool {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	h, ok := g.Headers[lines[0]]
	if !ok || len(lines) < 2 {
		return false
	}
	compile := func(p, flags string) *regexp.Regexp {
		if strings.Contains(flags, "i") {
			p = "(?i)" + p
		}
		return regexp.MustCompile(p)
	}
	for _, line := range lines[1:] {
		matched := false
		for on, p := range map[string]string{"node": h.Node, "edge": h.Edge} {
			m := compile(p, "").FindStringSubmatch(line)
			if m == nil {
				continue
			}
			misread := false
			for _, x := range g.Misread {
				if slices.Contains(x.Kinds, h.Kind) && slices.Contains(x.On, on) && compile(x.Pattern, x.Flags).MatchString(m[1]) {
					misread = true
				}
			}
			matched = matched || !misread
		}
		if !matched {
			return false
		}
	}
	return true
}

// grammarBlock is the page's grammar block.
func grammarBlock(t *testing.T) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `<script type="application/json" id="grammar">`)
	block, _, ok2 := strings.Cut(rest, "</script>")
	if !ok || !ok2 {
		t.Fatal("the page has no grammar block")
	}
	return block
}

// grammarCorpus is what diagram.Mermaid writes for labels of every kind
// that Mermaid draws as written, as something else or not at all, on a
// node and on an edge, and each line of it bent toward what Mermaid
// would read as more than a label.
func grammarCorpus() []string {
	labels := []string{"Ready", "a", "review exit 0", "f(x) = a+b, c/d: e.g_h-i", strings.Repeat("x", 60), strings.Repeat("x", 61),
		"x direction LR", "go Direction  td", "a::b", "a, b", "a: b", "wrap:x", "NOWRAP: x", ":wrap:x", ":NoWrap: x", "x wrap: y",
		"a # b", "a;b", "a%%b", `a"b`, "a]b", "a|b", "a<b>", "a\u202eb", " a", "a ",
		"_a_", "__a__", "a _b_ c", "ab_", "_ab", "a_b_c", "x__y__z", "(_a_)", "www.example.com", "x WWW.a.com y",
		"see http://a.b/c now", "HTTPS://x", "ftp://x", "x://y", "x.com", "x:", "x :", ":", "a:b:", "go: on", "1. x", "- x"}
	var sources []string
	for _, kind := range diagram.Kinds {
		for i := range 2 * len(labels) {
			// Each label on a node, then on an edge.
			node, edge := labels[i%len(labels)], "Ready"
			if i >= len(labels) {
				node, edge = edge, node
			}
			d := &diagram.Diagram{Kind: kind, Title: "t", At: strings.Repeat("a", 40),
				Nodes: []diagram.Node{{ID: "N1", Label: node}, {ID: "N22", Label: "Other"}},
				Edges: []diagram.Edge{{ID: "E1", From: "N1", To: "N22", Label: edge}}}
			src := d.Mermaid()
			sources = append(sources, src)
			lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
			for i := 1; i < len(lines); i++ {
				for _, bent := range []string{lines[i] + " ", lines[i] + "\n    click N1 href \"javascript:x\"", "    %%{init: {}}%%",
					"    style N1 fill:#f00", "    N1:::c", strings.Replace(lines[i], "N1", "N0", 1), strings.Replace(lines[i], "    ", "  ", 1),
					"    N1 --> N22", "    N1 -.-> N22", ""} {
					sources = append(sources, strings.Join(slices.Concat(lines[:i], []string{bent}, lines[i+1:]), "\n")+"\n")
				}
			}
		}
	}
	return append(sources, "flowchart TD\n    N1[\"a\"]\n", "flowchart LR\n", "graph LR\n    N1[\"a\"]\n", "")
}

// The page's grammar block takes what diagram.ValidSource takes and
// nothing else, over grammarCorpus.
func TestPageGrammar(t *testing.T) {
	var g grammar
	if err := json.Unmarshal([]byte(grammarBlock(t)), &g); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{"flowchart LR": "component", "sequenceDiagram": "sequence", "stateDiagram-v2": "state"}
	for header, h := range g.Headers {
		if kinds[header] != h.Kind {
			t.Errorf("%s draws %s, want %s", header, h.Kind, kinds[header])
		}
	}
	sources := grammarCorpus()
	took := 0
	for _, src := range sources {
		want := diagram.ValidSource(src)
		if got := g.valid(t, src); got != want {
			t.Errorf("the page takes %v, ValidSource %v:\n%s", got, want, src)
		}
		if want {
			took++
		}
	}
	if took < len(diagram.Kinds)*3 {
		t.Errorf("only %d of %d sources valid: the corpus tests little", took, len(sources))
	}
}

// validSourceJS runs the page's own grammar code, cut from its script,
// over the sources in the JSON file argv[3], and prints its verdicts.
const validSourceJS = `"use strict";
const fs = require("fs");
const page = fs.readFileSync(process.argv[2], "utf8");
const sources = JSON.parse(fs.readFileSync(process.argv[3], "utf8"));
const script = page.split("\n<script>\n")[1].split("\n</script>")[0];
const grammar = page.split('<script type="application/json" id="grammar">')[1].split("</script>")[0];
const cut = (from, to) => {
  const i = script.indexOf(from);
  const j = script.indexOf(to, i);
  if (i < 0 || j < 0) throw new Error("the script has no " + from);
  return script.slice(i, j + to.length);
};
const code = cut("  const GRAMMAR_SPEC = ", "\n\n") + cut("  function validSource(src) {", "\n  }\n") + "return validSource;";
const document = { getElementById: (id) => ({ textContent: id === "grammar" ? grammar : "" }) };
const validSource = new Function("document", code)(document);
process.stdout.write(JSON.stringify(sources.map((s) => validSource(s))));
`

// The page's validSource itself, run by node, takes what
// diagram.ValidSource takes and nothing else, over grammarCorpus. The
// package build has node; elsewhere the test needs it on PATH.
func TestPageValidSource(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	dir := t.TempDir()
	runner, pageFile, corpusFile := filepath.Join(dir, "run.js"), filepath.Join(dir, "page.html"), filepath.Join(dir, "corpus.json")
	sources := grammarCorpus()
	data, _ := json.Marshal(sources)
	for file, content := range map[string][]byte{runner: []byte(validSourceJS), pageFile: agentplans.Page, corpusFile: data} {
		if err := os.WriteFile(file, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, runner, pageFile, corpusFile)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, stderr.String())
	}
	var got []bool
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(sources) {
		t.Fatalf("node's verdicts %s: %v", out, err)
	}
	took := 0
	for i, src := range sources {
		want := diagram.ValidSource(src)
		if got[i] != want {
			t.Errorf("validSource takes %v, ValidSource %v:\n%s", got[i], want, src)
		}
		if want {
			took++
		}
	}
	if took < len(diagram.Kinds)*3 || took > len(sources)/2 {
		t.Errorf("%d of %d sources valid: the corpus tests little", took, len(sources))
	}
}

// The page's tables are looked up by own key alone, so a value from a
// document, such as a plan whose STATUS is "constructor", finds no
// inherited property and falls back to its label instead of throwing.
// The check reads the script: every table literal is indexed only
// through own(), which asks Object.hasOwn.
func TestPageTables(t *testing.T) {
	code := script(t)
	if !regexp.MustCompile(`function own\(table, key, fallback\) \{\s*return Object\.hasOwn\(table, key\) \? table\[key\] : fallback;\s*\}`).MatchString(code) {
		t.Error("no own() asking Object.hasOwn")
	}
	tables := regexp.MustCompile(`const ([A-Z_]+) = \{`).FindAllStringSubmatch(code, -1)
	if len(tables) < 3 {
		t.Fatalf("tables %v", tables)
	}
	for _, m := range tables {
		if idx := regexp.MustCompile(`\b` + m[1] + `\s*\[`).FindString(code); idx != "" {
			t.Errorf("%s indexed directly: %s", m[1], idx)
		}
		if strings.Contains(code, m[1]+".") {
			t.Errorf("%s read by a property", m[1])
		}
	}
	if strings.Contains(code, "SPEC[") {
		t.Error("SPEC indexed by a value")
	}
	// Nothing shadows own() where a table is read.
	if m := regexp.MustCompile(`\b(?:const|let|var)\s+own\b|function\s+\w+\([^)]*\bown\b[^)]*\)|\(\s*own\s*\)\s*=>`).FindString(code); m != "" {
		t.Errorf("own shadowed: %s", m)
	}
}

// The page uses the capabilities it declares and no others, and its
// database is its owner's alone.
func TestPageCapabilities(t *testing.T) {
	var caps map[string]any
	if err := json.Unmarshal([]byte(agentplans.Capabilities), &caps); err != nil {
		t.Fatal(err)
	}
	var used []string
	for _, m := range regexp.MustCompile(`claude\.use\("([a-z]+)"\)`).FindAllStringSubmatch(script(t), -1) {
		if !slices.Contains(used, m[1]) {
			used = append(used, m[1])
		}
	}
	if n := strings.Count(script(t), ".use("); n != len(regexp.MustCompile(`claude\.use\("[a-z]+"\)`).FindAllString(script(t), -1)) {
		t.Errorf("a capability used by a name that is not a literal (%d uses)", n)
	}
	if got, want := slices.Sorted(slices.Values(used)), slices.Sorted(maps.Keys(caps)); !slices.Equal(got, want) {
		t.Errorf("the page uses %v, declares %v", got, want)
	}
	want := map[string]any{"rules": []any{map[string]any{"path": "", "read": "owner", "write": "owner"}}}
	if !reflect.DeepEqual(caps["db"], want) {
		t.Errorf("db %v, want owner-only %v", caps["db"], want)
	}
	if !reflect.DeepEqual(caps["comments"], map[string]any{}) {
		t.Errorf("comments %v, want {}", caps["comments"])
	}
}

// collections are the collections export writes, as Write lays them out.
func collections(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	if err := export.Write(dir, export.Export{}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// The page reads the collections export writes and those it writes
// itself, and writes only answers and dispatch documents.
func TestPageCollections(t *testing.T) {
	code := script(t)
	read := map[string]bool{}
	literal := regexp.MustCompile(`\.collection\("([a-z]+)"\)`)
	for _, m := range literal.FindAllStringSubmatch(code, -1) {
		read[m[1]] = true
	}
	if n := strings.Count(code, ".collection("); n != len(literal.FindAllString(code, -1)) {
		t.Errorf("a collection named by other than a literal (%d)", n)
	}
	docs := regexp.MustCompile(`db\.doc\("([a-z]+)/[a-z]+"\)`)
	for _, m := range docs.FindAllStringSubmatch(code, -1) {
		read[m[1]] = true
	}
	// Every other doc() is a write's, on answers or dispatch.
	write := regexp.MustCompile(`\.collection\("(answers|dispatch)"\)\.doc\(id\)`)
	if n := strings.Count(code, ".doc("); n != len(docs.FindAllString(code, -1))+len(write.FindAllString(code, -1)) {
		t.Errorf("a document named other than by a literal or a write's ID (%d)", n)
	}
	want := append(collections(t), "answers", "dispatch")
	if got := slices.Sorted(maps.Keys(read)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("the page reads %v, want %v", got, want)
	}
	// The database is written in save alone: one set on a reference to an
	// answers or dispatch document, and no verb chained on another.
	if n := strings.Count(code, "ref.set("); n != 1 || strings.Count(code, "ref = ") != 3 || !strings.Contains(code, "let ref = null;") ||
		len(regexp.MustCompile(`ref = db\.collection\("(answers|dispatch)"\)\.doc\(id\);`).FindAllString(code, -1)) != 2 {
		t.Errorf("writes other than save's one set on an answers or dispatch document")
	}
	if m := regexp.MustCompile(`\)\s*\.(set|update|delete|add|acquire)\(`).FindString(code); m != "" {
		t.Errorf("a write on a built reference: %s", m)
	}
	if m := regexp.MustCompile(`\bref\.(update|delete|add|acquire)\(|\bdb\.(?:[^dc]|d[^o]|c[^o])`).FindString(code); m != "" {
		t.Errorf("another database call: %s", m)
	}
	for _, m := range regexp.MustCompile(`save\("([a-z]+)"`).FindAllStringSubmatch(code, -1) {
		if m[1] != "answers" && m[1] != "dispatch" {
			t.Errorf("save to %s", m[1])
		}
	}
}

// spec is the schema block's form of typ: each JSON key's type, a slice
// of structs as a list of one.
func spec(t *testing.T, typ reflect.Type) map[string]any {
	t.Helper()
	out := map[string]any{}
	for i := range typ.NumField() {
		f := typ.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		switch f.Type.Kind() {
		case reflect.String:
			out[name] = "string"
		case reflect.Int:
			out[name] = "number"
		case reflect.Bool:
			out[name] = "boolean"
		case reflect.Slice:
			out[name] = []any{spec(t, f.Type.Elem())}
		default:
			t.Fatalf("%s.%s: %s", typ, f.Name, f.Type)
		}
	}
	return out
}

// The fields the page reads are those the host writes: the export's
// documents, and the answers and dispatch documents the session writes
// back, with the fields the page itself writes for the session to read.
func TestPageSchema(t *testing.T) {
	_, rest, _ := strings.Cut(page, `<script type="application/json" id="schema">`)
	block, _, _ := strings.Cut(rest, "</script>")
	var got map[string]any
	if err := json.Unmarshal([]byte(block), &got); err != nil {
		t.Fatal(err)
	}
	answers := spec(t, reflect.TypeFor[dashboard.AnswerDoc]())
	answers["text"] = "string"
	dispatch := spec(t, reflect.TypeFor[queue.Doc]())
	dispatch["anyway"] = "boolean"
	want := map[string]any{
		"plans":    spec(t, reflect.TypeFor[export.Plan]()),
		"github":   spec(t, reflect.TypeFor[export.GitHub]()),
		"meta":     spec(t, reflect.TypeFor[export.Sync]()),
		"today":    spec(t, reflect.TypeFor[export.Today]()),
		"answers":  answers,
		"dispatch": dispatch,
	}
	for _, c := range collections(t) {
		if want[c] == nil {
			t.Errorf("export writes %s, which the test does not know", c)
		}
	}
	if !reflect.DeepEqual(got, want) {
		g, _ := json.MarshalIndent(got, "", " ")
		w, _ := json.MarshalIndent(want, "", " ")
		t.Errorf("the page's schema\n%s\nwant\n%s", g, w)
	}
	// What the page writes carries the schema version it was built for.
	if !strings.Contains(script(t), "const SCHEMA = 1;") || agentplans.Schema != 1 {
		t.Errorf("the page writes another schema than %d", agentplans.Schema)
	}
}

// WritePage writes the page and says whether it differs from the page
// last published, which MarkPublished records only where the guard let a
// publish of that page through.
func TestWritePage(t *testing.T) {
	dir := t.TempDir()
	file, published := filepath.Join(dir, "page", "agent-plans.html"), filepath.Join(dir, "page", "published")
	publishing := filepath.Join(dir, "page", "publishing")
	letThrough := func(data []byte) {
		os.WriteFile(publishing, []byte(agentplans.PageHash(data)+"\n"), 0o600)
	}
	if changed, err := export.WritePage(file, published); err != nil || !changed {
		t.Fatalf("first: changed %v, %v", changed, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != page {
		t.Fatalf("the page written: %v", err)
	}
	if err := export.MarkPublished(file, publishing, published); err == nil {
		t.Error("recorded as published with no publish let through")
	}
	letThrough([]byte("another page"))
	if err := export.MarkPublished(file, publishing, published); err == nil {
		t.Error("recorded as published after another page's publish")
	}
	if changed, _ := export.WritePage(file, published); !changed {
		t.Error("a refused record counted")
	}
	letThrough(agentplans.Page)
	if err := export.MarkPublished(file, publishing, published); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(publishing); err == nil {
		t.Error("the publish let through was not used up")
	}
	if changed, err := export.WritePage(file, published); err != nil || changed {
		t.Errorf("after publishing: changed %v, %v", changed, err)
	}
	// A page changed on disk is put back, and what was published of it
	// does not count as this page.
	os.WriteFile(file, []byte("old"), 0o600)
	letThrough([]byte("old"))
	export.MarkPublished(file, publishing, published)
	if changed, err := export.WritePage(file, published); err != nil || !changed {
		t.Errorf("after an older page: changed %v, %v", changed, err)
	}
	if data, _ := os.ReadFile(file); string(data) != page {
		t.Error("the older page was kept")
	}
}
