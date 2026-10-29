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

// WorkstreamTitles are the titles of every host's workstreams by their
// file-level IDs, as their files hold them now: a listing names a
// workstream by these, so that a person's rename shows at once, and a
// plan's link holds only the title it was made with.
func (c Config) WorkstreamTitles() (map[string]string, error) {
	ws, err := c.Workstreams()
	out := map[string]string{}
	for _, w := range ws {
		out[w.ID] = w.Title
	}
	return out, err
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

// workstreamNamed is the workstream name names: its slug, its name
// (workstream-<slug>.<host>) or its file in the plans directory.
func (c Config) workstreamNamed(name string) (Workstream, error) {
	listed := func() error {
		return refusal.New("%q names no workstream. Pass one `agent-plan workstream list` lists, by its slug or name, or make it with `agent-plan workstream new --title T`", name)
	}
	ws, err := c.Workstreams()
	if err != nil {
		return Workstream{}, err
	}
	base := strings.TrimSuffix(filepath.Base(name), ".org")
	if strings.Contains(name, "/") {
		dir, err := filepath.EvalSymlinks(filepath.Dir(name))
		root, rerr := filepath.EvalSymlinks(c.Dir)
		if err != nil || rerr != nil || dir != root {
			return Workstream{}, listed()
		}
	}
	var found []Workstream
	for _, w := range ws {
		if w.Name == base || w.Slug == name {
			found = append(found, w)
		}
	}
	switch len(found) {
	case 0:
		return Workstream{}, listed()
	case 1:
		return found[0], nil
	}
	var names []string
	for _, w := range found {
		names = append(names, w.Name)
	}
	return Workstream{}, refusal.New("%q names a workstream of each of several hosts: %s. Pass one by its name, as `--workstream %s`", name, strings.Join(names, ", "), names[0])
}

// WorkstreamRef is the workstream a plan's WORKSTREAM property links to:
// its file-level ID, and its title as the link describes it.
type WorkstreamRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// workstreamLink is a WORKSTREAM value, an org-roam link to the
// workstream's node, as [[id:ID][TITLE]]: group 1 is the ID and 2 the
// title.
var workstreamLink = regexp.MustCompile(`^\[\[id:([^]\s]+)\](?:\[([^]]*)\])?\]$`)

// ref is the WORKSTREAM link to w.
func (w Workstream) ref() WorkstreamRef {
	// A link's description ends at its first ]].
	return WorkstreamRef{ID: w.ID, Title: strings.NewReplacer("[", "(", "]", ")").Replace(w.Title)}
}

// link is the WORKSTREAM value that names r.
func (r WorkstreamRef) link() string { return "[[id:" + r.ID + "][" + r.Title + "]]" }

// refOf is the workstream a WORKSTREAM value names, or none for a value
// that is not an id: link.
func refOf(value string) WorkstreamRef {
	m := workstreamLink.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return WorkstreamRef{}
	}
	return WorkstreamRef{ID: m[1], Title: m[2]}
}

// Workstream is the workstream the plan's WORKSTREAM names, which its
// parts are in too; the zero WorkstreamRef where it names none.
func (p Plan) Workstream() (WorkstreamRef, error) {
	props, err := planHeading(p.file(""))
	if err != nil {
		return WorkstreamRef{}, err
	}
	return refOf(props["WORKSTREAM"]), nil
}

// workstreamFor is the workstream a new plan made with o is in: the one
// o names, or else that of the newest plan, any host's, whose branch it
// continues; or none.
func (c Config) workstreamFor(o NewOptions) (WorkstreamRef, error) {
	if o.Workstream != "" {
		w, err := c.workstreamNamed(o.Workstream)
		return w.ref(), err
	}
	ps, _, err := c.Plans()
	if err != nil {
		return WorkstreamRef{}, err
	}
	mine := planFacts{repos: o.Repos}
	mine.addBranches(o.Branches)
	for _, p := range ps {
		other, err := factsOf(p)
		if err != nil || other.workstream.ID == "" {
			continue
		}
		if len(continued(mine, other)) > 0 {
			return other.workstream, nil
		}
	}
	return WorkstreamRef{}, nil
}
