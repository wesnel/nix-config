package plans

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/diagram"
	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// A plan's Visuals are the one published home of its changes' visuals,
// which the pull request description, review-guide, the demo digest and
// the brag sheet read. Each entry is a level-3 heading, its title the
// caption, with CUSTOM_ID V<n> and the BRANCH it shows; its pull request
// is the one of the plan's PULL_REQUESTS for that branch, never stored
// here. It holds one of
//   - a kept diagram: DIAGRAM, RANGE and VERDICT from the review record,
//     and its Mermaid in a mermaid source block;
//   - a VHS tape's source, in a tape source block;
//   - a screenshot's or recording's GitHub attachment URL alone;
//
// and a diagram or a tape may also have its recording's URL. No rendered
// image is kept anywhere: a GIF or PNG lives only as GitHub's attachment.

// keptVerdicts are the verdicts of a diagram the critic passed, first
// drawn or redrawn, which a visual may copy; an unresolved one may be
// untrue.
var keptVerdicts = []string{"holds", "corrected"}

var (
	diagramID = regexp.MustCompile(`^D[0-9]+$`)
	fullRange = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})\.\.(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// KeptDiagram is a diagram of a review record as a visual copies it: its
// record ID, title and verdict, the reviewed range as full SHAs, and its
// Mermaid.
type KeptDiagram struct {
	ID, Title, Verdict, Range, Mermaid string
}

// ReadDiagram reads diagram id from the review record of given, a range
// BASE..TIP resolved in the worktree top, under the GitHub name of top's
// origin: the record git delegate keeps and git delegate show prints.
func ReadDiagram(top, given, id string) (KeptDiagram, error) {
	if !diagramID.MatchString(id) {
		return KeptDiagram{}, refusal.New("%q is not a diagram ID. Pass one `git delegate show %s` lists, as `--diagram D1`", id, given)
	}
	base, tip, ok := strings.Cut(given, "..")
	if !ok || base == "" || tip == "" || strings.HasPrefix(tip, ".") {
		return KeptDiagram{}, refusal.New("%q is not a range BASE..TIP. Pass the range the review drew the diagram for, as `--range main..HEAD`", given)
	}
	origin, err := git.Run("-C", top, "remote", "get-url", "origin")
	if err != nil {
		return KeptDiagram{}, err
	}
	owner, repo, _ := strings.Cut(gh.Slug(origin), "/")
	if !record.ValidName(owner) || !record.ValidName(repo) {
		return KeptDiagram{}, refusal.New("%s has no GitHub origin, which names its review records. Run the command in the worktree of the reviewed change", top)
	}
	shas := make([]string, 2)
	for i, rev := range []string{base, tip} {
		if shas[i], err = git.Run("-C", top, "rev-parse", "--verify", "--end-of-options", rev+"^{commit}"); err != nil {
			return KeptDiagram{}, refusal.New("%s names no commit in %s. Pass the range the review drew the diagram for, as `--range main..HEAD`", rev, top)
		}
	}
	span := shas[0] + ".." + shas[1]
	path, err := record.RangeFile(owner, repo, shas[0], shas[1])
	if err != nil {
		return KeptDiagram{}, err
	}
	r, err := record.ReadRange(path)
	if errors.Is(err, fs.ErrNotExist) {
		return KeptDiagram{}, refusal.New("%s/%s has no review of %s (%s). Pass the range the review drew the diagram for, as `git delegate show` names it, or draw one with `git delegate --reviewer diagram`",
			owner, repo, given, span)
	}
	if err != nil {
		return KeptDiagram{}, err
	}
	var ids []string
	for _, d := range r.Diagrams {
		if d.ID != id {
			ids = append(ids, d.ID)
			continue
		}
		parsed, err := diagram.Parse([]byte(d.JSON))
		if err != nil {
			return KeptDiagram{}, fmt.Errorf("diagram %s of %s: %w", id, path, err)
		}
		return KeptDiagram{ID: d.ID, Title: d.Title, Verdict: d.Verdict, Range: span, Mermaid: parsed.Mermaid()}, nil
	}
	kept := "none"
	if len(ids) > 0 {
		kept = strings.Join(ids, ", ")
	}
	return KeptDiagram{}, refusal.New("the review of %s keeps no diagram %s; it keeps %s. Pass one it keeps with `--diagram`, as `git delegate show %s` lists them",
		given, id, kept, given)
}

// VisualOptions are a visual to keep in a plan's Visuals. Entry names an
// entry to give URL; otherwise Branch, and exactly one of Diagram, Tape,
// a tape's source, and URL. Caption is the entry's title: a diagram's
// own title where none is given, and needed for a tape or a URL alone.
type VisualOptions struct {
	Entry, Branch, Caption, Tape, URL string
	Diagram                           *KeptDiagram
}

// visual is the entry a VisualOptions makes, but its ID: its title, its
// properties after CUSTOM_ID in order, its body, and whether an entry
// already there stands for the same visual.
type visual struct {
	title string
	props [][2]string
	body  []string
	same  func(props map[string]string, body []string) bool
}

// Visual keeps a visual in the plan's own Visuals, the section added
// before References with its first entry, and logs it as planned. The
// same visual again, a diagram by branch, ID and range, a tape by branch
// and source, a URL alone by branch and URL, takes its entry's place,
// keeping the caption unless one is given and the URL a recording gave
// it; where that changes nothing, it writes and logs nothing.
func (p Plan) Visual(o VisualOptions, now time.Time) (Recorded, error) {
	if err := p.checkOwn(); err != nil {
		return Recorded{}, err
	}
	if o.Entry != "" {
		return p.visualURL(o, now)
	}
	v, err := newVisual(o)
	if err != nil {
		return Recorded{}, err
	}
	file := p.file("")
	r := Recorded{File: file}
	err = p.withLock(func(l *lockedPlan) error {
		changed, title := false, ""
		err := l.edit(file, func(lines []string) ([]string, error) {
			var out []string
			out, r.ID, title = keepVisual(lines, v, o.Caption)
			changed = !slices.Equal(out, lines)
			return out, nil
		})
		if err != nil || !changed {
			return err
		}
		r.Event, err = l.log(Event{Kind: "planned", Item: p.Name, Summary: "Visual " + r.ID + ": " + title}, now)
		return err
	})
	return r, err
}

// newVisual checks o at the boundary, as everything in it goes into a
// file synced to every host, and makes its entry.
func newVisual(o VisualOptions) (visual, error) {
	if o.Branch == "" {
		return visual{}, refusal.New("a visual is kept by the branch it shows. Pass it, as `--branch visuals-entry`")
	}
	if err := checkWords("branch", []string{o.Branch}); err != nil {
		return visual{}, err
	}
	if o.Caption != "" {
		if err := checkTitle("caption", o.Caption); err != nil {
			return visual{}, err
		}
	}
	given := 0
	for _, g := range []bool{o.Diagram != nil, o.Tape != "", o.URL != ""} {
		if g {
			given++
		}
	}
	if given != 1 {
		return visual{}, refusal.New("a visual is one of a diagram, a tape or a URL. Pass one, as `--range BASE..TIP --diagram D1`, `--tape FILE --caption C` or `--url URL --caption C`")
	}
	branch := [2]string{"BRANCH", o.Branch}
	switch {
	case o.Diagram != nil:
		d := *o.Diagram
		switch {
		case !slices.Contains(keptVerdicts, d.Verdict):
			return visual{}, refusal.New("diagram %s's verdict is %q, and only a diagram that %s may be published, since the critic found the rest untrue or could not tell. Redraw it with `git delegate --reviewer diagram`, or pick another",
				d.ID, d.Verdict, strings.Join(keptVerdicts, " or is "))
		case !diagramID.MatchString(d.ID) || !fullRange.MatchString(d.Range) || !diagram.ValidSource(d.Mermaid):
			return visual{}, refusal.New("diagram %q of %q is not one a review record keeps. Copy it with `agent-plan visual PLAN --range BASE..TIP --diagram D1`", d.ID, d.Range)
		}
		title := d.Title
		if err := checkTitle("diagram's title", title); err != nil && o.Caption == "" {
			return visual{}, refusal.New("diagram %s's title %q cannot be a heading. Pass one with `--caption 'one line'`", d.ID, title)
		}
		if o.Caption != "" {
			title = o.Caption
		}
		body := append(append([]string{"#+begin_src mermaid"}, strings.Split(strings.TrimRight(d.Mermaid, "\n"), "\n")...), "#+end_src")
		return visual{title: title, props: [][2]string{branch, {"DIAGRAM", d.ID}, {"RANGE", d.Range}, {"VERDICT", d.Verdict}}, body: body,
			same: func(props map[string]string, _ []string) bool {
				return props["BRANCH"] == o.Branch && props["DIAGRAM"] == d.ID && props["RANGE"] == d.Range
			}}, nil
	case o.Caption == "":
		return visual{}, refusal.New("a tape or a URL is shown with its caption. Pass one, as `--caption 'The status of a plan'`")
	case o.Tape != "":
		lines, err := tapeLines(o.Tape)
		if err != nil {
			return visual{}, err
		}
		body := append(append([]string{"#+begin_src tape"}, lines...), "#+end_src")
		return visual{title: o.Caption, props: [][2]string{branch}, body: body,
			same: func(props map[string]string, b []string) bool {
				return props["BRANCH"] == o.Branch && props["DIAGRAM"] == "" && slices.Equal(b, body)
			}}, nil
	default:
		if err := checkAttachment(o.URL); err != nil {
			return visual{}, err
		}
		return visual{title: o.Caption, props: [][2]string{branch, {"URL", o.URL}},
			same: func(props map[string]string, b []string) bool {
				return props["BRANCH"] == o.Branch && props["URL"] == o.URL && props["DIAGRAM"] == "" && len(b) == 0
			}}, nil
	}
}

// tapeLines are a tape's source as the lines of a source block: refused
// where a line is one textLines refuses, or any #+ line, which could end
// the block.
func tapeLines(tape string) ([]string, error) {
	lines, err := textLines(tape)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, refusal.New("the tape is empty. Pass the file VHS records from, as `--tape demo.tape`")
	}
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "#+") {
			return nil, refusal.New("the tape holds %q, which Org reads as a block's start or end. Leave it out of the tape, and pass it again with `--tape FILE`", l)
		}
	}
	return lines, nil
}

