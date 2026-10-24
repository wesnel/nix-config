package plans

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// linkedKind is the kind of event Link logs, whose summary is id:ID of the
// node linked; Related follows these events as References follow
// referenced ones.
const linkedKind = "linked"

// idLink is an org-roam link to a node by its ID, as Related holds them;
// group 1 is the ID.
var idLink = regexp.MustCompile(`\[\[id:([^]\s]+)\]`)

// Link writes, under Related in the plan plan names, a link to the plan
// other names, by other's file-level ID and with its title, and why, then
// logs a linked event. It returns no event when plan already links other,
// whatever why says. It refuses a link from a plan to itself, to a part's
// file, which is no org-roam node, and to a plan without a file-level ID,
// one in the old form.
func (c Config) Link(plan, other, why string, now time.Time) (Event, error) {
	p, err := c.Open(plan)
	if err != nil {
		return Event{}, err
	}
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	why = strings.TrimSpace(why)
	if why == "" {
		return Event{}, refusal.New("a link needs a reason. Pass one with `--why`, such as `--why 'It splits work out of this one.'`")
	}
	if err := checkLine("why", why); err != nil {
		return Event{}, err
	}
	if idLink.MatchString(why) {
		return Event{}, refusal.New("a link reason cannot contain another id: link. Add each related target with `agent-plan link PLAN OTHER --why TEXT`")
	}
	o, err := c.Open(other)
	if err != nil {
		return Event{}, err
	}
	if o.Name == p.Name {
		return Event{}, refusal.New("%s cannot link to itself. Pass another plan, as `agent-plan link %s OTHER --why TEXT`", p.Name, p.file(""))
	}
	// OTHER names a file, or a plan or part by its name, as Open reads it.
	base := filepath.Base(other)
	if !strings.HasSuffix(base, ".org") {
		base += ".org"
	}
	if m := planFile.FindStringSubmatch(base); m != nil && m[3] != "" {
		return Event{}, refusal.New("%s is a part's file, which is no org-roam node. Link its plan, `agent-plan link PLAN %s --why TEXT`", other, o.file(""))
	}
	data, err := readPlan(o.file(""))
	if err != nil {
		return Event{}, err
	}
	id := fileID(string(data))
	if id == "" {
		return Event{}, refusal.New("%s is in the old form, with no file-level ID for an org-roam link to name. Link it from that plan instead, `agent-plan link %s %s --why TEXT`, or leave it unlinked",
			o.Name, o.file(""), p.file(""))
	}
	// A link's description ends at its first ]].
	title := strings.NewReplacer("[", "(", "]", ")").Replace(orgTitle(string(data)))
	var e Event
	err = p.withLock(func(l *lockedPlan) error {
		changed := false
		err := l.edit(p.file(""), func(lines []string) ([]string, error) {
			out, ok := setRelated(lines, id, "- [[id:"+id+"]["+title+"]]: "+why)
			changed = ok
			return out, nil
		})
		if err != nil || !changed {
			return err
		}
		e, err = l.log(Event{Kind: linkedKind, Item: p.Name, Summary: "id:" + id}, now)
		return err
	})
	return e, err
}

// relatedIDs are the IDs the id: links under Related of a plan file's
// lines name, in order, each once.
func relatedIDs(lines []string) []string {
	start, end, ok := section(lines, "Related")
	if !ok {
		return nil
	}
	var out []string
	for _, l := range lines[start+1 : end] {
		for _, m := range idLink.FindAllStringSubmatch(l, -1) {
			if !slices.Contains(out, m[1]) {
				out = append(out, m[1])
			}
		}
	}
	return out
}

// setRelated is lines with line, a link to the node id, at the end of
// Related, and whether that changed anything: not where Related already
// links id. A plan without Related gets it after References, or else at
// the end of `* Plan`.
func setRelated(lines []string, id, line string) ([]string, bool) {
	if slices.Contains(relatedIDs(lines), id) {
		return lines, false
	}
	_, end, ok := section(lines, "Related")
	if !ok {
		at := len(lines)
		if _, refEnd, ok := section(lines, "References"); ok {
			at = refEnd
		}
		if at == len(lines) && lines[at-1] == "" {
			at--
		}
		return slices.Concat(lines[:at], []string{"** Related", line}, lines[at:]), true
	}
	// A section ending the file ends at its final empty line.
	if end == len(lines) && lines[end-1] == "" {
		end--
	}
	return slices.Concat(lines[:end], []string{line}, lines[end:]), true
}

// danglingLinks are the IDs the plan's Related links name that are the
// file-level ID of no Org file under repo, as id:ID.
func (p Plan) danglingLinks() ([]string, error) {
	data, err := readPlan(p.file(""))
	if err != nil {
		return nil, err
	}
	ids := relatedIDs(strings.Split(string(data), "\n"))
	if len(ids) == 0 {
		return []string{}, nil
	}
	known, err := fileIDs(p.Repo)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, id := range ids {
		if !known[id] {
			out = append(out, "id:"+id)
		}
	}
	return out, nil
}

// fileIDs are the file-level IDs of the Org files under dir, outside git's
// own directory, as org-roam would find its nodes there. It reads through
// an os.Root, so a link inside dir never leads a read outside it.
func fileIDs(dir string) (map[string]bool, error) {
	out := map[string]bool{}
	root, err := os.OpenRoot(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	files := root.FS()
	err = fs.WalkDir(files, ".", func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && d.Name() == ".git":
			return fs.SkipDir
		case !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".org"):
			return nil
		}
		data, err := fs.ReadFile(files, path)
		if err != nil {
			return err
		}
		if id := fileID(string(data)); id != "" {
			out[id] = true
		}
		return nil
	})
	return out, err
}
