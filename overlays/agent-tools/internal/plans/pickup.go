package plans

import (
	"fmt"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// pickupPrompt is the line carry prints for the operator to paste into a
// fresh session, which picks the plan up from it.
func pickupPrompt(p Plan) string {
	return fmt.Sprintf("Pick up the plan %s: run `agent-plan pickup %s`, and go on from what it prints.", p.Name, p.file(""))
}

// Pickup adds session, as harness:id, to the plan's SESSIONS, sets its
// STATUS working, and returns what the session reads first: the skills
// the plan works under, its open follow-ups, and its brief. A plan halted
// after planning is picked up as it is; one whose work was carried on is
// refused, naming the plan that holds it. A second run in the same
// session changes nothing.
func (p Plan) Pickup(session string, now time.Time) (string, error) {
	if err := p.checkOwn(); err != nil {
		return "", err
	}
	if session == "" {
		return "", refusal.New("pickup adds the session it runs in to %s, and none is named. Run it in a Claude Code or Codex session, or pass `--session HARNESS:ID`", p.Name)
	}
	props, err := planHeading(p.file(""))
	if err != nil {
		return "", err
	}
	if to := props[propCarriedTo]; to != "" {
		return "", refusal.New("%s's work was carried to %s. Pick that one up: `agent-plan pickup %s`", p.Name, to, p.carriedToFile(to))
	}
	if _, err := p.AddToProperty("", "SESSIONS", session, now); err != nil {
		return "", err
	}
	if props["STATUS"] != "working" {
		if err := p.SetProperty("", "STATUS", []string{"working"}, now); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Picked up plan %s in session %s.\n", p.file(""), session)
	if skills := strings.Fields(props["SKILLS"]); len(skills) > 0 {
		fmt.Fprintf(&b, "Load these skills before going on: %s\n", strings.Join(skills, " "))
	}
	s, err := p.StatusAt(now)
	if err != nil {
		return "", err
	}
	if len(s.Followups) > 0 {
		items, err := p.items()
		if err != nil {
			return "", err
		}
		b.WriteString("Follow-ups open, outside the plan's job until made a step:\n")
		for _, id := range s.Followups {
			fmt.Fprintf(&b, "- %s %s\n", id, items[id].Title)
		}
	}
	brief, err := p.Brief(now)
	if err != nil {
		return "", err
	}
	b.WriteString(brief)
	return b.String(), nil
}
