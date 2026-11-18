package plans

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// A plan covers one session's work. What it leaves open at the end, its
// unsettled steps, open follow-ups and open questions, Carry moves to a
// new plan: the old plan's CARRIED_TO names the new one, the new one's
// CARRIED_FROM the old, and each item keeps its ID.
const (
	propCarriedTo   = "CARRIED_TO"
	propCarriedFrom = "CARRIED_FROM"
)

// CarryOptions name the plan Carry makes: its title, slug and goal, each
// "" for the old plan's.
type CarryOptions struct{ Title, Slug, Goal, Template string }

// Carried is what Carry did: the plan the work went to, the plan it came
// from, and the items this run moved.
type Carried struct {
	Plan      string   `json:"plan"`
	ID        string   `json:"id"`
	From      string   `json:"from"`
	Steps     []string `json:"steps"`
	Followups []string `json:"followups"`
	Questions []string `json:"questions"`
	// Unlinked says why the new plan does not link the old one under
	// Related, as a plan in the old form cannot be linked to.
	Unlinked string `json:"unlinked,omitempty"`
	// Prompt is the line the operator pastes into a fresh session to
	// pick the new plan up.
	Prompt string `json:"prompt"`
}

// leftOver are the items of a plan's own file that Carry moves.
type leftOver struct{ steps, followups, questions []string }

func (l leftOver) ids() []string { return slices.Concat(l.steps, l.followups, l.questions) }

// Carry moves what the plan plan names leaves open to the plan it was
// carried to, making that plan the first time: a new plan with the old
// one's title, slug, goal, repositories, branches, worktrees, harness,
// workstream, pull requests, skills and pipeline unless o says otherwise. The old
// plan then settles each step and follow-up as CARRIED and drops each
// question, and takes no new work. Each stage first checks what an earlier
// run left, so a run stopped anywhere is finished by running it again,
// and a run with nothing left to move changes nothing. It refuses while a
// part's steps are open, since a part's work is its sub-agent's to settle
// or hand back, and a plan with nothing to carry.
func (c Config) Carry(plan string, o CarryOptions, now time.Time) (Carried, error) {
	old, err := c.Open(plan)
	if err != nil {
		return Carried{}, err
	}
	if err := old.checkOwn(); err != nil {
		return Carried{}, err
	}
	left, err := old.leftOver()
	if err != nil {
		return Carried{}, err
	}
	props, err := planHeading(old.file(""))
	if err != nil {
		return Carried{}, err
	}
	if len(left.ids()) == 0 && props[propCarriedTo] == "" {
		return Carried{}, refusal.New("%s leaves nothing open to carry. Hand it back with `agent-plan log %s --item %s --kind handed-back`",
			old.Name, old.file(""), old.Name)
	}
	target, err := c.carryTarget(old, o, now)
	if err != nil {
		return Carried{}, err
	}
	if err := target.inherit(old, o, now); err != nil {
		return Carried{}, err
	}
	if o.Template != "" {
		if err := c.templateSteps(target, o.Template, left.steps, now); err != nil {
			return Carried{}, err
		}
	}
	if err := old.markCarried(target, now); err != nil {
		return Carried{}, err
	}
	if err := target.take(old, left, now); err != nil {
		return Carried{}, err
	}
	if err := old.settleCarried(target, left, now); err != nil {
		return Carried{}, err
	}
	out := Carried{Plan: target.file(""), ID: target.Name, From: old.Name, Steps: left.steps, Followups: left.followups,
		Questions: left.questions, Prompt: pickupPrompt(target)}
	why := "Carried from it"
	if ids := left.ids(); len(ids) > 0 {
		why += ": " + strings.Join(ids, " ")
	}
	data, err := readPlan(old.file(""))
	if err != nil {
		return Carried{}, err
	}
	if fileID(string(data)) == "" {
		out.Unlinked = old.Name + " is in the old form, with no file-level ID for an org-roam link to name"
		return out, nil
	}
	if _, err := c.Link(target.file(""), old.file(""), why, now); err != nil {
		return Carried{}, err
	}
	return out, nil
}

