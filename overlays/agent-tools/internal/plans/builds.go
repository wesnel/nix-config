package plans

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// buildsKind is the kind of the event that records which follow-up a step
// or part builds: its Item is the entry, and its Builds the follow-up's ID,
// or "" for none. The entry's BUILDS property is the log's projection, and
// status reports a BUILDS the log does not give as property drift.
const buildsKind = "builds"

// buildsProperty is the property of a step's or part's heading that names
// the follow-up it builds.
const buildsProperty = "BUILDS"

// Link records that step or part id builds follow-up followup, which may
// be in the plan's file or a part's (F3, P1.F2), or with followup "" that
// it builds none; logs it as a builds event; and writes the entry's
// BUILDS. Once the entry is logged done or verified, so is the follow-up,
// at once for an entry that is already. Linking an entry to the follow-up
// it builds again logs nothing new but what closing it needs.
func (p Plan) Link(id, followup string, now time.Time) (Event, error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	var e Event
	err := p.withLock(func(l *lockedPlan) error {
		var err error
		e, err = l.link(id, followup, now)
		return err
	})
	return e, err
}

// link is Link under the lock.
func (l *lockedPlan) link(id, followup string, now time.Time) (Event, error) {
	items, err := l.p.items()
	if err != nil {
		return Event{}, err
	}
	events, err := l.p.Events()
	if err != nil {
		return Event{}, err
	}
	if err := l.p.checkLink(items, events, id, followup); err != nil {
		return Event{}, err
	}
	var e Event
	addNote := func(note string) { e.Note = strings.TrimPrefix(e.Note+"; "+note, "; ") }
	if last, _ := lastBuilds(events, id); loggedBuilds(events, id) != followup || followup == "" && last.Builds != "" {
		summary := "builds " + followup
		if followup == "" {
			summary = "builds no follow-up"
		}
		if e, err = l.log(Event{Kind: buildsKind, Item: id, Builds: followup, Summary: summary}, now); err != nil {
			return Event{}, err
		}
	} else {
		e = Event{Kind: buildsKind, Item: id, Builds: followup}
		if followup == "" {
			addNote(fmt.Sprintf("%s builds no follow-up in the log, so no link is logged", id))
		} else {
			addNote(fmt.Sprintf("%s already builds %s, so no link is logged", id, followup))
		}
	}
	if err := l.projectBuilds(id); err != nil {
		addNote(fmt.Sprintf("%s's %s is not written: %v", id, buildsProperty, err))
	}
	if state := stepState(events, id); followup != "" && (state == kwDone || state == kwVerified) {
		closing := lastOf(events, id, strings.ToLower(state))
		if note := l.followBuilds(Event{Kind: closing.Kind, Item: id, Role: closing.Role, Agent: closing.Agent,
			Evidence: closing.Evidence}, now); note != "" {
			addNote(note)
		}
	}
	return e, nil
}

// lastOf is the last event of kind for id.
func lastOf(events []Event, id, kind string) Event {
	last := Event{Kind: kind, Item: id}
	for _, e := range events {
		if e.Item == id && e.Kind == kind {
			last = e
		}
	}
	return last
}

// checkLink refuses a link from id to followup that the plan cannot keep:
// from anything but a step or part, from an abandoned one, from a done or
// verified one that builds another follow-up until that link is cleared
// (so that a mistyped ID cannot close the wrong follow-up), to anything but
// a TODO or STARTED follow-up, or to one another live entry builds; and
// refuses clearing the link of an entry that has none, in the log or its
// heading. The link id already has is never refused: linking it again
// settles what an older agent-plan left unclosed.
func (p Plan) checkLink(items map[string]item, events []Event, id, followup string) error {
	it, ok := items[id]
	if !ok || !stepPattern.MatchString(id) && !partPattern.MatchString(id) {
		return refusal.New("%q is not a step or part of %s. Pass the ID of the Steps or Distribution entry that builds the follow-up, as `agent-plan status %s` lists it",
			id, p.Name, p.file(""))
	}
	if followup == "" {
		if last, _ := lastBuilds(events, id); last.Builds == "" && strings.TrimSpace(it.Props[buildsProperty]) == "" {
			return refusal.New("%s builds no follow-up, so there is no link to clear. Check the entry with `agent-plan status %s`", id, p.file(""))
		}
		return nil
	}
	current := loggedBuilds(events, id)
	if current == followup {
		return nil
	}
	if state := stepState(events, id); current != "" && (state == kwDone || state == kwVerified) {
		return refusal.New("%s is %s and builds %s. Clear that link first with `agent-plan link %s %s --clear`, then link %s",
			id, state, current, p.file(""), id, followup)
	}
	if stepState(events, id) == kwAbandoned {
		return refusal.New("%s is abandoned, and an abandoned entry builds nothing. Link the entry that takes its work over, as `agent-plan link %s ID %s`",
			id, p.file(""), followup)
	}
	if _, ok := items[followup]; !ok || !followupID.MatchString(followup) {
		return refusal.New("%q is not a follow-up of %s. Pass a Follow-ups ID, F3, or P1.F2 for one in part P1's file, as `agent-plan status %s` lists them",
			followup, p.Name, p.file(""))
	}
	if state := stepState(events, followup); state != kwTodo && state != kwStarted {
		return refusal.New("%s is %s, so it is not open to build. Link an open follow-up, as `agent-plan status %s` lists them",
			followup, state, p.file(""))
	}
	if others := slices.DeleteFunc(builders(events)[followup], func(b string) bool { return b == id }); len(others) > 0 {
		return refusal.New("%s is already built by %s. Clear that link with `agent-plan link %s %s --clear` first, or abandon %s",
			followup, strings.Join(others, ", "), p.file(""), others[0], others[0])
	}
	return nil
}

