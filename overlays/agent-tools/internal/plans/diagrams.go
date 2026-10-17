package plans

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/diagram"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// The kinds of the events a diagram's critique and keeping log. A critique
// is never a receipt: a diagram is an aid to reading the change, and owes
// no route. Each is stored in plan logs, so renaming one means rewriting
// every log that holds it.
const (
	critiquedKind = "diagram-critiqued"
	keptKind      = "diagram-kept"
	movedKind     = "diagram-moved"
	staleKind     = "diagram-stale"
)

// DiagramFocus is the Focus of the prompt issued for a diagram's critique.
const DiagramFocus = "diagram"

// diagramsSection is the level-2 section of a plan's own file that holds
// its diagrams, which only KeepDiagram and RefreshDiagrams write.
const diagramsSection = "Diagrams"

// A kept diagram's STATUS.
const (
	DiagramChecked  = "checked"
	DiagramStale    = "stale"
	DiagramDisputed = "disputed"
)

// Critique is a diagram's critique by a model family other than the one
// that drew it: the diagram's hash, the range and tip it read, the nonce
// of the prompt a sub-agent's reply answered, and the IDs of its findings.
type Critique struct {
	Diagram, Range, Tip, Nonce, Model string
	Drafter, Critic                   string
	Findings                          []string
}

// WriteCritique logs c in the plan LocateCheckout finds for session and
// the worktree at top, and returns that plan's file, or "" where there is
// none.
func WriteCritique(session, top string, c Critique, now time.Time) (string, error) {
	cfg, err := Load()
	if err != nil {
		return "", err
	}
	p, part, ok := cfg.LocateCheckout(session, top)
	if !ok {
		return "", nil
	}
	if _, err := p.Record(part, Event{Kind: critiquedKind, Diagram: c.Diagram, Range: c.Range, Tip: c.Tip, Nonce: c.Nonce,
		Model: c.Model, Drafter: c.Drafter, Critic: c.Critic, Findings: strings.Join(c.Findings, " "),
		Summary: fmt.Sprintf("diagram %.12s: %d findings by %s", c.Diagram, len(c.Findings), c.Critic)}, now); err != nil {
		return "", err
	}
	return p.File(), nil
}

// CheckoutItems are the item IDs of the plan LocateCheckout finds for
// session and the worktree at top, its parts' too, or nil where there is
// none: what a planned element of a diagram may name.
func CheckoutItems(session, top string) map[string]bool {
	c, err := Load()
	if err != nil {
		return nil
	}
	p, _, ok := c.LocateCheckout(session, top)
	if !ok {
		return nil
	}
	return p.ItemIDs()
}

// ItemIDs are p's item IDs, its parts' too, or nil where its files cannot
// be read.
func (p Plan) ItemIDs() map[string]bool {
	items, err := p.items()
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for id := range items {
		out[id] = true
	}
	return out
}

// FindingAt is where a critique's finding points: the element it is
// about, or "" for an omission, and the line of code.
type FindingAt struct {
	About string `json:"about"`
	Path  string `json:"path"`
	Line  int    `json:"line"`
}

// KeepOptions say how a diagram is kept: as disputed, with the findings
// of its last critique, and in place of an existing entry.
type KeepOptions struct {
	Disputed bool
	Findings []FindingAt
	Replace  string
	// Repo names the repository the diagram's code is in, as
	// diagram.Repo's Root does.
	Repo string
}

// Kept is a diagram written to the plan.
type Kept struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Hash   string `json:"hash"`
	File   string `json:"file"`
}

var (
	diagramID = regexp.MustCompile(`^G[1-9][0-9]{0,3}$`)
	repoID    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	elementID = regexp.MustCompile(`^[NE][1-9][0-9]{0,2}$`)
	safePath  = regexp.MustCompile(`^[A-Za-z0-9._/@+-]{1,200}$`)
)

