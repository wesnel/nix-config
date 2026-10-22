package review

import (
	"regexp"
	"strings"
)

var (
	markdownHeading = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	// markdownFence is a fence line: up to three spaces, then a run of
	// three or more backticks or tildes, then the rest of the line.
	markdownFence = regexp.MustCompile("^[ \t]{0,3}(`{3,}|~{3,})(.*)$")
)

// SectionNamed is the text under text's Markdown heading named name, of
// any level and in any case, up to the next heading of its level or above
// outside a code fence, and whether text has the heading. A fence closes
// only on a line of its own character, at least as long as the one that
// opened it, with nothing after it but spaces, as CommonMark has it.
func SectionNamed(text, name string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	level, start := 0, -1
	open := "" // the fence that opened the block the line is in, or ""
	for i, line := range lines {
		if m := markdownFence.FindStringSubmatch(line); m != nil {
			switch {
			case open == "":
				open = m[1]
				continue
			case m[1][0] == open[0] && len(m[1]) >= len(open) && strings.TrimSpace(m[2]) == "":
				open = ""
				continue
			}
		}
		if open != "" {
			continue
		}
		m := markdownHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		switch {
		case start < 0 && strings.EqualFold(m[2], name):
			level, start = len(m[1]), i+1
		case start >= 0 && len(m[1]) <= level:
			return strings.Join(lines[start:i], "\n"), true
		}
	}
	if start < 0 {
		return "", false
	}
	return strings.Join(lines[start:], "\n"), true
}

// withoutComments is text without its HTML comments, such as a template
// leaves for the author to replace: each `<!--` outside a fenced block and
// a code span, up to the next `-->`, or to the end where none closes it.
// A `<!--` in code is text, and starts nothing.
func withoutComments(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var b strings.Builder
	open := ""         // the fence of the block the line is in, or ""
	inComment := false // whether the line starts inside a comment
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if !inComment {
			if m := markdownFence.FindStringSubmatch(line); m != nil {
				switch {
				case open == "":
					open = m[1]
				case m[1][0] == open[0] && len(m[1]) >= len(open) && strings.TrimSpace(m[2]) == "":
					open = ""
				}
				b.WriteString(line)
				continue
			}
			if open != "" {
				b.WriteString(line)
				continue
			}
		}
		for line != "" {
			if inComment {
				end := strings.Index(line, "-->")
				if end < 0 {
					line = ""
					break
				}
				line, inComment = line[end+3:], false
				continue
			}
			comment, span := strings.Index(line, "<!--"), strings.IndexByte(line, '`')
			if comment < 0 && span < 0 {
				b.WriteString(line)
				break
			}
			if span >= 0 && (comment < 0 || span < comment) {
				// A run of backticks opens a span that the next run of the
				// same length closes; an unclosed run is text.
				run := len(line[span:]) - len(strings.TrimLeft(line[span:], "`"))
				ticks := line[span : span+run]
				end := span + run
				if close := closingTicks(line[end:], ticks); close >= 0 {
					end += close + run
				}
				b.WriteString(line[:end])
				line = line[end:]
				continue
			}
			b.WriteString(line[:comment])
			line, inComment = line[comment+4:], true
		}
	}
	return b.String()
}

// closingTicks is where in s a run of exactly ticks starts, the end of a
// code span ticks opened, or -1.
func closingTicks(s, ticks string) int {
	for i := 0; i < len(s); {
		j := strings.Index(s[i:], ticks)
		if j < 0 {
			return -1
		}
		j += i
		end := j + len(ticks)
		if (j == 0 || s[j-1] != '`') && (end == len(s) || s[end] != '`') {
			return j
		}
		for end < len(s) && s[end] == '`' {
			end++
		}
		i = end
	}
	return -1
}