// lastBuilds is the last builds event of id, and whether there is one.
func lastBuilds(events []Event, id string) (Event, bool) {
	var last Event
	found := false
	for _, e := range events {
		if e.Kind == buildsKind && e.Item == id {
			last, found = e, true
		}
	}
	return last, found
}

// loggedBuilds is the follow-up the log says id builds: the one its last
// builds event names, or "" once id is abandoned after it. Abandoning an
// entry releases its link for good; taking the entry up again does not
// restore it.
func loggedBuilds(events []Event, id string) string {
	value := ""
	for _, e := range events {
		switch {
		case e.Item != id:
		case e.Kind == buildsKind:
			value = e.Builds
		case e.Kind == "abandoned":
			value = ""
		}
	}
	return value
}

// builders maps each follow-up to the entries the log says build it, in
// the order they were first linked.
func builders(events []Event) map[string][]string {
	var entries []string
	for _, e := range events {
		if e.Kind == buildsKind && !slices.Contains(entries, e.Item) {
			entries = append(entries, e.Item)
		}
	}
	out := map[string][]string{}
	for _, id := range entries {
		if f := loggedBuilds(events, id); f != "" {
			out[f] = append(out[f], id)
		}
	}
	return out
}

// projectBuilds writes the BUILDS the log gives id into its heading, or
// removes it for none, unless the heading has it.
func (l *lockedPlan) projectBuilds(id string) error {
	events, err := l.p.Events()
	if err != nil {
		return err
	}
	items, err := l.p.items()
	if err != nil {
		return err
	}
	it, ok := items[id]
	value := loggedBuilds(events, id)
	if !ok || strings.Join(strings.Fields(it.Props[buildsProperty]), " ") == value {
		return nil
	}
	return l.edit(it.File, func(lines []string) ([]string, error) {
		at := headingWithID(lines, id)
		switch {
		case at < 0:
			return lines, nil
		case value == "":
			return deleteProperty(lines, at, buildsProperty), nil
		}
		return setProperty(lines, at, buildsProperty, value), nil
	})
}

// stateRank orders the open, done and verified keywords a follow-up's log
// gives it by how far along it is, so that closing one never moves it
// back. ABANDONED is outside it: nothing a builder logs changes it.
var stateRank = map[string]int{kwTodo: 0, kwStarted: 1, kwDone: 2, kwVerified: 3}

