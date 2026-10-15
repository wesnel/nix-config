package plans

import (
	"regexp"
	"strings"
)

// heading is one Org heading, with its property drawer and the lines up to
// the next heading.
type heading struct {
	Level   int
	Keyword string
	Title   string
	Props   map[string]string
	Body    []string
}

var (
	headingLine  = regexp.MustCompile(`^(\*+)\s+(.*?)\s*$`)
	propertyLine = regexp.MustCompile(`^\s*:([^:\s]+):(?:\s+(.*?))?\s*$`)
	planningLine = regexp.MustCompile(`^\s*(SCHEDULED|DEADLINE|CLOSED):`)
	blockLine    = regexp.MustCompile(`(?i)^\s*#\+(begin|end)_`)
	keywords     = set("TODO", "NEXT", "DONE")
)

func set(words ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

// parseOrg reads the headings of an Org file. A line inside a block is
// never a heading.
func parseOrg(text string) []heading {
	var out []heading
	inBlock := false
	drawer := 0 // 1 while reading a heading's property drawer, 2 after it
	for _, line := range strings.Split(text, "\n") {
		if m := blockLine.FindStringSubmatch(line); m != nil {
			inBlock = strings.EqualFold(m[1], "begin")
		}
		if m := headingLine.FindStringSubmatch(line); m != nil && !inBlock {
			h := heading{Level: len(m[1]), Title: m[2], Props: map[string]string{}}
			if word, rest, _ := strings.Cut(h.Title, " "); keywords[word] {
				h.Keyword, h.Title = word, strings.TrimSpace(rest)
			} else if keywords[h.Title] {
				h.Keyword, h.Title = h.Title, ""
			}
			out = append(out, h)
			drawer = 0
			continue
		}
		if len(out) == 0 {
			continue
		}
		h := &out[len(out)-1]
		switch {
		case drawer == 0 && len(h.Body) == 0 && planningLine.MatchString(line):
			continue
		case drawer == 0 && len(h.Body) == 0 && strings.TrimSpace(line) == ":PROPERTIES:":
			drawer = 1
			continue
		case drawer == 1 && strings.TrimSpace(line) == ":END:":
			drawer = 2
			continue
		case drawer == 1:
			if m := propertyLine.FindStringSubmatch(line); m != nil {
				h.Props[strings.ToUpper(m[1])] = m[2]
			}
			continue
		}
		drawer = 2
		h.Body = append(h.Body, line)
	}
	return out
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
