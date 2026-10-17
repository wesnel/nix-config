package diagram

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
)

// Failure is an element whose citation does not hold, and why.
type Failure struct {
	Element string `json:"element"`
	Reason  string `json:"reason"`
}

// Result is what Check found: the elements that failed, and for each that
// passed, what was checked, for the critic to take from there.
type Result struct {
	Failures []Failure         `json:"failures"`
	Checked  map[string]string `json:"checked"`
}

// Check holds each element of d to the code it cites, as blob returns a
// file at d.At, and sets each citation's Hash. The path exists, the lines
// are in the file, and the symbol is a whole word in them. In a Go file,
// parsed from the blob and never built, a node's symbol is declared in its
// lines (a plain symbol names a function, T.M a method of T); a callflow
// edge's lines are inside the function its from node's own lines declare,
// in the same file, and call its symbol; and a state edge's
// lines assign its symbol. A callflow or state edge's symbol is its to
// node's; a call from a planned node is checked as text alone, and its
// Checked says so. A file over MaxBlob is refused. A planned element names
// an item planned reports, and with no planned, none is accepted. Nothing
// here runs the code or a tool of its repository.
func Check(d *Diagram, blob func(path string) ([]byte, error), planned func(id string) bool) Result {
	r := Result{Failures: []Failure{}, Checked: map[string]string{}}
	files := map[string]*source{}
	open := func(path string) (*source, error) {
		if s, ok := files[path]; ok {
			return s, s.err
		}
		s := &source{}
		data, err := blob(path)
		switch {
		case errors.Is(err, errTooBig) || err == nil && len(data) > MaxBlob:
			s.err = fmt.Errorf("%s is more than %d bytes at %s, more than a check reads", path, MaxBlob, d.At)
		case err != nil:
			s.err = fmt.Errorf("%s does not exist at %s", path, d.At)
		default:
			s.lines = splitLines(data)
			if strings.HasSuffix(path, ".go") {
				s.fset = token.NewFileSet()
				s.file, s.goErr = parser.ParseFile(s.fset, path, data, parser.SkipObjectResolution)
			}
		}
		files[path] = s
		return s, s.err
	}
	check := func(id string, c *Cite, p *string, edge *Edge) {
		fail := func(format string, args ...any) {
			r.Failures = append(r.Failures, Failure{Element: id, Reason: fmt.Sprintf(format, args...)})
		}
		if p != nil {
			switch {
			case planned == nil:
				fail("planned %s: a planned element is for a plan's diagram before the code, checked against its items", *p)
			case !planned(*p):
				fail("planned %s is not an item of the plan", *p)
			default:
				r.Checked[id] = "planned as " + *p
			}
			return
		}
		if edge != nil && (d.Kind == "callflow" || d.Kind == "state") {
			if to := d.Node(edge.To); to.Cite != nil && last(to.Cite.Symbol) != last(c.Symbol) {
				fail("symbol %s is not %s's, %s; a %s edge's symbol is its to node's", c.Symbol, edge.To, to.Cite.Symbol, d.Kind)
				return
			}
		}
		s, err := open(c.Path)
		if err != nil {
			fail("%v", err)
			return
		}
		if c.End() > len(s.lines) {
			fail("%s has %d lines at %s, so %d-%d are not in it", c.Path, len(s.lines), d.At, c.Line, c.End())
			return
		}
		span := s.lines[c.Line-1 : c.End()]
		if !wholeWord(last(c.Symbol)).MatchString(strings.Join(span, "\n")) {
			fail("%s is not a whole word in %s:%d-%d", last(c.Symbol), c.Path, c.Line, c.End())
			return
		}
		c.Hash = spanHash(span)
		what := fmt.Sprintf("%s is a word in %s:%d-%d", c.Symbol, c.Path, c.Line, c.End())
		if s.fset != nil {
			if s.goErr != nil {
				fail("%s does not parse as Go: %v", c.Path, s.goErr)
				return
			}
			var said, why string
			switch {
			case edge == nil:
				said, why = s.declared(c)
			case d.Kind == "callflow":
				said, why = s.calls(d, edge, c)
			case d.Kind == "state":
				said, why = s.assigns(c)
			}
			if why != "" {
				fail("%s", why)
				return
			}
			if said != "" {
				what = said
			}
		}
		r.Checked[id] = what
	}
	for i := range d.Nodes {
		n := &d.Nodes[i]
		check(n.ID, n.Cite, n.Planned, nil)
	}
	for i := range d.Edges {
		e := &d.Edges[i]
		check(e.ID, e.Cite, e.Planned, e)
	}
	return r
}

// source is a file at the diagram's commit, and, for Go, its syntax.
type source struct {
	lines []string
	err   error
	fset  *token.FileSet
	file  *ast.File
	goErr error
}

// splitLines is data's lines, without a last empty one after a final
// newline.
func splitLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// spanHash is the SHA-256 of lines, each trimmed, joined by newlines.
func spanHash(lines []string) string {
	var b bytes.Buffer
	for i, l := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimSpace(l))
	}
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:])
}

// last is a symbol's last part: M of T.M.
func last(symbol string) string {
	return symbol[strings.LastIndex(symbol, ".")+1:]
}

