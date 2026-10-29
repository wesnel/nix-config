package plans

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// OpenKinds are the kinds of an open item: a hand-back's command or a
// sign-pass waiting on the operator, a question the operator can answer
// now, a retro proposal, a ready step, and an open follow-up.
var OpenKinds = []string{"waiting", "question", "retro", "step", "follow-up"}

// OpenItem is one thing left open in one of this host's plans, with the
// plan it belongs to, so that a listing can group items by any of the
// plan's fields.
type OpenItem struct {
	// Kind is one of OpenKinds.
	Kind      string `json:"kind"`
	Plan      string `json:"plan"`
	PlanTitle string `json:"planTitle"`
	// Halted is a plan whose STATUS is halted.
	Halted bool `json:"halted,omitempty"`
	// Item is the item's ID, or the plan's for a sign-pass.
	Item  string `json:"item"`
	Part  string `json:"part,omitempty"`
	Title string `json:"title"`
	// Time is the log's time of the item's own event: a question's asked,
	// a follow-up's discovered, a hand-back or a sign-pass request; for a
	// step, its first event. An item with none takes its plan's first.
	Time string `json:"time"`
	// Command and Dir are a hand-back's; Sign and Branch a sign-pass's.
	Command string `json:"command,omitempty"`
	Dir     string `json:"dir,omitempty"`
	Sign    string `json:"sign,omitempty"`
	Branch  string `json:"branch,omitempty"`
}

// OpenFilter keeps the items of Kind, or of every kind for "", whose
// time is at or after Since, or all for the zero time.
type OpenFilter struct {
	Kind  string
	Since time.Time
}

// ParseSince reads a --since value, a day as 2006-01-02 or a time as
// 2006-01-02 15:04, in loc.
func ParseSince(s string, loc *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, refusal.New("%q is not a day or a time. Pass `--since 'YYYY-MM-DD'` or `--since 'YYYY-MM-DD HH:MM'`", s)
}

// logTime is the layout of a log event's time.
const logTime = "[2006-01-02 Mon 15:04]"

// OpenItems are the open items of this host's plans, halted ones
// included, newest plan first, that f keeps. A plan that cannot be read
// is left out and named in the error, beside the rest.
func (c Config) OpenItems(f OpenFilter, now time.Time) ([]OpenItem, error) {
	if f.Kind != "" && !oneOf(f.Kind, OpenKinds) {
		return nil, refusal.New("%q is not a kind of open item. Pass `--kind` with one of %s", f.Kind, strings.Join(OpenKinds, ", "))
	}
	ps, err := c.ownPlans()
	if err != nil {
		return nil, err
	}
	out := []OpenItem{}
	var errs []error
	for _, p := range ps {
		items, err := p.OpenItems(now)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p.Name, err))
			continue
		}
		for _, it := range items {
			if f.Kind != "" && it.Kind != f.Kind {
				continue
			}
			if at, err := time.ParseInLocation(logTime, it.Time, now.Location()); !f.Since.IsZero() && (err != nil || at.Before(f.Since)) {
				continue
			}
			out = append(out, it)
		}
	}
	return out, errors.Join(errs...)
}

// OpenItems are the plan's open items as at now, its parts' included:
// what waits on the operator, the questions they can answer now, the
// ready steps and the open follow-ups, in that order.
func (p Plan) OpenItems(now time.Time) ([]OpenItem, error) {
	st, err := p.StatusAt(now)
	if err != nil {
		return nil, err
	}
	list, err := p.itemList()
	if err != nil {
		return nil, err
	}
	events, err := p.Events()
	if err != nil {
		return nil, err
	}
	waits, err := p.Waiting()
	if err != nil {
		return nil, err
	}
	qs, err := p.Questions(true)
	if err != nil {
		return nil, err
	}
	first := map[string]string{}
	for _, e := range events {
		if first[e.Item] == "" {
			first[e.Item] = e.Time
		}
	}
	created := ""
	if len(events) > 0 {
		created = events[0].Time
	}
	// at is the time of item id's first event of kind, or "" for any
	// kind; else its plan's first event's.
	at := func(id, kind string) string {
		if kind == "" {
			if t := first[id]; t != "" {
				return t
			}
			return created
		}
		for _, e := range events {
			if e.Item == id && e.Kind == kind {
				return e.Time
			}
		}
		return created
	}
	byID := map[string]item{}
	for _, it := range list {
		byID[it.ID] = it
	}
	open := func(kind, id string) OpenItem {
		return OpenItem{Kind: kind, Plan: p.Name, PlanTitle: st.Title, Halted: st.State == "halted", Item: id,
			Part: byID[id].Part, Title: byID[id].Title}
	}

	out := []OpenItem{}
	for _, w := range waits {
		it := open("waiting", w.Item)
		it.Title, it.Time, it.Command, it.Dir = w.Summary, w.Time, w.Command, w.Dir
		out = append(out, it)
	}
	for _, e := range pendingSigns(events) {
		it := open("waiting", e.Item)
		it.Title, it.Time, it.Sign, it.Branch, it.Dir = "sign-pass "+e.Sign, e.Time, e.Sign, e.Branch, e.Worktree
		out = append(out, it)
	}
	for _, q := range Askable(qs) {
		kind := "question"
		if q.Kind == "retro" {
			kind = "retro"
		}
		it := open(kind, q.ID)
		it.Part, it.Title, it.Time = q.Part, q.Question, at(q.ID, "asked")
		out = append(out, it)
	}
	for _, id := range st.ReadySteps {
		it := open("step", id)
		it.Time = at(id, "")
		out = append(out, it)
	}
	for _, id := range st.Followups {
		it := open("follow-up", id)
		it.Time = at(id, "discovered")
		out = append(out, it)
	}
	return out, nil
}

// pendingSigns are the sign-requested events of events with no result
// logged: passes handed to the operator and not yet run.
func pendingSigns(events []Event) []Event {
	finished := map[string]bool{}
	for _, e := range events {
		if oneOf(e.Kind, signResultKinds) {
			finished[e.Sign] = true
		}
	}
	var out []Event
	for _, e := range events {
		if e.Kind == signRequested && !finished[e.Sign] {
			out = append(out, e)
		}
	}
	return out
}

// OpenItemsOrg is items as Org: under a heading per plan, in the order
// the items come, one line each.
func OpenItemsOrg(items []OpenItem) string {
	if len(items) == 0 {
		return "No open items.\n"
	}
	var b strings.Builder
	group := ""
	for _, it := range items {
		if it.Plan != group {
			group = it.Plan
			fmt.Fprintf(&b, "* %s (%s)", it.PlanTitle, it.Plan)
			if it.Halted {
				b.WriteString(" halted")
			}
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "- %s %s %s :: %s", it.Kind, it.Item, it.Time, it.Title)
		if it.Command != "" {
			fmt.Fprintf(&b, ": `%s`", it.Command)
		}
		if it.Dir != "" {
			fmt.Fprintf(&b, " in %s", it.Dir)
		}
		b.WriteString("\n")
	}
	return b.String()
}
