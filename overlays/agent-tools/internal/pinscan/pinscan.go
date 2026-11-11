// Package pinscan reads what a change of a file may pin, as the file's
// syntax has it: the routing of a change to the upgrade review and that
// review's scanner read it alike, so that every change the scanner finds a
// pin in is routed to it.
package pinscan

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
)

// Line is a line of a file and its number, from 1.
type Line struct {
	N    int
	Text string
}

// ChangedLines are the lines of old that next does not hold as many
// times, and the lines of next old does not: a diff by content alone,
// which a line moved within the file does not count as changing.
func ChangedLines(old, next string) (removed, added []Line) {
	count := func(text string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(text, "\n") {
			m[l]++
		}
		return m
	}
	a, b := count(old), count(next)
	pick := func(text string, other map[string]int) []Line {
		var out []Line
		seen := map[string]int{}
		for i, l := range strings.Split(text, "\n") {
			seen[l]++
			if seen[l] > other[l] && strings.TrimSpace(l) != "" {
				out = append(out, Line{i + 1, l})
			}
		}
		return out
	}
	return pick(old, b), pick(next, a)
}

// NixSet is an attribute set of a Nix file, `{ … }`: where it opens and
// closes, the set it is in (-1 for none), the attribute it is assigned to
// and the function it is passed to, where it is, and the attributes it
// sets to a string literal, with where each value starts.
type NixSet struct {
	Start, End, Parent int
	Name, Call         string
	Attrs              map[string]string
	At                 map[string]int
}

// InString and InComment are where NixSets has a byte of a string, or of
// a comment, rather than of code.
const (
	InString  = -2
	InComment = -3
)

var (
	// nixString is an attribute set to a string literal, as `rev =
	// "v${version}"`: attribute names only, as the scan of the code finds
	// them.
	nixString = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_'-]*)\s*=\s*"((?:[^"\\$]|\\.|\$[^{]|\$\{[^}]*\})*)"`)
	// nixCall is the end of what comes before a set: `NAME = CALL ARG… {`,
	// `CALL rec {`, or a lambda's `ARG:`.
	nixAssign = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_'.-]*)\s*=\s*[^=;{}]*$`)
	nixCallee = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_'.-]*)\s*(?:rec\s*)?$`)
)

// NixSets are the attribute sets of a Nix file's text, in the order they
// open, the first standing for the file itself, where its top-level let
// bindings are; and for each byte of it the innermost set it is in, or
// InString in a string (an interpolation's ${...} being code) or
// InComment in a comment.
func NixSets(text string) ([]NixSet, []int) {
	s := nixScanner{
		text:  text,
		where: make([]int, len(text)),
		sets:  []NixSet{{Start: 0, End: len(text), Parent: -1, Attrs: map[string]string{}, At: map[string]int{}}},
	}
	for s.i < len(text) {
		if s.str != 0 {
			s.stringToken()
		} else {
			s.codeToken()
		}
	}
	s.attributes()
	return s.sets, s.where
}

type nixFrame struct {
	set    int
	interp bool
	// quote is the string an interpolation returns to: '"' or '\''.
	quote byte
}

type nixScanner struct {
	text  string
	where []int
	sets  []NixSet
	// Each open brace is a set's index, or -1 for an interpolation's
	// `${`, which closes back into its string.
	stack []nixFrame
	i     int
	str   byte // the string being read, or 0 in code
}

func (s *nixScanner) current() int {
	for i := len(s.stack) - 1; i >= 0; i-- {
		if !s.stack[i].interp {
			return s.stack[i].set
		}
	}
	return 0
}

func (s *nixScanner) stringToken() {
	c := s.text[s.i]
	s.where[s.i] = InString
	switch {
	case s.str == '"' && c == '\\' && s.i+1 < len(s.text):
		s.where[s.i+1] = InString
		s.i += 2
		return
	case s.str == '"' && c == '"':
		s.str = 0
	case s.str == '\'' && strings.HasPrefix(s.text[s.i:], "''"):
		if strings.HasPrefix(s.text[s.i:], "'''") || strings.HasPrefix(s.text[s.i:], "''$") || strings.HasPrefix(s.text[s.i:], "''\\") {
			for j := s.i; j < min(s.i+3, len(s.text)); j++ {
				s.where[j] = InString
			}
			s.i += 3
			return
		}
		s.where[s.i+1] = InString
		s.str = 0
		s.i += 2
		return
	case c == '$' && strings.HasPrefix(s.text[s.i:], "${"):
		s.where[s.i+1] = InString
		s.stack = append(s.stack, nixFrame{set: -1, interp: true, quote: s.str})
		s.str = 0
		s.i += 2
		return
	}
	s.i++
}

