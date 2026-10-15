package plans

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Question is an open question: a heading under "** Open questions" with
// an ID such as Q1, or P1.Q1 in a part's file.
type Question struct {
	ID       string `json:"id"`
	Part     string `json:"part,omitempty"`
	File     string `json:"file"`
	Question string `json:"question"`
	// Context is the heading's text that is not a list item.
	Context string `json:"context,omitempty"`
	// Recommendation is the RECOMMENDATION property.
	Recommendation string `json:"recommendation,omitempty"`
	// Alternatives are the heading's top-level list items.
	Alternatives []string `json:"alternatives,omitempty"`
	// After is the AFTER property: the IDs it waits for, and Waiting those
	// of them not yet settled. Only a question waiting for nothing is put to
	// Wesley.
	After   []string `json:"after,omitempty"`
	Waiting []string `json:"waiting,omitempty"`
}

var (
	questionID = regexp.MustCompile(`^(P[0-9]+\.)?Q[0-9]+$`)
	listItem   = regexp.MustCompile(`^[-+] (.*)$`)
)

// Questions are the plan's open questions, and with all its parts' too, in
// file order, each with what it still waits for.
func (p Plan) Questions(all bool) ([]Question, error) {
	files := []string{p.file("")}
	if all {
		var err error
		if files, err = p.files(); err != nil {
			return nil, err
		}
	}
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
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		section := ""
		for _, h := range parseOrg(string(data)) {
			if h.Level <= 2 {
				section = h.Title
				continue
			}
			if section != "Open questions" || h.Level != 3 || !questionID.MatchString(h.Props["ID"]) {
				continue
			}
			q := Question{ID: h.Props["ID"], Part: partOf(f), File: f, Question: h.Title,
				Recommendation: h.Props["RECOMMENDATION"], After: afterIDs(h.Props)}
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
		}
	}
	return out, nil
}

// QuestionsOrg is qs as an Org list, one line each.
func QuestionsOrg(qs []Question) string {
	if len(qs) == 0 {
		return "No open questions.\n"
	}
	var b strings.Builder
	for _, q := range qs {
		fmt.Fprintf(&b, "- %s :: %s", q.ID, q.Question)
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
