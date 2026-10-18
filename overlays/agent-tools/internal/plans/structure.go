package plans

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// StructureDrift is a heading of one of a plan's files, File, that the
// plan's structure has no place for, and Problem says why. Every plan
// file holds what is intended and decided now; what happened is the
// log's.
type StructureDrift struct {
	File    string `json:"file"`
	Heading string `json:"heading"`
	Problem string `json:"problem"`
}

// entryIDs are the sections whose headings are entries, each with the
// IDs its entries take and how a problem names them.
var entryIDs = map[string]struct {
	match func(string) bool
	shown string
}{
	"Steps":        {stepPattern.MatchString, "S<n>"},
	"Distribution": {partPattern.MatchString, "P<n>"},
	"Questions":    {questionID.MatchString, "Q<n>"},
	"Follow-ups":   {followupID.MatchString, "F<n>"},
}

// questionKeywords are the keywords of questionSequence, in its order.
var questionKeywords = func() []string {
	var out []string
	for _, w := range strings.Fields(questionSequence) {
		if w != "|" {
			out = append(out, decoration.ReplaceAllString(w, ""))
		}
	}
	return out
}()

// orgHeading is a heading line as Org reads it, inside a block too.
var orgHeading = regexp.MustCompile(`^\*+ `)

// structureProblem is a heading of a plan file that the plan's structure
// has no place for, and why.
type structureProblem struct{ heading, problem string }

// structureOf is every heading of a plan file's text that is not where
// the plan-work skill puts it:
//   - a top-level heading but one `* Plan` and the `* Approved plan`s
//     plan mode records;
//   - a section of `* Plan` not in planSections, or one it has twice;
//   - a heading under Goal or Verification, which hold text;
//   - a References entry without a URL;
//   - an entry of Steps, Distribution, Questions or Follow-ups without its
//     ID, and a question without a question's keyword;
//   - a heading more than a level below its parent;
//   - a heading line inside a block, which Org reads as a heading where
//     agent-plan does not, and a #+begin_ line no #+end_ line closes;
//   - a group with text of its own, or with no entries under it.
//
// An entry is a level-3 heading, or a heading right under a group: in
// Steps and Distribution a heading with `:ORDERED: t`, which is an entry
// too when it has an ID, and in any of those sections Org's archive
// sibling, `Archive` tagged ARCHIVE. The headings under an entry are its
// own, and those under a heading reported, or under `* Approved plan`,
// are not checked.
func structureOf(text string) []structureProblem {
	var out []structureProblem
	add := func(h heading, format string, args ...any) {
		out = append(out, structureProblem{strings.Repeat("*", h.Level) + " " + h.Title, fmt.Sprintf(format, args...)})
	}
	lines := strings.Split(text, "\n")
	for i, in := range blockLines(lines) {
		switch m := blockLine.FindStringSubmatch(lines[i]); {
		case in && orgHeading.MatchString(lines[i]):
			out = append(out, structureProblem{strings.TrimSpace(lines[i]),
				"a heading line inside a block, which Org reads as a heading; Org escapes it with a comma"})
		case !in && m != nil && strings.EqualFold(m[1], "begin"):
			out = append(out, structureProblem{strings.TrimSpace(lines[i]), "a block with no end, which Org reads as text"})
		}
	}

	// above is the chain of headings above the current one, nearest last.
	type above struct {
		level   int
		section string
		// skip is whether the headings under it go unchecked: it was
		// reported, is an entry, or is Approved plan.
		skip  bool
		group bool
	}
	var stack []above
	plans, sections := 0, map[string]bool{}
	hs := parseOrg(text)
	for i, h := range hs {
		for len(stack) > 0 && stack[len(stack)-1].level >= h.Level {
			stack = stack[:len(stack)-1]
		}
		var parent above
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		me := above{level: h.Level, section: parent.section, skip: parent.skip}
		title := sectionTitle(h)
		switch {
		case parent.skip:
		case h.Level == 1:
			switch {
			case title == "Plan" && plans == 0:
				plans++
			case title == "Plan":
				add(h, "a second `* Plan`")
				me.skip = true
			case title == "Approved plan":
				me.skip = true
			default:
				add(h, "a top-level heading other than `* Plan` and `* Approved plan`")
				me.skip = true
			}
		case parent.level != h.Level-1:
			add(h, "a level-%d heading with no level-%d heading above it", h.Level, h.Level-1)
			me.skip = true
		case h.Level == 2:
			me.section = title
			switch {
			case !slices.Contains(planSections, title):
				add(h, "not a plan section")
				me.skip = true
			case sections[title]:
				add(h, "a second `** %s`", title)
				me.skip = true
			}
			sections[title] = true
		case me.section == "Goal" || me.section == "Verification":
			add(h, "a heading under %s, which holds text", me.section)
			me.skip = true
		case me.section == "References":
			if h.Props["URL"] == "" {
				add(h, "a References entry without a URL")
			}
			me.skip = true
		case h.Level == 3 || parent.group:
			ids := entryIDs[me.section]
			id := h.Props["ID"]
			archive := h.Title == "Archive" && slices.Contains(h.Tags, archiveTag)
			me.group = archive || h.Props["ORDERED"] == "t" && (me.section == "Steps" || me.section == "Distribution")
			if me.group && (archive || id == "") {
				switch {
				case strings.TrimSpace(strings.Join(h.Body, "")) != "":
					add(h, "a group with text of its own")
					me.skip = true
				case i+1 == len(hs) || hs[i+1].Level <= h.Level:
					add(h, "a group with no entries under it")
				}
				break
			}
			me.skip = !me.group
			if !ids.match(id) {
				add(h, "a %s entry without an ID %s", me.section, ids.shown)
			} else if me.section == "Questions" && !slices.Contains(questionKeywords, h.Keyword) {
				add(h, "a question without a keyword %s or %s", strings.Join(questionKeywords[:len(questionKeywords)-1], ", "),
					questionKeywords[len(questionKeywords)-1])
			}
		}
		stack = append(stack, me)
	}
	return out
}