func (s *nixScanner) codeToken() {
	c := s.text[s.i]
	switch {
	case c == '#':
		for s.i < len(s.text) && s.text[s.i] != '\n' {
			s.where[s.i] = InComment
			s.i++
		}
		return
	case strings.HasPrefix(s.text[s.i:], "/*"):
		end := strings.Index(s.text[s.i+2:], "*/")
		if end < 0 {
			end = len(s.text) - s.i - 2
		}
		for j := s.i; j < min(s.i+end+4, len(s.text)); j++ {
			s.where[j] = InComment
		}
		s.i += end + 4
		return
	case c == '"':
		s.where[s.i] = InString
		s.str = '"'
		s.i++
		return
	case strings.HasPrefix(s.text[s.i:], "''"):
		s.where[s.i], s.where[s.i+1] = InString, InString
		s.str = '\''
		s.i += 2
		return
	case c == '{':
		pre := s.text[max(0, s.i-200):s.i]
		set := NixSet{Start: s.i, End: len(s.text), Parent: s.current(), Attrs: map[string]string{}, At: map[string]int{}}
		if m := nixCallee.FindStringSubmatch(pre); m != nil && m[1] != "rec" {
			set.Call = m[1]
		}
		if m := nixAssign.FindStringSubmatch(pre); m != nil {
			set.Name = m[1]
		}
		s.sets = append(s.sets, set)
		s.stack = append(s.stack, nixFrame{set: len(s.sets) - 1})
		s.where[s.i] = s.current()
		s.i++
		return
	case c == '}':
		s.where[s.i] = s.current()
		if n := len(s.stack); n > 0 {
			top := s.stack[n-1]
			s.stack = s.stack[:n-1]
			if top.interp {
				s.str = top.quote
				s.where[s.i] = InString
			} else {
				s.sets[top.set].End = s.i
			}
		}
		s.i++
		return
	}
	s.where[s.i] = s.current()
	s.i++
}

func (s *nixScanner) attributes() {
	for _, m := range nixString.FindAllStringSubmatchIndex(s.text, -1) {
		at := m[2]
		// The name must be code, and start its token.
		if s.where[at] < 0 || at > 0 && strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_'-.", rune(s.text[at-1])) {
			continue
		}
		set := &s.sets[s.where[at]]
		name := s.text[m[2]:m[3]]
		if _, ok := set.Attrs[name]; !ok {
			set.Attrs[name], set.At[name] = s.text[m[4]:m[5]], m[4]
		}
	}
}

var (
	// nixToken is a word of Nix as nixPinWord reads it: a hyphen joins
	// two words into one, as rev-parse.
	nixToken = regexp.MustCompile(`[\w-]+`)
	// nixWord is a word that may pin a version, in any case: sha256 or
	// sha512 (alone, or opening an SRI hash), ref, getFlake, a word
	// ending in hash or starting with fetch, and rev, url or version.
	nixWord = regexp.MustCompile(`(?i)^(?:sha256|sha512|ref|getflake|\w*hash|fetch\w*|rev|url|version)$|^sha(?:256|512)-`)
	// nixName is a name whose camelCase or snake_case last part is rev,
	// url or version, as srcRev, baseUrl, goVersion or src_rev; prev,
	// which merely ends in rev, is none.
	nixName = regexp.MustCompile(`^\w*[a-z0-9](?:Rev|Url|URL|Version)$|(?i:^\w*_(?:rev|url|version)$)`)
	// nixValue is a value that pins by itself: a commit ID, an SRI or Nix
	// base32 hash, an archive URL, a flake reference to a forge, or an OCI
	// image named with a tag or a digest. An image is a path of at least
	// two parts, so that a host and port is none.
	nixValue = regexp.MustCompile(`(?:^|[^0-9a-fA-F])[0-9a-f]{40}(?:[^0-9a-fA-F]|$)|(?i:sha256|sha512)-[A-Za-z0-9+/]{16,}|` +
		`(?:^|[^0-9a-z])[0-9abcdfghijklmnpqrsvwxyz]{52}(?:[^0-9a-z]|$)|/archive/|(?:github|gitlab|sourcehut):[\w.-]+/[\w.-]+|` +
		`(?:^|[\s"'=])[a-z0-9][a-z0-9._-]*(?:/[a-z0-9][a-z0-9._-]*)+(?::\w[\w.-]*|@sha256:[0-9a-f]{64})(?:$|[\s"';])`)
	// nixArchiveURL is a URL to an archive, read across a line's strings
	// and their interpolations alike.
	nixArchiveURL = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"']*\.(?:tar\.gz|tgz|tar\.xz|tar\.bz2|zip)(?:[^\w.]|$)`)
)

