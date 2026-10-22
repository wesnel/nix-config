package export_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/diagram"
)

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
