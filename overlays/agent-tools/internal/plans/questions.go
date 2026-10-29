package plans

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Question is an open question: a heading under "** Questions" with an ID
// such as Q1, or P1.Q1 in a part's file, whose keyword is not a done one.
type Question struct {
	ID       string `json:"id"`
	Part     string `json:"part,omitempty"`
	File     string `json:"file"`
	Question string `json:"question"`
	// Priority is the heading's priority cookie's letter, if it has one.
	Priority string `json:"priority,omitempty"`
	// Kind is the KIND property, one of QuestionKinds, such as retro for
	// the retro skill's proposals; a plain question has none.
	Kind string `json:"kind,omitempty"`
	// Context is the heading's text that is not a list item.
	Context string `json:"context,omitempty"`
	// Recommendation is the RECOMMENDATION property.
	Recommendation string `json:"recommendation,omitempty"`
	// Alternatives are the heading's top-level list items.
	Alternatives []string `json:"alternatives,omitempty"`
	// After is the AFTER property: the IDs it waits for, and Waiting those
	// of them not yet settled. Only a question waiting for nothing is put to
	// the operator.
	After   []string `json:"after,omitempty"`
	Waiting []string `json:"waiting,omitempty"`
}

// Decision is a question settled as DECIDED.
type Decision struct {
	ID       string `json:"id"`
	Part     string `json:"part,omitempty"`
	File     string `json:"file"`
	Question string `json:"question"`
	// Answer is the ANSWER property, else the newest note in the heading's
	// LOGBOOK, which Org writes when the operator settles it in Emacs.
	Answer string `json:"answer"`
	// DecidedBy is "agent" for an agent's own decision on a constraint, and
	// empty for the operator's.
	DecidedBy  string `json:"decidedBy,omitempty"`
	Supersedes string `json:"supersedes,omitempty"`
}

// QuestionKinds are the kinds a question's KIND property may name: retro
// is a proposal of the retro skill's. They are stored in plans, so
// renaming one means rewriting every plan that holds it.
var QuestionKinds = []string{"retro"}

var listItem = regexp.MustCompile(`^[-+] (.*)$`)

// questionHeadings calls each with every question heading of the plan's
// file, and with all its parts' too, in file order.
func (p Plan) questionHeadings(all bool, each func(file string, h heading)) error {
	files := []string{p.file("")}
	if all {
		var err error
		if files, err = p.files(); err != nil {
			return err
		}
	}
	for _, f := range files {
		data, err := readPlan(f)
		if err != nil {
			return err
		}
		hs := parseOrg(string(data))
		sections := sectionsOf(hs)
		for i, h := range hs {
			if sections[i] == "Questions" && h.Level == 3 && h.Keyword != "" && questionID.MatchString(itemID(h)) {
				each(f, h)
			}
		}
	}
	return nil
}

// priorityRank orders priorities as Org does, a heading without one
// counting as [#B].
func priorityRank(p string) string {
	if p == "" {
		return "B"
	}
	return p
}

// Questions are the plan's open questions, and with all its parts' too,
// by priority, then in file order, each with what it still waits for.
func (p Plan) Questions(all bool) ([]Question, error) {
	list, err := p.itemList()
	if err != nil {
		return nil, err
	}
	events, err := p.Events()
	if err != nil {
		return nil, err
	}
	isSettled := settled(list, events)
	out := []Question{}
	err = p.questionHeadings(all, func(f string, h heading) {
		if h.Done {
			return
		}
		q := Question{ID: itemID(h), Part: partOf(f), File: f, Question: h.Title, Priority: h.Priority,
			Kind: h.Props["KIND"], Recommendation: h.Props["RECOMMENDATION"], After: afterIDs(h.Props)}
		q.Waiting = unsettled(q.After, isSettled)
		var context []string
		for _, line := range h.Body {
			if m := listItem.FindStringSubmatch(line); m != nil {
				q.Alternatives = append(q.Alternatives, strings.TrimSpace(m[1]))
			} else if strings.TrimSpace(line) != "" {
				context = append(context, strings.TrimSpace(line))
			}
		}
		q.Context = strings.Join(context, " ")
		out = append(out, q)
	})
	sort.SliceStable(out, func(i, j int) bool { return priorityRank(out[i].Priority) < priorityRank(out[j].Priority) })
	return out, err
}

// Decisions are the plan's questions settled as DECIDED, and with all its
// parts' too, in file order.
func (p Plan) Decisions(all bool) ([]Decision, error) {
	out := []Decision{}
	err := p.questionHeadings(all, func(f string, h heading) {
		if h.Keyword != kwDecided {
			return
		}
		answer := h.Props["ANSWER"]
		if answer == "" {
			answer = newestNote(h.Notes)
		}
		out = append(out, Decision{ID: itemID(h), Part: partOf(f), File: f, Question: h.Title, Answer: answer,
			DecidedBy: h.Props["DECIDED_BY"], Supersedes: h.Props["SUPERSEDES"]})
	})
	return out, err
}

// newestNote is the text of the first note in a LOGBOOK drawer's lines,
// where Org puts the newest: the indented lines under an item, joined.
func newestNote(lines []string) string {
	var text []string
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "- ") && len(text) > 0:
			return strings.Join(text, " ")
		case strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "":
			text = append(text, strings.TrimSpace(line))
		}
	}
	return strings.Join(text, " ")
}

// QuestionsOrg is qs as an Org list, one line each.
func QuestionsOrg(qs []Question) string {
	if len(qs) == 0 {
		return "No open questions.\n"
	}
	var b strings.Builder
	for _, q := range qs {
		fmt.Fprintf(&b, "- %s", q.ID)
		if q.Priority != "" {
			fmt.Fprintf(&b, " [#%s]", q.Priority)
		}
		fmt.Fprintf(&b, " :: %s", q.Question)
		if q.Recommendation != "" {
			fmt.Fprintf(&b, " Recommended: %s.", strings.TrimSuffix(q.Recommendation, "."))
		}
		if len(q.Alternatives) > 0 {
			fmt.Fprintf(&b, " Or: %s.", strings.Join(q.Alternatives, "; "))
		}
		if len(q.Waiting) > 0 {
			fmt.Fprintf(&b, " Waits for: %s.", strings.Join(q.Waiting, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// DecisionsOrg is ds as an Org list, one line each.
func DecisionsOrg(ds []Decision) string {
	if len(ds) == 0 {
		return "No decisions.\n"
	}
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "- %s :: %s %s\n", d.ID, d.Question, d.Answer)
	}
	return b.String()
}