// structureDrift is the structure drift of each of the plan's files, as
// structureOf finds it.
func (p Plan) structureDrift() ([]StructureDrift, error) {
	files, err := p.files()
	if err != nil {
		return nil, err
	}
	out := []StructureDrift{}
	for _, f := range files {
		data, err := readPlan(f)
		if err != nil {
			return nil, err
		}
		for _, d := range structureOf(string(data)) {
			out = append(out, StructureDrift{File: f, Heading: d.heading, Problem: d.problem})
		}
		if partOf(f) == "" {
			continue
		}
		for _, h := range parseOrg(string(data)) {
			if h.Level == 1 && sectionTitle(h) == "Plan" && strings.TrimSpace(h.Props[buildsProperty]) != "" {
				out = append(out, StructureDrift{File: f, Heading: "* " + h.Title,
					Problem: "a BUILDS property, which belongs on the part's Distribution entry, where `agent-plan link` writes it"})
				break
			}
		}
	}
	return out, nil
}

// headingShown is the most of a drifting heading status and the briefs
// show; a journal's headings run to paragraphs.
const headingShown = 80

// briefDrifts is how many drifting headings a brief names.
const briefDrifts = 3

// structureLine is a brief's line on s's structure drift: how many
// headings drift, and the first few; or "" without any.
func (s Status) structureLine() string {
	n := len(s.StructureDrift)
	if n == 0 {
		return ""
	}
	var headings []string
	for _, d := range s.StructureDrift[:min(n, briefDrifts)] {
		headings = append(headings, "`"+clip(d.Heading, headingShown)+"`")
	}
	if n > briefDrifts {
		headings = append(headings, fmt.Sprintf("and %d more", n-briefDrifts))
	}
	return fmt.Sprintf("Structure drift in %d headings, which `agent-plan status %s` lists: %s", n, s.Plan,
		strings.Join(headings, ", "))
}
