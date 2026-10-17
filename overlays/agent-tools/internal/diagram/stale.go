package diagram

import (
	"fmt"
	"slices"
)

// State is where an element's citation stands at a later commit.
type State string

const (
	// Current is the same lines, by hash, at the same place.
	Current State = "current"
	// Moved is the same lines elsewhere in the file, or in the file it
	// was renamed to.
	Moved State = "moved"
	// Gone is lines no longer in the file, or a file no longer there:
	// the element no longer says what the code does.
	Gone State = "stale"
)

// ElementState is where one element's citation stands.
type ElementState struct {
	ID    string `json:"id"`
	State State  `json:"state"`
}

// Staleness is a checked diagram held to a later commit.
type Staleness struct {
	// Stale is whether any element is Gone, which a redraw covers.
	Stale    bool           `json:"stale"`
	Elements []ElementState `json:"elements"`
	// Incomplete are the cited files changed since the diagram's commit
	// whose citations all still hold: a call or a state added there
	// changes no cited line, so the diagram may lack it.
	Incomplete []string `json:"incomplete"`
	// Updated is the diagram at the later commit, its moved citations
	// rewritten, where some moved and none is Gone; nil otherwise.
	Updated *Diagram `json:"-"`
}

// Repo is the git repository a diagram's code is in.
type Repo interface {
	// Blob is path's content at rev.
	Blob(rev, path string) ([]byte, error)
	// Changes are the paths changed from one commit to another, and the
	// new path of each renamed.
	Changes(from, to string) (changed []string, renamed map[string]string, err error)
	// Hunks are the changes from fromPath at from to toPath at to,
	// whitespace ignored.
	Hunks(from, fromPath, to, toPath string) ([]Hunk, error)
	// Root names the repository rev is in, the same in every clone.
	Root(rev string) (string, error)
}

// Hunk is one change of a diff with no context: OldLines lines from
// OldStart replaced by NewLines from NewStart; where OldLines is 0, the
// new lines follow line OldStart.
type Hunk struct {
	OldStart, OldLines, NewStart, NewLines int
}

// Stale holds each citation of d, checked at d.At so that it has a hash,
// to commit now. A citation in a file the diff from d.At does not name is
// Current. Otherwise its own lines are followed, renames included, as git's
// diff of the file maps them, never another copy of the same text: lines a
// hunk changed are Gone, lines only shifted, or in a renamed file, Moved,
// once their hash still holds there. A diagram whose citations moved, none
// Gone, is checked again at now as Check does, and an element that fails
// there is Gone too. It compares content, never blame, so rebases and
// re-signing leave a diagram current.
func Stale(d *Diagram, now string, repo Repo) (Staleness, error) {
	s := Staleness{Elements: []ElementState{}, Incomplete: []string{}}
	changed, renamed, err := repo.Changes(d.At, now)
	if err != nil {
		return s, err
	}
	updated := d.Clone()
	hunks := map[string][]Hunk{}
	files := map[string][]string{}
	gone := map[string]bool{}
	moved := false
	for _, c := range updated.cites() {
		if c.cite.Hash == "" {
			return s, fmt.Errorf("%s has no hash: check the diagram at %s first", c.id, d.At)
		}
		old, path := c.cite.Path, c.cite.Path
		if to, ok := renamed[old]; ok {
			path = to
		} else if !slices.Contains(changed, old) {
			s.Elements = append(s.Elements, ElementState{ID: c.id, State: Current})
			continue
		}
		state := Gone
		if _, ok := hunks[old]; !ok {
			hs, err := repo.Hunks(d.At, old, now, path)
			if err == nil {
				hunks[old] = hs
				if data, err := repo.Blob(now, path); err == nil {
					files[old] = splitLines(data)
				}
			}
		}
		lines := files[old]
		if offset, ok := shift(hunks[old], c.cite.Line, c.cite.End()); ok && lines != nil {
			start, end := c.cite.Line+offset, c.cite.End()+offset
			if start >= 1 && end <= len(lines) && spanHash(lines[start-1:end]) == c.cite.Hash {
				state = Current
				if offset != 0 || path != old {
					state = Moved
				}
				c.cite.Path, c.cite.Line = path, start
				if c.cite.EndLine != nil {
					c.cite.EndLine = &end
				}
			}
		}
		if state == Gone {
			gone[old] = true
			s.Stale = true
		}
		moved = moved || state == Moved
		s.Elements = append(s.Elements, ElementState{ID: c.id, State: state})
	}
	if moved && !s.Stale {
		updated.At = now
		r := Check(updated, func(path string) ([]byte, error) { return repo.Blob(now, path) }, func(string) bool { return true })
		for _, f := range r.Failures {
			for i := range s.Elements {
				if s.Elements[i].ID == f.Element {
					s.Elements[i].State, s.Stale = Gone, true
				}
			}
			for _, c := range d.cites() {
				if c.id == f.Element {
					gone[c.cite.Path] = true
				}
			}
		}
		if !s.Stale {
			s.Updated = updated
		}
	}
	for _, path := range changed {
		if slices.ContainsFunc(d.cites(), func(c cited) bool { return c.cite.Path == path }) && !gone[path] {
			s.Incomplete = append(s.Incomplete, path)
		}
	}
	return s, nil
}

// shift is how far hunks move lines start to end, and false where a hunk
// changes or adds lines among them.
func shift(hunks []Hunk, start, end int) (int, bool) {
	offset := 0
	for _, h := range hunks {
		if h.OldLines == 0 {
			switch {
			case h.OldStart >= start && h.OldStart < end:
				return 0, false
			case h.OldStart < start:
				offset += h.NewLines
			}
			continue
		}
		last := h.OldStart + h.OldLines - 1
		switch {
		case h.OldStart <= end && last >= start:
			return 0, false
		case last < start:
			offset += h.NewLines - h.OldLines
		}
	}
	return offset, true
}

type cited struct {
	id   string
	cite *Cite
}

// cites are d's citations, nodes' then edges', planned elements left out.
func (d *Diagram) cites() []cited {
	var out []cited
	for i := range d.Nodes {
		if c := d.Nodes[i].Cite; c != nil {
			out = append(out, cited{d.Nodes[i].ID, c})
		}
	}
	for i := range d.Edges {
		if c := d.Edges[i].Cite; c != nil {
			out = append(out, cited{d.Edges[i].ID, c})
		}
	}
	return out
}

// Paths are the files d cites, each once.
func (d *Diagram) Paths() []string {
	var out []string
	for _, c := range d.cites() {
		if !slices.Contains(out, c.cite.Path) {
			out = append(out, c.cite.Path)
		}
	}
	return out
}

// Planned reports whether any element of d is planned rather than cited.
func (d *Diagram) Planned() bool {
	return len(d.cites()) < len(d.Nodes)+len(d.Edges)
}