// KeepDiagram writes d, checked at its commit so that its citations have
// hashes, to the plan's Diagrams section as a new entry, or in place of
// o.Replace, and logs it. Its log must hold a critique of d, by its hash,
// that found nothing; with o.Disputed, one that found something, after at
// least two critiques that did of this diagram, its earlier drawings as
// the entry o.Replace included, and the entry keeps where its findings
// point.
func (p Plan) KeepDiagram(d *diagram.Diagram, o KeepOptions, now time.Time) (Kept, error) {
	if err := p.checkOwn(); err != nil {
		return Kept{}, err
	}
	if o.Replace != "" && !diagramID.MatchString(o.Replace) {
		return Kept{}, refusal.New("%q is not a diagram's ID. Pass one `agent-plan diagram check %s` lists, such as `--replace G1`",
			o.Replace, p.File())
	}
	if o.Repo != "" && !repoID.MatchString(o.Repo) {
		return Kept{}, fmt.Errorf("%q does not name a repository by its root commit", o.Repo)
	}
	hash := d.Hash()
	events, err := p.Events()
	if err != nil {
		return Kept{}, err
	}
	for _, n := range d.Nodes {
		if err := plannedOpen(events, n.ID, n.Planned); err != nil {
			return Kept{}, err
		}
	}
	for _, e := range d.Edges {
		if err := plannedOpen(events, e.ID, e.Planned); err != nil {
			return Kept{}, err
		}
	}
	// The drawings of this diagram: d, and the earlier ones of the entry it
	// replaces, as the log kept, moved or marked them.
	drawings := map[string]bool{hash: true}
	for _, e := range events {
		if (e.Kind == keptKind || e.Kind == movedKind || e.Kind == staleKind) && o.Replace != "" && e.Entry == o.Replace {
			drawings[e.Diagram] = true
			if before, ok := strings.CutPrefix(e.Evidence, "was "); ok {
				drawings[before] = true
			}
		}
	}
	var clean, found *Event
	disputes := 0
	for i := range events {
		e := &events[i]
		if e.Kind != critiquedKind {
			continue
		}
		if e.Findings != "" && drawings[e.Diagram] {
			disputes++
		}
		if e.Diagram == hash {
			if e.Findings == "" {
				clean = e
			} else {
				found = e
			}
		}
	}
	critique, status := clean, DiagramChecked
	switch {
	case o.Disputed && (found == nil || disputes < 2):
		return Kept{}, refusal.New("a diagram is kept as disputed only after two critiques of it with findings, the last of "+
			"diagram %.12s, the earlier of it or of the entry it replaces (`--replace G1`); redraw it for its findings and "+
			"critique it again with `git fresh-review diagram`", hash)
	case o.Disputed:
		critique, status = found, DiagramDisputed
	case clean == nil:
		return Kept{}, refusal.New("no critique of diagram %.12s in %s's log found nothing; critique it with `git fresh-review "+
			"diagram --diagram FILE BASE..TIP`, redraw it for the findings, or after two critiques with findings keep it "+
			"with `--disputed --findings FILE`", hash, p.Name)
	}
	kept := Kept{Status: status, Hash: hash, File: p.File()}
	err = p.withLock(func(l *lockedPlan) error {
		if err := l.edit(p.File(), func(lines []string) ([]string, error) {
			lines, start, end := diagramsIn(lines)
			at, stop := end, end
			if o.Replace != "" {
				if at = entryAt(lines, start, end, o.Replace); at < 0 {
					return nil, refusal.New("%s has no diagram %s to replace; list them with `agent-plan diagram check %s`",
						p.File(), o.Replace, p.File())
				}
				stop = subtreeEnd(lines, at)
				kept.ID = o.Replace
			} else {
				kept.ID = nextDiagramID(lines[start:end])
			}
			entry := entryLines(kept.ID, d, status, critique.Critic, o.Repo, o.Findings)
			return slices.Concat(lines[:at], entry, lines[stop:]), nil
		}); err != nil {
			return err
		}
		_, err := l.log(Event{Kind: keptKind, Item: p.Name, Diagram: hash, Critic: critique.Critic, Findings: critique.Findings,
			Entry: kept.ID, Status: status, Repo: o.Repo, Summary: kept.ID + " " + status}, now)
		return err
	})
	return kept, err
}

// plannedOpen refuses an element planned for an item the log has done or
// verified: by the design review of the result, its code is there to
// cite.
func plannedOpen(events []Event, id string, planned *string) error {
	if planned == nil {
		return nil
	}
	for _, e := range events {
		if e.Item == *planned && (e.Kind == "done" || e.Kind == "verified") {
			return refusal.New("%s is planned for %s, which the log has %s; cite the code %s added, check the diagram "+
				"again with `agent-plan diagram check`, and critique it", id, *planned, e.Kind, *planned)
		}
	}
	return nil
}

// diagramsIn is lines with a Diagrams section, added at the end where
// there is none, and the section's bounds: its heading and the first line
// after it.
func diagramsIn(lines []string) ([]string, int, int) {
	if start, end, ok := section(lines, diagramsSection); ok {
		return lines, start, end
	}
	lines = slices.Clone(lines)
	last := len(lines)
	if last > 0 && lines[last-1] == "" {
		last--
	}
	lines = slices.Insert(lines, last, "** "+diagramsSection)
	return lines, last, last + 1
}