// checkAttachment refuses an address that is not an https URL on GitHub,
// where a pull request's attachments are kept.
func checkAttachment(address string) error {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.User != nil || strings.Trim(u.Path, "/") == "" ||
		(u.Host != "github.com" && !strings.HasSuffix(u.Host, ".githubusercontent.com")) || strings.ContainsAny(address, " \t\n\r") {
		return refusal.New("%q is not an https URL of a GitHub attachment. Pass the URL GitHub gave the file attached to the pull request, as `--url https://github.com/user-attachments/assets/...`", address)
	}
	return nil
}

// visualEntries are the indexes of the entries of lines' Visuals, or none
// without the section.
func visualEntries(lines []string) []int {
	start, end, ok := section(lines, "Visuals")
	if !ok {
		return nil
	}
	var out []int
	blocks := blockLines(lines)
	for i := start + 1; i < end; i++ {
		if level(lines[i]) == 3 && !blocks[i] {
			out = append(out, i)
		}
	}
	return out
}

// entryBody is the lines of the entry at lines[at] after its heading and
// property drawer, to the next heading of its level or above.
func entryBody(lines []string, at int) []string {
	end := subtreeEnd(lines, at)
	if end == len(lines) && lines[end-1] == "" {
		end--
	}
	i := at + 1
	if drawer := drawerOf(lines, at); drawer != nil {
		i += len(drawer) + 2
	}
	return lines[min(i, end):end]
}

