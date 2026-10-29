package plans

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// A workstream is a line of work that many plans, sessions and
// repositories share, such as the agent tooling. Its file sits in the
// plans directory as workstream-<slug>.<host>.org, so that sync sends it
// to the other hosts as it sends this host's plans, and each host writes
// only its own. It is an org-roam node holding only its title and the
// summary a person writes: which plans belong to it is each plan's own
// WORKSTREAM link to it, and nothing computed from the plans is ever
// written into it.
var workstreamFile = regexp.MustCompile(`^(workstream-([a-z0-9]+(?:-[a-z0-9]+)*)\.([A-Za-z0-9][A-Za-z0-9_-]*))\.org$`)

// IsWorkstream reports whether name is the file name of a workstream,
// which agents make through agent-plan alone and a person edits.
func IsWorkstream(name string) bool { return workstreamFile.MatchString(name) }

// Workstream is a workstream's file.
type Workstream struct {
	// Name is the file's name without .org: workstream-<slug>.<host>.
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Host  string `json:"host"`
	ID    string `json:"id"`
	Title string `json:"title"`
	File  string `json:"file"`
	// Existed is NewWorkstream's: the workstream was there already.
	Existed bool `json:"existed,omitempty"`
}

// slugOf is title in lower case, each run of other characters than
// letters and digits made one hyphen.
func slugOf(title string) string {
	var b strings.Builder
	gap := false
	for _, r := range strings.ToLower(title) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			if gap && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			gap = false
		} else {
			gap = true
		}
	}
	return b.String()
}

// NewWorkstream writes a workstream titled title, with summary as its
// text, as this host's. A workstream of the same slug that any host has
// is that workstream already: it is returned as it stands, Existed, and
// no file is written, so that a person's edits of it stay.
func (c Config) NewWorkstream(title, summary string) (Workstream, error) {
	if err := checkTitle("title", title); err != nil {
		return Workstream{}, err
	}
	slug := slugOf(title)
	if slug == "" {
		return Workstream{}, refusal.New("title %q has no letter or digit to name the workstream's file by. Pass one that does, as `--title 'Agent tooling'`", title)
	}
	body, err := textLines(summary)
	if err != nil {
		return Workstream{}, err
	}
	if w, ok, err := c.workstreamBySlug(slug); err != nil || ok {
		w.Existed = ok
		return w, err
	}
	ready, err := c.Init()
	if err != nil {
		return Workstream{}, err
	}
	if ready.Note != "" {
		if err := c.makeDir(); err != nil {
			return Workstream{}, err
		}
	}
	id := newUUID()
	text := fmt.Sprintf(":PROPERTIES:\n:ID: %s\n:END:\n#+title: %s\n", id, strings.TrimSpace(title))
	if len(body) > 0 {
		text += "\n" + strings.Join(body, "\n") + "\n"
	}
	name := "workstream-" + slug + "." + c.Host
	file := filepath.Join(c.Dir, name+".org")
	if err := writeNew(file, text); errors.Is(err, fs.ErrExist) {
		// Another run wrote it first.
		w, err := c.readWorkstream(name + ".org")
		w.Existed = true
		return w, err
	} else if err != nil {
		return Workstream{}, err
	}
	return Workstream{Name: name, Slug: slug, Host: c.Host, ID: id, Title: strings.TrimSpace(title), File: file}, nil
}

// Workstreams are every host's workstreams, by title.
func (c Config) Workstreams() ([]Workstream, error) {
	entries, err := os.ReadDir(c.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []Workstream{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Workstream{}
	var errs []error
	for _, e := range entries {
		if !e.Type().IsRegular() || !workstreamFile.MatchString(e.Name()) {
			continue
		}
		w, err := c.readWorkstream(e.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, w)
	}
	slices.SortFunc(out, func(a, b Workstream) int {
		return strings.Compare(a.Title+"\x00"+a.Name, b.Title+"\x00"+b.Name)
	})
	return out, errors.Join(errs...)
}

// readWorkstream reads the workstream file of that name in the plans
// directory, as readPlan reads a plan's.
func (c Config) readWorkstream(name string) (Workstream, error) {
	m := workstreamFile.FindStringSubmatch(name)
	if m == nil {
		return Workstream{}, fmt.Errorf("%s is not a workstream's file name", name)
	}
	file := filepath.Join(c.Dir, name)
	data, err := readPlan(file)
	if err != nil {
		return Workstream{}, err
	}
	w := Workstream{Name: m[1], Slug: m[2], Host: m[3], ID: fileID(string(data)), Title: orgTitle(string(data)), File: file}
	if w.ID == "" {
		return Workstream{}, fmt.Errorf("%s has no file-level ID for a plan to link to", file)
	}
	return w, nil
}

// workstreamBySlug is the workstream of slug, this host's before
// another's, and whether there is one.
func (c Config) workstreamBySlug(slug string) (Workstream, bool, error) {
	ws, err := c.Workstreams()
	var found []Workstream
	for _, w := range ws {
		if w.Slug == slug {
			found = append(found, w)
		}
	}
	for _, w := range found {
		if w.Host == c.Host {
			return w, true, err
		}
	}
	if len(found) > 0 {
		return found[0], true, err
	}
	return Workstream{}, false, err
}
