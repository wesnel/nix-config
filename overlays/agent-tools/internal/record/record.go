// Package record keeps review records as Org files: one per reviewed range,
// `<owner>/<repo>/<base>..<head>.org`, and one per pull request's threads,
// `<owner>/<repo>/pr-<n>.org`, under Dir. Agents, Emacs and the Agent plans
// page read them; only Update writes them, through the renderer, which keeps
// every piece of text from a model, GitHub or a test run from forming Org
// syntax Emacs would act on (see text.go).
//
// Nothing derived is stored: Counts are computed from the sections when read.
package record

import (
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// Version is the format the renderer writes and the parser reads. A
// change to what a file holds is a new version.
const Version = "2"

// The first two lines of every record. Line 1 holds nothing from a source,
// since a `-*-` there would set file variables. DEFER and OPERATOR come
// before the bar because review-change's Triage keeps them open.
const (
	titleLine = "#+title: review"
	todoLine  = "#+TODO: OPEN DEFER OPERATOR | FIX PROPOSE REBUT DUPLICATE BELONGS"
)

// State is a finding's Org keyword.
type State string

const (
	Open      State = "OPEN"
	Defer     State = "DEFER"
	Operator  State = "OPERATOR"
	Fix       State = "FIX"
	Propose   State = "PROPOSE"
	Rebut     State = "REBUT"
	Duplicate State = "DUPLICATE"
	Belongs   State = "BELONGS"
)

var states = map[State]bool{Open: true, Defer: true, Operator: true, Fix: true, Propose: true, Rebut: true,
	Duplicate: true, Belongs: true}

// Outcomes a pull request thread's draft answers it with: review-change's
// Triage outcomes for a thread.
var Outcomes = []string{"fix", "rebut", "defer", "duplicate-of", "belongs-in", "needs-operator"}

// Range is the record of one reviewed range, BASE..HEAD. A rerun of the same
// range adds a round to it.
type Range struct {
	Repo    string // owner/repo, GitHub's name from origin
	Base    string // full SHA
	Head    string // full SHA
	PR      int    // the pull request it was reviewed for, or 0
	Title   string
	Updated time.Time

	Walkthrough []Chunk
	Diagrams    []Diagram
	Findings    []Finding
	Rounds      []Round
}

// Chunk is one part of the walkthrough, with the hunks it covers.
type Chunk struct {
	ID      string
	Title   string
	Summary string
	Hunks   []Hunk
}

// Hunk is a diff hunk by its lines on each side, as `@@ -old +new @@`
// numbers them. Its link points at NewStart in HEAD.
type Hunk struct {
	Path               string
	NewStart, NewLines int
	OldStart, OldLines int
}

// Diagram is a diagram's JSON alone: Mermaid is generated when read.
type Diagram struct {
	ID          string
	Title       string
	DrawnBy     string
	CritiquedBy string
	Verdict     string
	JSON        string
}

// Finding is one reviewer finding and how it was settled. Line and EndLine
// are on Side's tree: HEAD for "new", BASE for "old". Reason is the
// outcome's reason, empty while the finding is open.
type Finding struct {
	ID            string
	State         State
	Title         string
	Reviewer      string
	Round         int
	Kind          string
	Path          string
	Line, EndLine int
	Side          string
	OutcomeTarget string
	Evidence      string
	Reason        string
}

// Round is one reviewer's run over the range. RedCheck is "session" when
// the session whose change is reviewed passed red-check's results, and
// "absent" otherwise.
type Round struct {
	ID         string
	Reviewer   string
	Model      string
	At         time.Time
	RedCheck   string
	Unverified []string
	Dropped    []string
}

// PR is the record of one pull request's review threads and the drafted
// answers to them. It outlives pushes and re-signing.
type PR struct {
	Repo    string
	Number  int
	Updated time.Time
	Threads []Thread
}

// Thread is a review thread, by its GitHub node ID, with every comment.
type Thread struct {
	ID       string
	Resolved bool
	Path     string
	Line     int // 0 when GitHub gives none
	Comments []Comment
	Draft    *Draft
}

// Comment is one comment of a thread. Type is GitHub's author type, such as
// User or Bot.
type Comment struct {
	Author string
	Type   string
	Body   string
}

// Draft is a drafted answer to a thread, posted only by the operator.
// Posted is zero until it is.
type Draft struct {
	Outcome string
	Posted  time.Time
	Body    string
}

// Counts are computed from a record's sections, never stored.
type Counts struct {
	// Findings still open, deferred, and held for the operator.
	Open     int `json:"open"`
	Deferred int `json:"deferred"`
	Operator int `json:"operator"`
	// Unresolved threads with no draft, and drafts not yet posted.
	Unanswered int `json:"unanswered"`
	Drafted    int `json:"drafted"`
}

// Counts of a range's findings.
func (r *Range) Counts() Counts {
	var c Counts
	for _, f := range r.Findings {
		switch f.State {
		case Open:
			c.Open++
		case Defer:
			c.Deferred++
		case Operator:
			c.Operator++
		}
	}
	return c
}

// Counts of a pull request's threads.
func (p *PR) Counts() Counts {
	var c Counts
	for _, t := range p.Threads {
		switch {
		case t.Draft != nil && t.Draft.Posted.IsZero():
			c.Drafted++
		case t.Draft == nil && !t.Resolved:
			c.Unanswered++
		}
	}
	return c
}

// Dir is where records are kept, a fixed path under the host's XDG state
// directory, which Emacs computes too. The sandbox denies its commands
// writing here, and Claude Code's file tools are denied it
// (modules/home-manager/claude), so only the excluded writers change it.
func Dir() string {
	return filepath.Join(hostpaths.StateHome(), "agent-tools", "reviews")
}

// name is an owner or repository name, GitHub's form, which keeps a record
// inside Dir.
var name = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// ValidName reports whether s is a GitHub owner or repository name.
func ValidName(s string) bool { return name.MatchString(s) }

var sha = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// RangeFile is the record of base..head in owner/repo, both full SHAs.
func RangeFile(owner, repo, base, head string) (string, error) {
	if err := checkRepo(owner, repo); err != nil {
		return "", err
	}
	if !sha.MatchString(base) || !sha.MatchString(head) {
		return "", fmt.Errorf("a range's record needs full SHAs, got %q..%q", base, head)
	}
	return filepath.Join(Dir(), owner, repo, base+".."+head+".org"), nil
}

// PRFile is the record of pull request n's threads in owner/repo.
func PRFile(owner, repo string, n int) (string, error) {
	if err := checkRepo(owner, repo); err != nil {
		return "", err
	}
	if n <= 0 {
		return "", fmt.Errorf("%d is not a pull request number", n)
	}
	return filepath.Join(Dir(), owner, repo, fmt.Sprintf("pr-%d.org", n)), nil
}

func checkRepo(owner, repo string) error {
	if !ValidName(owner) || !ValidName(repo) {
		return fmt.Errorf("%q/%q is not a GitHub owner/name", owner, repo)
	}
	return nil
}