// keepVisual is lines with v kept in Visuals, as Visual says, its entry's
// ID and title.
func keepVisual(lines []string, v visual, caption string) ([]string, string, string) {
	next := 1
	for _, h := range parseOrg(strings.Join(lines, "\n")) {
		if id := itemID(h); visualID.MatchString(id) {
			if n, _ := strconv.Atoi(id[1:]); n >= next {
				next = n + 1
			}
		}
	}
	for _, at := range visualEntries(lines) {
		props := drawerProps(drawerOf(lines, at))
		if !visualID.MatchString(props["CUSTOM_ID"]) || !v.same(props, entryBody(lines, at)) {
			continue
		}
		title := v.title
		if caption == "" {
			title = strings.TrimPrefix(lines[at], "*** ")
		}
		extra := v.props
		if u := props["URL"]; u != "" && !slices.ContainsFunc(extra, func(p [2]string) bool { return p[0] == "URL" }) {
			extra = append(slices.Clone(extra), [2]string{"URL", u})
		}
		end := subtreeEnd(lines, at)
		if end == len(lines) && lines[end-1] == "" {
			end--
		}
		entry := visualLines(title, props["CUSTOM_ID"], extra, v.body)
		return slices.Concat(lines[:at], entry, lines[end:]), props["CUSTOM_ID"], title
	}
	id := "V" + strconv.Itoa(next)
	entry := visualLines(v.title, id, v.props, v.body)
	if _, end, ok := section(lines, "Visuals"); ok {
		if end == len(lines) && lines[end-1] == "" {
			end--
		}
		return slices.Concat(lines[:end], entry, lines[end:]), id, v.title
	}
	entry = append([]string{"** Visuals"}, entry...)
	after := slices.Index(planSections, "Visuals") + 1
	for _, s := range planSections[after:] {
		if start, _, ok := section(lines, s); ok {
			return slices.Concat(lines[:start], entry, lines[start:]), id, v.title
		}
	}
	end := len(lines)
	if lines[end-1] == "" {
		end--
	}
	return slices.Concat(lines[:end], entry, lines[end:]), id, v.title
}

