package plans

import (
	"fmt"
	"os"
	"strings"
)

// Step is one step's progress.
type Step struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Part      string   `json:"part,omitempty"`
	Done      bool     `json:"done"`
	Verified  bool     `json:"verified"`
	Abandoned bool     `json:"abandoned,omitempty"`
	Commits   []string `json:"commits,omitempty"`
}

// Status is a plan's progress: its steps, which are the items whose IDs are
// S1, S2… or P1.S1…, against its log.
type Status struct {
	Plan  string `json:"plan"`
	Title string `json:"title"`
	State string `json:"status"`
	Steps []Step `json:"steps"`
	// NotDone are the steps with no done event, and DoneNotVerified those
	// with one but no verified event; abandoned steps are in neither.
	NotDone         []string `json:"notDone"`
	DoneNotVerified []string `json:"doneNotVerified"`
	// Implemented is Verified of Total, the steps not abandoned.
	Verified    int    `json:"verified"`
	Total       int    `json:"total"`
	Implemented string `json:"implemented"`
}

// Status reads the plan and its log.
func (p Plan) Status() (Status, error) {
	list, err := p.itemList()
	if err != nil {
		return Status{}, err
	}
	events, err := p.Events()
	if err != nil {
		return Status{}, err
	}
	data, err := os.ReadFile(p.file(""))
	if err != nil {
		return Status{}, err
	}
	s := Status{Plan: p.Dir, Title: orgTitle(string(data)), Steps: []Step{}, NotDone: []string{},
		DoneNotVerified: []string{}}
	index := map[string]int{}
	for _, it := range list {
		if it.ID == p.Name {
			s.State = it.Props["STATUS"]
		}
		if !stepPattern.MatchString(it.ID) {
			continue
		}
		index[it.ID] = len(s.Steps)
		s.Steps = append(s.Steps, Step{ID: it.ID, Title: it.Title, Part: it.Part})
	}
	for _, e := range events {
		i, ok := index[e.Item]
		if !ok {
			continue
		}
		step := &s.Steps[i]
		switch e.Kind {
		case "done":
			step.Done, step.Abandoned = true, false
			if e.Commit != "" {
				step.Commits = append(step.Commits, e.Commit)
			}
		case "verified":
			step.Verified = true
		case "abandoned":
			step.Abandoned = true
		}
	}
	for _, step := range s.Steps {
		switch {
		case step.Abandoned:
			continue
		case !step.Done:
			s.NotDone = append(s.NotDone, step.ID)
		case !step.Verified:
			s.DoneNotVerified = append(s.DoneNotVerified, step.ID)
		default:
			s.Verified++
		}
		s.Total++
	}
	s.Implemented = fmt.Sprintf("%d/%d", s.Verified, s.Total)
	return s, nil
}

// row is an Org table row; a cell's | is written as \vert.
func row(cells ...string) string {
	var b strings.Builder
	b.WriteString("|")
	for _, c := range cells {
		if c == "" {
			b.WriteString(" |")
		} else {
			b.WriteString(" " + strings.ReplaceAll(c, "|", `\vert{}`) + " |")
		}
	}
	return b.String() + "\n"
}

func yes(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func list(ids []string) string {
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// Org is s as Org: a table of steps, and what is left.
func (s Status) Org() string {
	var b strings.Builder
	fmt.Fprintf(&b, "* Status of %s\n", s.Title)
	b.WriteString(row("Step", "Title", "Done", "Verified", "Commit"))
	b.WriteString("|-\n")
	for _, step := range s.Steps {
		done := yes(step.Done)
		if step.Abandoned {
			done = "abandoned"
		}
		b.WriteString(row(step.ID, step.Title, done, yes(step.Verified), strings.Join(step.Commits, "; ")))
	}
	fmt.Fprintf(&b, "\n- Status: %s\n- Implemented: %s verified\n- Without done: %s\n- Done, not verified: %s\n",
		s.State, s.Implemented, list(s.NotDone), list(s.DoneNotVerified))
	return b.String()
}
