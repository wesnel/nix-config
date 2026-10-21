package record

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Text from a model, GitHub or a test run is untrusted: Emacs follows links,
// evaluates inline code and sets file variables from what it reads in an Org
// file. The renderer keeps it inert, as testdata/inert.el checks with Org
// itself:
//   - headings hold only a keyword and an ID;
//   - a title or other property value is one line with `[` and `]` made
//     `(` and `)`, so it holds no link;
//   - a path is linked only where the link syntax can hold it, and written
//     as pathText, which keeps every path whole;
//   - every other text goes in an example block, where Org parses nothing,
//     with Org's own comma escape on a line that would start a heading or
//     end the block;
//   - `Local Variables:` and `-*-` are broken everywhere, since Emacs scans
//     plain text, blocks included, for file variables.
//
// Each step is idempotent, so a file the renderer wrote parses to values
// that render to the same bytes: the structure check rests on that.

// Caps on what a record keeps, in bytes for text and in characters for a
// title.
const (
	TextCap    = 8 << 10
	DiagramCap = 64 << 10
	TitleCap   = 120
	ValueCap   = 200
)

// Clean drops control characters but newline and tab, which a terminal or
// an editor could act on, and the bidirectional controls, which make text
// read other than it is.
func Clean(text string) string {
	text = strings.ToValidUTF8(text, "�")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return -1
		case r == 0x061c || r == 0x200e || r == 0x200f || (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069):
			return -1
		}
		return r
	}, text)
}

// Cap cuts text to at most limit bytes, ending it with a line saying so.
func Cap(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	const cut = "\n[cut]"
	end := limit - len(cut)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + cut
}

var localVariables = regexp.MustCompile(`(?i)(local variables):`)

// neutralise breaks what Emacs reads file variables from: a `Local
// Variables:` line, matched without case as Emacs does, and a `-*-` line.
func neutralise(text string) string {
	text = localVariables.ReplaceAllString(text, "$1 :")
	for strings.Contains(text, "-*-") {
		text = strings.ReplaceAll(text, "-*-", "- * -")
	}
	return text
}

// freeText is text as an example block holds it.
func freeText(text string, limit int) string {
	return Cap(neutralise(Clean(text)), limit)
}

// Text is a comment's or a draft's text as the record keeps it, and as it
// reads back.
func Text(text string) string { return freeText(text, TextCap) }

// value is text as a property holds it: one line, no link, at most limit
// characters.
func value(text string, limit int) string {
	text = neutralise(strings.Join(strings.Fields(Clean(text)), " "))
	text = strings.NewReplacer("[", "(", "]", ")").Replace(text)
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

// escapable is a line Org escapes inside a block: one that would start a
// heading or a keyword such as #+end_example, after any commas already
// escaping it.
var escapable = regexp.MustCompile(`^([ \t]*)(,*(?:\*|#\+))`)

// escaped is how Org writes an escaped line back.
var escaped = regexp.MustCompile(`^([ \t]*),(,*(?:\*|#\+))`)

func escapeLine(line string) string   { return escapable.ReplaceAllString(line, "$1,$2") }
func unescapeLine(line string) string { return escaped.ReplaceAllString(line, "$1$2") }

// pathPattern is a repository path a link may name: nothing Org's link
// syntax or a heading could take from it.
var pathPattern = regexp.MustCompile(`^[A-Za-z0-9_.@+-][A-Za-z0-9_.@+/ -]*$`)

// validPath reports whether a link may name p. Any other path is written
// without a link, as pathText.
func validPath(p string) bool {
	if !pathPattern.MatchString(p) || strings.HasSuffix(p, " ") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// pathText is a path as a property or a hunk's line holds it: a path a
// link may name as it is, and any other with each byte outside
// [A-Za-z0-9_.@+/-] written %XX, so that Org reads no syntax in it and
// readPath gives back exactly the path. A valid path holds no `%`, so the
// two forms cannot be confused.
func pathText(p string) string {
	if validPath(p) {
		return p
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c < 0x80 && (c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("_.@+/-", c) >= 0) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// readPath undoes pathText, and reports false for text pathText does not
// write.
func readPath(text string) (string, bool) {
	if !strings.Contains(text, "%") {
		return text, true
	}
	p, err := url.PathUnescape(text)
	if err != nil || pathText(p) != text {
		return "", false
	}
	return p, true
}

// idPattern is a heading's ID: a chunk's C1, a finding's F3, a thread's
// GitHub node ID.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_=-]*$`)

// login is a GitHub login and author type, which AUTHORS holds verbatim.
var (
	loginPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	typePattern  = regexp.MustCompile(`^[A-Za-z]+$`)
)
