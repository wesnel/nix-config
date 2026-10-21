package diagram

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const at = "0123456789abcdef0123456789abcdef01234567"

// draft is a state diagram as a drafter writes it, with its fields
// replaced as edit says.
func draft(t *testing.T, edit func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(`{"kind": "state", "title": "Queue entry states", "at": "`+at+`",
 "nodes": [
  {"id": "N1", "label": "Queued", "cite": {"path": "queue/queue.go", "line": 5, "endLine": null, "symbol": "Queued"}, "planned": null},
  {"id": "N2", "label": "Ready", "cite": {"path": "queue/queue.go", "line": 6, "endLine": null, "symbol": "Ready"}, "planned": null}],
 "edges": [
  {"id": "E1", "from": "N1", "to": "N2", "label": "review exit 0",
   "cite": {"path": "queue/run.go", "line": 9, "endLine": null, "symbol": "Ready"}, "planned": null}]}`), &m); err != nil {
		t.Fatal(err)
	}
	if edit != nil {
		edit(m)
	}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func node(m map[string]any, i int) map[string]any {
	return m["nodes"].([]any)[i].(map[string]any)
}

func edge(m map[string]any, i int) map[string]any {
	return m["edges"].([]any)[i].(map[string]any)
}

func TestParse(t *testing.T) {
	d, err := Parse(draft(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != "state" || d.Title != "Queue entry states" || len(d.Nodes) != 2 || len(d.Edges) != 1 ||
		d.Edges[0].Cite.Symbol != "Ready" || d.Nodes[0].Cite.EndLine != nil {
		t.Errorf("parsed %+v", d)
	}
	if got := d.IDs(); !slices.Equal(got, []string{"N1", "N2", "E1"}) {
		t.Errorf("IDs %v", got)
	}
}

// Every way a draft can break the schema is refused, naming what broke.
func TestParseRefuses(t *testing.T) {
	many := func(n int, prefix string) []any {
		var out []any
		for i := 1; i <= n; i++ {
			out = append(out, map[string]any{"id": prefix + strconv.Itoa(i), "label": "x",
				"cite": map[string]any{"path": "a.go", "line": 1, "endLine": nil, "symbol": "x"}, "planned": nil})
		}
		return out
	}
	for name, tc := range map[string]struct {
		edit func(m map[string]any)
		says string
	}{
		"an unknown kind":       {func(m map[string]any) { m["kind"] = "class" }, "kind"},
		"a short commit":        {func(m map[string]any) { m["at"] = "0123456" }, "at"},
		"an unknown field":      {func(m map[string]any) { m["click"] = "N1 callback" }, "click"},
		"a field on a node":     {func(m map[string]any) { node(m, 0)["href"] = "javascript:x" }, "href"},
		"a node ID":             {func(m map[string]any) { node(m, 0)["id"] = "N1;click" }, "N1;click"},
		"an edge ID":            {func(m map[string]any) { edge(m, 0)["id"] = "N9" }, "N9"},
		"a repeated ID":         {func(m map[string]any) { node(m, 1)["id"] = "N1" }, "N1"},
		"an edge to nowhere":    {func(m map[string]any) { edge(m, 0)["to"] = "N7" }, "N7"},
		"too many nodes":        {func(m map[string]any) { m["nodes"], m["edges"] = many(41, "N"), []any{} }, "40"},
		"no nodes":              {func(m map[string]any) { m["nodes"], m["edges"] = []any{}, []any{} }, "node"},
		"an empty label":        {func(m map[string]any) { node(m, 0)["label"] = "<>{}" }, "label"},
		"a long label":          {func(m map[string]any) { node(m, 0)["label"] = strings.Repeat("a", 61) }, "60"},
		"a path out":            {func(m map[string]any) { node(m, 0)["cite"].(map[string]any)["path"] = "../secret.go" }, "../secret.go"},
		"an absolute path":      {func(m map[string]any) { node(m, 0)["cite"].(map[string]any)["path"] = "/etc/passwd" }, "/etc/passwd"},
		"a path with a space":   {func(m map[string]any) { node(m, 0)["cite"].(map[string]any)["path"] = "a b.go" }, "a b.go"},
		"a symbol with markup":  {func(m map[string]any) { node(m, 0)["cite"].(map[string]any)["symbol"] = "<b>x" }, "<b>x"},
		"line zero":             {func(m map[string]any) { node(m, 0)["cite"].(map[string]any)["line"] = 0 }, "line"},
		"an end before a start": {func(m map[string]any) { node(m, 0)["cite"].(map[string]any)["endLine"] = 2 }, "endLine"},
		"a span too long":       {func(m map[string]any) { node(m, 0)["cite"].(map[string]any)["endLine"] = 500 }, "lines"},
		"cite and planned":      {func(m map[string]any) { node(m, 0)["planned"] = "P1.S3" }, "both"},
		"neither":               {func(m map[string]any) { node(m, 0)["cite"] = nil }, "neither"},
		"a planned ID":          {func(m map[string]any) { node(m, 0)["cite"], node(m, 0)["planned"] = nil, "S1 :x:" }, "S1 :x:"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(draft(t, tc.edit))
			if err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("err %v, want it to name %q", err, tc.says)
			}
		})
	}
	if _, err := Parse([]byte(`{"kind": "state"} {"kind": "state"}`)); err == nil {
		t.Error("two objects were accepted")
	}
	big := append(draft(t, nil), []byte(strings.Repeat(" ", MaxSource))...)
	if _, err := Parse(big); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("a source past the cap: %v", err)
	}
}