// entryAt is the index of the entry with id in lines[start:end], or -1.
func entryAt(lines []string, start, end int, id string) int {
	for i := start + 1; i < end; i++ {
		if level(lines[i]) == 3 {
			if j := headingWithID(lines[i:subtreeEnd(lines, i)], id); j == 0 {
				return i
			}
		}
	}
	return -1
}

// nextDiagramID is one more than the highest diagram ID in lines.
func nextDiagramID(lines []string) string {
	highest := 0
	for _, l := range lines {
		if m := idLine.FindStringSubmatch(l); m != nil && diagramID.MatchString(m[1]) {
			if n, _ := strconv.Atoi(m[1][1:]); n > highest {
				highest = n
			}
		}
	}
	return fmt.Sprintf("G%d", highest+1)
}

// headingTitle is a diagram's title as its heading holds it: the label
// without the characters that, in a heading, make tags (`:`) or markup
// (`/`, `+`, `=`, `_`); the JSON keeps the title whole.
func headingTitle(title string) string {
	t := diagram.CleanLabel(strings.Map(func(r rune) rune {
		if strings.ContainsRune(":/+=_", r) {
			return ' '
		}
		return r
	}, title))
	if t == "" {
		return "diagram"
	}
	return t
}

// entryLines are a diagram's entry: its heading, properties, the JSON it
// is drawn from and the Mermaid generated from that.
func entryLines(id string, d *diagram.Diagram, status, critic, repo string, findings []FindingAt) []string {
	lines := []string{"*** " + id + " " + headingTitle(d.Title), ":PROPERTIES:", ":ID: " + id, ":KIND: " + d.Kind,
		":AT: " + d.At, ":STATUS: " + status}
	if slices.Contains(families, critic) {
		lines = append(lines, ":CRITIC: "+critic)
	}
	if repoID.MatchString(repo) {
		lines = append(lines, ":REPO: "+repo)
	}
	var at []string
	for _, f := range findings {
		about := "-"
		if elementID.MatchString(f.About) {
			about = f.About
		}
		if safePath.MatchString(f.Path) && f.Line > 0 {
			at = append(at, fmt.Sprintf("%s@%s:%d", about, f.Path, f.Line))
		}
	}
	if len(at) > 0 && status == DiagramDisputed {
		lines = append(lines, ":FINDINGS: "+strings.Join(at, " "))
	}
	// Neither block is for Babel to run, whatever language support Emacs
	// gains.
	lines = append(lines, ":END:", "#+begin_src json :eval never")
	lines = append(lines, strings.Split(escapeOrg(string(d.JSON())), "\n")...)
	lines = append(lines, "#+end_src", "#+begin_src mermaid :eval never")
	lines = append(lines, strings.Split(escapeOrg(strings.TrimSuffix(d.Mermaid(), "\n")), "\n")...)
	return append(lines, "#+end_src")
}

// families are the model families a critic's CRITIC may name.
var families = []string{"claude", "codex"}

// KeptDiagram is an entry of a plan's Diagrams section as its file holds
// it. Edited says why it is not as agent-plan wrote it, if it is not: its
// JSON does not parse, its properties or Mermaid are not those of its
// JSON, another entry before it has its ID, or its JSON, STATUS, CRITIC or
// REPO are not those of the log's last kept, moved or stale event of its
// ID; such an entry is never refreshed, exported or trusted. The plan file
// is the agents' to edit; the log is what agent-plan alone appends to.
type KeptDiagram struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	Kind     string           `json:"kind"`
	At       string           `json:"at"`
	Status   string           `json:"status"`
	Critic   string           `json:"critic,omitempty"`
	Repo     string           `json:"repo,omitempty"`
	Findings string           `json:"findings,omitempty"`
	Edited   string           `json:"edited,omitempty"`
	Diagram  *diagram.Diagram `json:"-"`
	// reason is why a refresh found it stale other than by its elements.
	reason string
}

