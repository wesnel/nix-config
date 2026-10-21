// Package diagram is a diagram of code: the JSON a model drafts against
// Schema, which Parse holds to it and Check holds to the code it cites,
// and the one Mermaid source Mermaid renders from it. A model never writes Mermaid: what reaches a renderer
// is built here from IDs this package numbers and labels of a character
// set that can form neither Mermaid nor Org syntax.
package diagram

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Schema is the JSON Schema a drafter's reply must fit, in the strict
// subset Codex's --output-schema takes: every property required, the
// optional ones nullable. A cite's hash is Check's, never the drafter's.
//
//go:embed schema.json
var Schema []byte

// Limits a diagram is held to.
const (
	MaxNodes = 40
	MaxEdges = 60
	// MaxLabel is in runes, after CleanLabel.
	MaxLabel = 60
	// MaxSpan is the most lines one citation may cover.
	MaxSpan = 200
	maxPath = 200
	// MaxSource is the most bytes of JSON Parse reads, and MaxBlob of a
	// cited file a check reads.
	MaxSource = 1 << 20
	MaxBlob   = 1 << 20
)

// Kinds are the kinds of diagram, in the order a drafter tests for them.
var Kinds = []string{"state", "sequence", "callflow", "component"}

// Cite is the code an element stands for, at the diagram's commit: lines
// Line to EndLine (Line alone where it is nil) of Path, in which Symbol
// is a whole word. Hash is the SHA-256 of those lines, each trimmed, which
// Check sets.
type Cite struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	EndLine *int   `json:"endLine"`
	Symbol  string `json:"symbol"`
	Hash    string `json:"hash,omitempty"`
}

// End is the last line the citation covers.
func (c *Cite) End() int {
	if c.EndLine == nil {
		return c.Line
	}
	return *c.EndLine
}

// Node is a box, participant or state. It has a Cite, or in a plan's
// diagram drawn before the code, Planned: the ID of the plan item that
// adds it.
type Node struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Cite    *Cite   `json:"cite"`
	Planned *string `json:"planned"`
}

// Edge is an arrow from one node to another: a dependency, a call, a
// message or a transition. A sequence's edges are in the order they
// happen.
type Edge struct {
	ID      string  `json:"id"`
	From    string  `json:"from"`
	To      string  `json:"to"`
	Label   string  `json:"label"`
	Cite    *Cite   `json:"cite"`
	Planned *string `json:"planned"`
}

// Diagram is one diagram of the code at commit At.
type Diagram struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
	At    string `json:"at"`
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

var (
	nodeID    = regexp.MustCompile(`^N[1-9][0-9]{0,2}$`)
	edgeID    = regexp.MustCompile(`^E[1-9][0-9]{0,2}$`)
	commit    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pathChars = regexp.MustCompile(`^[A-Za-z0-9._/@+-]+$`)
	// symbol is an identifier, or a type's and its method's.
	symbol = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,79}(\.[A-Za-z_][A-Za-z0-9_]{0,79})?$`)
	// planned is a plan item's ID, a step's or a part's: S3, P1, P1.S3.
	planned   = regexp.MustCompile(`^(P[0-9]{1,4}\.)?[A-Z][0-9]{1,4}$`)
	hash      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	labelRune = func(r rune) bool {
		return r < utf8.RuneSelf && (r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
			strings.ContainsRune(" _.,:/()+=-", r))
	}
	// Text Mermaid 11.17.2's lexers read as another statement wherever it
	// stands on a line (a direction), as a class operator in a state
	// transition, as the end of a participant's alias, and as a message's
	// wrap directive (extractWrap's `^:?wrap:` and `^:?nowrap:`).
	direction  = regexp.MustCompile(`(?i)direction\s+(tb|td|bt|rl|lr)`)
	classOp    = regexp.MustCompile(`::`)
	aliasEnd   = regexp.MustCompile(`[,:]`)
	wrapPrefix = regexp.MustCompile(`(?i)^:?(no)?wrap:`)
	// A state diagram's labels go through Markdown, which reads an
	// underscore opening or closing a word as emphasis and a web address
	// as a link, changing the text drawn; and a transition's label that
	// ends with a colon fails to parse.
	emphasis = regexp.MustCompile(`(^|[^A-Za-z0-9_])_|_($|[^A-Za-z0-9_])`)
	autolink = regexp.MustCompile(`(?i)www\.|https?://|ftp://`)
	colonEnd = regexp.MustCompile(`:$`)
)

