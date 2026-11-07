package plans

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// A brag item is a question of kind brag: its heading is the entry's
// title, and the operator's answer, once DECIDED, its approved summary.

// BragItemOptions name an approved brag item to write: the change merged
// on Day, its pull request PR, the plan as Open reads it, and the item's
// question ID there, in the plan's file or a part's.
type BragItemOptions struct {
	Day                time.Time
	PR, Plan, Question string
}

// BragItem is an approved brag item: its plan's file, its question ID and
// part, its title and summary, and the PULL_REQUESTS of the file that
// holds it, the plan's or the part's, among which is its change's.
type BragItem struct {
	Plan         string   `json:"plan"`
	ID           string   `json:"id"`
	Part         string   `json:"part,omitempty"`
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	PullRequests []string `json:"pullRequests,omitempty"`
}

// WriteBragItem writes the approved brag item o names into the brag file
// as WriteBrag does, its title and summary read from the decided question,
// so that only a summary the operator approved reaches the file. It
// refuses a question that is not a brag item, or not approved.
func (c Config) WriteBragItem(o BragItemOptions) (NotesWrite, error) {
	p, err := c.Open(o.Plan)
	if err != nil {
		return NotesWrite{}, err
	}
	var found *heading
	err = p.questionHeadings(true, func(_ string, h heading) {
		if itemID(h) == o.Question {
			found = &h
		}
	})
	switch {
	case err != nil:
		return NotesWrite{}, err
	case found == nil:
		return NotesWrite{}, refusal.New("%s has no question %s. Pass the ID of a brag item, as `agent-plan brags` lists it", p.Name, o.Question)
	case found.Props["KIND"] != "brag":
		return NotesWrite{}, refusal.New("%s is not a brag item, a question of kind brag. Record one with `agent-plan ask --kind brag`", o.Question)
	case found.Keyword == kwAsk:
		return NotesWrite{}, refusal.New("brag item %s awaits the operator's approval of its summary. Leave it for the operator to answer through the questions skill, and write it once it is DECIDED", o.Question)
	case found.Keyword != kwDecided:
		return NotesWrite{}, refusal.New("brag item %s is %s, so it has no approved summary to keep. Leave it out of the brag file, and report it if it belongs there", o.Question, found.Keyword)
	case !approved(*found):
		return NotesWrite{}, refusal.New("brag item %s was settled by an agent, not approved by the operator. Leave it out of the brag file, and report it for the operator to ask again", o.Question)
	}
	return c.WriteBrag(BragOptions{Day: o.Day, PR: o.PR, Plan: o.Plan, Question: o.Question, Title: found.Title,
		Summary: answerOf(*found)})
}

// approved reports whether h is a brag item the operator approved: DECIDED
// with no DECIDED_BY, which only an agent's own decision (ask --answer)
// carries. A decision recorded with decide carries none whoever ran it, so
// refusing ask --answer for a brag item is what keeps an agent from
// approving one.
func approved(h heading) bool {
	return h.Props["KIND"] == "brag" && h.Keyword == kwDecided && h.Props["DECIDED_BY"] == ""
}

// bragPlanID is the plan's file-level ID in a brag entry's PLAN link.
var bragPlanID = regexp.MustCompile(`id:([A-Za-z0-9-]+)`)

// PendingBrags are the approved brag items of every host's plans, their
// parts' included, that no brag file holds yet, newest plan first. A brag
// file holds an item when one of its entries names its plan in PLAN and
// its ID in QUESTION. A plan that cannot be read is named in the error,
// beside the rest.
func (c Config) PendingBrags() ([]BragItem, error) {
	written := map[string]bool{}
	files, err := c.notesFiles()
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		if !strings.HasPrefix(rel, "brag/") {
			continue
		}
		data, err := readPlan(filepath.Join(c.repo(), filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		for _, h := range parseOrg(string(data)) {
			if m := bragPlanID.FindStringSubmatch(h.Props["PLAN"]); h.Level == 1 && m != nil {
				written[m[1]+"\n"+h.Props["QUESTION"]] = true
			}
		}
	}
	ps, failed, err := c.plans("")
	if err != nil {
		return nil, err
	}
	out := []BragItem{}
	var errs []error
	for _, name := range failed {
		errs = append(errs, fmt.Errorf("%s could not be read", name))
	}
	for _, p := range ps {
		data, err := readPlan(p.File())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		id := fileID(string(data))
		prs := map[string][]string{}
		var ferr error
		err = p.questionHeadings(true, func(f string, h heading) {
			if !approved(h) || written[id+"\n"+itemID(h)] {
				return
			}
			if _, ok := prs[f]; !ok {
				props, err := planHeading(f)
				ferr = errors.Join(ferr, err)
				prs[f] = strings.Fields(props["PULL_REQUESTS"])
			}
			out = append(out, BragItem{Plan: p.File(), ID: itemID(h), Part: partOf(f), Title: h.Title,
				Summary: answerOf(h), PullRequests: prs[f]})
		})
		if err = errors.Join(err, ferr); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
		}
	}
	return out, errors.Join(errs...)
}
