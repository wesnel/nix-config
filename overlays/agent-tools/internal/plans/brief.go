package plans

import (
	"fmt"
	"strings"
	"time"
)

// Brief is what an agent needs from the plan to go on after its
// conversation was compacted: the goal, the decisions, what is ready, the
// questions Wesley can answer now, the steps done but not verified, and
// how many follow-ups are open.
func (p Plan) Brief(now time.Time) (string, error) {
	s, err := p.StatusAt(now)
	if err != nil {
		return "", err
	}
	ds, err := p.Decisions(true)
	if err != nil {
		return "", err
	}
	qs, err := p.Questions(true)
	if err != nil {
		return "", err
	}
	goal, err := p.Goal("")
	if err != nil {
		goal = "none written\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Brief of plan %s, %s:\nGoal:\n%s", p.file(""), s.Title, goal)
	b.WriteString("Decided:\n")
	if len(ds) == 0 {
		b.WriteString("none\n")
	}
	b.WriteString(strings.TrimPrefix(DecisionsOrg(ds), "No decisions.\n"))
	var askable []string
	for _, q := range Askable(qs) {
		askable = append(askable, q.ID)
	}
	fmt.Fprintf(&b, "Steps ready: %s\nParts ready to start: %s\nQuestions for Wesley: %s\nDone, not verified: %s\nFollow-ups open: %d\n",
		list(s.ReadySteps), list(s.Ready), list(askable), list(s.DoneNotVerified), len(s.Followups))
	return b.String(), nil
}
