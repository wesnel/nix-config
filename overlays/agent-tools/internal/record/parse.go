package record

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DriftExit is the exit status of a command refused because a record failed
// its structure check.
const DriftExit = 5

// DriftError is a record that is not what the renderer writes, which only a
// hand edit makes. Every write to it is refused until the operator fixes or
// deletes the file.
type DriftError struct {
	Path string
	Line int
	Part string // the heading above the line, or "the file's header"
	Why  string
}

func (e *DriftError) Error() string {
	path := e.Path
	if path == "" {
		path = "the record"
	}
	return fmt.Sprintf("%s fails its structure check at line %d, in %s: %s. Nothing was written to it; "+
		"the operator fixes it in Emacs or deletes it (a range's record is remade by reviewing the range again, "+
		"a pull request's threads by `pr-loop threads`, though its unposted drafts are lost)", path, e.Line, e.Part, e.Why)
}

// ParseRange reads a range's record. A file that is not exactly what Render
// writes for the values it holds fails with a *DriftError.
func ParseRange(data []byte) (*Range, error) {
	p := newParser(data)
	r := &Range{}
	if err := p.rangeRecord(r); err != nil {
		return nil, err
	}
	return r, p.same(r.Render())
}

// ParsePR reads a pull request's record, as ParseRange does.
func ParsePR(data []byte) (*PR, error) {
	p := newParser(data)
	pr := &PR{}
	if err := p.prRecord(pr); err != nil {
		return nil, err
	}
	return pr, p.same(pr.Render())
}

type parser struct {
	data  []byte
	lines []string
	i     int
	part  string
}

func newParser(data []byte) *parser {
	text := string(data)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	return &parser{data: data, lines: lines, part: "the file's header"}
}

func (p *parser) fail(format string, args ...any) error {
	return &DriftError{Line: p.i + 1, Part: p.part, Why: fmt.Sprintf(format, args...)}
}

func (p *parser) done() bool { return p.i >= len(p.lines) }

func (p *parser) peek() string {
	if p.done() {
		return ""
	}
	return p.lines[p.i]
}

func (p *parser) expect(want string) error {
	if p.done() {
		return p.fail("the file ends where %q belongs", want)
	}
	if got := p.lines[p.i]; got != want {
		if strings.HasPrefix(got, "*") {
			p.part = got
		}
		return p.fail("%q where %q belongs", got, want)
	}
	if strings.HasPrefix(want, "*") {
		p.part = want
	}
	p.i++
	return nil
}

// heading takes a heading of exactly level stars and returns its text.
func (p *parser) heading(level int) (string, bool) {
	stars := strings.Repeat("*", level) + " "
	line := p.peek()
	if !strings.HasPrefix(line, stars) {
		return "", false
	}
	p.part = line
	p.i++
	return line[len(stars):], true
}

// id takes a heading's ID.
func (p *parser) id(text string) (string, error) {
	if !idPattern.MatchString(text) {
		return "", p.fail("%q is not a heading's ID", text)
	}
	return text, nil
}

var property = regexp.MustCompile(`^:([A-Z_]+):(?: (.*))?$`)

func (p *parser) props() (map[string]string, error) {
	if err := p.expect(":PROPERTIES:"); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for !p.done() {
		line := p.lines[p.i]
		if line == ":END:" {
			p.i++
			return out, nil
		}
		m := property.FindStringSubmatch(line)
		if m == nil {
			return nil, p.fail("%q in a property drawer", line)
		}
		out[m[1]] = m[2]
		p.i++
	}
	return nil, p.fail("a property drawer never ends")
}

func (p *parser) block() (string, error) {
	if err := p.expect("#+begin_example"); err != nil {
		return "", err
	}
	var text []string
	for !p.done() {
		line := p.lines[p.i]
		p.i++
		if line == "#+end_example" {
			return strings.Join(text, "\n"), nil
		}
		text = append(text, unescapeLine(line))
	}
	return "", p.fail("an example block never ends")
}