// leftOver are the items of the plan's own file Carry moves: the steps
// and follow-ups the log leaves open, and the questions still asked. A
// step done but not verified stays, to be verified here, and the next
// plan's brief names it. A part, or a part's item, still open it refuses
// to leave in a plan that takes no new work.
func (p Plan) leftOver() (leftOver, error) {
	list, err := p.itemList()
	if err != nil {
		return leftOver{}, err
	}
	events, err := p.Events()
	if err != nil {
		return leftOver{}, err
	}
	var l leftOver
	var parts []string
	for _, it := range list {
		state := stepState(events, it.ID)
		open := projected(it.ID) && !planKeywords.done[state]
		asked := questionID.MatchString(it.ID) && it.Keyword == kwAsk
		switch {
		case it.File != p.file("") || partPattern.MatchString(it.ID):
			if open || asked {
				parts = append(parts, it.ID)
			}
		case stepPattern.MatchString(it.ID) && open:
			l.steps = append(l.steps, it.ID)
		case followupID.MatchString(it.ID) && open:
			l.followups = append(l.followups, it.ID)
		case asked:
			l.questions = append(l.questions, it.ID)
		}
	}
	if len(parts) > 0 {
		return leftOver{}, refusal.New("%s cannot be carried while its parts' %s are open: a part's work is its sub-agent's to settle. "+
			"Have each part settle its items and hand back, as `agent-plan log %s --item P1 --kind handed-back`, then carry",
			p.Name, strings.Join(parts, ", "), p.file(""))
	}
	return l, nil
}

// carryTarget is the plan the old plan's work goes to: the one its
// CARRIED_TO names; else one of this host's whose CARRIED_FROM names it,
// made by a run that stopped before CARRIED_TO was written; else a new
// one.
func (c Config) carryTarget(old Plan, o CarryOptions, now time.Time) (Plan, error) {
	props, err := planHeading(old.file(""))
	if err != nil {
		return Plan{}, err
	}
	if to := props[propCarriedTo]; to != "" {
		return c.Open(to)
	}
	own, err := c.ownPlans()
	if err != nil {
		return Plan{}, err
	}
	for _, p := range own {
		from, err := p.carriedFrom()
		if err != nil {
			return Plan{}, err
		}
		if from.Name == old.Name {
			return p, nil
		}
	}
	data, err := readPlan(old.file(""))
	if err != nil {
		return Plan{}, err
	}
	title, slug := o.Title, o.Slug
	if title == "" {
		title = orgTitle(string(data))
	}
	if slug == "" {
		slug = planSlug(old.Name)
	}
	made, err := c.New(NewOptions{Title: title, Slug: slug, Repos: strings.Fields(props["REPOSITORIES"]),
		Branches: strings.Fields(props["BRANCHES"]), Worktrees: strings.Fields(props["WORKTREES"]),
		Harness: props["HARNESS"], carriedFrom: old.Name, carriedWorkstream: refOf(props["WORKSTREAM"])}, now)
	if err != nil {
		return Plan{}, err
	}
	return c.Open(made.Plan)
}

// planDate is the time or date a plan's name starts with.
var planDate = regexp.MustCompile(`^(?:[0-9]{14}|[0-9]{4}-[0-9]{2}-[0-9]{2})-`)

// planSlug is the slug of a plan's name, <time>-<slug>.<host>.
func planSlug(name string) string {
	if at := strings.LastIndex(name, "."); at >= 0 {
		name = name[:at]
	}
	return planDate.ReplaceAllString(name, "")
}

// carriedFrom is the plan the plan's CARRIED_FROM names, or a Plan with no
// name where it names none.
func (p Plan) carriedFrom() (Plan, error) {
	props, err := planHeading(p.file(""))
	if err != nil || props[propCarriedFrom] == "" {
		return Plan{}, err
	}
	return Plan{Dir: p.Dir, Name: props[propCarriedFrom], Host: p.Host, Own: p.Own, Repo: p.Repo}, nil
}

