package plans

import (
	"regexp"
	"strings"
	"time"
)

// heading is one Org heading, with its property drawer and the lines up to
// the next heading.
type heading struct {
	Level int
	// Keyword is a TODO keyword its file defines, and Done whether it is
	// one of the file's done keywords.
	Keyword  string
	Done     bool
	Priority string
	Title    string
	Tags     []string
	// Scheduled is the SCHEDULED timestamp's day, or its time when it has
	// one; zero without one.
	Scheduled        time.Time
	ScheduledHasTime bool
	Props            map[string]string
	// Body is the text outside drawers, and Notes the lines inside the
	// LOGBOOK drawer, where Org writes notes; other drawers are neither.
	Body  []string
	Notes []string
}

var (
	// headingLine is a heading as Org reads one: stars, then a space; a
	// tab after the stars makes a line of text. starLine agrees with it.
	headingLine  = regexp.MustCompile(`^(\*+) +(.*?)\s*$`)
	propertyLine = regexp.MustCompile(`^\s*:([^:\s]+):(?:\s+(.*?))?\s*$`)
	planningLine = regexp.MustCompile(`^\s*(SCHEDULED|DEADLINE|CLOSED):`)
	blockLine    = regexp.MustCompile(`(?i)^\s*#\+(begin|end)_(\S*)`)
	priorityWord = regexp.MustCompile(`^\[#([A-Z0-9])\]$`)
	tagsSuffix   = regexp.MustCompile(`\s+:((?:[\p{L}\p{N}_@#%]+:)+)$`)
	drawerStart  = regexp.MustCompile(`^\s*:([A-Za-z0-9_-]+):\s*$`)
	// scheduledStamp is a SCHEDULED timestamp's date and, if it has one,
	// time of day.
	scheduledStamp = regexp.MustCompile(`SCHEDULED:\s*<([0-9]{4}-[0-9]{2}-[0-9]{2})[^>0-9]*(?:([0-9]{1,2}:[0-9]{2})[^>]*)?>`)
)

func set(words ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

// blockLines is, for each of lines, whether it is part of a block, its
// #+begin_ and #+end_ lines included, as Org reads one: a #+begin_NAME
// line starts a block only where a #+end_NAME line follows it, and is a
// line of text otherwise.
func blockLines(lines []string) []bool {
	in := make([]bool, len(lines))
	for i := 0; i < len(lines); i++ {
		begin := blockLine.FindStringSubmatch(lines[i])
		if begin == nil || !strings.EqualFold(begin[1], "begin") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if end := blockLine.FindStringSubmatch(lines[j]); end != nil && strings.EqualFold(end[1], "end") &&
				strings.EqualFold(end[2], begin[2]) {
				for k := i; k <= j; k++ {
					in[k] = true
				}
				i = j
				break
			}
		}
	}
	return in
}

// parseOrg reads the headings of an Org file, with the TODO keywords the
// file defines. A line inside a block, by blockLines, is never a heading.
func parseOrg(text string) []heading {
	keywords := fileKeywords(text)
	var out []heading
	lines := strings.Split(text, "\n")
	blocks := blockLines(lines)
	// Where the current heading's lines are: before its property drawer,
	// in it, after it, or in another drawer.
	const (
		top = iota
		properties
		section
		otherDrawer
	)
	at := top
	logbook := false
	for i, line := range lines {
		inBlock := blocks[i]
		if m := headingLine.FindStringSubmatch(line); m != nil && !inBlock {
			out = append(out, headingOf(len(m[1]), m[2], keywords))
			at = top
			continue
		}
		if len(out) == 0 {
			continue
		}
		h := &out[len(out)-1]
		trimmed := strings.TrimSpace(line)
		switch {
		case at == top && len(h.Body) == 0 && planningLine.MatchString(line):
			if m := scheduledStamp.FindStringSubmatch(line); m != nil {
				if m[2] == "" {
					h.Scheduled, _ = time.ParseInLocation("2006-01-02", m[1], time.Local)
				} else {
					h.Scheduled, _ = time.ParseInLocation("2006-01-02 15:04", m[1]+" "+m[2], time.Local)
					h.ScheduledHasTime = true
				}
			}
			continue
		case at == top && len(h.Body) == 0 && trimmed == ":PROPERTIES:":
			at = properties
			continue
		case (at == properties || at == otherDrawer) && trimmed == ":END:":
			at = section
			continue
		case at == properties:
			if m := propertyLine.FindStringSubmatch(line); m != nil {
				h.Props[strings.ToUpper(m[1])] = m[2]
			}
			continue
		case at == otherDrawer:
			if logbook {
				h.Notes = append(h.Notes, line)
			}
			continue
		case !inBlock && drawerStart.MatchString(line) && trimmed != ":END:":
			at, logbook = otherDrawer, strings.EqualFold(trimmed, ":LOGBOOK:")
			continue
		}
		at = section
		h.Body = append(h.Body, line)
	}
	return out
}

// headingOf is the heading of level whose line holds text after its stars.
func headingOf(level int, text string, keywords keywordSet) heading {
	h := heading{Level: level, Props: map[string]string{}}
	if word, rest, _ := strings.Cut(text, " "); keywords.all[word] {
		h.Keyword, h.Done, text = word, keywords.done[word], strings.TrimSpace(rest)
	}
	if word, rest, _ := strings.Cut(text, " "); priorityWord.MatchString(word) {
		h.Priority, text = priorityWord.FindStringSubmatch(word)[1], strings.TrimSpace(rest)
	}
	if m := tagsSuffix.FindStringSubmatch(text); m != nil {
		h.Tags = strings.Split(strings.Trim(m[1], ":"), ":")
		text = strings.TrimSpace(text[:len(text)-len(m[0])])
	}
	h.Title = text
	return h
}

// orgTitle is the #+title of an Org file.
func orgTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := cutFold(line, "#+title:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

func cutFold(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// escapeOrg escapes text for an Org block as Org does: a comma before every
// line that starts, after indentation, with `*` or `#+`, or with commas
// before one.
func escapeOrg(text string) string {
	return escapable.ReplaceAllString(text, "$1,$2")
}

var escapable = regexp.MustCompile(`(?m)^([ \t]*)(,*(?:\*|#\+))`)
