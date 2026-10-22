package export_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/export"
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
	for _, bad := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "srcdoc",
		"DOMParser", "createContextualFragment", "eval(", "Function(", "setTimeout(\"", "javascript:", "<iframe",
		"<link", "@import", "localStorage", "window.open"} {
		if strings.Contains(page, bad) {
			t.Errorf("the page holds %q", bad)
		}
	}
	if m := regexp.MustCompile(`(?i)<[a-z][^>]*\son[a-z]+\s*=`).FindString(page); m != "" {
		t.Errorf("an inline event handler: %s", m)
	}
	if m := regexp.MustCompile(`(?i)\.on[a-z]+\s*=[^=]`).FindString(script(t)); m != "" {
		t.Errorf("a handler set as a property: %s", m)
	}
	tags := regexp.MustCompile(`<script[^>]*>`).FindAllString(page, -1)
	if !slices.Equal(tags, []string{`<script type="application/json" id="schema">`, `<script>`}) {
		t.Errorf("script tags %q", tags)
	}
	// The artifact publishes no files, so the page loads none: no font,
	// no image.
	for _, bad := range []string{"url(", "@font-face"} {
		if strings.Contains(page, bad) {
			t.Errorf("the page loads a file: %s", bad)
		}
	}
	for _, s := range regexp.MustCompile(`"https?://[^"]*"`).FindAllString(page, -1) {
		if s != `"https://github.com/"` {
			t.Errorf("the page links to %s", s)
		}
	}
	// Attributes come from h alone, which sets each by a name the page's
	// own code gives; the one other assignment of markup is textContent.
	if n := strings.Count(script(t), ".setAttribute("); n != 1 {
		t.Errorf("%d calls of setAttribute, want h's one", n)
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
	if len(tables) < 2 {
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

// The page uses the capabilities it declares and no others, its database
// is its owner's alone, and it asks for no comments.
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
	if _, ok := caps["comments"]; ok || len(caps) != 1 {
		t.Errorf("capabilities %v, want the database alone", caps)
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

// The page reads the collections export writes, and makes no database
// write.
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
	if n := strings.Count(code, ".doc("); n != len(docs.FindAllString(code, -1)) {
		t.Errorf("a document named other than by a literal (%d)", n)
	}
	want := collections(t)
	if got := slices.Sorted(maps.Keys(read)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("the page reads %v, want %v", got, want)
	}
	// Of the database, the page builds what it watches with collection,
	// doc and limit, passes it straight to watchCollection or watchDoc, and
	// calls onSnapshot on it there: nothing writes.
	if m := regexp.MustCompile(`\)\s*\.(set|update|delete|add|acquire)\(`).FindString(code); m != "" {
		t.Errorf("a write on a built reference: %s", m)
	}
	if m := regexp.MustCompile(`\bdb\.(?:[^dc]|d[^o]|c[^o])`).FindString(code); m != "" {
		t.Errorf("another database call: %s", m)
	}
	if regexp.MustCompile(`=\s*db\.`).MatchString(code) {
		t.Error("a database reference kept in a variable")
	}
	for _, m := range regexp.MustCompile(`\b(?:query|ref)\.(\w+)\(`).FindAllStringSubmatch(code, -1) {
		if m[1] != "onSnapshot" {
			t.Errorf("a call of %s on a watched reference", m[1])
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

// The fields the page reads are those the host's export writes.
func TestPageSchema(t *testing.T) {
	_, rest, _ := strings.Cut(page, `<script type="application/json" id="schema">`)
	block, _, _ := strings.Cut(rest, "</script>")
	var got map[string]any
	if err := json.Unmarshal([]byte(block), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"plans":  spec(t, reflect.TypeFor[export.Plan]()),
		"github": spec(t, reflect.TypeFor[export.GitHub]()),
		"meta":   spec(t, reflect.TypeFor[export.Sync]()),
		"today":  spec(t, reflect.TypeFor[export.Today]()),
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
}

// WritePage writes the page and says whether it differs from the page
// last published, which MarkPublished records.
func TestWritePage(t *testing.T) {
	dir := t.TempDir()
	file, published := filepath.Join(dir, "page", "agent-plans.html"), filepath.Join(dir, "page", "published")
	if changed, err := export.WritePage(file, published); err != nil || !changed {
		t.Fatalf("first: changed %v, %v", changed, err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != page {
		t.Fatalf("the page written: %v", err)
	}
	if changed, _ := export.WritePage(file, published); !changed {
		t.Error("unpublished, the page does not need publishing")
	}
	if err := export.MarkPublished(file, published); err != nil {
		t.Fatal(err)
	}
	if changed, err := export.WritePage(file, published); err != nil || changed {
		t.Errorf("after publishing: changed %v, %v", changed, err)
	}
	// A page changed on disk is put back, and what was published of it
	// does not count as this page.
	os.WriteFile(file, []byte("old"), 0o600)
	export.MarkPublished(file, published)
	if changed, err := export.WritePage(file, published); err != nil || !changed {
		t.Errorf("after an older page: changed %v, %v", changed, err)
	}
	if data, _ := os.ReadFile(file); string(data) != page {
		t.Error("the older page was kept")
	}
}
