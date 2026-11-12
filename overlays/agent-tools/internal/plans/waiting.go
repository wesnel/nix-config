package plans

import (
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// handedBack is the kind of a hand-back, which alone carries a command for
// the operator.
const handedBack = "handed-back"

// Wait is a command a hand-back left for the operator to run: Item waits
// on them until progress or its acknowledgement is logged. Commands are
// returned as text for the operator to run themselves.
type Wait struct {
	Item    string `json:"item"`
	Command string `json:"command"`
	Dir     string `json:"dir,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Event and Time are the hand-back's.
	Event string `json:"event"`
	Time  string `json:"time"`
}

// unsettling events leave commands pending: observations and changes to
// plan text, properties, schedule or the log seal.
var unsettling = set("planned", propertyKind, "resealed", "referenced", linkedKind, "postponed", "observed")

// Waiting are the plan's items whose newest event, of the kinds that
// settle one, is a hand-back with a command, in log order.
func (p Plan) Waiting() ([]Wait, error) {
	events, err := p.Events()
	if err != nil {
		return nil, err
	}
	return waiting(events), nil
}

func waiting(events []Event) []Wait {
	newest := map[string]int{}
	for i, e := range events {
		if e.Kind == "acknowledged" {
			if pending, ok := newest[e.Item]; ok && events[pending].ID == e.Acknowledges {
				delete(newest, e.Item)
			}
			continue
		}
		if !unsettling[e.Kind] {
			newest[e.Item] = i
		}
	}
	out := []Wait{}
	for i, e := range events {
		if at, ok := newest[e.Item]; ok && at == i && e.Kind == handedBack && e.Command != "" {
			out = append(out, Wait{Item: e.Item, Command: e.Command, Dir: e.Dir, Summary: e.Summary, Event: e.ID, Time: e.Time})
		}
	}
	return out
}

// Acknowledge records evidence for the pending command identified by event.
// Retries return its existing acknowledgement, even after another handoff.
func (p Plan) Acknowledge(event, evidence string, now time.Time) (Event, error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	if strings.TrimSpace(evidence) == "" {
		return Event{}, refusal.New("acknowledgement needs evidence. Use `agent-plan acknowledge PLAN EVENT --evidence TEXT`")
	}
	if err := checkLine("evidence", evidence); err != nil {
		return Event{}, err
	}
	var result Event
	err := p.withLock(func(l *lockedPlan) error {
		var err error
		result, err = l.acknowledge(event, evidence, now)
		return err
	})
	return result, err
}

func (l *lockedPlan) acknowledge(event, evidence string, now time.Time) (Event, error) {
	events, err := l.p.Events()
	if err != nil {
		return Event{}, err
	}
	var handoff Event
	for _, e := range events {
		if e.ID == event {
			handoff = e
		}
	}
	if handoff.Kind != handedBack || handoff.Command == "" {
		return Event{}, refusal.New("%q is not a command-bearing handoff. Choose its event from `agent-plan waiting PLAN --json`", event)
	}
	for _, e := range events {
		if e.Kind == "acknowledged" && e.Acknowledges == event && e.Item == handoff.Item {
			return e, nil
		}
	}
	for _, w := range waiting(events) {
		if w.Event == event {
			return l.log(Event{Kind: "acknowledged", Item: handoff.Item, Acknowledges: event, Evidence: evidence}, now)
		}
	}
	return Event{}, refusal.New("%q is no longer pending. Choose the current handoff from `agent-plan waiting PLAN --json`", event)
}
