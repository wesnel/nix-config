package plans

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	Tokens    int64    `json:"tokens,omitempty"`
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
	// Tokens is every event's tokens; TokensByPart keys a part's by its ID,
	// and the rest by "".
	Tokens       int64            `json:"tokens"`
	TokensByPart map[string]int64 `json:"tokensByPart"`
	TokensBy     []TokenRow       `json:"tokensBy"`
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
	s := Status{Plan: p.file(""), Title: orgTitle(string(data)), Steps: []Step{}, NotDone: []string{},
		DoneNotVerified: []string{}, TokensByPart: map[string]int64{}, TokensBy: tokenRows(events)}
	partOf := map[string]string{}
	index := map[string]int{}
	for _, it := range list {
		partOf[it.ID] = it.Part
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
		part := partOf[e.Item]
		if before, _, ok := strings.Cut(e.Item, "."); ok && partPattern.MatchString(before) {
			part = before
		} else if partPattern.MatchString(e.Item) {
			part = e.Item
		}
		s.Tokens += e.Tokens
		s.TokensByPart[part] += e.Tokens
		i, ok := index[e.Item]
		if !ok {
			continue
		}
		step := &s.Steps[i]
		step.Tokens += e.Tokens
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

// Org is s as Org: a table of steps, what is left, and tables of tokens.
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
	fmt.Fprintf(&b, "\n- Status: %s\n- Implemented: %s verified\n- Without done: %s\n- Done, not verified: %s\n- Tokens: %d\n",
		s.State, s.Implemented, list(s.NotDone), list(s.DoneNotVerified), s.Tokens)
	if len(s.TokensByPart) > 0 {
		b.WriteString("\n" + row("Part", "Tokens") + "|-\n")
		var parts []string
		for part := range s.TokensByPart {
			parts = append(parts, part)
		}
		sort.Strings(parts)
		for _, part := range parts {
			name := part
			if name == "" {
				name = "plan"
			}
			b.WriteString(row(name, fmt.Sprint(s.TokensByPart[part])))
		}
	}
	writeTokenRows(&b, s.TokensBy)
	return b.String()
}

func writeTokenRows(b *strings.Builder, rows []TokenRow) {
	if len(rows) == 0 {
		return
	}
	b.WriteString("\n" + row("Kind", "Model", "Effort", "Events", "Tokens") + "|-\n")
	for _, r := range rows {
		b.WriteString(row(r.Kind, r.Model, r.Effort, fmt.Sprint(r.Events), fmt.Sprint(r.Tokens)))
	}
}

// TokensOrg is rows as an Org table.
func TokensOrg(rows []TokenRow) string {
	var b strings.Builder
	writeTokenRows(&b, rows)
	return strings.TrimPrefix(b.String(), "\n")
}

// Tokens totals the tokens of every plan, every host's, by event kind,
// model and effort.
func (c Config) Tokens() ([]TokenRow, error) {
	logs, err := filepath.Glob(filepath.Join(c.Dir, "*.log.org"))
	if err != nil {
		return nil, err
	}
	var events []Event
	for _, log := range logs {
		p, err := c.Open(log)
		if err != nil {
			continue
		}
		e, err := p.Events()
		if err != nil {
			return nil, err
		}
		events = append(events, e...)
	}
	return tokenRows(events), nil
}