func (p *parser) blocks() ([]string, error) {
	var out []string
	for p.peek() == "#+begin_example" {
		b, err := p.block()
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (p *parser) number(props map[string]string, key string) (int, error) {
	v := props[key]
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, p.fail("%s is %q, not a number", key, v)
	}
	return n, nil
}

func (p *parser) when(props map[string]string, key string) (time.Time, error) {
	v := props[key]
	if v == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation(stampLayout, v, time.Local)
	if err != nil {
		return time.Time{}, p.fail("%s is %q, not an Org timestamp", key, v)
	}
	return t, nil
}

func (p *parser) header(top string) (map[string]string, error) {
	for _, want := range []string{titleLine, todoLine, top} {
		if err := p.expect(want); err != nil {
			return nil, err
		}
	}
	props, err := p.props()
	if err == nil && props["VERSION"] != Version {
		err = p.fail("VERSION is %q, and this program reads version %s", props["VERSION"], Version)
	}
	return props, err
}

// hunkPattern is a hunk's line, its path in a link or as =pathText=.
var hunkPattern = regexp.MustCompile(`^- (?:\[\[file:([^]]+)::\d+@[0-9a-f]{12}\]\[[^]]*\]\]|=([^ =]*)=) new (\d+),(\d+) old (\d+),(\d+)$`)

// path reads a path pathText wrote.
func (p *parser) path(text string) (string, error) {
	path, ok := readPath(text)
	if !ok {
		return "", p.fail("%q is not a path as the record's writer puts it", text)
	}
	return path, nil
}

func (p *parser) rangeRecord(r *Range) error {
	props, err := p.header("* Review")
	if err != nil {
		return err
	}
	r.Repo, r.Base, r.Head, r.Title = props["REPO"], props["BASE"], props["HEAD"], props["TITLE"]
	if r.PR, err = p.number(props, "PR"); err != nil {
		return err
	}
	if r.Updated, err = p.when(props, "UPDATED"); err != nil {
		return err
	}

	if err := p.expect("** Walkthrough"); err != nil {
		return err
	}
	for {
		text, ok := p.heading(3)
		if !ok {
			break
		}
		id, err := p.id(text)
		if err != nil {
			return err
		}
		c := Chunk{ID: id}
		props, err := p.props()
		if err != nil {
			return err
		}
		c.Title = props["TITLE"]
		if c.Summary, err = p.block(); err != nil {
			return err
		}
		for strings.HasPrefix(p.peek(), "- ") {
			m := hunkPattern.FindStringSubmatch(p.peek())
			if m == nil {
				return p.fail("%q is not a hunk's line", p.peek())
			}
			n := make([]int, 4)
			for i := range n {
				n[i], _ = strconv.Atoi(m[i+3])
			}
			path := m[1]
			if path == "" {
				if path, err = p.path(m[2]); err != nil {
					return err
				}
			}
			c.Hunks = append(c.Hunks, Hunk{Path: path, NewStart: n[0], NewLines: n[1], OldStart: n[2], OldLines: n[3]})
			p.i++
		}
		r.Walkthrough = append(r.Walkthrough, c)
	}

	if err := p.expect("** Diagrams"); err != nil {
		return err
	}
	for {
		text, ok := p.heading(3)
		if !ok {
			break
		}
		id, err := p.id(text)
		if err != nil {
			return err
		}
		props, err := p.props()
		if err != nil {
			return err
		}
		d := Diagram{ID: id, Title: props["TITLE"], DrawnBy: props["DRAWN_BY"], CritiquedBy: props["CRITIQUED_BY"],
			Verdict: props["VERDICT"]}
		if d.JSON, err = p.block(); err != nil {
			return err
		}
		r.Diagrams = append(r.Diagrams, d)
	}

	if err := p.expect("** Findings"); err != nil {
		return err
	}
	for {
		text, ok := p.heading(3)
		if !ok {
			break
		}
		state, rest, _ := strings.Cut(text, " ")
		if !states[State(state)] {
			return p.fail("%q is not one of the #+TODO line's keywords", state)
		}
		id, err := p.id(rest)
		if err != nil {
			return err
		}
		f := Finding{ID: id, State: State(state)}
		props, err := p.props()
		if err != nil {
			return err
		}
		f.Title, f.Reviewer, f.Kind = props["TITLE"], props["REVIEWER"], props["KIND"]
		if f.Path, err = p.path(props["PATH"]); err != nil {
			return err
		}
		f.Side, f.OutcomeTarget = props["SIDE"], props["OUTCOME_TARGET"]
		for key, n := range map[string]*int{"ROUND": &f.Round, "LINE": &f.Line, "END_LINE": &f.EndLine} {
			if *n, err = p.number(props, key); err != nil {
				return err
			}
		}
		if strings.HasPrefix(p.peek(), "[[file:") {
			p.i++
		}
		if f.Evidence, err = p.block(); err != nil {
			return err
		}
		if p.peek() == "**** Outcome" {
			p.heading(4)
			if f.Reason, err = p.block(); err != nil {
				return err
			}
		}
		r.Findings = append(r.Findings, f)
	}

	if err := p.expect("** Rounds"); err != nil {
		return err
	}
	for {
		text, ok := p.heading(3)
		if !ok {
			break
		}
		id, err := p.id(text)
		if err != nil {
			return err
		}
		props, err := p.props()
		if err != nil {
			return err
		}
		rd := Round{ID: id, Reviewer: props["REVIEWER"], Model: props["MODEL"], RedCheck: props["RED_CHECK"]}
		if rd.At, err = p.when(props, "AT"); err != nil {
			return err
		}
		if p.peek() == "**** Unverified" {
			p.heading(4)
			if rd.Unverified, err = p.blocks(); err != nil {
				return err
			}
		}
		if p.peek() == "**** Dropped" {
			p.heading(4)
			if rd.Dropped, err = p.blocks(); err != nil {
				return err
			}
		}
		r.Rounds = append(r.Rounds, rd)
	}
	if !p.done() {
		return p.fail("%q is not part of a record", p.peek())
	}
	return nil
}

func (p *parser) prRecord(pr *PR) error {
	props, err := p.header("* Pull request")
	if err != nil {
		return err
	}
	pr.Repo = props["REPO"]
	if pr.Number, err = p.number(props, "PR"); err != nil {
		return err
	}
	if pr.Updated, err = p.when(props, "UPDATED"); err != nil {
		return err
	}
	for {
		text, ok := p.heading(2)
		if !ok {
			break
		}
		rest, ok := strings.CutPrefix(text, "T-")
		if !ok {
			return p.fail("a thread's heading is T-<thread ID>")
		}
		id, err := p.id(rest)
		if err != nil {
			return err
		}
		t := Thread{ID: id}
		props, err := p.props()
		if err != nil {
			return err
		}
		t.Resolved = props["RESOLVED"] == "t"
		if t.Path, err = p.path(props["PATH"]); err != nil {
			return err
		}
		if t.Line, err = p.number(props, "LINE"); err != nil {
			return err
		}
		bodies, err := p.blocks()
		if err != nil {
			return err
		}
		authors := strings.Fields(props["AUTHORS"])
		if len(authors) != len(bodies) {
			return p.fail("AUTHORS names %d comments, and the thread holds %d", len(authors), len(bodies))
		}
		for i, a := range authors {
			login, kind, _ := strings.Cut(a, ":")
			t.Comments = append(t.Comments, Comment{Author: login, Type: kind, Body: bodies[i]})
		}
		if p.peek() == "*** Draft" {
			p.heading(3)
			props, err := p.props()
			if err != nil {
				return err
			}
			d := &Draft{Outcome: props["OUTCOME"]}
			if d.Posted, err = p.when(props, "POSTED"); err != nil {
				return err
			}
			if d.Body, err = p.block(); err != nil {
				return err
			}
			t.Draft = d
		}
		pr.Threads = append(pr.Threads, t)
	}
	if !p.done() {
		return p.fail("%q is not part of a record", p.peek())
	}
	return nil
}

// same holds the file to what the renderer writes for the values parsed
// from it: any other heading, keyword, drawer, property, block, escape or
// text outside a block is drift.
func (p *parser) same(rendered []byte, err error) error {
	if err != nil {
		return &DriftError{Line: 1, Part: "the file", Why: strings.TrimPrefix(err.Error(), "record: ")}
	}
	if bytes.Equal(rendered, p.data) {
		return nil
	}
	want := strings.Split(strings.TrimSuffix(string(rendered), "\n"), "\n")
	got := strings.Split(string(p.data), "\n")
	k := 0
	for k < len(want) && k < len(got) && want[k] == got[k] {
		k++
	}
	// The part is the last heading above the first line that differs,
	// which file and rendering share.
	part := "the file's header"
	for j := min(k-1, len(want)-1); j >= 0; j-- {
		if strings.HasPrefix(want[j], "*") {
			part = want[j]
			break
		}
	}
	why := "the file ends early"
	switch {
	case k < len(got) && k >= len(want):
		why = fmt.Sprintf("%q follows the record's end", got[k])
	case k < len(got):
		why = fmt.Sprintf("%q is not what the record's writer puts there", got[k])
	}
	return &DriftError{Line: k + 1, Part: part, Why: why}
}
