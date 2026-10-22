package record

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// stampLayout is an inactive Org timestamp, in the host's time zone.
const stampLayout = "[2006-01-02 Mon 15:04]"

func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.In(time.Local).Format(stampLayout)
}

func itoa(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// writer collects a record's lines. Every free text it is given goes
// through freeText or value, so no caller can write raw text.
type writer struct{ lines []string }

func (w *writer) line(s string) { w.lines = append(w.lines, s) }

// props writes a property drawer, keys in the order given. pairs alternate
// key and value; each value is already one line.
func (w *writer) props(pairs ...string) {
	w.line(":PROPERTIES:")
	for i := 0; i < len(pairs); i += 2 {
		if pairs[i+1] == "" {
			w.line(":" + pairs[i] + ":")
		} else {
			w.line(":" + pairs[i] + ": " + pairs[i+1])
		}
	}
	w.line(":END:")
}

// block writes text, already cleaned, as an example block.
func (w *writer) block(text string) {
	w.line("#+begin_example")
	for _, l := range strings.Split(text, "\n") {
		w.line(escapeLine(l))
	}
	w.line("#+end_example")
}

func (w *writer) bytes() []byte { return []byte(strings.Join(w.lines, "\n") + "\n") }

// link is org-plans' citation of a line in a commit: file:PATH::LINE@SHA,
// with the first twelve hex digits of the SHA.
func link(path string, line int, sha, label string) string {
	return fmt.Sprintf("[[file:%s::%d@%s][%s]]", path, line, sha[:12], label)
}

func span(path string, start, end int) string {
	if end <= start {
		return fmt.Sprintf("%s:%d", path, start)
	}
	return fmt.Sprintf("%s:%d-%d", path, start, end)
}

// hunkLine cites a hunk with a link, or, where no link can hold its path,
// names the path as pathText in verbatim markup, inside which Org reads no
// emphasis or subscript.
func hunkLine(head string, h Hunk) string {
	numbers := fmt.Sprintf(" new %d,%d old %d,%d", h.NewStart, h.NewLines, h.OldStart, h.OldLines)
	if !validPath(h.Path) {
		return "- =" + pathText(h.Path) + "=" + numbers
	}
	start := max(h.NewStart, 1)
	return "- " + link(h.Path, start, head, span(h.Path, start, start+h.NewLines-1)) + numbers
}

// Render writes the range's record, or says which value it cannot hold.
func (r *Range) Render() ([]byte, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	w := &writer{}
	w.line(titleLine)
	w.line(todoLine)
	w.line("* Review")
	pairs := []string{"VERSION", Version, "REPO", r.Repo, "BASE", r.Base, "HEAD", r.Head}
	if r.PR != 0 {
		pairs = append(pairs, "PR", strconv.Itoa(r.PR))
	}
	w.props(append(pairs, "TITLE", value(r.Title, TitleCap), "UPDATED", stamp(r.Updated))...)

	w.line("** Walkthrough")
	for _, c := range r.Walkthrough {
		w.line("*** " + c.ID)
		w.props("TITLE", value(c.Title, TitleCap))
		w.block(freeText(c.Summary, TextCap))
		for _, h := range c.Hunks {
			w.line(hunkLine(r.Head, h))
		}
	}

	w.line("** Diagrams")
	for _, d := range r.Diagrams {
		w.line("*** " + d.ID)
		w.props("TITLE", value(d.Title, TitleCap), "DRAWN_BY", value(d.DrawnBy, ValueCap),
			"CRITIQUED_BY", value(d.CritiquedBy, ValueCap), "VERDICT", value(d.Verdict, ValueCap))
		w.block(freeText(d.JSON, DiagramCap))
	}

	w.line("** Findings")
	for _, f := range r.Findings {
		w.line("*** " + string(f.State) + " " + f.ID)
		w.props("TITLE", value(f.Title, TitleCap), "REVIEWER", value(f.Reviewer, ValueCap), "ROUND", itoa(f.Round),
			"KIND", value(f.Kind, ValueCap), "PATH", pathText(f.Path), "LINE", itoa(f.Line), "END_LINE", itoa(f.EndLine),
			"SIDE", f.Side, "OUTCOME_TARGET", value(f.OutcomeTarget, ValueCap))
		if validPath(f.Path) && f.Line > 0 {
			at := r.Head
			if f.Side == "old" {
				at = r.Base
			}
			w.line(link(f.Path, f.Line, at, span(f.Path, f.Line, f.EndLine)))
		}
		w.block(freeText(f.Evidence, TextCap))
		// A reason that cleans to nothing writes no Outcome: an empty one
		// would read back as no reason, and render differently.
		if reason := freeText(f.Reason, TextCap); reason != "" {
			w.line("**** Outcome")
			w.block(reason)
		}
	}

	w.line("** Rounds")
	for _, rd := range r.Rounds {
		w.line("*** " + rd.ID)
		w.props("REVIEWER", value(rd.Reviewer, ValueCap), "MODEL", value(rd.Model, ValueCap), "AT", stamp(rd.At),
			"RED_CHECK", rd.RedCheck)
		if len(rd.Unverified) > 0 {
			w.line("**** Unverified")
			for _, u := range rd.Unverified {
				w.block(freeText(u, TextCap))
			}
		}
		if len(rd.Dropped) > 0 {
			w.line("**** Dropped")
			for _, d := range rd.Dropped {
				w.block(freeText(d, TextCap))
			}
		}
	}
	return w.bytes(), nil
}

// Render writes the pull request's record, or says which value it cannot
// hold.
func (p *PR) Render() ([]byte, error) {
	if err := p.check(); err != nil {
		return nil, err
	}
	w := &writer{}
	w.line(titleLine)
	w.line(todoLine)
	w.line("* Pull request")
	w.props("VERSION", Version, "REPO", p.Repo, "PR", strconv.Itoa(p.Number), "UPDATED", stamp(p.Updated))
	for _, t := range p.Threads {
		w.line("** T-" + t.ID)
		authors := make([]string, len(t.Comments))
		for i, c := range t.Comments {
			authors[i] = c.Author + ":" + c.Type
		}
		resolved := "nil"
		if t.Resolved {
			resolved = "t"
		}
		w.props("AUTHORS", strings.Join(authors, " "), "RESOLVED", resolved, "PATH", pathText(t.Path), "LINE", itoa(t.Line))
		for _, c := range t.Comments {
			w.block(freeText(c.Body, TextCap))
		}
		if d := t.Draft; d != nil {
			w.line("*** Draft")
			w.props("OUTCOME", d.Outcome, "POSTED", stamp(d.Posted))
			w.block(freeText(d.Body, TextCap))
		}
	}
	return w.bytes(), nil
}

// check refuses what the renderer cannot write as given: the values that go
// into headings, links and properties verbatim.
func (r *Range) check() error {
	owner, repo, ok := strings.Cut(r.Repo, "/")
	switch {
	case !ok || checkRepo(owner, repo) != nil:
		return fmt.Errorf("record: %q is not a GitHub owner/name", r.Repo)
	case !sha.MatchString(r.Base) || !sha.MatchString(r.Head):
		return fmt.Errorf("record: a range needs full SHAs, got %q..%q", r.Base, r.Head)
	case r.PR < 0:
		return fmt.Errorf("record: %d is not a pull request number", r.PR)
	}
	ids := ids{}
	for _, c := range r.Walkthrough {
		if err := ids.add(c.ID); err != nil {
			return err
		}
		for _, h := range c.Hunks {
			if h.NewStart < 0 || h.NewLines < 0 || h.OldStart < 0 || h.OldLines < 0 {
				return fmt.Errorf("record: chunk %s's hunk %q has a negative line or count", c.ID, h.Path)
			}
		}
	}
	for _, d := range r.Diagrams {
		if err := ids.add(d.ID); err != nil {
			return err
		}
	}
	for _, f := range r.Findings {
		if err := ids.add(f.ID); err != nil {
			return err
		}
		switch {
		case !states[f.State]:
			return fmt.Errorf("record: finding %s has state %q, not one of the #+TODO line's", f.ID, f.State)
		case f.Side != "" && f.Side != "new" && f.Side != "old":
			return fmt.Errorf("record: finding %s's side is %q, not new or old", f.ID, f.Side)
		case f.Round < 0 || f.Line < 0 || f.EndLine < 0:
			return fmt.Errorf("record: finding %s has a negative round or line", f.ID)
		}
	}
	for _, rd := range r.Rounds {
		if err := ids.add(rd.ID); err != nil {
			return err
		}
		if rd.RedCheck != "session" && rd.RedCheck != "absent" {
			return fmt.Errorf("record: round %s's red-check is %q, not session or absent", rd.ID, rd.RedCheck)
		}
	}
	return nil
}

func (p *PR) check() error {
	owner, repo, ok := strings.Cut(p.Repo, "/")
	switch {
	case !ok || checkRepo(owner, repo) != nil:
		return fmt.Errorf("record: %q is not a GitHub owner/name", p.Repo)
	case p.Number <= 0:
		return fmt.Errorf("record: %d is not a pull request number", p.Number)
	}
	ids := ids{}
	for _, t := range p.Threads {
		if err := ids.add(t.ID); err != nil {
			return err
		}
		if t.Line < 0 {
			return fmt.Errorf("record: thread %s has a negative line", t.ID)
		}
		for _, c := range t.Comments {
			if !loginPattern.MatchString(c.Author) || !typePattern.MatchString(c.Type) {
				return fmt.Errorf("record: thread %s has a comment by %q of type %q, which AUTHORS cannot hold", t.ID, c.Author, c.Type)
			}
		}
		if d := t.Draft; d != nil && !validOutcome(d.Outcome) {
			return fmt.Errorf("record: thread %s's draft has outcome %q, not one of %s", t.ID, d.Outcome, strings.Join(Outcomes, ", "))
		}
	}
	return nil
}

func validOutcome(o string) bool {
	for _, v := range Outcomes {
		if o == v {
			return true
		}
	}
	return false
}

// ids holds the IDs a record's headings carry, which are unique.
type ids map[string]bool

func (s ids) add(id string) error {
	if !idPattern.MatchString(id) || states[State(id)] {
		return fmt.Errorf("record: %q cannot be a heading's ID", id)
	}
	if s[id] {
		return fmt.Errorf("record: the ID %s is used twice", id)
	}
	s[id] = true
	return nil
}
