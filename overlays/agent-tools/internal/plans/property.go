package plans

import (
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// trackedProperties are the list properties of a plan file's `* Plan`
// heading that are a projection of the log: each change through agent-plan
// or a hook is logged as a property event holding the whole new value, and
// status reports a file whose value is not the one its last event gives.
// An item's properties are compared from its first property event on,
// except PIPELINE, whose log gives defaultPipeline until it logs one.
var trackedProperties = []string{"WORKTREES", "BRANCHES", "PULL_REQUESTS", "OWNERS", "REVIEWS", "PIPELINE", "DIAGRAMS"}

// settableProperties are the tracked properties SetProperty sets; OWNERS is
// SetOwner's.
var settableProperties = []string{"WORKTREES", "BRANCHES", "PULL_REQUESTS", "REVIEWS", "PIPELINE", "DIAGRAMS"}

// defaultPipeline is the PIPELINE a plan's log gives it before any
// PIPELINE event: what a new plan's file starts with.
const defaultPipeline = "full"

// checkValues refuses values that property name cannot hold: REVIEWS
// takes routes a focused review exists for, PIPELINE one value, full or
// opted-out:<reason>, and DIAGRAMS one value, auto or none.
func checkValues(name string, values []string) error {
	switch name {
	case "REVIEWS":
		for _, v := range values {
			if !routes.IsKnown(v) {
				return refusal.New("%q is not a review route. Name one of `%s`", v, strings.Join(routes.Known, "`, `"))
			}
		}
	case "PIPELINE":
		if len(values) != 1 || (values[0] != defaultPipeline && (!strings.HasPrefix(values[0], "opted-out:") ||
			values[0] == "opted-out:")) {
			return refusal.New("PIPELINE is one value, `full` or `opted-out:<reason>`. Set it as `agent-plan set PLAN PIPELINE opted-out:operator-asked`, " +
				"once the operator has opted the task out")
		}
	case "DIAGRAMS":
		if len(values) != 1 || (values[0] != "auto" && values[0] != "none") {
			return refusal.New("DIAGRAMS is one value, `auto` or `none`. Set it as `agent-plan set PLAN DIAGRAMS none` " +
				"once the operator has said the plan needs no diagrams")
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
	if !slices.Contains(settableProperties, name) {
		return refusal.New("%s is not a property set this way. Set one of `%s`; record an owner with `agent-plan own`, and edit any other property in the plan file",
			name, strings.Join(settableProperties, "`, `"))
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
		return l.setPlanProperty(part, name, func([]string) []string { return values }, now)
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
// so a change written before a failed append is logged when run again.
func (l *lockedPlan) setPlanProperty(part, name string, edit func(values []string) []string, now time.Time) error {
	file := l.p.file(part)
	var after []string
	err := l.edit(file, func(lines []string) ([]string, error) {
		lines = slices.Clone(lines)
		start := slices.Index(lines, "* Plan")
		if start < 0 {
			return nil, refusal.New("%s has no `* Plan` heading. Restore it as the plan-work skill describes, then try again", file)
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
			return nil, refusal.New("%s's `* Plan` heading has no property drawer. Restore it as the plan-work skill describes, then try again", file)
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
	if logged, ok := loggedProperty(events, l.p.item(part), name); ok && logged == value && name != "PIPELINE" {
		return nil
	}
	if name == "PIPELINE" && value == loggedPipeline(events, l.p.item(part)) {
		return nil
	}
	return l.logProperty(part, name, value, now)
}

func (l *lockedPlan) logProperty(part, name, value string, now time.Time) error {
	summary := name
	if value != "" {
		summary += " " + value
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
		if value := strings.Join(strings.Fields(props[name]), " "); value != "" && (name != "PIPELINE" || value != defaultPipeline) {
			if err := l.logProperty(part, name, value, now); err != nil {
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

// loggedPipeline is the PIPELINE the last PIPELINE event of item gives it,
// else defaultPipeline.
func loggedPipeline(events []Event, item string) string {
	value := defaultPipeline
	for _, e := range events {
		if e.Kind == propertyKind && e.Item == item && e.Property == "PIPELINE" {
			value = e.Value
		}
	}
	return value
}

// pipeline is the plan's PIPELINE as its log gives it: what stop-check and
// the gates go by, whatever the file says.
func (p Plan) pipeline(events []Event) string { return loggedPipeline(events, p.Name) }

// planHeading is the properties of file's `* Plan` heading.
func planHeading(file string) (map[string]string, error) {
	data, err := readPlan(file)
	if err != nil {
		return nil, err
	}
	for _, h := range parseOrg(string(data)) {
		if h.Level == 1 && h.Title == "Plan" {
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
			if name == "PIPELINE" {
				logged, ok = loggedPipeline(events, p.item(part)), inFile != ""
			}
			if ok && inFile != logged {
				out = append(out, PropertyDrift{ID: p.item(part), Property: name, File: inFile, Log: logged})
			}
		}
	}
	data, err := readPlan(p.file(""))
	if err != nil {
		return nil, err
	}
	var inFile, logged []string
	section := ""
	for _, h := range parseOrg(string(data)) {
		if h.Level <= 2 {
			section = h.Title
		}
		if url := h.Props["URL"]; section == "References" && url != "" && !slices.Contains(inFile, url) {
			inFile = append(inFile, url)
		}
	}
	for _, e := range events {
		if e.Kind == "referenced" && !slices.Contains(logged, e.Summary) {
			logged = append(logged, e.Summary)
		}
	}
	slices.Sort(inFile)
	slices.Sort(logged)
	if !slices.Equal(inFile, logged) {
		out = append(out, PropertyDrift{ID: p.Name, Property: "References", File: strings.Join(inFile, " "),
			Log: strings.Join(logged, " ")})
	}
	return out, nil
}
