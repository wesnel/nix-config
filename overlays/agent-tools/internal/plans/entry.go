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

// The commands here write the parts of a plan file the others do not:
// steps and parts, an entry's title, text and AFTER, and the sections that
// hold text. Agents never edit a plan file by hand, so each of these is
// the one way to make its change, and logs it as planned.

// EntryOptions are a step or a part to add: Section is Steps or
// Distribution, Part the part whose file takes a step, Title and Text its
// heading and body, After the IDs it waits for. Repo, Branch
// (BASE..BRANCH) and Worktree are a part's, written as the first line of
// its text.
type EntryOptions struct {
	Section, Part, Title, Text string
	After                      []string
	Repo, Branch, Worktree     string
}

// entryLetters are the sections AddEntry adds to, with the letter of
// their IDs.
var entryLetters = map[string]string{"Steps": "S", "Distribution": "P"}

// textSections are the sections that hold text, which SetSection writes.
var textSections = []string{"Goal", "Verification"}

// checkTitle refuses a heading title, passed as --what, Org would read
// otherwise: on two lines, starting with COMMENT, a keyword or a
// priority, or ending in tags.
func checkTitle(what, title string) error {
	if err := checkLine(what, title); err != nil {
		return err
	}
	if strings.TrimSpace(title) == "" {
		return refusal.New("the %s is empty. Pass one with `--%s 'one line'`", what, what)
	}
	if word, _, _ := strings.Cut(strings.TrimSpace(title), " "); word == "COMMENT" {
		return refusal.New("%s %q starts with COMMENT, which makes Org leave the entry out of exports and the agenda. Pass it starting with another word, as `--%s 'Comment on ...'`", what, title, what)
	}
	if h := headingOf(3, title, planKeywords); h.Keyword != "" || h.Priority != "" || h.Tags != nil || h.Title != strings.TrimSpace(title) {
		return refusal.New("%s %q starts with a keyword or a priority, or ends in tags, which Org reads as the heading's and not its title. Pass it without them, as `--%s 'Tag the release'`; the log writes keywords, and `--priority` a question's priority", what, title, what)
	}
	return nil
}

// propertyShaped is a line that starts a drawer, ends one or sets a
// property, as `:ID: S2` does.
var propertyShaped = regexp.MustCompile(`^\s*:[A-Za-z0-9_-]+:(\s|$)`)

// textLines are text as the lines of an entry's or section's body. It
// refuses a line Org reads as structure rather than text: a heading, a
// drawer or property line, a planning line, or a #+ keyword line other
// than a block's #+begin_ and #+end_. A block left open is the structure
// check's.
func textLines(text string) ([]string, error) {
	if strings.ContainsAny(text, "\r\x00") {
		return nil, refusal.New("the text holds a carriage return or a NUL. Pass it as plain lines, as `--text 'Done when ...'`")
	}
	text = strings.Trim(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		switch {
		case orgHeading.MatchString(l):
			return nil, refusal.New("the text holds a heading line, %q. Add a step or a part with `agent-plan step` or `agent-plan part`, or start the line with a word", l)
		case propertyShaped.MatchString(l):
			return nil, refusal.New("the text holds %q, which Org reads as a drawer or a property, and a command looking for an entry's ID or AFTER might too. Set properties with the command that owns them, such as `agent-plan after`, or start the line with a word", l)
		case planningLine.MatchString(l):
			return nil, refusal.New("the text holds %q, which Org reads as a planning line. Schedule with `agent-plan postpone`, or start the line with a word", l)
		case strings.HasPrefix(trimmed, "#+") && !blockLine.MatchString(l):
			return nil, refusal.New("the text holds %q, an Org keyword line, which changes how the whole file reads. Leave it out, or ask the operator to change the file's keyword lines", l)
		}
	}
	return lines, nil
}

// knownAfter refuses an AFTER list that names something the plan does not
// hold, or id itself.
func (p Plan) knownAfter(id string, after []string, items map[string]item) error {
	if err := checkWords("after", after); err != nil {
		return err
	}
	for i, a := range after {
		if slices.Contains(after[:i], a) {
			return refusal.New("%s is named twice. Pass each ID it waits for once, as `--after S1 --after Q2`", a)
		}
		if a == id {
			return refusal.New("%s cannot wait for itself. Pass the IDs it waits for, as `agent-plan status %s` lists them", id, p.file(""))
		}
		if _, ok := items[a]; !ok {
			return refusal.New("%q is not an item of %s, so %s would wait for it forever. Pass IDs `agent-plan status %s` lists", a, p.Name, id, p.file(""))
		}
	}
	return nil
}

