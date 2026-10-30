package plans

import "slices"

// Checkpoint is a natural stopping point of a session's plan: its file,
// the kind of the event that made it, and that event's ID, which tells one
// checkpoint from the next.
type Checkpoint struct {
	Plan string
	Kind string
	Key  string
}

// passiveKinds are events the hooks log without the work moving on, which
// leave a checkpoint standing.
var passiveKinds = []string{signDelivered, "referenced"}

// Checkpoint is the stopping point the plan session answers for, as it
// stops in cwd, stands at: its newest event, past the passive ones, is a
// checkpoint event, or a decision that left no question the operator can
// answer now. It reads local files alone and is false on any error.
func (c Config) Checkpoint(session, cwd string) (Checkpoint, bool) {
	top, _ := checkout(cwd)
	p, found := c.sessionPlan(session, top)
	if !found {
		return Checkpoint{}, false
	}
	events, err := p.Events()
	if err != nil {
		return Checkpoint{}, false
	}
	i := len(events) - 1
	for i >= 0 && slices.Contains(passiveKinds, events[i].Kind) {
		i--
	}
	if i < 0 {
		return Checkpoint{}, false
	}
	e := events[i]
	switch {
	case slices.Contains(checkpointKinds, e.Kind):
	case e.Kind == "decided":
		qs, err := p.Questions(true)
		if err != nil || len(Askable(qs)) > 0 {
			return Checkpoint{}, false
		}
	default:
		return Checkpoint{}, false
	}
	return Checkpoint{Plan: p.File(), Kind: e.Kind, Key: e.ID}, true
}

// sessionPlan is the plan session answers for in the worktree at top: the
// one Locate finds, which the session's commits there go in.
func (c Config) sessionPlan(session, top string) (Plan, bool) {
	if session == "" {
		return Plan{}, false
	}
	p, _, ok := c.Locate(session, top)
	return p, ok
}
