package review

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/redact"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// typeRun is one reviewer type's part of a run: its output, or why it
// could not run, and the replies dropped on the way.
type typeRun struct {
	name string
	out  *Output
	why  string
	// refused is whether why is a refusal of its inputs, which no other
	// host or sub-agent would get past.
	refused  bool
	model    string
	redCheck bool
	failures []string
}

// numbered is the next number after the highest of prefix's IDs in ids.
func numbered(prefix string, ids []string) int {
	n := 0
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `([0-9]+)$`)
	for _, id := range ids {
		if m := pattern.FindStringSubmatch(id); m != nil {
			if k, _ := strconv.Atoi(m[1]); k > n {
				n = k
			}
		}
	}
	return n + 1
}

// writeRuns adds each run to the range's record as a round, with its
// findings under IDs of the record's own; a finding of the review's own
// keeps its RC- or UP- ID, and one the record already holds is not added
// again. It gives
// each finding in the runs' output its record ID. The guide's walkthrough
// replaces the record's, and so do a diagram run's diagrams.
func writeRuns(path string, s *source, r *span, title string, runs []*typeRun, now time.Time) (*record.Range, error) {
	var kept record.Range
	err := record.Update(path, func(rec *record.Range) error {
		rec.Repo, rec.Base, rec.Head, rec.PR, rec.Title, rec.Updated = s.slug(), r.base, r.tip, s.number, title, now
		var findings, rounds []string
		for _, f := range rec.Findings {
			findings = append(findings, f.ID)
		}
		for _, rd := range rec.Rounds {
			rounds = append(rounds, rd.ID)
		}
		for _, run := range runs {
			n := numbered("R", rounds)
			round := record.Round{ID: "R" + strconv.Itoa(n), Reviewer: run.name, Model: run.model, At: now, RedCheck: "absent"}
			rounds = append(rounds, round.ID)
			if run.redCheck {
				round.RedCheck = "session"
			}
			if out := run.out; out != nil {
				if out.Walkthrough != nil {
					rec.Walkthrough = walkthroughOf(out.Walkthrough)
				}
				if len(out.diagrams) > 0 {
					rec.Diagrams = replaceDiagrams(rec.Diagrams, out.diagrams)
				}
				round.Unverified = out.Unverified
				for _, d := range out.Dropped {
					round.Dropped = append(round.Dropped, string(d.Finding)+"\n"+d.Reason)
				}
				for _, f := range out.Findings {
					id := f.ID
					if !isOwn(id) {
						id = "F" + strconv.Itoa(numbered("F", findings))
					} else if contains(findings, id) {
						f.Finding = id
						continue
					}
					findings = append(findings, id)
					f.Finding = id
					rec.Findings = append(rec.Findings, findingOf(f, id, run.name, n))
				}
			}
			round.Dropped = append(round.Dropped, run.failures...)
			if run.why != "" {
				round.Dropped = append(round.Dropped, "The review could not run: "+run.why)
			}
			rec.Rounds = append(rec.Rounds, round)
		}
		kept = *rec
		return nil
	})
	return &kept, err
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// findingOf is f as the record keeps it.
func findingOf(f *Finding, id, reviewer string, round int) record.Finding {
	evidence := f.Claim + "\n\n" + f.Evidence
	if f.About != nil {
		evidence += "\n\nAbout: " + *f.About
	}
	if f.Reopens != nil {
		evidence += "\n\nReopens: " + *f.Reopens
	}
	if f.Reviewer != "" {
		reviewer = f.Reviewer
	}
	rf := record.Finding{ID: id, State: record.Open, Title: f.Claim, Reviewer: reviewer, Round: round, Kind: f.Kind,
		Path: f.Path, Line: f.Line, Side: "new", Evidence: evidence}
	if f.EndLine != nil && *f.EndLine > f.Line {
		rf.EndLine = *f.EndLine
	}
	if f.Path == "" {
		rf.Line, rf.Side = 0, ""
	}
	return rf
}

// priorText is the earlier findings a review's reviewers are given: the
// record's own, from earlier rounds of this range, and for a pull request
// those of its newest earlier review, at another head or base.
func priorText(path string, s *source) (string, error) {
	var b strings.Builder
	add := func(rec *record.Range, what string) {
		if len(rec.Findings) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s, by their IDs in the review record, with how each was settled:\n\n", what)
		for _, f := range rec.Findings {
			at := ""
			if f.Path != "" {
				at = fmt.Sprintf(" at %s:%d", f.Path, f.Line)
			}
			fmt.Fprintf(&b, "- %s %s (%s, round %d, %s)%s\n  %s\n", f.ID, f.State, f.Reviewer, f.Round, f.Kind, at,
				strings.ReplaceAll(strings.TrimSpace(f.Evidence), "\n", "\n  "))
			if f.Reason != "" {
				fmt.Fprintf(&b, "  Settled: %s\n", strings.ReplaceAll(strings.TrimSpace(f.Reason), "\n", "\n  "))
			}
		}
		b.WriteString("\n")
	}
	rec, err := record.ReadRange(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", err
	default:
		add(rec, "Findings of this range's earlier rounds")
	}
	if s.number != 0 {
		if earlier, _, err := newestForPR(s, path); err != nil {
			return "", err
		} else if earlier != nil {
			add(earlier, fmt.Sprintf("Findings of the review of %s#%d at %s..%s", s.slug(), s.number, earlier.Base[:12],
				earlier.Head[:12]))
		}
	}
	return record.Cap(redact.Secrets(b.String()), 64<<10), nil
}

// newestForPR is the newest range record of s's pull request but the one
// at except, with its path, as record.NewestRange finds it.
func newestForPR(s *source, except string) (*record.Range, string, error) {
	return record.NewestRange(s.owner, s.repo, s.number, except)
}

// recordOf is the path of the range record a SOURCE names, for show and
// settle: a range's in the current repository, or a pull request's newest.
// It is "" for a pull request reviewed nowhere yet.
func recordOf(s *source) (string, error) {
	if s.number != 0 {
		_, path, err := newestForPR(s, "")
		return path, err
	}
	r, err := resolve("", s.given)
	if err != nil {
		return "", err
	}
	return record.RangeFile(s.owner, s.repo, r.base, r.tip)
}

// ShowUsage is `git delegate show -h`.
const ShowUsage = `usage: git delegate show SOURCE

Prints the review of SOURCE as one JSON document, computed from its
review records: for a range, BASE..TIP resolved in this repository, its
record; for a pull request, OWNER/NAME#N or N, its newest range record
and its threads and drafts. "range" holds the record's properties, its
walkthrough, findings and rounds, and its counts; "pr" the threads, each
comment and draft, and their counts. Each diagram is an object with a
string "id" and its Mermaid as a string "mermaid", generated when shown,
beside its other fields; no other object holds both.

Exit status: 0, 2 a usage error or refusal, 5 a record that fails its
structure check.
`

// ShowDoc is what show prints.
type ShowDoc struct {
	Source string     `json:"source"`
	Range  *ShowRange `json:"range"`
	PR     *ShowPR    `json:"pr"`
}

type ShowRange struct {
	File        string        `json:"file"`
	Repo        string        `json:"repo"`
	Base        string        `json:"base"`
	Head        string        `json:"head"`
	PR          int           `json:"pr,omitempty"`
	Title       string        `json:"title"`
	Updated     string        `json:"updated"`
	Walkthrough []ShowChunk   `json:"walkthrough"`
	Diagrams    []ShowDiagram `json:"diagrams"`
	Findings    []ShowFinding `json:"findings"`
	Rounds      []ShowRound   `json:"rounds"`
	Counts      record.Counts `json:"counts"`
}

type ShowChunk struct {
	Chunk   string     `json:"chunk"`
	Title   string     `json:"title"`
	Summary string     `json:"summary"`
	Hunks   []ShowHunk `json:"hunks"`
}

type ShowHunk struct {
	Path     string `json:"path"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
}

type ShowRound struct {
	Round      string   `json:"round"`
	Reviewer   string   `json:"reviewer"`
	Model      string   `json:"model"`
	At         string   `json:"at"`
	RedCheck   string   `json:"redCheck"`
	Unverified []string `json:"unverified"`
	Dropped    []string `json:"dropped"`
}

type ShowThread struct {
	Thread   string        `json:"thread"`
	Resolved bool          `json:"resolved"`
	Path     string        `json:"path"`
	Line     int           `json:"line,omitempty"`
	Comments []ShowComment `json:"comments"`
	Draft    *ShowDraft    `json:"draft"`
}

type ShowComment struct {
	Author string `json:"author"`
	Type   string `json:"type"`
	Body   string `json:"body"`
}

type ShowDraft struct {
	Outcome string `json:"outcome"`
	Posted  string `json:"posted,omitempty"`
	Body    string `json:"body"`
}

// ShowDiagram is a diagram as show prints it, the one object of the
// document with a string id and mermaid.
type ShowDiagram struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	DrawnBy     string `json:"drawnBy"`
	CritiquedBy string `json:"critiquedBy"`
	Verdict     string `json:"verdict"`
	JSON        string `json:"json"`
	Mermaid     string `json:"mermaid,omitempty"`
}

type ShowFinding struct {
	Finding       string `json:"finding"`
	State         string `json:"state"`
	Title         string `json:"title"`
	Reviewer      string `json:"reviewer"`
	Round         int    `json:"round"`
	Kind          string `json:"kind"`
	Path          string `json:"path,omitempty"`
	Line          int    `json:"line,omitempty"`
	EndLine       int    `json:"endLine,omitempty"`
	Side          string `json:"side,omitempty"`
	OutcomeTarget string `json:"outcomeTarget,omitempty"`
	Evidence      string `json:"evidence"`
	Reason        string `json:"reason,omitempty"`
}

type ShowPR struct {
	File    string        `json:"file"`
	Repo    string        `json:"repo"`
	Number  int           `json:"number"`
	Updated string        `json:"updated"`
	Threads []ShowThread  `json:"threads"`
	Counts  record.Counts `json:"counts"`
}

// stamp is a time as show prints it, or "" for none.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// mermaid renders a diagram's stored JSON as Mermaid, with the renderer
// internal/diagram builds from numbered IDs and labels Mermaid can only
// draw; where it is nil, or fails, a diagram is shown without it.
var mermaid = diagramMermaid

func showFinding(f record.Finding) ShowFinding {
	return ShowFinding{Finding: f.ID, State: string(f.State), Title: f.Title, Reviewer: f.Reviewer, Round: f.Round,
		Kind: f.Kind, Path: f.Path, Line: f.Line, EndLine: f.EndLine, Side: f.Side, OutcomeTarget: f.OutcomeTarget,
		Evidence: f.Evidence, Reason: f.Reason}
}

func showRange(path string, rec *record.Range) *ShowRange {
	out := &ShowRange{File: path, Repo: rec.Repo, Base: rec.Base, Head: rec.Head, PR: rec.PR, Title: rec.Title,
		Updated: stamp(rec.Updated), Walkthrough: []ShowChunk{}, Diagrams: []ShowDiagram{},
		Findings: []ShowFinding{}, Rounds: []ShowRound{}, Counts: rec.Counts()}
	for _, rd := range rec.Rounds {
		out.Rounds = append(out.Rounds, ShowRound{Round: rd.ID, Reviewer: rd.Reviewer, Model: rd.Model, At: stamp(rd.At),
			RedCheck: rd.RedCheck, Unverified: nonNil(rd.Unverified), Dropped: nonNil(rd.Dropped)})
	}
	for _, c := range rec.Walkthrough {
		chunk := ShowChunk{Chunk: c.ID, Title: c.Title, Summary: c.Summary, Hunks: []ShowHunk{}}
		for _, h := range c.Hunks {
			chunk.Hunks = append(chunk.Hunks, ShowHunk{Path: h.Path, NewStart: h.NewStart, NewLines: h.NewLines,
				OldStart: h.OldStart, OldLines: h.OldLines})
		}
		out.Walkthrough = append(out.Walkthrough, chunk)
	}
	for _, d := range rec.Diagrams {
		sd := ShowDiagram{ID: d.ID, Title: d.Title, DrawnBy: d.DrawnBy, CritiquedBy: d.CritiquedBy, Verdict: d.Verdict, JSON: d.JSON}
		if mermaid != nil {
			sd.Mermaid, _ = mermaid(d.JSON)
		}
		out.Diagrams = append(out.Diagrams, sd)
	}
	for _, f := range rec.Findings {
		out.Findings = append(out.Findings, showFinding(f))
	}
	return out
}

// show runs `git delegate show` with args.
func show(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" show", flag.ContinueOnError)
	if code, ok := cli.Parse(flags, args, ShowUsage, stdout, stderr); !ok {
		return code
	}
	if flags.NArg() != 1 {
		fmt.Fprint(stderr, ShowUsage)
		return 2
	}
	report := func(result any, err error) int { return reportRecord(result, err, 0, stdout, stderr) }
	s, err := parseSource(flags.Arg(0))
	if err != nil {
		return report(nil, err)
	}
	doc, err := showDoc(s)
	return report(doc, err)
}

func showDoc(s *source) (*ShowDoc, error) {
	doc := &ShowDoc{Source: s.given}
	path, err := recordOf(s)
	if err != nil {
		return nil, err
	}
	number := s.number
	if path != "" {
		rec, err := record.ReadRange(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return nil, err
		default:
			doc.Range = showRange(path, rec)
			if number == 0 {
				number = rec.PR
			}
		}
	}
	if number != 0 {
		prPath, err := record.PRFile(s.owner, s.repo, number)
		if err != nil {
			return nil, err
		}
		p, err := record.ReadPR(prPath)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return nil, err
		default:
			doc.PR = &ShowPR{File: prPath, Repo: p.Repo, Number: p.Number, Updated: stamp(p.Updated),
				Threads: []ShowThread{}, Counts: p.Counts()}
			for _, t := range p.Threads {
				st := ShowThread{Thread: t.ID, Resolved: t.Resolved, Path: t.Path, Line: t.Line, Comments: []ShowComment{}}
				for _, c := range t.Comments {
					st.Comments = append(st.Comments, ShowComment{Author: c.Author, Type: c.Type, Body: c.Body})
				}
				if d := t.Draft; d != nil {
					st.Draft = &ShowDraft{Outcome: d.Outcome, Posted: stamp(d.Posted), Body: d.Body}
				}
				doc.PR.Threads = append(doc.PR.Threads, st)
			}
		}
	}
	if doc.Range == nil && doc.PR == nil {
		return nil, refusal.New("there is no review of %s; run `git delegate %s` to make one", s.given, s.given)
	}
	return doc, nil
}

// reportRecord reports as cli.Report does, and a record that fails its
// structure check with exit 5.
func reportRecord(result any, err error, code int, stdout, stderr io.Writer) int {
	var drift *record.DriftError
	if errors.As(err, &drift) {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return record.DriftExit
	}
	return cli.Report(name, result, err, code, stdout, stderr)
}

// SettleUsage is `git delegate settle -h`.
const SettleUsage = `usage: git delegate settle SOURCE FINDING OUTCOME [--target X] [--reason-file F]

Records how a finding of SOURCE's review was settled, in its record: the
one change to a record made by hand. FINDING is its record ID, as F3 or
RC-<key>; OUTCOME is review-change's Triage outcome: fix, propose, rebut,
defer, duplicate-of (with --target naming the finding it repeats),
belongs-in (with --target naming where it belongs), needs-operator, or
open to take it back. --reason-file names a file, under the working
directory, holding the reason. Prints the finding. Exit status: 0, 2 a
usage error or refusal, 5 a record that fails its structure check.
`

// outcomes are the Triage outcomes, as the record's keywords.
var outcomes = map[string]record.State{"fix": record.Fix, "propose": record.Propose, "rebut": record.Rebut,
	"defer": record.Defer, "duplicate-of": record.Duplicate, "belongs-in": record.Belongs,
	"needs-operator": record.Operator, "open": record.Open}

// settle runs `git delegate settle` with args.
func settle(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name+" settle", flag.ContinueOnError)
	target := flags.String("target", "", "")
	reasonFile := flags.String("reason-file", "", "")
	var positional []string
	for {
		if code, ok := cli.Parse(flags, args, SettleUsage, stdout, stderr); !ok {
			return code
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(positional) != 3 {
		fmt.Fprint(stderr, SettleUsage)
		return 2
	}
	report := func(result any, err error) int { return reportRecord(result, err, 0, stdout, stderr) }
	id, outcome := positional[1], positional[2]
	state, ok := outcomes[outcome]
	if !ok {
		return report(nil, refusal.New("%q is not an outcome; pass one of fix, propose, rebut, defer, duplicate-of, "+
			"belongs-in, needs-operator or open", outcome))
	}
	needsTarget := state == record.Duplicate || state == record.Belongs
	if needsTarget != (*target != "") {
		return report(nil, refusal.New("--target goes with duplicate-of, naming the finding repeated, and with belongs-in, "+
			"naming where it belongs, and with no other outcome; pass `--target` with those two alone"))
	}
	reason := ""
	if *reasonFile != "" {
		text, err := readFile(*reasonFile, stdin)
		if err != nil {
			return report(nil, err)
		}
		reason = strings.TrimSpace(text)
	}
	s, err := parseSource(positional[0])
	if err != nil {
		return report(nil, err)
	}
	path, err := recordOf(s)
	if err != nil {
		return report(nil, err)
	}
	if _, err := os.Stat(path); path == "" || err != nil {
		return report(nil, noReviewToSettle(s, path))
	}
	var settled *ShowFinding
	err = record.Update(path, func(rec *record.Range) error {
		for i := range rec.Findings {
			f := &rec.Findings[i]
			if f.ID != id {
				continue
			}
			if state == record.Duplicate && (*target == id || !hasFinding(rec, *target)) {
				return refusal.New("duplicate-of names the finding %s repeats, and %q is no other finding of the record; "+
					"pass `--target` with one of its finding IDs", id, *target)
			}
			f.State, f.OutcomeTarget, f.Reason = state, *target, reason
			rec.Updated = time.Now()
			sf := showFinding(*f)
			settled = &sf
			return nil
		}
		return refusal.New("the review of %s has no finding %s; pass a finding ID that `git delegate show %s` prints",
			s.given, id, s.given)
	})
	if err != nil {
		return report(nil, err)
	}
	return report(settled, nil)
}

// noReviewToSettle is settle's refusal for a source with no record at
// path. For a range, it lists each record under the same base, such as
// those an earlier round left before a fixup moved the tip: a line for each
// open finding, BASE..TIP then the finding's ID and title, or one saying
// the record has none or cannot be read. Where the records cannot be
// listed, it is the refusal it would be with none.
func noReviewToSettle(s *source, path string) error {
	none := refusal.New("there is no review of %s to settle; run `git delegate %s` first", s.given, s.given)
	if s.number != 0 || path == "" {
		return none
	}
	base, _, _ := strings.Cut(filepath.Base(path), "..")
	listed, err := record.Ranges(s.owner, s.repo, base)
	if err != nil || len(listed) == 0 {
		return none
	}
	var b strings.Builder
	for _, l := range listed {
		at := "\n  " + l.Base + ".." + l.Head + ": "
		if l.Err != nil {
			fmt.Fprintf(&b, "%sunreadable: %v", at, l.Err)
			continue
		}
		open := 0
		for _, f := range l.Range.Findings {
			if f.State == record.Open {
				fmt.Fprintf(&b, "%s%s %s", at, f.ID, f.Title)
				open++
			}
		}
		if open == 0 {
			b.WriteString(at + "no open findings")
		}
	}
	return refusal.New("there is no review of %s to settle; settle a finding of a review under its base with "+
		"`git delegate settle BASE..TIP FINDING OUTCOME`, or run `git delegate %s` first. The reviews under %s, "+
		"by their open findings:%s", s.given, s.given, base, b.String())
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func hasFinding(rec *record.Range, id string) bool {
	for _, f := range rec.Findings {
		if f.ID == id {
			return true
		}
	}
	return false
}