func wholeWord(word string) *regexp.Regexp {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_])` + regexp.QuoteMeta(word) + `([^A-Za-z0-9_]|$)`)
}

// line is the line of pos.
func (s *source) line(pos token.Pos) int { return s.fset.Position(pos).Line }

// within reports whether node starts and ends inside c's lines.
func (s *source) within(node ast.Node, c *Cite) bool {
	return s.line(node.Pos()) >= c.Line && s.line(node.End()) <= c.End()
}

// receiver is the name of a method's receiver type, or "".
func receiver(f *ast.FuncDecl) string {
	if f.Recv == nil || len(f.Recv.List) == 0 {
		return ""
	}
	t := f.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	switch t := t.(type) {
	case *ast.IndexExpr:
		return name(t.X)
	case *ast.IndexListExpr:
		return name(t.X)
	}
	return name(t)
}

func name(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// funcNamed reports whether f is the function symbol names: F, or T.M for
// a method M of T.
func funcNamed(f *ast.FuncDecl, symbol string) bool {
	if t, m, ok := strings.Cut(symbol, "."); ok {
		return receiver(f) == t && f.Name.Name == m
	}
	return f.Recv == nil && f.Name.Name == symbol
}

// declared says what declares c's symbol in its lines: the package
// clause, a function or method, a type, a constant or variable, a field or
// an interface method.
func (s *source) declared(c *Cite) (what, why string) {
	found := ""
	at := func(id *ast.Ident, kind string) {
		if found == "" && id != nil && s.line(id.Pos()) >= c.Line && s.line(id.Pos()) <= c.End() {
			found = kind
		}
	}
	if !strings.Contains(c.Symbol, ".") && s.file.Name.Name == c.Symbol {
		at(s.file.Name, "package")
	}
	ast.Inspect(s.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if funcNamed(n, c.Symbol) {
				at(n.Name, "func")
			}
		case *ast.TypeSpec:
			if n.Name.Name == c.Symbol {
				at(n.Name, "type")
			}
		case *ast.ValueSpec:
			for _, id := range n.Names {
				if id.Name == c.Symbol {
					at(id, "value")
				}
			}
		case *ast.Field:
			for _, id := range n.Names {
				if id.Name == c.Symbol {
					at(id, "field")
				}
			}
		}
		return found == ""
	})
	if found == "" {
		return "", fmt.Sprintf("%s is not declared in %s:%d-%d", c.Symbol, c.Path, c.Line, c.End())
	}
	return fmt.Sprintf("%s %s declared in %s:%d-%d", found, c.Symbol, c.Path, c.Line, c.End()), ""
}

// calls says where e's lines, inside the function its from node declares,
// call e's symbol.
func (s *source) calls(d *Diagram, e *Edge, c *Cite) (what, why string) {
	from := d.Node(e.From)
	if from.Cite == nil {
		return fmt.Sprintf("%s is a word in %s:%d-%d; its caller is planned, so the call itself is not checked",
			c.Symbol, c.Path, c.Line, c.End()), ""
	}
	if from.Cite.Path != c.Path {
		return "", fmt.Sprintf("a call from %s is in %s, where %s declares it, not in %s", e.From, from.Cite.Path, e.From, c.Path)
	}
	// The caller is the function the from node's own lines declare, by
	// receiver and name.
	var caller *ast.FuncDecl
	for _, decl := range s.file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Body != nil && funcNamed(f, from.Cite.Symbol) &&
			s.line(f.Name.Pos()) >= from.Cite.Line && s.line(f.Name.Pos()) <= from.Cite.End() {
			caller = f
		}
	}
	if caller == nil || s.line(caller.Body.Lbrace) > c.Line || s.line(caller.Body.Rbrace) < c.End() {
		return "", fmt.Sprintf("%s:%d-%d is not inside %s's body", c.Path, c.Line, c.End(), from.Cite.Symbol)
	}
	called := false
	ast.Inspect(caller.Body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && s.within(call.Fun, c) {
			switch f := call.Fun.(type) {
			case *ast.Ident:
				called = called || f.Name == last(c.Symbol)
			case *ast.SelectorExpr:
				called = called || f.Sel.Name == last(c.Symbol)
			}
		}
		return !called
	})
	if !called {
		return "", fmt.Sprintf("%s:%d-%d does not call %s", c.Path, c.Line, c.End(), c.Symbol)
	}
	return fmt.Sprintf("%s calls %s at %s:%d-%d", from.Cite.Symbol, c.Symbol, c.Path, c.Line, c.End()), ""
}

// assigns says where c's lines assign its symbol: an assignment, or a
// field of a composite literal.
func (s *source) assigns(c *Cite) (what, why string) {
	is := func(e ast.Expr) bool {
		switch e := e.(type) {
		case *ast.Ident:
			return e.Name == last(c.Symbol)
		case *ast.SelectorExpr:
			return e.Sel.Name == last(c.Symbol)
		}
		return false
	}
	found := false
	ast.Inspect(s.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for _, rhs := range n.Rhs {
				found = found || is(rhs) && s.within(rhs, c)
			}
		case *ast.KeyValueExpr:
			found = found || is(n.Value) && s.within(n.Value, c)
		}
		return !found
	})
	if !found {
		return "", fmt.Sprintf("%s:%d-%d does not assign %s", c.Path, c.Line, c.End(), c.Symbol)
	}
	return fmt.Sprintf("assigns %s at %s:%d-%d", c.Symbol, c.Path, c.Line, c.End()), ""
}
