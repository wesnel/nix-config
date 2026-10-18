package plans

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// These edit an Org file's lines as Org does, so that a file agent-plan
// wrote reads the same as one the operator changed in Emacs. Each takes the
// lines and returns new ones, leaving its argument as it was.

var (
	idLine     = regexp.MustCompile(`^\s*:ID:\s*(\S+)\s*$`)
	closedPart = regexp.MustCompile(`CLOSED:\s*\[[^]]*\]\s*`)
	starLine   = regexp.MustCompile(`^(\*+) `)
)

// level is the level of a heading line, or 0 for any other line.
func level(line string) int {
	if m := starLine.FindStringSubmatch(line); m != nil {
		return len(m[1])
	}
	return 0
}

// sectionTitle is the title a plan's section or `* Plan` heading is known
// by: its title without the statistics cookies Org keeps current in it,
// as in `** Steps [1/4]`. Every lookup of a section by its title goes
// through it, so none disagrees with the structure check.
func sectionTitle(h heading) string {
	return strings.Join(strings.Fields(percentCookie.ReplaceAllString(fractionCookie.ReplaceAllString(h.Title, ""), "")), " ")
}

// lineTitle is sectionTitle of the heading on line, read with the
// keywords ks, or "" for a line that is no heading.
func lineTitle(line string, ks keywordSet) string {
	n := level(line)
	if n == 0 {
		return ""
	}
	return sectionTitle(headingOf(n, strings.TrimSpace(line[n:]), ks))
}

// sectionsOf is, for each of hs, the section of the plan it is in: the
// sectionTitle of its level-2 heading under the first `* Plan`, its own
// for a section; "" outside one. Every reader of a section's entries goes
// by it, as section does for writers.
func sectionsOf(hs []heading) []string {
	out := make([]string, len(hs))
	inPlan, plans, current := false, 0, ""
	for i, h := range hs {
		switch h.Level {
		case 1:
			inPlan = sectionTitle(h) == "Plan" && plans == 0
			if inPlan {
				plans++
			}
			current = ""
		case 2:
			current = ""
			if inPlan {
				current = sectionTitle(h)
			}
		}
		out[i] = current
	}
	return out
}

// headings are the indexes of lines' heading lines, those inside a block
// left out as parseOrg leaves them, with their levels and their titles by
// lineTitle.
func headings(lines []string) (at, levels []int, titles []string) {
	ks := fileKeywords(strings.Join(lines, "\n"))
	blocks := blockLines(lines)
	for i, l := range lines {
		if n := level(l); n > 0 && !blocks[i] {
			at, levels, titles = append(at, i), append(levels, n), append(titles, lineTitle(l, ks))
		}
	}
	return at, levels, titles
}

// planLine is the index of the first `* Plan` heading of lines, or -1.
func planLine(lines []string) int {
	at, levels, titles := headings(lines)
	for k := range at {
		if levels[k] == 1 && titles[k] == "Plan" {
			return at[k]
		}
	}
	return -1
}

// section is the lines of the section titled title of the first `* Plan`
// of lines: from its heading to the next heading of level 1 or 2.
func section(lines []string, title string) (start, end int, ok bool) {
	at, levels, titles := headings(lines)
	inPlan, plans := false, 0
	for k := range at {
		switch {
		case levels[k] == 1:
			inPlan = titles[k] == "Plan" && plans == 0
			if inPlan {
				plans++
			}
		case levels[k] == 2 && inPlan && titles[k] == title:
			end = len(lines)
			for j := k + 1; j < len(at); j++ {
				if levels[j] <= 2 {
					end = at[j]
					break
				}
			}
			return at[k], end, true
		}
	}
	return 0, 0, false
}

// headingWithID is the index of the heading whose property drawer holds
// :ID: id, or -1.
func headingWithID(lines []string, id string) int {
	for i, l := range lines {
		if m := idLine.FindStringSubmatch(l); m != nil && m[1] == id {
			for j := i; j >= 0; j-- {
				if level(lines[j]) > 0 {
					return j
				}
			}
		}
	}
	return -1
}

// closedStamp is a CLOSED: line's timestamp, as Org writes it.
func closedStamp(now time.Time) string {
	return "CLOSED: " + now.Format("[2006-01-02 Mon 15:04]")
}

