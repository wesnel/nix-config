package plans

import (
	"slices"
	"strings"
)

// DesignReviewStarted is the kind agent-plan remind records when the main
// thread of a session starts a design-reviewer sub-agent in the
// background, or one returns in the foreground; only agent-tools logs it.
const DesignReviewStarted = "design-review-started"

// AnswersReview reports whether e answers a design review started before
// it in the same log: plan-reviewed for a review of the plan, or reviewed
// with role design-reviewer for one of the result. ReviewAnswers names
// them for an agent.
func AnswersReview(e Event) bool {
	return e.Kind == "plan-reviewed" || e.Kind == "reviewed" && e.Role == "design-reviewer"
}

// ReviewAnswers is how an agent logs an answer AnswersReview takes, for a
// plan whose ID stands at PLAN_ID.
const ReviewAnswers = "`--item PLAN_ID --kind plan-reviewed` for a review of the plan, " +
	"or `--item PLAN_ID --kind reviewed --role design-reviewer` for one of the result"

// reviewPlan is the plan a design review that session starts belongs in,
// among the plans naming the session: the one the reviewer's prompt names
// first, by its file or its ID, since a prompt names the plan under review
// before any it cites; else the one whose log has the latest event; false
// with none. A session works on one plan at a time, and the latest to
// change is the one under way.
func (c Config) reviewPlan(session, prompt string) (Plan, bool) {
	var named, latest Plan
	first := -1
	var at string
	ok := false
	for _, p := range c.sessionPlans(session) {
		if i := strings.Index(prompt, p.Name); i >= 0 && (first < 0 || i < first) {
			named, first = p, i
		}
		if first >= 0 {
			continue
		}
		events, err := p.Events()
		if err != nil {
			continue
		}
		last := ""
		if len(events) > 0 {
			last = events[len(events)-1].Time
		}
		if !ok || last > at {
			latest, at, ok = p, last, true
		}
	}
	if first >= 0 {
		return named, true
	}
	return latest, ok
}

// AnswerHint is the sentence that tells an agent how to answer a design
// review held in p, once its findings are settled.
func AnswerHint(p Plan) string {
	return "Once its findings are settled in the plan, log `agent-plan log " + p.File() + "` with " +
		strings.ReplaceAll(ReviewAnswers, "PLAN_ID", p.Name) + "; edits wait until then."
}

// sessionPlans are the plans naming session, each once, in Find's order.
func (c Config) sessionPlans(session string) []Plan {
	found, err := c.Find(FindOptions{Session: session})
	if err != nil {
		return nil
	}
	var seen []string
	var out []Plan
	for _, f := range found {
		if slices.Contains(seen, f.Plan) {
			continue
		}
		seen = append(seen, f.Plan)
		if p, err := c.Open(f.Plan); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// PendingPlanReviewOf is a design review that session started and that has
// not been answered, the plan whose log holds its record, and whether
// there is one, among the plans naming the session. The guard holds the
// session's edits while there is.
func (c Config) PendingPlanReviewOf(session string) (Plan, Event, bool) {
	if pending := c.pendingReviews(session); len(pending) > 0 {
		return pending[0].Plan, pending[0].start, true
	}
	return Plan{}, Event{}, false
}

// pendingReview is a plan holding a pending design review, and its last
// start.
type pendingReview struct {
	Plan
	start Event
}

// pendingReviews are the plans naming session that hold a pending design
// review, in Find's order. A session has few plans, so all are read.
func (c Config) pendingReviews(session string) []pendingReview {
	var out []pendingReview
	for _, p := range c.sessionPlans(session) {
		if e, pending, err := p.PendingPlanReview(session); err == nil && pending {
			out = append(out, pendingReview{p, e})
		}
	}
	return out
}

// PendingPlanReview is the last design-review-started of session in this
// plan's log, and whether one is pending: each answer (AnswersReview)
// answers one start before it, so two reviews in flight wait for two. Only
// the log's order counts, never its times or other plans.
func (p Plan) PendingPlanReview(session string) (Event, bool, error) {
	events, err := p.Events()
	if err != nil {
		return Event{}, false, err
	}
	var record Event
	open := 0
	for _, e := range events {
		switch {
		case e.Kind == DesignReviewStarted && e.Session == session:
			record, open = e, open+1
		case AnswersReview(e) && open > 0:
			open--
		}
	}
	return record, open > 0, nil
}