// A label keeps only its character set: whatever else a drafter, or text
// steering it, writes is dropped, never escaped, so neither Mermaid nor Org
// sees syntax in it.
func TestLabelsDropOtherCharacters(t *testing.T) {
	for in, want := range map[string]string{
		`Ready"] --> X[click N1 call alert()`:   "Ready -- Xclick N1 call alert()",
		"a\nclick N1 href \"javascript:x\"":     "aclick N1 href javascript:x",
		"%%{init: {'securityLevel':'loose'}}%%": "init: securityLevel:loose",
		"<img src=x onerror=alert(1)>":          "img src=x onerror=alert(1)",
		"#+end_src":                             "+end_src",
		"a # comment":                           "a comment",
		"[[file:/etc/passwd][x]]":               "file:/etc/passwdx",
		"a |b| `c` ;d;":                         "a b c d",
		"  spaced\t\tout  ":                     "spacedout",
		"Ünïcode ✓ ready":                       "ncode ready",
	} {
		if got := CleanLabel(in); got != want {
			t.Errorf("CleanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMermaid(t *testing.T) {
	hostile := func(m map[string]any) {
		node(m, 0)["label"] = `Queued"] --> X[click N1 call alert()`
		edge(m, 0)["label"] = "exit 0|\n%%{init: {}}%% 1"
	}
	for kind, want := range map[string]string{
		"state": "stateDiagram-v2\n" +
			"    state \"Queued -- Xclick N1 call alert()\" as N1\n" +
			"    state \"Ready\" as N2\n" +
			"    N1 --> N2: exit 0init: 1\n",
		"callflow": "flowchart LR\n" +
			"    N1[\"Queued -- Xclick N1 call alert()\"]\n" +
			"    N2[\"Ready\"]\n" +
			"    N1 -->|\"exit 0init: 1\"| N2\n",
		"component": "flowchart LR\n" +
			"    N1[\"Queued -- Xclick N1 call alert()\"]\n" +
			"    N2[\"Ready\"]\n" +
			"    N1 -->|\"exit 0init: 1\"| N2\n",
		"sequence": "sequenceDiagram\n" +
			"    participant N1 as Queued -- Xclick N1 call alert()\n" +
			"    participant N2 as Ready\n" +
			"    N1->>N2: exit 0init: 1\n",
	} {
		t.Run(kind, func(t *testing.T) {
			d, err := Parse(draft(t, func(m map[string]any) { hostile(m); m["kind"] = kind }))
			if err != nil {
				t.Fatal(err)
			}
			got := d.Mermaid()
			if strings.TrimSpace(got) != strings.TrimSpace(want) {
				t.Errorf("Mermaid:\n%s\nwant:\n%s", got, want)
			}
			if !ValidSource(got) {
				t.Errorf("its own source does not match the grammar:\n%s", got)
			}
		})
	}
}

// ValidSource takes only the grammar Mermaid emits: a source written or
// edited by anyone else, with a directive, a click or a style, fails.
func TestValidSource(t *testing.T) {
	for _, src := range []string{
		"flowchart LR\n    N1[\"a\"]\n    click N1 href \"javascript:alert(1)\"\n",
		"%%{init: {\"securityLevel\": \"loose\"}}%%\nflowchart LR\n    N1[\"a\"]\n",
		"flowchart LR\n    N1[\"a\"]\n    style N1 fill:#f00\n",
		"flowchart LR\n    N1[\"<img src=x onerror=alert(1)>\"]\n",
		"sequenceDiagram\n    N1[\"a\"]\n",
		"flowchart LR\n    N1 -->|\"a\"| N2\n    N3[\"" + strings.Repeat("a", 61) + "\"]\n",
		"stateDiagram-v2\n    N1->>N2: a\n",
		"",
		// What Mermaid 11.17.2's lexers read as other statements.
		"sequenceDiagram\n    participant N1 as a # b\n",
		"sequenceDiagram\n    participant N1 as a, b\n",
		"sequenceDiagram\n    participant N1 as a: b\n",
		"sequenceDiagram\n    participant N1 as a\n    participant N2 as b\n    N1->>N2: wrap: x\n",
		"stateDiagram-v2\n    state \"x direction LR\" as N1\n",
		"stateDiagram-v2\n    state \"a\" as N1\n    state \"b\" as N2\n    N1 --> N2: a::b\n",
		"flowchart LR\n    N1[\"go Direction TB\"]\n",
		"sequenceDiagram\n    participant N1 as a\n    participant N2 as b\n    N1->>N2: :wrap:x\n",
		"stateDiagram-v2\n    state \"_a_\" as N1\n",
		"stateDiagram-v2\n    state \"see www.a.com\" as N1\n",
		"stateDiagram-v2\n    state \"a\" as N1\n    state \"b\" as N2\n    N1 --> N2: go:\n",
	} {
		if ValidSource(src) {
			t.Errorf("accepted:\n%s", src)
		}
	}
}

// Labels a lexer of Mermaid 11.17.2 would read as another statement, or cut
// short, are refused for the kinds where it would.
func TestLabelsMermaidKeywords(t *testing.T) {
	for name, tc := range map[string]struct {
		kind string
		edit func(m map[string]any)
	}{
		"a direction in a state":         {"state", func(m map[string]any) { node(m, 0)["label"] = "x direction LR" }},
		"a direction in a flowchart":     {"component", func(m map[string]any) { edge(m, 0)["label"] = "go DIRECTION td" }},
		"a class operator":               {"state", func(m map[string]any) { edge(m, 0)["label"] = "a::b" }},
		"a comma in a participant":       {"sequence", func(m map[string]any) { node(m, 0)["label"] = "a, b" }},
		"a colon in a participant":       {"sequence", func(m map[string]any) { node(m, 0)["label"] = "a: b" }},
		"a wrap directive in a message":  {"sequence", func(m map[string]any) { edge(m, 0)["label"] = "nowrap: x" }},
		"a wrap directive after a colon": {"sequence", func(m map[string]any) { edge(m, 0)["label"] = ":Wrap: x" }},
		"emphasis in a state":            {"state", func(m map[string]any) { node(m, 0)["label"] = "a _b_ c" }},
		"strong emphasis in a state":     {"state", func(m map[string]any) { edge(m, 0)["label"] = "__a__" }},
		"an underscore ending a word":    {"state", func(m map[string]any) { node(m, 0)["label"] = "ab_" }},
		"a web address in a state":       {"state", func(m map[string]any) { node(m, 0)["label"] = "x www.a.com y" }},
		"a URL in a transition":          {"state", func(m map[string]any) { edge(m, 0)["label"] = "see HTTPS://a.b/c" }},
		"an FTP URL in a state":          {"state", func(m map[string]any) { node(m, 0)["label"] = "ftp://x" }},
		"a transition ending in a colon": {"state", func(m map[string]any) { edge(m, 0)["label"] = "x :" }},
		"a transition of a colon alone":  {"state", func(m map[string]any) { edge(m, 0)["label"] = ":" }},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(draft(t, func(m map[string]any) { tc.edit(m); m["kind"] = tc.kind })); err == nil {
				t.Error("accepted")
			}
		})
	}
	// The same text is a plain label where no lexer reads it, and an
	// underscore inside a word is no emphasis.
	for kind, labels := range map[string][2]string{"component": {"a, b: c", "see www.a.com _a_ x:"},
		"state": {"a_b_c and x__y__z", "x:"}, "sequence": {"a_b", "say wrap: x"}} {
		if _, err := Parse(draft(t, func(m map[string]any) {
			node(m, 0)["label"], edge(m, 0)["label"], m["kind"] = labels[0], labels[1], kind
			if kind == "state" {
				node(m, 0)["label"], edge(m, 0)["label"] = labels[1], labels[0]
			}
		})); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

// The canonical JSON is the one source of the diagram, and its hash names
// it: the same diagram written with other spacing or field order hashes
// the same.
func TestCanonical(t *testing.T) {
	a, err := Parse(draft(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(draft(t, nil), &m)
	indented, _ := json.MarshalIndent(m, "", "\t")
	b, err := Parse(indented)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() != b.Hash() || len(a.Hash()) != 64 {
		t.Errorf("hashes %s and %s", a.Hash(), b.Hash())
	}
	c, _ := Parse(draft(t, func(m map[string]any) { node(m, 0)["label"] = "Waiting" }))
	if c.Hash() == a.Hash() {
		t.Error("a changed label kept the hash")
	}
	again, err := Parse(a.JSON())
	if err != nil || again.Hash() != a.Hash() {
		t.Errorf("the canonical JSON does not parse to itself: %v", err)
	}
	for _, line := range strings.Split(string(a.JSON()), "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "#") {
			t.Errorf("a line of the JSON could start Org syntax: %q", line)
		}
	}
}

// The schema a drafter is given names the fields Parse takes, and no more.
func TestSchema(t *testing.T) {
	var s struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Items struct {
				Required []string `json:"required"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(Schema, &s); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(s.Required, []string{"kind", "title", "at", "nodes", "edges"}) ||
		!slices.Equal(s.Properties["nodes"].Items.Required, []string{"id", "label", "cite", "planned"}) ||
		!slices.Equal(s.Properties["edges"].Items.Required, []string{"id", "from", "to", "label", "cite", "planned"}) {
		t.Errorf("schema %+v", s)
	}
	if !strings.Contains(string(Schema), `"additionalProperties": false`) {
		t.Error("the schema lets a drafter add properties")
	}
}