// partFile is part's file, refused when it does not exist.
func (p Plan) partFile(part string) (string, error) {
	if part != "" && !partPattern.MatchString(part) {
		return "", refusal.New("%q is not a part ID. Pass one such as `--part P1`", part)
	}
	file := p.file(part)
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return "", refusal.New("%s has no plan file for part %s. The part's sub-agent creates it with `agent-plan new --parent %s --part %s`", p.Name, part, p.file(""), part)
	}
	return file, nil
}

// AddEntry writes a step or a part at the end of its section of the plan's
// file, or a step in the part's, under the next free ID of its kind there,
// and logs it as planned.
func (p Plan) AddEntry(o EntryOptions, now time.Time) (Recorded, error) {
	if err := p.checkOwn(); err != nil {
		return Recorded{}, err
	}
	letter, ok := entryLetters[o.Section]
	switch {
	case !ok:
		return Recorded{}, refusal.New("agent-plan adds steps and parts, not %s entries. Ask with `agent-plan ask`, record follow-ups with `agent-plan followup`, references with `agent-plan reference`", o.Section)
	case o.Section == "Distribution" && o.Part != "":
		return Recorded{}, refusal.New("a part's file has no Distribution; its parts are the plan's. Add the part to the plan with `agent-plan part PLAN --title T`, without `--part`")
	case o.Section == "Steps" && o.Repo+o.Branch+o.Worktree != "":
		return Recorded{}, refusal.New("a step has no repository, branch or worktree of its own; a part does. Add a part with `agent-plan part`, or pass the step without them")
	}
	if err := checkTitle("title", o.Title); err != nil {
		return Recorded{}, err
	}
	o.Title = strings.TrimSpace(o.Title)
	body, err := textLines(o.Text)
	if err != nil {
		return Recorded{}, err
	}
	if o.Repo+o.Branch+o.Worktree != "" {
		where, err := partWhere(o.Repo, o.Branch, o.Worktree)
		if err != nil {
			return Recorded{}, err
		}
		body = append([]string{where}, body...)
	}
	file, err := p.partFile(o.Part)
	if err != nil {
		return Recorded{}, err
	}
	var r Recorded
	err = p.withLock(func(l *lockedPlan) error {
		items, err := p.items()
		if err != nil {
			return err
		}
		prefix := ""
		if o.Part != "" {
			prefix = o.Part + "."
		}
		id, err := p.nextID(prefix, letter, items)
		if err != nil {
			return err
		}
		if err := p.knownAfter(id, o.After, items); err != nil {
			return err
		}
		block := []string{"*** " + kwTodo + " " + o.Title, ":PROPERTIES:", ":CUSTOM_ID: " + id}
		if len(o.After) > 0 {
			block = append(block, ":AFTER: "+strings.Join(o.After, " "))
		}
		block = append(append(block, ":END:"), body...)
		err = l.edit(file, func(lines []string) ([]string, error) {
			_, end, ok := section(lines, o.Section)
			if !ok {
				return nil, refusal.New("%s has no `** %s` heading. Ask the operator to restore it as the plan-work skill describes, then run the command again", file, o.Section)
			}
			// A section ending the file ends at its final empty line.
			if end == len(lines) && lines[end-1] == "" {
				end--
			}
			return slices.Insert(slices.Clone(lines), end, block...), nil
		})
		if err != nil {
			return err
		}
		e, err := l.log(Event{Kind: "planned", Item: id, Summary: o.Title}, now)
		r = Recorded{ID: id, File: file, Event: e}
		return err
	})
	return r, err
}

// partWhere is a part's first line of text, where its work is, as
// Distribution entries say it: `REPO: BRANCH on BASE, worktree DIR.`
func partWhere(repo, branch, worktree string) (string, error) {
	for what, v := range map[string]string{"repo": repo, "branch": branch, "worktree": worktree} {
		if v != "" {
			if err := checkWords(what, []string{v}); err != nil {
				return "", err
			}
		}
	}
	var b strings.Builder
	if repo != "" {
		b.WriteString(repo + ": ")
	}
	if branch != "" {
		base, name, ok := strings.Cut(branch, "..")
		if !ok || base == "" || name == "" {
			return "", refusal.New("branch %q is not BASE..BRANCH. Pass it as `--branch main..run-loop`", branch)
		}
		b.WriteString(name + " on " + base)
	}
	if worktree != "" {
		if branch != "" {
			b.WriteString(", ")
		}
		b.WriteString("worktree " + worktree)
	}
	return strings.TrimSuffix(strings.TrimSpace(b.String()), ":") + ".", nil
}