// CleanLabel is s with every character outside a label's set dropped, runs
// of spaces made one, and the ends trimmed. The set, letters, digits and
// ` _.,:/()+=-`, holds no quote, bracket, brace, pipe, angle bracket,
// semicolon, percent sign, hash, backtick or line break, so a label can
// close no Mermaid string, start no directive or comment, and break no Org
// line.
func CleanLabel(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case !labelRune(r):
		case r == ' ':
			space = b.Len() > 0
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Parse reads a diagram, refusing one that does not fit Schema or breaks a
// limit, and cleans its labels. Its error names every problem.
func Parse(data []byte) (*Diagram, error) {
	if len(data) > MaxSource {
		return nil, fmt.Errorf("the diagram is %d bytes, more than %d", len(data), MaxSource)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var d Diagram
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("the diagram is not the JSON its schema describes: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the diagram is followed by more than one JSON object")
	}
	var problems []string
	bad := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if !contains(Kinds, d.Kind) {
		bad("kind %q is not one of %s", d.Kind, strings.Join(Kinds, ", "))
	}
	if !commit.MatchString(d.At) {
		bad("at %q is not a full commit ID", d.At)
	}
	d.Title = label("the title", d.Title, bad)
	switch {
	case len(d.Nodes) == 0:
		bad("a diagram has at least one node")
	case len(d.Nodes) > MaxNodes:
		bad("%d nodes is more than %d", len(d.Nodes), MaxNodes)
	}
	if len(d.Edges) > MaxEdges {
		bad("%d edges is more than %d", len(d.Edges), MaxEdges)
	}
	seen := map[string]bool{}
	id := func(id string, pattern *regexp.Regexp, what string) {
		switch {
		case !pattern.MatchString(id):
			bad("%q is not a %s ID", id, what)
		case seen[id]:
			bad("the ID %s is used twice", id)
		}
		seen[id] = true
	}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		id(n.ID, nodeID, "node (N1, N2, ...)")
		n.Label = label(n.ID, n.Label, bad)
		lexed(n.ID, d.Kind, true, n.Label, bad)
		origin(n.ID, n.Cite, n.Planned, bad)
	}
	nodes := map[string]bool{}
	for _, n := range d.Nodes {
		nodes[n.ID] = true
	}
	for i := range d.Edges {
		e := &d.Edges[i]
		id(e.ID, edgeID, "edge (E1, E2, ...)")
		for _, end := range []string{e.From, e.To} {
			if !nodes[end] {
				bad("%s: %q is not a node of the diagram", e.ID, end)
			}
		}
		e.Label = label(e.ID, e.Label, bad)
		lexed(e.ID, d.Kind, false, e.Label, bad)
		origin(e.ID, e.Cite, e.Planned, bad)
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("the diagram does not fit its schema: %s", strings.Join(problems, "; "))
	}
	return &d, nil
}

// label is s cleaned, refused where nothing or too much is left.
func label(what, s string, bad func(string, ...any)) string {
	s = CleanLabel(s)
	switch n := utf8.RuneCountInString(s); {
	case n == 0:
		bad("%s has no label left of letters, digits and ` _.,:/()+=-`", what)
	case n > MaxLabel:
		bad("%s's label is %d characters, more than %d", what, n, MaxLabel)
	}
	return s
}

// lexed refuses a label of a diagram of kind, a node's or an edge's, that
// Mermaid's lexer for that kind would read as something else; see
// lexerProblem.
func lexed(what, kind string, node bool, s string, bad func(string, ...any)) {
	if why := lexerProblem(kind, node, s); why != "" {
		bad("%s's label %q %s; reword it", what, s, why)
	}
}

// lexerProblem says how Mermaid 11.17.2 would misread label s, a node's or
// an edge's, in a diagram of kind, or "".
func lexerProblem(kind string, node bool, s string) string {
	switch {
	case direction.MatchString(s):
		return "reads as a direction statement"
	case kind == "state" && classOp.MatchString(s):
		return "holds `::`, a class operator"
	case kind == "sequence" && node && aliasEnd.MatchString(s):
		return "holds `,` or `:`, which end a participant's alias"
	case kind == "sequence" && !node && wrapPrefix.MatchString(s):
		return "starts with a wrap directive"
	case kind == "state" && emphasis.MatchString(s):
		return "has `_` opening or closing a word, which Markdown reads as emphasis"
	case kind == "state" && autolink.MatchString(s):
		return "holds a web address, which Markdown makes a link"
	case kind == "state" && !node && colonEnd.MatchString(s):
		return "ends with `:`, which a transition's label cannot"
	}
	return ""
}