// followBuilds does what e, just logged, means for the follow-up its item
// builds, or for the entries that build e's follow-up, and returns what
// the caller should know of it, or "". An abandoned entry's link is logged
// as released and its BUILDS removed, and so is every link to an abandoned
// follow-up. A done or verified entry that is the follow-up's one builder
// logs the same for the follow-up, and writes its keyword, unless the
// follow-up is that far along already or abandoned; one of several
// builders closes nothing, which status reports.
func (l *lockedPlan) followBuilds(e Event, now time.Time) string {
	events, err := l.p.Events()
	if err != nil {
		return fmt.Sprintf("%s's follow-up is not followed: %v", e.Item, err)
	}
	if e.Kind == "abandoned" && followupID.MatchString(e.Item) {
		var notes []string
		for _, b := range builders(events)[e.Item] {
			if _, err := l.log(Event{Kind: buildsKind, Item: b, Summary: e.Item + " abandoned, releasing it"}, now); err != nil {
				notes = append(notes, fmt.Sprintf("%s's link to %s is not logged as released: %v", b, e.Item, err))
				continue
			}
			note := fmt.Sprintf("released %s's link to %s, which is abandoned", b, e.Item)
			if err := l.projectBuilds(b); err != nil {
				note += fmt.Sprintf(", but not its %s: %v", buildsProperty, err)
			}
			notes = append(notes, note)
		}
		return strings.Join(notes, "; ")
	}
	if e.Kind == "abandoned" {
		last, _ := lastBuilds(events, e.Item)
		if last.Builds == "" {
			return ""
		}
		if _, err := l.log(Event{Kind: buildsKind, Item: e.Item, Summary: "abandoned, releasing " + last.Builds}, now); err != nil {
			return fmt.Sprintf("%s's link to %s is not logged as released: %v", e.Item, last.Builds, err)
		}
		note := fmt.Sprintf("released %s, which %s built", last.Builds, e.Item)
		if err := l.projectBuilds(e.Item); err != nil {
			note += fmt.Sprintf(", but not its %s: %v", buildsProperty, err)
		}
		return note
	}
	if e.Kind != "done" && e.Kind != "verified" {
		return ""
	}
	followup := loggedBuilds(events, e.Item)
	if followup == "" || !slices.Equal(builders(events)[followup], []string{e.Item}) || !followupID.MatchString(followup) {
		return ""
	}
	items, err := l.p.items()
	if err != nil {
		return fmt.Sprintf("%s is not closed: %v", followup, err)
	}
	if _, ok := items[followup]; !ok {
		return ""
	}
	state, current := map[string]string{"done": kwDone, "verified": kwVerified}[e.Kind], stepState(events, followup)
	if current == kwAbandoned || stateRank[current] >= stateRank[state] {
		return ""
	}
	closed := Event{Kind: e.Kind, Item: followup, Summary: "built by " + e.Item, BuiltBy: e.Item, Role: e.Role,
		Agent: e.Agent, Evidence: e.Evidence}
	if _, err := l.log(closed, now); err != nil {
		return fmt.Sprintf("%s, which %s builds, is not logged %s: %v", followup, e.Item, e.Kind, err)
	}
	note := fmt.Sprintf("logged %s for %s, which %s builds", e.Kind, followup, e.Item)
	if err := l.project(followup, now); err != nil {
		note += fmt.Sprintf(", but not its heading keyword: %v", err)
	}
	return note
}

// LinkDrift is a follow-up link the plan cannot keep: Entries, the steps
// and parts the log says build Followup, where Followup is no follow-up of
// the plan, where more than one builds it, or, as an older agent-plan left
// it, where it is abandoned or behind its done or verified builder.
type LinkDrift struct {
	Followup string   `json:"followup"`
	Entries  []string `json:"entries"`
	Problem  string   `json:"problem"`
}

// linkDrift is the plan's follow-up links that drift, by follow-up.
func (p Plan) linkDrift(events []Event) ([]LinkDrift, error) {
	items, err := p.items()
	if err != nil {
		return nil, err
	}
	built := builders(events)
	var followups []string
	for f := range built {
		followups = append(followups, f)
	}
	slices.Sort(followups)
	out := []LinkDrift{}
	for _, f := range followups {
		by, state := built[f], stepState(events, f)
		switch _, ok := items[f]; {
		case !ok || !followupID.MatchString(f):
			out = append(out, LinkDrift{Followup: f, Entries: by, Problem: "no follow-up of the plan"})
		case len(by) > 1:
			out = append(out, LinkDrift{Followup: f, Entries: by, Problem: "built by more than one live entry"})
		case state == kwAbandoned:
			out = append(out, LinkDrift{Followup: f, Entries: by, Problem: fmt.Sprintf("ABANDONED, but %s builds it", by[0])})
		default:
			s, what := stepState(events, by[0]), state
			if state == kwTodo || state == kwStarted {
				what = "open"
			}
			if (s == kwDone || s == kwVerified) && stateRank[state] < stateRank[s] {
				out = append(out, LinkDrift{Followup: f, Entries: by, Problem: fmt.Sprintf("%s, but %s is %s", what, by[0], s)})
			}
		}
	}
	return out, nil
}

// buildsDrift is the plan's entries whose BUILDS is not the one their log
// gives them: written or changed by hand.
func (p Plan) buildsDrift(events []Event) ([]PropertyDrift, error) {
	list, err := p.itemList()
	if err != nil {
		return nil, err
	}
	out := []PropertyDrift{}
	for _, it := range list {
		inFile, logged := strings.Join(strings.Fields(it.Props[buildsProperty]), " "), loggedBuilds(events, it.ID)
		if inFile != logged {
			out = append(out, PropertyDrift{ID: it.ID, Property: buildsProperty, File: inFile, Log: logged})
		}
	}
	return out, nil
}
