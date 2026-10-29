package plans

import "slices"

// DesignReviewStarted is the kind agent-plan remind records, about the
// plan itself, when a design-reviewer sub-agent of the plan's session is
// started in the background, or returns in the foreground; only
// agent-tools logs it.
const DesignReviewStarted = "design-review-started"

// PendingPlanReviewOf is the design review of a plan that session started
// and that has not been answered, the plan whose log holds its record, and
// whether there is one. The guard holds the session's edits while there is.
// A record is pending when:
//
//   - it is design-review-started of session, the last one in its plan;
//   - no plan-reviewed follows it in that plan; and
//   - no committed or plan-reviewed event, in any plan naming the session,
//     falls between that plan's first event and the record's minute,
//     either included: such an event makes it a review of the result, a
//     hook having recorded the commit and the review in either plan.
//
// Events of an earlier task, before the plan was begun, do not count. Times
// are compared as Event.Time sorts, to the minute.
func (c Config) PendingPlanReviewOf(session string) (Plan, Event, bool) {
	found, err := c.Find(FindOptions{Session: session})
	if err != nil {
		return Plan{}, Event{}, false
	}
	var seen []string
	var plans []Plan
	var logs [][]Event
	for _, f := range found {
		if slices.Contains(seen, f.Plan) {
			continue
		}
		seen = append(seen, f.Plan)
		p, err := c.Open(f.Plan)
		if err != nil {
			continue
		}
		events, err := p.Events()
		if err != nil {
			continue
		}
		plans, logs = append(plans, p), append(logs, events)
	}
	for i, own := range logs {
		if e, ok := pendingIn(session, own, logs); ok {
			return plans[i], e, true
		}
	}
	return Plan{}, Event{}, false
}

// PendingPlanReview is PendingPlanReviewOf for this plan's log alone.
func (p Plan) PendingPlanReview(session string) (Event, bool, error) {
	events, err := p.Events()
	if err != nil {
		return Event{}, false, err
	}
	e, ok := pendingIn(session, events, [][]Event{events})
	return e, ok, nil
}

// pendingIn applies PendingPlanReviewOf's rule to one plan's log, own, with
// the logs of every plan naming the session, own among them.
func pendingIn(session string, own []Event, logs [][]Event) (Event, bool) {
	var record Event
	open := false
	for _, e := range own {
		switch {
		case e.Kind == DesignReviewStarted && e.Session == session:
			record, open = e, true
		case e.Kind == "plan-reviewed":
			open = false
		}
	}
	if !open {
		return Event{}, false
	}
	begun := own[0].Time
	for _, events := range logs {
		for _, e := range events {
			if (e.Kind == "committed" || e.Kind == "plan-reviewed") && e.Time >= begun && e.Time <= record.Time {
				return Event{}, false
			}
		}
	}
	return record, true
}