// origin holds an element to one citation or one planned item.
func origin(what string, c *Cite, p *string, bad func(string, ...any)) {
	switch {
	case c != nil && p != nil:
		bad("%s has both a cite and planned; give one", what)
	case c == nil && p == nil:
		bad("%s has neither a cite nor planned", what)
	case p != nil && !planned.MatchString(*p):
		bad("%s: planned %q is not a plan item's ID, such as P1.S3", what, *p)
	case c != nil:
		clean := path.Clean(c.Path)
		switch {
		case !pathChars.MatchString(c.Path) || len(c.Path) > maxPath:
			bad("%s: path %q is not a repository path of letters, digits and `._/@+-`", what, c.Path)
		case clean != c.Path || strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../"):
			bad("%s: path %q is not a clean path relative to the top of the repository", what, c.Path)
		}
		if !symbol.MatchString(c.Symbol) {
			bad("%s: symbol %q is not an identifier, or a type's and its method's (T.M)", what, c.Symbol)
		}
		switch {
		case c.Line < 1:
			bad("%s: line %d is not a line, which count from 1", what, c.Line)
		case c.EndLine != nil && *c.EndLine < c.Line:
			bad("%s: endLine %d comes before line %d", what, *c.EndLine, c.Line)
		case c.End()-c.Line+1 > MaxSpan:
			bad("%s cites %d lines, more than %d", what, c.End()-c.Line+1, MaxSpan)
		}
		if c.Hash != "" && !hash.MatchString(c.Hash) {
			bad("%s: hash %q is not a SHA-256", what, c.Hash)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// IDs are the diagram's element IDs, nodes then edges.
func (d *Diagram) IDs() []string {
	var out []string
	for _, n := range d.Nodes {
		out = append(out, n.ID)
	}
	for _, e := range d.Edges {
		out = append(out, e.ID)
	}
	return out
}

// Node is the node with id, or nil.
func (d *Diagram) Node(id string) *Node {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			return &d.Nodes[i]
		}
	}
	return nil
}

// JSON is the diagram's one source, indented. Every line starts with
// indentation then a quote or a bracket, so no line of it is Org syntax.
func (d *Diagram) JSON() []byte {
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		panic(err)
	}
	return data
}

// Hash names the diagram: the SHA-256 of its compact JSON, the same
// however the JSON it was parsed from was laid out.
func (d *Diagram) Hash() string {
	data, err := json.Marshal(d)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Clone is a deep copy of d.
func (d *Diagram) Clone() *Diagram {
	c, err := Parse(d.JSON())
	if err != nil {
		panic(err)
	}
	return c
}

const indent = "    "

// Mermaid is the diagram's Mermaid source, in the one grammar ValidSource
// takes.
func (d *Diagram) Mermaid() string {
	var b strings.Builder
	switch d.Kind {
	case "sequence":
		b.WriteString("sequenceDiagram\n")
		for _, n := range d.Nodes {
			fmt.Fprintf(&b, "%sparticipant %s as %s\n", indent, n.ID, n.Label)
		}
		for _, e := range d.Edges {
			fmt.Fprintf(&b, "%s%s->>%s: %s\n", indent, e.From, e.To, e.Label)
		}
	case "state":
		b.WriteString("stateDiagram-v2\n")
		for _, n := range d.Nodes {
			fmt.Fprintf(&b, "%sstate \"%s\" as %s\n", indent, n.Label, n.ID)
		}
		for _, e := range d.Edges {
			fmt.Fprintf(&b, "%s%s --> %s: %s\n", indent, e.From, e.To, e.Label)
		}
	default:
		b.WriteString("flowchart LR\n")
		for _, n := range d.Nodes {
			fmt.Fprintf(&b, "%s%s[\"%s\"]\n", indent, n.ID, n.Label)
		}
		for _, e := range d.Edges {
			fmt.Fprintf(&b, "%s%s -->|\"%s\"| %s\n", indent, e.From, e.Label, e.To)
		}
	}
	return b.String()
}

// line is one line of the grammar: its pattern, whose group 1 is the
// label, and whether it draws a node or an edge.
type line struct {
	pattern *regexp.Regexp
	node    bool
}

// The grammar Mermaid emits, by header, with the kind each header draws:
// each line after it must match one of its lines.
var grammar = func() map[string][]line {
	const id, l = `N[1-9][0-9]{0,2}`, `([A-Za-z0-9_.,:/()+=-](?:[A-Za-z0-9 _.,:/()+=-]{0,58}[A-Za-z0-9_.,:/()+=-])?)`
	compile := func(node, edge string) []line {
		return []line{{regexp.MustCompile(`^` + indent + node + `$`), true}, {regexp.MustCompile(`^` + indent + edge + `$`), false}}
	}
	return map[string][]line{
		"flowchart LR":    compile(id+`\["`+l+`"\]`, id+` -->\|"`+l+`"\| `+id),
		"sequenceDiagram": compile(`participant `+id+` as `+l, id+`->>`+id+`: `+l),
		"stateDiagram-v2": compile(`state "`+l+`" as `+id, id+` --> `+id+`: `+l),
	}
}()

// kindOf is the kind whose lexer reads a source under each header; a
// flowchart's are all one lexer's.
var kindOf = map[string]string{"flowchart LR": "component", "sequenceDiagram": "sequence", "stateDiagram-v2": "state"}

// ValidSource reports whether src is in the grammar Mermaid emits: a
// header, then nodes and edges with numbered IDs and labels of a label's
// character set that its lexer reads as labels. A page checks this before
// it renders a source, so that no directive, click, style or link reaches
// the renderer.
func ValidSource(src string) bool {
	lines := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	grammarLines, ok := grammar[lines[0]]
	if !ok || len(lines) < 2 {
		return false
	}
	for _, text := range lines[1:] {
		matched := false
		for _, g := range grammarLines {
			if m := g.pattern.FindStringSubmatch(text); m != nil && lexerProblem(kindOf[lines[0]], g.node, m[1]) == "" {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