// nextID is the next ID prefix+letter+n that no heading, no event and,
// for a part, no part's file has named: an ID once named is never handed
// out again, or an event naming it would attach to the new entry.
func (p Plan) nextID(prefix, letter string, items map[string]item) (string, error) {
	events, err := p.Events()
	if err != nil {
		return "", err
	}
	files, err := p.files()
	if err != nil {
		return "", err
	}
	var seen []string
	for id := range items {
		seen = append(seen, id)
	}
	for _, e := range events {
		seen = append(seen, e.Item)
	}
	for _, f := range files {
		seen = append(seen, partOf(f))
	}
	pattern, next := idOf(prefix, letter), 1
	for _, id := range seen {
		if m := pattern.FindStringSubmatch(id); m != nil {
			if n, _ := strconv.Atoi(m[1]); n >= next {
				next = n + 1
			}
		}
	}
	return fmt.Sprintf("%s%s%d", prefix, letter, next), nil
}

// EditOptions are what EditEntry changes: the title, the text, or both;
// nil leaves it as it is, and an empty Text removes the text.
type EditOptions struct {
	Title, Text *string
}

// EditEntry changes the title or text of step, part or follow-up id in
// place, keeping its keyword, priority, tags, planning line, drawers and
// the headings under it, and logs it as planned.
func (p Plan) EditEntry(id string, o EditOptions, now time.Time) (Recorded, error) {
	if err := p.checkOwn(); err != nil {
		return Recorded{}, err
	}
	switch {
	case questionID.MatchString(id):
		return Recorded{}, refusal.New("%s is a question, whose text is what the operator answers. Settle it with `agent-plan decide PLAN %s --drop --answer WHY` and ask the new one with `agent-plan ask`, or reword it in your report for the operator", id, id)
	case !projected(id):
		return Recorded{}, refusal.New("%q is not a step, a part or a follow-up. Pass one such as S1, P1 or F1 that `agent-plan status %s` lists; write Goal or Verification with `agent-plan section`", id, p.file(""))
	case o.Title == nil && o.Text == nil:
		return Recorded{}, refusal.New("there is nothing to change. Pass `--title 'one line'`, `--text TEXT` or both")
	}
	var body []string
	if o.Title != nil {
		if err := checkTitle("title", *o.Title); err != nil {
			return Recorded{}, err
		}
	}
	if o.Text != nil {
		var err error
		if body, err = textLines(*o.Text); err != nil {
			return Recorded{}, err
		}
	}
	var r Recorded
	err := p.withLock(func(l *lockedPlan) error {
		items, err := p.items()
		if err != nil {
			return err
		}
		it, ok := items[id]
		if !ok {
			return refusal.New("%q is not an item of %s. Use an ID `agent-plan status %s` lists", id, p.Name, p.file(""))
		}
		err = l.edit(it.File, func(lines []string) ([]string, error) {
			at := headingWithID(lines, id)
			if at < 0 {
				return nil, fmt.Errorf("%s is no longer in %s", id, it.File)
			}
			ks := fileKeywords(strings.Join(lines, "\n"))
			if o.Text != nil {
				lines = setBody(lines, at, body)
			}
			if o.Title != nil {
				lines = setTitle(lines, at, strings.TrimSpace(*o.Title), ks)
			}
			return lines, nil
		})
		if err != nil {
			return err
		}
		summary := "title and text edited"
		switch {
		case o.Text == nil:
			summary = "title edited"
		case o.Title == nil:
			summary = "text edited"
		}
		e, err := l.log(Event{Kind: "planned", Item: id, Summary: summary}, now)
		r = Recorded{ID: id, File: it.File, Event: e}
		return err
	})
	return r, err
}

// setTitle gives the heading at lines[at] title, keeping its stars,
// keyword, priority and tags.
func setTitle(lines []string, at int, title string, ks keywordSet) []string {
	out := slices.Clone(lines)
	n := level(out[at])
	h := headingOf(n, strings.TrimSpace(out[at][n:]), ks)
	words := []string{strings.Repeat("*", n)}
	if h.Keyword != "" {
		words = append(words, h.Keyword)
	}
	if h.Priority != "" {
		words = append(words, "[#"+h.Priority+"]")
	}
	words = append(words, title)
	if len(h.Tags) > 0 {
		words = append(words, ":"+strings.Join(h.Tags, ":")+":")
	}
	out[at] = strings.Join(words, " ")
	return out
}