// Diagrams are the entries of the Diagrams section of the plan's own
// file, in file order.
func (p Plan) Diagrams() ([]KeptDiagram, error) {
	data, err := readPlan(p.File())
	if err != nil {
		return nil, err
	}
	events, err := p.Events()
	if err != nil {
		return nil, err
	}
	last := map[string]Event{}
	for _, e := range events {
		if (e.Kind == keptKind || e.Kind == movedKind || e.Kind == staleKind) && e.Entry != "" {
			last[e.Entry] = e
		}
	}
	lines := strings.Split(string(data), "\n")
	start, end, ok := section(lines, diagramsSection)
	out := []KeptDiagram{}
	if !ok {
		return out, nil
	}
	seen := map[string]bool{}
	for i := start + 1; i < end; i++ {
		if level(lines[i]) == 3 {
			k := readEntry(lines[i:subtreeEnd(lines, i)], last)
			if seen[k.ID] && k.Edited == "" {
				k.Edited, k.Diagram = "another entry before it has its ID", nil
			}
			seen[k.ID] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// readEntry reads one entry's lines, trusting them as far as last, the
// log's last event of each entry, says.
func readEntry(lines []string, last map[string]Event) KeptDiagram {
	h := headingOf(3, strings.TrimSpace(strings.TrimLeft(lines[0], "*")), keywordSet{})
	k := KeptDiagram{Title: h.Title}
	props := map[string]string{}
	blocks := map[string][]string{}
	block := ""
	for _, l := range lines[1:] {
		trimmed := strings.TrimSpace(l)
		switch {
		case block != "" && strings.EqualFold(trimmed, "#+end_src"):
			block = ""
		case block != "":
			blocks[block] = append(blocks[block], l)
		case strings.HasPrefix(strings.ToLower(trimmed), "#+begin_src "):
			block = strings.TrimSpace(trimmed[len("#+begin_src "):])
			if lang, args, _ := strings.Cut(block, " "); strings.TrimSpace(args) == ":eval never" {
				block = lang
			}
			if _, seen := blocks[block]; seen {
				block = block + " again"
			}
			blocks[block] = []string{}
		default:
			if m := propertyLine.FindStringSubmatch(l); m != nil && trimmed != ":PROPERTIES:" && trimmed != ":END:" {
				props[strings.ToUpper(m[1])] = m[2]
			}
		}
	}
	k.ID, k.Kind, k.At, k.Status, k.Critic, k.Repo, k.Findings = props["ID"], props["KIND"], props["AT"], props["STATUS"],
		props["CRITIC"], props["REPO"], props["FINDINGS"]
	k.Title = strings.TrimPrefix(k.Title, k.ID+" ")
	d, err := diagram.Parse([]byte(strings.Join(blocks["json"], "\n")))
	logged, found := last[k.ID]
	switch {
	case err != nil:
		k.Edited = "its JSON: " + err.Error()
	case len(blocks) != 2 || blocks["mermaid"] == nil:
		k.Edited = "it holds other blocks than one json and one mermaid"
	case strings.Join(blocks["mermaid"], "\n") != strings.TrimSuffix(d.Mermaid(), "\n"):
		k.Edited = "its Mermaid is not the one its JSON generates"
	case k.Kind != d.Kind || k.At != d.At:
		k.Edited = "its KIND or AT is not its JSON's"
	case !diagramID.MatchString(k.ID) || !slices.Contains([]string{DiagramChecked, DiagramStale, DiagramDisputed}, k.Status):
		k.Edited = "its ID or STATUS is not one agent-plan writes"
	case !found:
		k.Edited = "the log keeps no diagram " + k.ID
	case logged.Diagram != d.Hash():
		k.Edited = "its JSON is not the one the log last kept, moved or marked as " + k.ID
	case logged.Status != k.Status || logged.Critic != k.Critic || logged.Repo != k.Repo:
		k.Edited = "its STATUS, CRITIC or REPO is not the log's"
	default:
		k.Diagram = d
	}
	return k
}

// DiagramState is a kept diagram held to a later commit.
type DiagramState struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Edited     string   `json:"edited,omitempty"`
	Stale      []string `json:"stale,omitempty"`
	Moved      []string `json:"moved,omitempty"`
	Planned    bool     `json:"planned,omitempty"`
	Incomplete []string `json:"incomplete,omitempty"`
	// Other is a diagram of another repository than the one held to, left
	// as it is; Reason why a diagram could not be held to it, as when its
	// commit is not there, which makes it stale.
	Other  bool   `json:"other,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// DiagramStates holds the plan's checked diagrams to commit now of repo,
// changing nothing: a stale one says which elements, and one with planned
// elements says so.
func (p Plan) DiagramStates(repo diagram.Repo, now string) ([]DiagramState, error) {
	return p.diagramStates(repo, now, nil)
}

// RefreshDiagrams is DiagramStates, writing what it finds: a diagram whose
// citations moved, none stale, is rewritten at now and logged as
// diagram-moved; a stale one gets STATUS stale, logged as diagram-stale.
func (p Plan) RefreshDiagrams(repo diagram.Repo, now string, at time.Time) ([]DiagramState, error) {
	if err := p.checkOwn(); err != nil {
		return nil, err
	}
	var out []DiagramState
	err := p.withLock(func(l *lockedPlan) error {
		var err error
		out, err = p.diagramStates(repo, now, func(k KeptDiagram, s diagram.Staleness) error {
			status, d, kind := k.Status, k.Diagram, ""
			switch {
			case s.Stale && k.Status != DiagramStale:
				status, kind = DiagramStale, staleKind
			case s.Updated != nil:
				d, kind = s.Updated, movedKind
			default:
				return nil
			}
			if err := l.edit(p.File(), func(lines []string) ([]string, error) {
				start, end, _ := section(lines, diagramsSection)
				i := entryAt(lines, start, end, k.ID)
				if i < 0 {
					return nil, fmt.Errorf("%s is no longer in %s", k.ID, p.File())
				}
				var findings []FindingAt
				return slices.Concat(lines[:i], entryLines(k.ID, d, status, k.Critic, k.Repo, findings),
					lines[subtreeEnd(lines, i):]), nil
			}); err != nil {
				return err
			}
			evidence := "was " + k.Diagram.Hash()
			if k.reason != "" {
				evidence += "; " + k.reason
			}
			_, err := l.log(Event{Kind: kind, Item: p.Name, Diagram: d.Hash(), Evidence: evidence, Tip: now, Entry: k.ID,
				Status: status, Critic: k.Critic, Repo: k.Repo, Summary: k.ID + " " + status}, at)
			return err
		})
		return err
	})
	return out, err
}

// diagramsTimeout bounds the git calls of the status line's diagram check.
const diagramsTimeout = 2 * time.Second

// diagramsLine is the status line's word on the diagrams p keeps, at HEAD
// of the worktree cwd is in: "diagram stale" where one is stale or edited,
// "diagram check failed" where they could not be held to it, as when HEAD
// has no commit or git took longer than diagramsTimeout, or "". It reads
// only git's objects, and a plan that keeps none costs no git call.
func diagramsLine(p Plan, cwd string) string {
	kept, err := p.Diagrams()
	if err != nil {
		return "diagram check failed"
	}
	if len(kept) == 0 {
		return ""
	}
	top, _ := checkout(cwd)
	if top == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), diagramsTimeout)
	defer cancel()
	head, err := git.BytesContext(ctx, "-C", top, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "diagram check failed"
	}
	states, err := p.DiagramStates(diagram.TimedRepo{Dir: top, Ctx: ctx}, strings.TrimSpace(string(head)))
	if err != nil || ctx.Err() != nil {
		return "diagram check failed"
	}
	for _, s := range states {
		if s.Status == DiagramStale || s.Edited != "" {
			return "diagram stale"
		}
	}
	return ""
}

// diagramStates holds each kept diagram to now, calling write, where
// given, for each one held.
func (p Plan) diagramStates(repo diagram.Repo, now string, write func(KeptDiagram, diagram.Staleness) error) ([]DiagramState, error) {
	kept, err := p.Diagrams()
	if err != nil {
		return nil, err
	}
	// root names the repository held to; with none to name, every diagram
	// is held to it.
	root, _ := repo.Root(now)
	out := []DiagramState{}
	for _, k := range kept {
		st := DiagramState{ID: k.ID, Title: k.Title, Status: k.Status, Edited: k.Edited}
		if k.Diagram != nil && k.Repo != "" && root != "" && k.Repo != root {
			st.Other = true
		} else if k.Diagram != nil && k.Status != DiagramDisputed {
			st.Planned = k.Diagram.Planned()
			s, err := diagram.Stale(k.Diagram, now, repo)
			if err != nil {
				// A diagram that cannot be held to now, its commit gone or
				// never here, no longer says what the code does.
				st.Reason = fmt.Sprintf("its commit %.12s cannot be compared with %.12s: %v", k.At, now, err)
				s, k.reason = diagram.Staleness{Stale: true}, st.Reason
			}
			for _, e := range s.Elements {
				switch e.State {
				case diagram.Gone:
					st.Stale = append(st.Stale, e.ID)
				case diagram.Moved:
					st.Moved = append(st.Moved, e.ID)
				}
			}
			st.Incomplete = s.Incomplete
			if s.Stale {
				st.Status = DiagramStale
			}
			if write != nil {
				if err := write(k, s); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, st)
	}
	return out, nil
}
