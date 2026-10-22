package export

import (
	"encoding/json"
	"strings"

	"github.com/wesnel/agent-tools/internal/diagram"
	"github.com/wesnel/agent-tools/internal/record"
)

// maxDiagrams is the most diagrams a pull request's review shows.
const maxDiagrams = 5

// diagramBudget is the most bytes of JSON the GitHub document's diagrams
// take in all; a diagram that would go past it is left out, so the
// document stays within maxDoc.
const diagramBudget = 64 << 10

// A pull request's review, as this host's records keep it (git delegate's
// newest range record of it, and its threads' record), goes into a PR's
// and a Request's Record* fields in counts alone: no title, summary,
// finding or other model text about a colleague's code leaves the host
// but its diagrams' labels, redacted and capped.

// FindingCount is how many of the record's findings have State, a record
// keyword such as OPEN or REBUT.
type FindingCount struct {
	State string `json:"state"`
	Count int    `json:"count"`
}

// ReviewDiagram is a diagram the record keeps: its ID and kind, and the
// Mermaid that diagram.Mermaid renders from its JSON with labels redacted,
// in the grammar diagram.ValidSource takes. Its title stays on the host.
type ReviewDiagram struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
}

// reviewSummary is a pull request's review, as its Record* fields carry
// it.
type reviewSummary struct {
	exists                       bool
	findings                     []FindingCount
	threads, unanswered, drafted int
	diagrams                     []ReviewDiagram
}

// states are the record's keywords, in the order a summary lists them.
var states = []record.State{record.Open, record.Defer, record.Operator, record.Fix, record.Propose, record.Rebut,
	record.Duplicate, record.Belongs}

// reviewOf is repo#number's review, its diagrams taken from what is left
// of budget.
func reviewOf(repo string, number int, budget *int) reviewSummary {
	out := reviewSummary{findings: []FindingCount{}, diagrams: []ReviewDiagram{}}
	owner, name, _ := strings.Cut(repo, "/")
	if rec, _, err := record.NewestRange(owner, name, number, ""); err == nil && rec != nil {
		out.exists = true
		for _, s := range states {
			n := 0
			for _, f := range rec.Findings {
				if f.State == s {
					n++
				}
			}
			if n > 0 {
				out.findings = append(out.findings, FindingCount{State: string(s), Count: n})
			}
		}
		for _, d := range rec.Diagrams {
			if len(out.diagrams) == maxDiagrams {
				break
			}
			rd, ok := reviewDiagramOf(d)
			if !ok {
				continue
			}
			if size := rd.size(); size <= *budget {
				*budget -= size
				out.diagrams = append(out.diagrams, rd)
			}
		}
	}
	if path, err := record.PRFile(owner, name, number); err == nil {
		if p, err := record.ReadPR(path); err == nil {
			c := p.Counts()
			out.threads, out.unanswered, out.drafted = len(p.Threads), c.Unanswered, c.Drafted
		}
	}
	return out
}

// reviewDiagramOf is d as exported, or false where its critique has not
// passed it (the page draws a diagram as one that holds), its JSON is no
// diagram, or its labels, redacted, no longer make a source of the grammar
// the page renders.
func reviewDiagramOf(d record.Diagram) (ReviewDiagram, bool) {
	if d.Verdict != "holds" && d.Verdict != "corrected" {
		return ReviewDiagram{}, false
	}
	parsed, err := diagram.Parse([]byte(d.JSON))
	if err != nil {
		return ReviewDiagram{}, false
	}
	for i := range parsed.Nodes {
		parsed.Nodes[i].Label = label(parsed.Nodes[i].Label)
	}
	for i := range parsed.Edges {
		parsed.Edges[i].Label = label(parsed.Edges[i].Label)
	}
	out := ReviewDiagram{ID: d.ID, Kind: parsed.Kind, Source: parsed.Mermaid()}
	return out, diagram.ValidSource(out.Source)
}

// size is the bytes d takes in the GitHub document.
func (d ReviewDiagram) size() int {
	data, err := json.MarshalIndent(d, "        ", "  ")
	if err != nil {
		return diagramBudget + 1
	}
	return len(data)
}

// label is a diagram's label with known secret shapes redacted, then
// cleaned again, since a redaction's brackets are outside a label's
// characters, and cut to diagram.MaxLabel runes.
func label(s string) string {
	s = diagram.CleanLabel(text(s, short))
	if r := []rune(s); len(r) > diagram.MaxLabel {
		s = strings.TrimSpace(string(r[:diagram.MaxLabel]))
	}
	return s
}