// setBody replaces the text of the heading at lines[at], up to the next
// heading, with body, keeping its planning line and every drawer where it
// was; body goes where the old text began, or after the drawers.
func setBody(lines []string, at int, body []string) []string {
	start := at + 1
	if start < len(lines) && planningLine.MatchString(lines[start]) {
		start++
	}
	end := len(lines)
	blocks := blockLines(lines)
	for i := start; i < len(lines); i++ {
		if level(lines[i]) > 0 && !blocks[i] {
			end = i
			break
		}
	}
	// A heading ending the file ends at its final empty line.
	if end == len(lines) && end > start && lines[end-1] == "" {
		end--
	}
	var kept []string
	insert := -1
	for i := start; i < end; i++ {
		if !blocks[i] && drawerStart.MatchString(lines[i]) && strings.TrimSpace(lines[i]) != ":END:" {
			if stop := slices.IndexFunc(lines[i:end], func(l string) bool { return strings.TrimSpace(l) == ":END:" }); stop >= 0 {
				kept = append(kept, lines[i:i+stop+1]...)
				i += stop
				continue
			}
		}
		if insert < 0 {
			insert = len(kept)
		}
	}
	if insert < 0 {
		insert = len(kept)
	}
	kept = slices.Insert(kept, insert, body...)
	return slices.Concat(lines[:start], kept, lines[end:])
}

// SetAfter sets the AFTER of step, part or question id to after, or with
// none removes it, and logs it as planned.
func (p Plan) SetAfter(id string, after []string, now time.Time) (Event, error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	if !stepPattern.MatchString(id) && !partPattern.MatchString(id) && !questionID.MatchString(id) {
		return Event{}, refusal.New("%q is not a step, a part or a question, the items that wait for others. Pass an ID such as S1, P1 or Q1 that `agent-plan status %s` lists", id, p.file(""))
	}
	var e Event
	err := p.withLock(func(l *lockedPlan) error {
		items, err := p.items()
		if err != nil {
			return err
		}
		it, ok := items[id]
		if !ok {
			return refusal.New("%q is not an item of %s. Use an ID `agent-plan status %s` lists", id, p.Name, p.file(""))
		}
		if err := p.knownAfter(id, after, items); err != nil {
			return err
		}
		err = l.edit(it.File, func(lines []string) ([]string, error) {
			at := headingWithID(lines, id)
			if at < 0 {
				return nil, fmt.Errorf("%s is no longer in %s", id, it.File)
			}
			if len(after) == 0 {
				return deleteProperty(lines, at, "AFTER"), nil
			}
			return setProperty(lines, at, "AFTER", strings.Join(after, " ")), nil
		})
		if err != nil {
			return err
		}
		summary := "AFTER cleared"
		if len(after) > 0 {
			summary = "AFTER " + strings.Join(after, " ")
		}
		e, err = l.log(Event{Kind: "planned", Item: id, Summary: summary}, now)
		return err
	})
	return e, err
}

// SetSection writes text as the whole of section name, Goal or
// Verification, of the plan's file or the part's, and logs it as planned.
func (p Plan) SetSection(part, name, text string, now time.Time) (Event, error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	if !slices.Contains(textSections, name) {
		return Event{}, refusal.New("%q is not a section that holds text (%s). Add steps and parts with `agent-plan step` and `agent-plan part`, and change one with `agent-plan edit`",
			name, strings.Join(textSections, ", "))
	}
	body, err := textLines(text)
	if err != nil {
		return Event{}, err
	}
	if len(body) == 0 && name == "Goal" {
		return Event{}, refusal.New("the goal is empty, and the fresh review reads it as its goal statement. Pass the goal and what is not this change's job with `--text`")
	}
	file, err := p.partFile(part)
	if err != nil {
		return Event{}, err
	}
	var e Event
	err = p.withLock(func(l *lockedPlan) error {
		err := l.edit(file, func(lines []string) ([]string, error) {
			start, end, ok := section(lines, name)
			if !ok {
				return nil, refusal.New("%s has no `** %s` heading. Ask the operator to restore it as the plan-work skill describes, then run the command again", file, name)
			}
			if end == len(lines) && lines[end-1] == "" {
				end--
			}
			// Headings under a text section are drift the operator settles;
			// writing the section would delete them.
			if at, _, _ := headings(lines[start+1 : end]); len(at) > 0 {
				return nil, refusal.New("%s has headings under `** %s`, which hold the operator's text and would be lost. Ask the operator to move them, then run the command again", file, name)
			}
			return slices.Concat(lines[:start+1], body, lines[end:]), nil
		})
		if err != nil {
			return err
		}
		e, err = l.log(Event{Kind: "planned", Item: p.item(part), Summary: name + " written"}, now)
		return err
	})
	return e, err
}
