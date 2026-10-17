package loop

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
)

// ProposalKinds are what the loop may propose: a follow-up for a finding
// the PR should not fix, a split of fixups into a PR of their own, or a
// deferral of threads.
var ProposalKinds = []string{"follow-up", "split", "defer"}

// ProposedFixup is a loop fixup a proposal would move out of the PR.
type ProposedFixup struct {
	Oid     string `json:"oid"`
	Subject string `json:"subject"`
}

// Proposal is a decision the loop puts to the operator. Nothing acts on
// it: the operator decides, and the loop goes on as it was.
type Proposal struct {
	ID      int             `json:"id"`
	Kind    string          `json:"kind"`
	PR      int             `json:"pr"`
	Summary string          `json:"summary"`
	Threads []string        `json:"threads,omitempty"`
	Fixups  []ProposedFixup `json:"fixups,omitempty"`
	// Size is a rough count of the changed lines involved.
	Size  int    `json:"size,omitempty"`
	Round int    `json:"round"`
	At    string `json:"at"`
}

// threadID is the shape of a review thread's GraphQL node ID.
var threadID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Propose records a proposal of kind on PR pr in the loop's state, for
// report.md, and writes its marker line to marker, which `pr-loop watch
// --events proposal` passes on to whoever started the loop. fixups name
// loop fixups, by any revision, that the proposal would move.
func Propose(kind string, pr int, summary string, threads, fixups []string, size int, marker io.Writer) (*Proposal, error) {
	if !contains(ProposalKinds, kind) {
		return nil, refuse("%q is not a proposal; propose one of %s", kind, strings.Join(ProposalKinds, ", "))
	}
	summary = strings.Join(strings.Fields(summary), " ")
	if summary == "" {
		return nil, refuse("a proposal needs a summary the operator can decide on; pass it with --summary")
	}
	// The marker line must stay one line of plain text.
	if strings.ContainsFunc(summary, unicode.IsControl) {
		return nil, refuse("the summary holds control characters; write it as plain text")
	}
	for _, id := range threads {
		if !threadID.MatchString(id) {
			return nil, refuse("%q is not a thread ID; take thread IDs from `pr-loop threads PR`", id)
		}
	}
	if size < 0 {
		return nil, refuse("--size is a count of changed lines, so it cannot be negative")
	}
	state, err := loadState(true)
	if err != nil {
		return nil, err
	}
	if _, ok := state.prBranches()[pr]; !ok {
		return nil, refuse(notInLoop, pr)
	}
	p := &Proposal{ID: len(state.Proposals) + 1, Kind: kind, PR: pr, Summary: summary, Threads: threads, Size: size,
		Round: state.Round, At: now().UTC().Format(time.RFC3339)}
	for _, rev := range fixups {
		oid, err := git.Run("rev-parse", "--verify", "-q", rev+"^{commit}")
		if err != nil {
			return nil, refuse("%s is not a commit here; name each fixup by its commit ID", rev)
		}
		message, err := git.Run("log", "-1", "--format=%B", oid)
		if err != nil {
			return nil, err
		}
		if k, _ := fold(message); k != history.Fixup {
			return nil, refuse("%s %q is not a loop fixup; name only the fixups the proposal would move", rev, subject(message))
		}
		p.Fixups = append(p.Fixups, ProposedFixup{Oid: oid, Subject: subject(message)})
	}
	state.Proposals = append(state.Proposals, p)
	if err := state.save(); err != nil {
		return nil, err
	}
	fmt.Fprintln(marker, p.Marker())
	return p, nil
}

// Marker is the proposal in one line.
func (p *Proposal) Marker() string {
	var details []string
	if len(p.Threads) > 0 {
		details = append(details, "threads "+strings.Join(p.Threads, " "))
	}
	if len(p.Fixups) > 0 {
		var short []string
		for _, f := range p.Fixups {
			short = append(short, f.Oid[:min(12, len(f.Oid))])
		}
		details = append(details, "fixups "+strings.Join(short, " "))
	}
	if p.Size > 0 {
		details = append(details, fmt.Sprintf("about %d changed lines", p.Size))
	}
	line := fmt.Sprintf("%s %d for #%d: %s: %s", ProposalMarker, p.ID, p.PR, p.Kind, p.Summary)
	if len(details) > 0 {
		line += " (" + strings.Join(details, "; ") + ")"
	}
	return line
}

// ProposalMarker starts a proposal's marker line.
const ProposalMarker = "pr-loop: proposal"