// CarriedTo is the name of the plan the plan's work was carried to, or ""
// for one carried nowhere.
func (p Plan) CarriedTo() string {
	props, _ := planHeading(p.file(""))
	return props[propCarriedTo]
}

// carriedChain are the plans the plan's work was carried from, nearest
// first; a plan deleted since ends the chain, and one that cannot be read
// is an error, since it may hold IDs and decisions the chain's readers
// need.
func (p Plan) carriedChain() ([]Plan, error) {
	var out []Plan
	seen := map[string]bool{p.Name: true}
	q, err := p.carriedFrom()
	for err == nil && q.Name != "" && !seen[q.Name] {
		if _, err := os.Stat(q.file("")); errors.Is(err, fs.ErrNotExist) {
			break
		} else if err != nil {
			return nil, err
		}
		seen[q.Name] = true
		out = append(out, q)
		q, err = q.carriedFrom()
	}
	return out, err
}

// carryLines are the lines a brief starts with to say which plan the
// plan's work came from and which it went to, with how to pick that up.
func (p Plan) carryLines() []string {
	props, err := planHeading(p.file(""))
	if err != nil {
		return nil
	}
	var out []string
	if from := props[propCarriedFrom]; from != "" {
		out = append(out, "Carried from: "+from)
	}
	if to := props[propCarriedTo]; to != "" {
		out = append(out, "Carried to: "+to+"; pick it up with `agent-plan pickup "+p.carriedToFile(to)+"`")
	}
	return out
}

// lastCarriedTo is the plan p's work was last carried to, following each
// CARRIED_TO on, or p where it was carried nowhere; a plan named that
// cannot be opened, or one already passed, ends the chain.
func (c Config) lastCarriedTo(p Plan) Plan {
	seen := map[string]bool{p.Name: true}
	for {
		props, err := planHeading(p.file(""))
		if err != nil || props[propCarriedTo] == "" || seen[props[propCarriedTo]] {
			return p
		}
		next, err := c.Open(props[propCarriedTo])
		if err != nil {
			return p
		}
		seen[next.Name], p = true, next
	}
}

// inherit gives the plan what it takes over from old and does not have
// yet: its goal, pull requests, skills and pipeline.
func (p Plan) inherit(old Plan, o CarryOptions, now time.Time) error {
	if err := p.inheritGoal(old, o.Goal, now); err != nil {
		return err
	}
	from, err := planHeading(old.file(""))
	if err != nil {
		return err
	}
	to, err := planHeading(p.file(""))
	if err != nil {
		return err
	}
	for _, name := range []string{"PULL_REQUESTS", "SKILLS"} {
		if values := strings.Fields(from[name]); len(values) > 0 && to[name] == "" {
			if err := p.SetProperty("", name, values, now); err != nil {
				return err
			}
		}
	}
	// A new plan's PIPELINE is full; one the next session set otherwise
	// is its own.
	if pipeline := from["PIPELINE"]; pipeline != "" && pipeline != "full" && to["PIPELINE"] == "full" {
		return p.withLock(func(l *lockedPlan) error {
			return l.setPlanProperty("", "PIPELINE", func([]string) []string { return []string{pipeline} }, "", now)
		})
	}
	return nil
}

// inheritGoal gives the plan, while it has no goal, goal, or else old's.
func (p Plan) inheritGoal(old Plan, goal string, now time.Time) error {
	have, err := p.goalText("")
	if err != nil || have != "" {
		return err
	}
	if goal == "" {
		if goal, err = old.goalText(""); err != nil {
			return err
		}
	}
	if goal = strings.TrimSpace(goal); goal == "" {
		return nil
	}
	_, err = p.SetSection("", "Goal", goal, now)
	return err
}