// setKeyword gives the heading at lines[at] keyword kw, one of ks's. As
// Org does with org-log-done set to time, a change from an open keyword to
// a done one writes CLOSED, before any SCHEDULED or DEADLINE on the line
// after the heading; one from done to open removes it; one from done to
// done keeps it.
func setKeyword(lines []string, at int, kw string, ks keywordSet, now time.Time) []string {
	out := slices.Clone(lines)
	stars, text, _ := strings.Cut(out[at], " ")
	old := ""
	if word, rest, _ := strings.Cut(text, " "); ks.all[word] {
		old, text = word, rest
	} else if ks.all[text] {
		old, text = text, ""
	}
	out[at] = strings.TrimRight(stars+" "+kw+" "+text, " ")
	next := at + 1
	planning := next < len(out) && planningLine.MatchString(out[next])
	switch {
	case ks.done[kw] && !ks.done[old] && planning:
		rest := closedPart.ReplaceAllString(strings.TrimLeft(out[next], " \t"), "")
		indent := out[next][:len(out[next])-len(strings.TrimLeft(out[next], " \t"))]
		out[next] = strings.TrimRight(indent+closedStamp(now)+" "+rest, " ")
	case ks.done[kw] && !ks.done[old]:
		out = slices.Insert(out, next, closedStamp(now))
	case !ks.done[kw] && ks.done[old] && planning:
		if rest := strings.TrimRight(closedPart.ReplaceAllString(out[next], ""), " \t"); strings.TrimSpace(rest) == "" {
			out = slices.Delete(out, next, next+1)
		} else {
			out[next] = rest
		}
	}
	return out
}

// setProperty sets property name of the heading at lines[at] to value,
// adding it at the end of the heading's property drawer, and the drawer
// after the heading and its planning line if it has none.
func setProperty(lines []string, at int, name, value string) []string {
	out := slices.Clone(lines)
	line := ":" + name + ": " + value
	i := at + 1
	if i < len(out) && planningLine.MatchString(out[i]) {
		i++
	}
	if i >= len(out) || strings.TrimSpace(out[i]) != ":PROPERTIES:" {
		return slices.Insert(out, i, ":PROPERTIES:", line, ":END:")
	}
	for j := i + 1; j < len(out) && level(out[j]) == 0; j++ {
		switch {
		case strings.TrimSpace(out[j]) == ":END:":
			return slices.Insert(out, j, line)
		case strings.HasPrefix(strings.TrimSpace(out[j]), ":"+name+":"):
			out[j] = line
			return out
		}
	}
	return out
}

// deleteProperty removes property name from the property drawer of the
// heading at lines[at].
func deleteProperty(lines []string, at int, name string) []string {
	for j := at + 1; j < len(lines) && level(lines[j]) == 0; j++ {
		switch trimmed := strings.TrimSpace(lines[j]); {
		case trimmed == ":END:":
			return slices.Clone(lines)
		case strings.HasPrefix(trimmed, ":"+name+":"):
			return slices.Delete(slices.Clone(lines), j, j+1)
		}
	}
	return slices.Clone(lines)
}

// addTag adds tag to the heading at lines[at] unless it has it, a space
// after the title, as Org does with org-tags-column 0.
func addTag(lines []string, at int, tag string) []string {
	out := slices.Clone(lines)
	m := tagsSuffix.FindStringSubmatch(out[at])
	switch {
	case m == nil:
		out[at] += " :" + tag + ":"
	case !slices.Contains(strings.Split(strings.Trim(m[1], ":"), ":"), tag):
		out[at] += tag + ":"
	}
	return out
}

var (
	fractionCookie = regexp.MustCompile(`\[[0-9]*/[0-9]*\]`)
	percentCookie  = regexp.MustCompile(`\[[0-9]*%\]`)
)

// updateCookie updates the statistics cookie of the parent of the heading
// at lines[at], as Org does: its direct children with a keyword, and of
// those the done ones.
func updateCookie(lines []string, at int, ks keywordSet) []string {
	n := level(lines[at])
	parent := at - 1
	for parent >= 0 && (level(lines[parent]) == 0 || level(lines[parent]) >= n) {
		parent--
	}
	if parent < 0 || !(fractionCookie.MatchString(lines[parent]) || percentCookie.MatchString(lines[parent])) {
		return lines
	}
	closed, total := 0, 0
	for i := parent + 1; i < subtreeEnd(lines, parent); i++ {
		if level(lines[i]) != level(lines[parent])+1 {
			continue
		}
		h := headingOf(level(lines[i]), strings.SplitN(lines[i], " ", 2)[1], ks)
		if h.Keyword != "" {
			total++
			if h.Done {
				closed++
			}
		}
	}
	out := slices.Clone(lines)
	out[parent] = fractionCookie.ReplaceAllString(out[parent], fmt.Sprintf("[%d/%d]", closed, total))
	percent := 0
	if total > 0 {
		percent = 100 * closed / total
	}
	out[parent] = percentCookie.ReplaceAllString(out[parent], fmt.Sprintf("[%d%%]", percent))
	return out
}

// subtreeEnd is the index of the first line after the subtree of the
// heading at lines[at].
func subtreeEnd(lines []string, at int) int {
	n := level(lines[at])
	for i := at + 1; i < len(lines); i++ {
		if l := level(lines[i]); l > 0 && l <= n {
			return i
		}
	}
	return len(lines)
}
