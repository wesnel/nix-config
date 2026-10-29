package plans

import (
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// trackedProperties are the list properties of a plan file's `* Plan`
// heading that are a projection of the log: each change through agent-plan
// or a hook is logged as a property event holding the whole new value, and
// status reports a file whose value is not the one its last event gives.
// An item's properties are compared from its first property event on.
var trackedProperties = []string{"WORKTREES", "BRANCHES", "PULL_REQUESTS", "OWNERS"}

// settableProperties are the tracked properties SetProperty sets; OWNERS is
// SetOwner's.
var settableProperties = []string{"WORKTREES", "BRANCHES", "PULL_REQUESTS"}

// plainProperties are the `* Plan` properties SetProperty sets without a
// log event, since no reader takes them from the log. ID, HOST and PARENT
// are fixed when the file is made.
var plainProperties = []string{"STATUS", "SKILLS", "REPOSITORIES", "SESSIONS", "HARNESS"}

// statuses are the values of STATUS; the status page and the export read
// them.
var statuses = []string{"planning", "reviewing-plan", "working", "reviewing", "handed-back", "halted"}

// checkValues refuses values that property name cannot hold: STATUS takes
// one of statuses, and HARNESS one harness.
func checkValues(name string, values []string) error {
	switch name {
	case "STATUS":
		if len(values) != 1 || !slices.Contains(statuses, values[0]) {
			return refusal.New("STATUS is one of `%s`. Set it as `agent-plan set PLAN STATUS halted`", strings.Join(statuses, "`, `"))
		}
	case "HARNESS":
		if len(values) > 1 {
			return refusal.New("HARNESS is one harness. Set it as `agent-plan set PLAN HARNESS claude`")
		}
	}
	return nil
}

// propertyKind is the kind of a property event, whose Property names the
// property and Value holds its values, separated by spaces.
const propertyKind = "property"

// SetProperty sets the tracked list property name of the `* Plan` heading
// in the plan's file, or the part's, to values, and logs it.
func (p Plan) SetProperty(part, name string, values []string, now time.Time) error {
	if err := p.checkOwn(); err != nil {
		return err
	}
	if !slices.Contains(settableProperties, name) && !slices.Contains(plainProperties, name) {
		return refusal.New("%s is not a property set this way. Set one of `%s`; record an owner with `agent-plan own`, an item's AFTER with `agent-plan after`",
			name, strings.Join(slices.Concat(settableProperties, plainProperties), "`, `"))
	}
	if part != "" && !partPattern.MatchString(part) {
		return refusal.New("%q is not a part ID. Pass one such as `--part P1`", part)
	}
	if err := checkWords(strings.ToLower(name), values); err != nil {
		return err
	}
	if err := checkValues(name, values); err != nil {
		return err
	}
	return p.withLock(func(l *lockedPlan) error {
		return l.setPlanProperty(part, name, func([]string) []string { return values }, "", now)
	})
}

// RemoveFromProperty removes value from the tracked list property name of
// the `* Plan` heading in the plan's file, or the part's, and logs the new
// value with why it was removed: a hook or an agent filed it in the wrong
// plan. It refuses a value the file does not hold.
func (p Plan) RemoveFromProperty(part, name, value, why string, now time.Time) error {
	if err := p.checkOwn(); err != nil {
		return err
	}
	if !slices.Contains(settableProperties, name) {
		return refusal.New("%s is not a property values are removed from. Remove one from `%s`", name, strings.Join(settableProperties, "`, `"))
	}
	if part != "" && !partPattern.MatchString(part) {
		return refusal.New("%q is not a part ID. Pass one such as `--part P1`", part)
	}
	if value == "" {
		return refusal.New("A removal takes one value. Name it, as `agent-plan unfile PLAN %s VALUE --reason TEXT`", name)
	}
	if err := checkWords(strings.ToLower(name), []string{value}); err != nil {
		return err
	}
	if strings.TrimSpace(why) == "" || strings.Contains(why, "\n") {
		return refusal.New("A removal is logged with its reason, on one line. Say why %s is removed, naming the plan it belongs to, with `--reason`", value)
	}
	return p.withLock(func(l *lockedPlan) error {
		props, err := planHeading(p.file(part))
		if err != nil {
			return err
		}
		values := strings.Fields(props[name])
		if !slices.Contains(values, value) {
			return refusal.New("%s's %s does not hold %s; it holds `%s`", p.item(part), name, value, strings.Join(values, " "))
		}
		return l.setPlanProperty(part, name, func(values []string) []string {
			return slices.DeleteFunc(slices.Clone(values), func(v string) bool { return v == value })
		}, "removed "+value+": "+why, now)
	})
}

// item is the log's item for the plan's file, or the part's.
func (p Plan) item(part string) string {
	if part == "" {
		return p.Name
	}
	return part
}

// setPlanProperty sets the list property name of the `* Plan` heading in
// the plan's file, or the part's, to what edit makes of its values, adding
// the property at the end of the drawer if it has none; edit may run more
// than once, each time on the values the file then holds. A tracked
// property whose new value is not the one the log last gave it is logged,
// so a change written before a failed append is logged when run again; with
// a note, it is logged whatever its value, the note ending the summary.
func (l *lockedPlan) setPlanProperty(part, name string, edit func(values []string) []string, note string, now time.Time) error {
	file := l.p.file(part)
	var after []string
	err := l.edit(file, func(lines []string) ([]string, error) {
		lines = slices.Clone(lines)
		start := planLine(lines)
		if start < 0 {
			return nil, refusal.New("%s has no `* Plan` heading. Ask the operator to restore it as the plan-work skill describes, then try again", file)
		}
		at, end := -1, -1
		var values []string
		for i := start + 1; i < len(lines) && end < 0; i++ {
			switch {
			case lines[i] == ":END:":
				end = i
			case strings.HasPrefix(lines[i], ":"+name+":"):
				at = i
				values = strings.Fields(strings.TrimPrefix(lines[i], ":"+name+":"))
			}
		}
		if end < 0 {
			return nil, refusal.New("%s's `* Plan` heading has no property drawer. Ask the operator to restore it as the plan-work skill describes, then try again", file)
		}
		after = edit(values)
		line := ":" + name + ":"
		if len(after) > 0 {
			line += " " + strings.Join(after, " ")
		}
		if at >= 0 {
			lines[at] = line
			return lines, nil
		}
		return slices.Insert(lines, end, line), nil
	})
	if err != nil || !slices.Contains(trackedProperties, name) {
		return err
	}
	events, err := l.p.Events()
	if err != nil {
		return err
	}
	value := strings.Join(after, " ")
	if logged, ok := loggedProperty(events, l.p.item(part), name); ok && logged == value && note == "" {
		return nil
	}
	return l.logProperty(part, name, value, note, now)
}

func (l *lockedPlan) logProperty(part, name, value, note string, now time.Time) error {
	summary := name
	if value != "" {
		summary += " " + value
	}
	if note != "" {
		summary += "; " + note
	}
	_, err := l.log(Event{Kind: propertyKind, Item: l.p.item(part), Property: name, Value: value, Summary: summary}, now)
	return err
}

// logNewProperties logs the tracked properties a new plan file, or a new
// part's, starts with that hold values.
func (l *lockedPlan) logNewProperties(part string, now time.Time) error {
	props, err := planHeading(l.p.file(part))
	if err != nil {
		return err
	}
	for _, name := range trackedProperties {
		if value := strings.Join(strings.Fields(props[name]), " "); value != "" {
			if err := l.logProperty(part, name, value, "", now); err != nil {
				return err
			}
		}
	}
	return nil
}

// loggedProperty is the value the last property event of item gives name,
// and whether the log holds a property event of item at all; a property
// with none of its own is empty then.
func loggedProperty(events []Event, item, name string) (string, bool) {
	value, any := "", false
	for _, e := range events {
		if e.Kind != propertyKind || e.Item != item {
			continue
		}
		any = true
		if e.Property == name {
			value = e.Value
		}
	}
	return value, any
}

// planHeading is the properties of file's `* Plan` heading.
func planHeading(file string) (map[string]string, error) {
	data, err := readPlan(file)
	if err != nil {
		return nil, err
	}
	for _, h := range parseOrg(string(data)) {
		if h.Level == 1 && sectionTitle(h) == "Plan" {
			return h.Props, nil
		}
	}
	return map[string]string{}, nil
}

// PropertyDrift is a tracked property of an item's file, or the plan's
// References, whose value in the file, File, is not Log, the one its log
// gives it.
type PropertyDrift struct {
	ID       string `json:"id"`
	Property string `json:"property"`
	File     string `json:"file"`
	Log      string `json:"log"`
}

// propertyDrift is the plan's tracked properties, file by file, and its
// References, where the file and the log disagree.
func (p Plan) propertyDrift(events []Event) ([]PropertyDrift, error) {
	files, err := p.files()
	if err != nil {
		return nil, err
	}
	out := []PropertyDrift{}
	for _, f := range files {
		part := partOf(f)
		props, err := planHeading(f)
		if err != nil {
			return nil, err
		}
		for _, name := range trackedProperties {
			logged, ok := loggedProperty(events, p.item(part), name)
			inFile := strings.Join(strings.Fields(props[name]), " ")
			if ok && inFile != logged {
				out = append(out, PropertyDrift{ID: p.item(part), Property: name, File: inFile, Log: logged})
			}
		}
	}
	data, err := readPlan(p.file(""))
	if err != nil {
		return nil, err
	}
	var urls []string
	hs := parseOrg(string(data))
	sections := sectionsOf(hs)
	for i, h := range hs {
		if url := h.Props["URL"]; sections[i] == "References" && url != "" && !slices.Contains(urls, url) {
			urls = append(urls, url)
		}
	}
	var links []string
	for _, id := range relatedIDs(strings.Split(string(data), "\n")) {
		links = append(links, "id:"+id)
	}
	for _, d := range []PropertyDrift{
		listDrift(p.Name, "References", urls, "referenced", events),
		listDrift(p.Name, "Related", links, linkedKind, events),
	} {
		if d.Property != "" {
			out = append(out, d)
		}
	}
	return out, nil
}

// listDrift is the drift of section, whose entries are inFile, from the
// summaries of the events of kind, which each add one; or no drift, with
// no Property, where both hold the same.
func listDrift(item, section string, inFile []string, kind string, events []Event) PropertyDrift {
	var logged []string
	for _, e := range events {
		if e.Kind == kind && !slices.Contains(logged, e.Summary) {
			logged = append(logged, e.Summary)
		}
	}
	inFile = slices.Clone(inFile)
	slices.Sort(inFile)
	slices.Sort(logged)
	if slices.Equal(inFile, logged) {
		return PropertyDrift{}
	}
	return PropertyDrift{ID: item, Property: section, File: strings.Join(inFile, " "), Log: strings.Join(logged, " ")}
}