// nixUnpinned are names nixName takes that pin no dependency.
var nixUnpinned = []string{"stateVersion"}

// nixPinWord reports whether s, Nix code or, where code is false, a
// string's text, holds a word that may pin a version (nixWord), or, in
// code, a name that does (nixName).
func nixPinWord(s string, code bool) bool {
	for _, w := range nixToken.FindAllString(s, -1) {
		if nixWord.MatchString(w) || code && nixName.MatchString(w) && !slices.Contains(nixUnpinned, w) {
			return true
		}
	}
	return false
}

// NixPinLines are the lines of a Nix file's text, from 1, that name what
// may pin a version, read as the file has them (NixSets): a word in its
// code (nixPinWord) or in a string's text, a string holding whitespace
// being prose that names none, or any value that pins by itself
// (nixValue), or a URL to an archive (nixArchiveURL) anywhere but a
// comment. Comments name nothing, and a string's ${...} is code. A line
// that binds a name such a line reads, in a ${...} or as the value of a
// pin attribute (nixUses), is one too, and so on until none is added.
func NixPinLines(text string) map[int]bool {
	_, where := NixSets(text)
	prose := nixProse(text, where)
	out := map[int]bool{}
	start := 0
	lines := strings.SplitAfter(text, "\n")
	codes, uncommenteds := make([]string, len(lines)), make([]string, len(lines))
	for n, line := range lines {
		code, uncommented, hit := nixLine(line, start, where, prose)
		if hit {
			out[n+1] = true
		}
		codes[n], uncommenteds[n] = code, uncommented
		start += len(line)
	}
	expandNixPins(codes, uncommenteds, out)
	return out
}

func nixProse(text string, where []int) []bool {
	// Whether each run of a string's text, between its interpolations,
	// holds whitespace.
	prose := make([]bool, len(text))
	for i := 0; i < len(text); {
		if where[i] != InString {
			i++
			continue
		}
		j := i
		for j < len(text) && where[j] == InString {
			j++
		}
		p := strings.ContainsAny(text[i:j], " \t\n")
		for k := i; k < j; k++ {
			prose[k] = p
		}
		i = j
	}
	return prose
}

func nixLine(line string, start int, where []int, prose []bool) (string, string, bool) {
	code, uncommented := []byte(line), []byte(line)
	hit := false
	for i := 0; i < len(line); {
		if where[start+i] >= 0 {
			i++
			continue
		}
		j := i
		for j < len(line) && where[start+j] == where[start+i] {
			code[j] = ' '
			if where[start+i] == InComment {
				uncommented[j] = ' '
			}
			j++
		}
		if s := line[i:j]; where[start+i] == InString && (nixValue.MatchString(s) || !prose[start+i] && nixPinWord(s, false)) {
			hit = true
		}
		i = j
	}
	return string(code), string(uncommented), hit || nixPinWord(string(code), true) || nixValue.MatchString(string(code)) || nixArchiveURL.Match(uncommented)
}

func expandNixPins(codes, uncommenteds []string, out map[int]bool) {
	read := map[string]bool{}
	for grew := true; grew; {
		grew = readNixNames(codes, uncommenteds, out, read)
		for n := range codes {
			if out[n+1] {
				continue
			}
			for _, m := range nixBinding.FindAllStringSubmatch(codes[n], -1) {
				if read[m[1]] {
					out[n+1], grew = true, true
				}
			}
		}
	}
}