// reviewStage is the STAGE value that marks a template's step as a review
// stage, which carry --template copies.
const reviewStage = "review"

// templateSteps starts the plan, while it has no steps, with the review
// stages of template name, the steps it marks `:STAGE: review`, under the
// next free IDs, and logs each as planned. Their AFTER is renumbered with
// them; a stage that waits on nothing copied waits on carried, the steps
// the plan takes over, instead. A plan with steps already has them.
func (c Config) templateSteps(p Plan, name string, carried []string, now time.Time) error {
	sections, err := c.template(name)
	if err != nil {
		return err
	}
	return p.withLock(func(l *lockedPlan) error {
		if has, err := p.hasStep(); err != nil || has {
			return err
		}
		next, err := p.nextID("", "S")
		if err != nil {
			return err
		}
		n, _ := strconv.Atoi(strings.TrimPrefix(next, "S"))
		stages, renamed := reviewStages(sections["Steps"], n)
		var block []string
		var events []Event
		for _, entry := range stages {
			entry, id := stageEntry(entry, renamed, carried)
			block = append(block, entry...)
			events = append(events, Event{Kind: "planned", Item: id, Summary: lineTitle(entry[0], planKeywords) + "; from template " + name})
		}
		if len(events) == 0 {
			return refusal.New("template %s marks no step `:STAGE: %s`, so carry has no review stages to take from it. Carry without `--template`, or ask the operator to mark the template's review steps",
				name, reviewStage)
		}
		if err := l.appendToSection("Steps", block); err != nil {
			return err
		}
		for _, e := range events {
			if _, err := l.log(e, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// hasStep reports whether the plan has a step.
func (p Plan) hasStep() (bool, error) {
	items, err := p.items()
	if err != nil {
		return false, err
	}
	for id := range items {
		if stepPattern.MatchString(id) {
			return true, nil
		}
	}
	return false, nil
}

// reviewStages are the entries of a template's Steps lines marked
// `:STAGE: review`, and the new ID of each, numbered from n on, by its
// template ID.
func reviewStages(lines []string, n int) ([][]string, map[string]string) {
	var stages [][]string
	renamed := map[string]string{}
	for at := 0; at < len(lines); at++ {
		if level(lines[at]) == 0 {
			continue
		}
		entry := slices.Clone(lines[at:subtreeEnd(lines, at)])
		props := drawerProps(drawerOf(entry, 0))
		if props["STAGE"] == reviewStage && stepPattern.MatchString(propsID(props)) {
			renamed[propsID(props)] = fmt.Sprintf("S%d", n+len(stages))
			stages = append(stages, entry)
		}
	}
	return stages, renamed
}

// stageEntry is a review stage's entry under its new ID, waiting on the
// renamed stages it waited on, or else on carried, and that ID.
func stageEntry(entry []string, renamed map[string]string, carried []string) ([]string, string) {
	props := drawerProps(drawerOf(entry, 0))
	id := renamed[propsID(props)]
	entry = setProperty(entry, 0, "CUSTOM_ID", id)
	var after []string
	for _, a := range strings.Fields(props["AFTER"]) {
		if renamed[a] != "" {
			after = append(after, renamed[a])
		}
	}
	if len(after) == 0 {
		after = carried
	}
	if len(after) == 0 {
		return deleteProperty(entry, 0, "AFTER"), id
	}
	return setProperty(entry, 0, "AFTER", strings.Join(after, " ")), id
}

// markCarried writes target as the plan's CARRIED_TO, and CARRIED into
// its file's step keywords where a plan from before carrying lacks it.
func (p Plan) markCarried(target Plan, now time.Time) error {
	return p.withLock(func(l *lockedPlan) error {
		err := l.edit(p.file(""), func(lines []string) ([]string, error) {
			lines = slices.Clone(lines)
			for i, line := range lines {
				if todoLine.MatchString(line) && slices.Contains(strings.Fields(line), kwAbandoned) &&
					!slices.Contains(strings.Fields(line), kwCarried) {
					lines[i] = line + " " + kwCarried
				}
			}
			return lines, nil
		})
		if err != nil {
			return err
		}
		return l.setPlanProperty("", propCarriedTo, func([]string) []string { return []string{target.Name} }, "", now)
	})
}

// take adds each item of left that the plan does not have yet, copied
// from old's file under the same ID at the end of its section: a step or
// follow-up as TODO, waiting only on what is carried with it, and a
// question as asked. It logs each as its own command would.
func (p Plan) take(old Plan, left leftOver, now time.Time) error {
	data, err := readPlan(old.file(""))
	if err != nil {
		return err
	}
	from := strings.Split(string(data), "\n")
	carried := left.ids()
	return p.withLock(func(l *lockedPlan) error {
		items, err := p.items()
		if err != nil {
			return err
		}
		var events []Event
		for _, s := range []struct {
			section, kind string
			ids           []string
		}{{"Steps", "planned", left.steps}, {"Questions", "asked", left.questions}, {"Follow-ups", "discovered", left.followups}} {
			block, logged, err := carriedBlock(old, from, s.ids, items, carried, s.kind, now)
			if err != nil {
				return err
			}
			events = append(events, logged...)
			if len(block) == 0 {
				continue
			}
			if err := l.appendToSection(s.section, block); err != nil {
				return err
			}
		}
		for _, e := range events {
			if _, err := l.log(e, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// carriedBlock is the entries of ids, from old's file lines from, that
// items does not hold yet, as carriedEntry carries them, and the event of
// kind that logs each.
func carriedBlock(old Plan, from, ids []string, items map[string]item, carried []string, kind string, now time.Time) ([]string, []Event, error) {
	var block []string
	var events []Event
	for _, id := range ids {
		if _, ok := items[id]; ok {
			continue
		}
		at := headingWithID(from, id)
		if at < 0 {
			return nil, nil, refusal.New("%s has no heading for %s, which it was to carry. Run `agent-plan status %s` and report what it lists", old.file(""), id, old.file(""))
		}
		entry := carriedEntry(relevel(slices.Clone(from[at:subtreeEnd(from, at)]), 3), orderedBefore(from, at), carried, now)
		block = append(block, entry...)
		events = append(events, Event{Kind: kind, Item: id, Summary: lineTitle(entry[0], planKeywords) + "; carried from " + old.Name})
	}
	return block, events, nil
}

// relevel is entry with its headings moved by one amount, so its first
// is at level to: a carried entry sits right under its section, whatever
// group held it.
func relevel(entry []string, to int) []string {
	by := to - level(entry[0])
	blocks := blockLines(entry)
	for i, line := range entry {
		if n := level(line); n > 0 && !blocks[i] {
			entry[i] = strings.Repeat("*", n+by) + line[n:]
		}
	}
	return entry
}

// orderedBefore are the IDs of the siblings before the heading at
// lines[at] where its parent's ORDERED property is t, the items it waits
// on as earlierSettled reads them, which a carried entry outside the
// group waits on in AFTER; none under any other parent.
func orderedBefore(lines []string, at int) []string {
	hs, levels, _ := headings(lines)
	k := slices.Index(hs, at)
	parent := k - 1
	for parent >= 0 && levels[parent] >= levels[k] {
		parent--
	}
	if parent < 0 || drawerProps(drawerOf(lines, hs[parent]))["ORDERED"] != "t" {
		return nil
	}
	var ids []string
	for j := parent + 1; j < k; j++ {
		if id := propsID(drawerProps(drawerOf(lines, hs[j]))); levels[j] == levels[k] && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// appendToSection writes block at the end of the plan file's `** name`
// section, refusing a file without one.
func (l *lockedPlan) appendToSection(name string, block []string) error {
	file := l.p.file("")
	return l.edit(file, func(lines []string) ([]string, error) {
		_, end, ok := section(lines, name)
		if !ok {
			return nil, refusal.New("%s has no `** %s` heading. Ask the operator to restore it as the plan-work skill describes, then carry again", file, name)
		}
		// A section ending the file ends at its final empty line.
		if end == len(lines) && lines[end-1] == "" {
			end--
		}
		return slices.Insert(slices.Clone(lines), end, block...), nil
	})
}

// carriedEntry is an entry's subtree as the plan it is carried to holds
// it: a step or follow-up open as TODO, its ID in CUSTOM_ID as a plan in
// the new form holds it, and its AFTER naming only items carried with it,
// among them those of ordered, the siblings its ORDERED group put first.
func carriedEntry(entry, ordered, carried []string, now time.Time) []string {
	if ks := planKeywords; ks.all[lineKeyword(entry[0])] && lineKeyword(entry[0]) != kwAsk {
		entry = setKeyword(entry, 0, kwTodo, ks, now)
	}
	props := drawerProps(drawerOf(entry, 0))
	if id := props["ID"]; id != "" && props["CUSTOM_ID"] == "" {
		entry = setProperty(deleteProperty(entry, 0, "ID"), 0, "CUSTOM_ID", id)
	}
	if after, ok := props["AFTER"]; ok || len(ordered) > 0 {
		var kept []string
		for _, id := range slices.Concat(ordered, strings.Fields(after)) {
			if slices.Contains(carried, id) && !slices.Contains(kept, id) {
				kept = append(kept, id)
			}
		}
		if len(kept) == 0 {
			entry = deleteProperty(entry, 0, "AFTER")
		} else {
			entry = setProperty(entry, 0, "AFTER", strings.Join(kept, " "))
		}
	}
	return entry
}

// lineKeyword is the keyword a heading line starts with, or "".
func lineKeyword(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

// settleCarried logs each step and follow-up of left carried, and drops
// each question, naming target; what an earlier run settled it leaves.
// It projects every item the log has carried, so a run stopped between
// logging an item and projecting it is finished by the next.
func (p Plan) settleCarried(target Plan, left leftOver, now time.Time) error {
	err := p.withLock(func(l *lockedPlan) error {
		if err := l.logCarried(target, slices.Concat(left.steps, left.followups), now); err != nil {
			return err
		}
		return l.projectCarried(now)
	})
	if err != nil {
		return err
	}
	for _, q := range left.questions {
		if _, err := p.Decide(q, DecideOptions{Drop: true, Answer: "carried to " + target.Name}, now); err != nil {
			return err
		}
	}
	return nil
}

// logCarried logs each of ids carried to target, but those the log has
// carried already.
func (l *lockedPlan) logCarried(target Plan, ids []string, now time.Time) error {
	events, err := l.p.Events()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if stepState(events, id) == kwCarried {
			continue
		}
		if _, err := l.log(Event{Kind: carriedKind, Item: id, Summary: "to " + target.Name}, now); err != nil {
			return err
		}
	}
	return nil
}

// projectCarried writes the heading keyword of every item the log has
// carried.
func (l *lockedPlan) projectCarried(now time.Time) error {
	events, err := l.p.Events()
	if err != nil {
		return err
	}
	list, err := l.p.itemList()
	if err != nil {
		return err
	}
	for _, it := range list {
		if stepState(events, it.ID) != kwCarried {
			continue
		}
		if err := l.project(it.ID, now); err != nil {
			return err
		}
	}
	return nil
}

// checkOpen refuses new work in a plan whose work was carried on: it
// belongs in the plan it was carried to.
func (p Plan) checkOpen() error {
	props, err := planHeading(p.file(""))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if to := props[propCarriedTo]; to != "" {
		return refusal.New("%s's work was carried to %s, which takes what is new. Run the same command with `%s` as its plan",
			p.Name, to, p.carriedToFile(to))
	}
	return nil
}

// carriedToFile is the file of the plan named to, beside the plan's.
func (p Plan) carriedToFile(to string) string { return Plan{Dir: p.Dir, Name: to}.file("") }
