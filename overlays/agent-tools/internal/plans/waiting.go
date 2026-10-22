package plans

// handedBack is the kind of a hand-back, which alone carries a command for
// the operator.
const handedBack = "handed-back"

// Wait is a command a hand-back left for the operator to run: Item waits
// on them until an event that settles it is logged. Emacs's agenda and the
// dashboard show these, from `agent-plan waiting PLAN --json`, for the
// operator to run themselves.
type Wait struct {
	Item    string `json:"item"`
	Command string `json:"command"`
	Dir     string `json:"dir,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Event and Time are the hand-back's.
	Event string `json:"event"`
	Time  string `json:"time"`
}

// unsettling are the kinds of event that change a plan's text, properties
// or schedule, or its log's seal, and so leave an item waiting on the
// operator as it was.
var unsettling = set("planned", propertyKind, "resealed", "referenced", "postponed")

// Waiting are the plan's items whose newest event, of the kinds that
// settle one, is a hand-back with a command, in log order.
func (p Plan) Waiting() ([]Wait, error) {
	events, err := p.Events()
	if err != nil {
		return nil, err
	}
	newest := map[string]int{}
	for i, e := range events {
		if !unsettling[e.Kind] {
			newest[e.Item] = i
		}
	}
	out := []Wait{}
	for i, e := range events {
		if newest[e.Item] == i && e.Kind == handedBack && e.Command != "" {
			out = append(out, Wait{Item: e.Item, Command: e.Command, Dir: e.Dir, Summary: e.Summary, Event: e.ID, Time: e.Time})
		}
	}
	return out, nil
}