func readNixNames(codes, uncommenteds []string, out map[int]bool, read map[string]bool) bool {
	grew := false
	for n := range codes {
		if !out[n+1] {
			continue
		}
		for _, name := range nixUses(codes[n], uncommenteds[n]) {
			if !read[name] {
				read[name], grew = true, true
			}
		}
	}
	return grew
}

var (
	// nixBinding is a name bound on a line of code: `NAME = …`.
	nixBinding = regexp.MustCompile(`(?:^|[\s{;(])([A-Za-z_][\w'-]*)\s*=(?:[^=]|$)`)
	// nixInterpolated is a name a ${...} opens with.
	nixInterpolated = regexp.MustCompile(`\$\{\s*([A-Za-z_][\w'-]*)`)
	// nixPinValueName is a name a pin attribute is set to.
	nixPinValueName = regexp.MustCompile(`(?i)(?:^|[^\w-])(?:ref|rev|url|version|tag|\w*hash|sha256|sha512)\s*=\s*([A-Za-z_][\w'-]*)`)
)

// nixUses are the names a line reads where they may pin: those a ${...}
// in it opens with, from its text but its comment, and those its code
// sets a pin attribute to.
func nixUses(code, uncommented string) []string {
	var out []string
	for _, m := range nixInterpolated.FindAllStringSubmatch(uncommented, -1) {
		out = append(out, m[1])
	}
	for _, m := range nixPinValueName.FindAllStringSubmatch(code, -1) {
		out = append(out, m[1])
	}
	return out
}

// NixChangedLines are ChangedLines of a Nix file, a line keyed by its
// text, whether each of its bytes is code, a string or a comment, and the
// names of the sets it is in: a line moved into or out of a comment or a
// string, or into another set, has changed though its text has not.
func NixChangedLines(old, next string) (removed, added []Line) {
	a, b := nixLineKeys(old), nixLineKeys(next)
	count := func(ks []string) map[string]int {
		m := map[string]int{}
		for _, k := range ks {
			m[k]++
		}
		return m
	}
	ca, cb := count(a), count(b)
	pick := func(ks []string, other map[string]int) []Line {
		var out []Line
		seen := map[string]int{}
		for i, k := range ks {
			seen[k]++
			text, _, _ := strings.Cut(k, "\x00")
			if seen[k] > other[k] && strings.TrimSpace(text) != "" {
				out = append(out, Line{i + 1, text})
			}
		}
		return out
	}
	return pick(a, cb), pick(b, ca)
}

func nixLineKeys(text string) []string {
	sets, where := NixSets(text)
	var out []string
	start, set := 0, 0
	for _, line := range strings.SplitAfter(text, "\n") {
		class := []byte(line)
		first := -1
		for i := range line {
			switch w := where[start+i]; {
			case w == InString:
				class[i] = 's'
			case w == InComment:
				class[i] = 'm'
			default:
				class[i] = 'c'
				if first < 0 {
					first = w
				}
			}
		}
		if first >= 0 {
			set = first
		}
		var path []string
		for s := set; s >= 0; s = sets[s].Parent {
			path = append(path, cmp.Or(sets[s].Name, sets[s].Call))
		}
		out = append(out, strings.TrimSuffix(line, "\n")+"\x00"+string(class)+"\x00"+strings.Join(path, "\x00"))
		start += len(line)
	}
	return out
}

// NixPinned reports whether a change of a Nix file from old to next
// changes a line on either side (NixChangedLines) that NixPinLines takes.
func NixPinned(old, next string) bool {
	removed, added := NixChangedLines(old, next)
	a, b := NixPinLines(old), NixPinLines(next)
	return slices.ContainsFunc(removed, func(l Line) bool { return a[l.N] }) ||
		slices.ContainsFunc(added, func(l Line) bool { return b[l.N] })
}

// FlakeConfigured reports whether a flake.nix, old or next, may set
// nixConfig, the caches, keys and other settings Nix takes from the flake:
// whether either names it anywhere, since an edit anywhere inside it
// changes them, and a name in a comment or a string counts too.
func FlakeConfigured(old, next string) bool {
	return strings.Contains(old, "nixConfig") || strings.Contains(next, "nixConfig")
}
