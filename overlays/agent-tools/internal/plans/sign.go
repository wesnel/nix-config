package plans

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// A sign-pass handed to the operator is tied to its session through the plan's
// log: agent-plan sign-id logs a sign-requested event naming the session
// and mints the ID, git sign-pass --notify ID logs the result, and remind
// or sign-result logs sign-delivered once the session has it. Only those
// commands log these kinds, so a result in the log is the pass's own.
const (
	signRequested = "sign-requested"
	signDelivered = "sign-delivered"
)

// signResultKinds are a finished pass's kinds, by outcome.
var signResultKinds = []string{"signed", "sign-refused", "sign-failed"}

// signKinds are every kind of a sign-pass's events.
var signKinds = append([]string{signRequested, signDelivered}, signResultKinds...)

// signID matches a sign-pass ID: the plan's name, then eight hex digits.
// It is safe as a file name and a shell word.
var signID = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]{0,190})\.([0-9a-f]{8})$`)

// signStaleAfter is how long after a pass finished its result is still
// news: later, it is delivered for the record and asks for nothing.
const signStaleAfter = 24 * time.Hour

// SignResult is how a sign-pass ended: its exit status (0 signed, 2
// refused before any touch, anything else failed) and one line saying
// what it did or why not.
type SignResult struct {
	Exit    int
	Summary string
}

// SignDelivery is a sign-pass's result as its session receives it.
type SignDelivery struct {
	ID       string `json:"id"`
	Plan     string `json:"plan"`
	Kind     string `json:"kind"`
	Exit     int    `json:"exit"`
	Summary  string `json:"summary"`
	Finished string `json:"finished"`
	// Stale is a result first delivered more than signStaleAfter after the
	// pass finished.
	Stale bool `json:"stale,omitempty"`
	// Delivered is a result the session already had.
	Delivered bool `json:"alreadyDelivered,omitempty"`
}

// Line is the result as one line of context for the session.
func (d SignDelivery) Line() string {
	outcome := map[string]string{"signed": "signed", "sign-refused": "refused"}[d.Kind]
	if outcome == "" {
		outcome = "failed"
	}
	if d.Stale {
		return fmt.Sprintf("sign-pass %s %s at %s, over a day ago (stale: for the record, no follow-up): %s",
			d.ID, outcome, strings.Trim(d.Finished, "[]"), d.Summary)
	}
	return fmt.Sprintf("sign-pass %s %s: %s", d.ID, outcome, d.Summary)
}

// SignRequest names a signing pass's notification destination and context.
// Plan, when nonempty, selects a plan instead of implicit session/worktree lookup.
type SignRequest struct {
	Plan     string
	Session  string
	Worktree string
	Branches []string
	Base     string
}

// RequestSign logs a sign-pass in r.Plan when set, else in the plan Locate
// finds for its session and worktree, and returns the pass's ID. An invalid
// explicit plan returns an error instead of falling back to Locate. The event's
// WORKTREE is where git sign-pass runs and its BASE, omitted for main by
// any of its names, what it signs from: Emacs and the dashboard show the
// operator the command from them.
func (c Config) RequestSign(r SignRequest, now time.Time) (string, error) {
	if r.Session == "" {
		return "", refusal.New("there is no session to deliver the sign-pass's result to. Pass the harness's session ID with `agent-plan sign-id --session ID`")
	}
	if r.Worktree != "" && !filepath.IsAbs(r.Worktree) {
		return "", refusal.New("worktree %q is not absolute, and the operator runs git sign-pass from anywhere. Run `agent-plan sign-id` in the worktree, or pass its full path with `--worktree`", r.Worktree)
	}
	base := r.Base
	if base != "" {
		if err := checkWords("base", []string{base}); err != nil {
			return "", err
		}
	}
	if slices.Contains([]string{"main", "origin/main", "refs/heads/main", "refs/remotes/origin/main"}, base) {
		base = ""
	}
	var p Plan
	if r.Plan != "" {
		var err error
		p, err = c.Open(r.Plan)
		if err != nil {
			return "", err
		}
	} else {
		var ok bool
		p, _, ok = c.Locate(r.Session, r.Worktree)
		if !ok {
			return "", refusal.New("no plan of this host names session %s or this worktree, and a sign-pass's result is kept in its plan's log. Make one with `agent-plan new` as the plan-work skill says, then run `agent-plan sign-id` again", r.Session)
		}
	}
	var b [4]byte
	rand.Read(b[:])
	id := fmt.Sprintf("%s.%x", p.Name, b)
	if _, err := p.record(Event{Kind: signRequested, Item: p.Name, Summary: id, Sign: id, Session: r.Session,
		Branch: strings.Join(r.Branches, ","), Worktree: r.Worktree, Base: base}, now); err != nil {
		return "", err
	}
	return id, nil
}

// CheckSignID refuses what cannot be a sign-pass ID.
func CheckSignID(id string) error {
	if !signID.MatchString(id) {
		return refusal.New("%q is not a sign-pass ID. Pass the ID `agent-plan sign-id` printed", id)
	}
	return nil
}

// signPlan is the plan whose log holds sign-pass id.
func (c Config) signPlan(id string) (Plan, error) {
	if err := CheckSignID(id); err != nil {
		return Plan{}, err
	}
	m := signID.FindStringSubmatch(id)
	p, err := c.Open(m[1])
	if err != nil {
		return Plan{}, refusal.New("sign-pass %s names plan %s, which is not under %s. Check the ID against the one `agent-plan sign-id` printed", id, m[1], c.Dir)
	}
	return p, p.checkOwn()
}

// RecordSign logs how sign-pass id ended, in the plan that requested it,
// and returns that plan's file.
func (c Config) RecordSign(id string, r SignResult, now time.Time) (string, error) {
	p, err := c.signPlan(id)
	if err != nil {
		return "", err
	}
	err = p.withLock(func(l *lockedPlan) error {
		events, err := p.Events()
		if err != nil {
			return err
		}
		i := slices.IndexFunc(events, func(e Event) bool { return e.Kind == signRequested && e.Sign == id })
		if i < 0 {
			return refusal.New("%s's log holds no sign-pass %s. Check the ID against the one `agent-plan sign-id` printed", p.Name, id)
		}
		kind := "sign-failed"
		switch r.Exit {
		case 0:
			kind = "signed"
		case 2:
			kind = "sign-refused"
		}
		_, err = l.log(Event{Kind: kind, Item: events[i].Item, Summary: strings.Join(strings.Fields(r.Summary), " "),
			Sign: id, Session: events[i].Session, Exit: strconv.Itoa(r.Exit)}, now)
		return err
	})
	return p.File(), err
}

// DeliverSigns is the results of the sign-passes session requested that it
// has not yet received. The events name the session, even when an explicit
// plan has no session or worktree association.
// Each is logged as delivered before it is returned, under the log's lock,
// so prompts arriving at once receive it once between them.
func (c Config) DeliverSigns(session, worktree string, now time.Time) []SignDelivery {
	if session == "" {
		return nil
	}
	own, err := c.ownPlans()
	if err != nil {
		return nil
	}
	var out []SignDelivery
	for _, p := range own {
		mine := func(e Event) bool { return e.Session == session }
		// Most prompts find nothing to deliver, and need not wait for the lock.
		if events, err := p.Events(); err != nil || len(pending(events, mine)) == 0 {
			continue
		}
		p.withLock(func(l *lockedPlan) error {
			ds, err := l.deliver(mine, now)
			for _, d := range ds {
				if !d.Delivered {
					out = append(out, d)
				}
			}
			return err
		})
	}
	return out
}

// SignResults is every result of sign-pass id so far. With consume, those
// not yet delivered are logged as delivered, as DeliverSigns does.
func (c Config) SignResults(id string, consume bool, now time.Time) ([]SignDelivery, error) {
	p, err := c.signPlan(id)
	if err != nil {
		return nil, err
	}
	this := func(e Event) bool { return e.Sign == id }
	if !consume {
		events, err := p.Events()
		if err != nil {
			return nil, err
		}
		delivered := deliveredIDs(events)
		var out []SignDelivery
		for _, e := range results(events, this) {
			out = append(out, delivery(p, e, delivered[e.ID], now))
		}
		return out, nil
	}
	var out []SignDelivery
	err = p.withLock(func(l *lockedPlan) error {
		var err error
		out, err = l.deliver(this, now)
		return err
	})
	return out, err
}

// deliver logs each result that matches and is not yet delivered as
// delivered, and returns every result that matches.
func (l *lockedPlan) deliver(match func(Event) bool, now time.Time) ([]SignDelivery, error) {
	events, err := l.p.Events()
	if err != nil {
		return nil, err
	}
	delivered := deliveredIDs(events)
	var out []SignDelivery
	for _, e := range results(events, match) {
		d := delivery(l.p, e, delivered[e.ID], now)
		if !d.Delivered {
			if _, err := l.log(Event{Kind: signDelivered, Item: e.Item, Summary: e.Sign, Sign: e.Sign, Session: e.Session,
				Delivers: e.ID}, now); err != nil {
				return out, err
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// results are the finished passes' events that match.
func results(events []Event, match func(Event) bool) []Event {
	var out []Event
	for _, e := range events {
		if slices.Contains(signResultKinds, e.Kind) && match(e) {
			out = append(out, e)
		}
	}
	return out
}

// pending are the results that match and are not yet delivered.
func pending(events []Event, match func(Event) bool) []Event {
	delivered := deliveredIDs(events)
	return slices.DeleteFunc(results(events, match), func(e Event) bool { return delivered[e.ID] })
}

// deliveredIDs are the event IDs of the results already delivered.
func deliveredIDs(events []Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		if e.Kind == signDelivered {
			out[e.Delivers] = true
		}
	}
	return out
}

func delivery(p Plan, e Event, delivered bool, now time.Time) SignDelivery {
	exit, _ := strconv.Atoi(e.Exit)
	d := SignDelivery{ID: e.Sign, Plan: p.File(), Kind: e.Kind, Exit: exit, Summary: e.Summary, Finished: e.Time,
		Delivered: delivered}
	if at, err := time.ParseInLocation("[2006-01-02 Mon 15:04]", e.Time, now.Location()); err == nil && !delivered {
		d.Stale = now.Sub(at) > signStaleAfter
	}
	return d
}

// remindSigns is the line of each sign-pass result the session of a main
// thread's prompt has not yet received: Claude Code's and Codex's
// UserPromptSubmit.
func (c Config) remindSigns(input []byte, now time.Time) string {
	var in struct {
		Event     string `json:"hook_event_name"`
		SessionID string `json:"session_id"`
		AgentID   string `json:"agent_id"`
		Cwd       string `json:"cwd"`
	}
	if json.Unmarshal(input, &in) != nil || in.Event != "UserPromptSubmit" || in.AgentID != "" {
		return ""
	}
	top, _ := checkout(in.Cwd)
	var lines []string
	for _, d := range c.DeliverSigns(in.SessionID, top, now) {
		lines = append(lines, d.Line())
	}
	return strings.Join(lines, "\n")
}

// WithContext is the hook output out with text added to its
// additionalContext, or a new hookSpecificOutput for input's event carrying
// text.
func WithContext(out, input []byte, text string) []byte {
	var event struct {
		Name string `json:"hook_event_name"`
	}
	json.Unmarshal(input, &event)
	var o map[string]any
	if json.Unmarshal(out, &o) != nil {
		o = map[string]any{}
	}
	specific, _ := o["hookSpecificOutput"].(map[string]any)
	if specific == nil {
		specific = map[string]any{"hookEventName": event.Name}
		o["hookSpecificOutput"] = specific
	}
	if before, _ := specific["additionalContext"].(string); before != "" {
		text = before + "\n\n" + text
	}
	specific["additionalContext"] = text
	data, _ := json.Marshal(o)
	return data
}