// visualLines is an entry of Visuals.
func visualLines(title, id string, props [][2]string, body []string) []string {
	out := []string{"*** " + title, ":PROPERTIES:", ":CUSTOM_ID: " + id}
	for _, p := range props {
		out = append(out, ":"+p[0]+": "+p[1])
	}
	return append(append(out, ":END:"), body...)
}

// visualURL gives entry o.Entry of the plan's Visuals the attachment URL
// o.URL, and logs it as planned; where it has that URL already, it writes
// and logs nothing.
func (p Plan) visualURL(o VisualOptions, now time.Time) (Recorded, error) {
	if !visualID.MatchString(o.Entry) || o.URL == "" || o.Branch != "" || o.Caption != "" || o.Tape != "" || o.Diagram != nil {
		return Recorded{}, refusal.New("a visual's URL is set by its ID alone. Pass `agent-plan visual PLAN V1 --url URL`")
	}
	if err := checkAttachment(o.URL); err != nil {
		return Recorded{}, err
	}
	file := p.file("")
	r := Recorded{ID: o.Entry, File: file}
	err := p.withLock(func(l *lockedPlan) error {
		changed := false
		err := l.edit(file, func(lines []string) ([]string, error) {
			for _, at := range visualEntries(lines) {
				if drawerProps(drawerOf(lines, at))["CUSTOM_ID"] == o.Entry {
					out := setProperty(lines, at, "URL", o.URL)
					changed = !slices.Equal(out, lines)
					return out, nil
				}
			}
			return nil, refusal.New("%s has no visual %s. Pass the ID of an entry under `** Visuals`, or add one with `--branch`", file, o.Entry)
		})
		if err != nil || !changed {
			return err
		}
		r.Event, err = l.log(Event{Kind: "planned", Item: p.Name, Summary: "Visual " + o.Entry + " attached " + o.URL}, now)
		return err
	})
	return r, err
}
