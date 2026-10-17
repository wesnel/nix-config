// Package publictext finds what text bound for GitHub carries over from the
// harness that wrote it: GitHub node IDs and the short keys agents make of
// them, plan IDs, the review loop's vocabulary, local paths and session
// IDs. Each means nothing to a reader who has never seen the harness.
//
// The checks favour missing a leak over refusing a good reply: what the
// caller knows (Context) is matched exactly, and the patterns that guess
// without it are kept narrow, and to prose outside code.
package publictext

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Context is what the caller knows of the harness behind a text.
type Context struct {
	// Harness is set where the text goes to one of the repositories the
	// harness itself lives in, where naming its tools and files is the
	// subject.
	Harness bool
	// Threads are review thread node IDs the author has seen, whose
	// suffixes are the short keys agents make of them.
	Threads []string
	// PlanIDs are the IDs of the author's plan.
	PlanIDs []string
	// Sessions are the harness's session IDs.
	Sessions []string
	// Paths are local directories none of whose paths a reader can open.
	Paths []string
}

// Finding is a token of the text that leaks the harness, and what it is,
// as a phrase.
type Finding struct {
	Token string
	Kind  string
}

const (
	nodeID   = "a GitHub node ID"
	shortKey = "a review thread's short key"
	planID   = "a plan's ID"
	term     = "a term of the review harness"
	path     = "a local path"
	session  = "a session ID"
)

var (
	// A node ID: a type's prefix and base64 of its MessagePack ID, or
	// the legacy base64 of "NNN:Type…".
	nodeIDs = regexp.MustCompile(`\b(?:[A-Z]{1,5}_k[wg]D[A-Za-z0-9_-]{4,}|MD[A-Za-z0-9]{2}O[A-Za-z0-9+/]{8,}={0,2})`)
	// Short keys in brackets, alone or listed, or after "thread".
	bracketKeys = regexp.MustCompile(`[(\[]([A-Za-z0-9]{5}(?:,\s*[A-Za-z0-9]{5})*)[)\]]`)
	threadKey   = regexp.MustCompile(`(?i:\bthreads?)\s+([A-Za-z0-9]{5})\b`)
	// A number and its unit, or a name and its version.
	unitOrVersion = regexp.MustCompile(`^(?:[0-9]+[A-Za-z]+|[A-Za-z]+[0-9]+)$`)
	// Words a plan ID is a whole one of: a run that may hold the
	// characters of paths, URLs, issue numbers and plan IDs.
	words = regexp.MustCompile(`[\w./#-]+`)
	// A decision's or question's ID, or one in a part. One digit is too
	// easily something else ("Q4 earnings").
	planIDs = regexp.MustCompile(`^(?:(?:P[0-9]+\.)?[DQ][0-9]{2,4}|P[0-9]+\.[SQFD][0-9]+)$`)
	// The review loop's words, and where its state lives.
	terms = regexp.MustCompile(`(?i)\bpr-loop\b|\bpr-owners?\b|\b(?:loop|agent)(?:'s)?\s+coordinator\b|` +
		`\bcoordinator(?:'s)?\s+(?:agent|session|sub-?agent)s?\b|\bowner\s+(?:agent|sub-?agent)s?\b|\bsub-?agents?\b|` +
		`\bround\s+[0-9]+\b|\breview\s+receipts?\b|\broute-receipts?\b|\breview\s+routes?\b|\bfresh-review\b|` +
		`\breview-loop\b|\bagent-plan\b`)
	harnessPaths = regexp.MustCompile(`\.git/pr-loop\b|\.pr-loop/|~/\.claude/`)
	// Directories of a session's own, and a note or patch written to
	// $TMPDIR: a program's file there ($TMPDIR/ngrok/ngrok.sock) is a
	// product's path.
	scratchPaths = regexp.MustCompile(`\$\{?TMPDIR\}?/[^\s/]+\.(?:md|txt|json|patch|diff|log|org)\b|/tmp/claude-|/var/folders/|/scratchpad\b`)
	sessionIDs   = regexp.MustCompile(`(?i)\bsession(?:[ _-]?id)?[\s:=]+([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\b|` +
		`\b(session_[A-Za-z0-9]{10,})\b`)
)

// span is a finding at text[start:end].
type span struct {
	start, end int
	kind       string
}

// Check lists the tokens of text that leak the harness, in the order they
// appear, each once.
func Check(text string, ctx Context) []Finding {
	var spans []span
	add := func(start, end int, kind string) { spans = append(spans, span{start, end, kind}) }
	addAll := func(re *regexp.Regexp, in string, kind string, group int) {
		for _, m := range re.FindAllStringSubmatchIndex(in, -1) {
			for g := group; g < len(m)/2; g++ {
				if m[2*g] >= 0 {
					add(m[2*g], m[2*g+1], kind)
					break
				}
			}
		}
	}
	addAll(nodeIDs, text, nodeID, 0)
	addAll(sessionIDs, text, session, 1)
	for _, id := range ctx.Sessions {
		for _, at := range exact(text, id, false) {
			add(at, at+len(id), session)
		}
	}
	for _, id := range ctx.Threads {
		_, key, _ := strings.Cut(id, "_")
		for n := 5; n <= 8 && n < len(key); n++ {
			suffix := key[len(key)-n:]
			for _, at := range exact(text, suffix, true) {
				add(at, at+n, shortKey)
			}
		}
	}
	for _, m := range scratchPaths.FindAllStringIndex(text, -1) {
		start, end := pathAround(text, m[0], m[1])
		add(start, end, path)
	}
	for _, dir := range ctx.Paths {
		if len(dir) < 2 {
			continue
		}
		for _, at := range exact(text, dir, false) {
			start, end := pathAround(text, at, at+len(dir))
			add(start, end, path)
		}
	}
	if !ctx.Harness {
		addAll(terms, text, term, 0)
		for _, m := range harnessPaths.FindAllStringIndex(text, -1) {
			start, end := pathAround(text, m[0], m[1])
			add(start, end, path)
		}
	}

	// The guesses read prose alone: code names what it likes.
	prose := withoutCode(text)
	for _, re := range []*regexp.Regexp{bracketKeys, threadKey} {
		for _, m := range re.FindAllStringSubmatchIndex(prose, -1) {
			at := m[2]
			for _, key := range strings.Split(prose[m[2]:m[3]], ",") {
				lead := len(key) - len(strings.TrimLeft(key, " \t\n"))
				key = strings.TrimSpace(key)
				if keyLike(key) {
					add(at+lead, at+lead+len(key), shortKey)
				}
				at += len(key) + lead + 1
			}
		}
	}
	for _, m := range words.FindAllStringIndex(prose, -1) {
		word := strings.TrimRight(prose[m[0]:m[1]], ".-/")
		if planIDs.MatchString(word) || ownID(word, ctx.PlanIDs) {
			add(m[0], m[0]+len(word), planID)
		}
	}
	return findings(text, spans)
}

// findings are the tokens spans name, in order, each once, dropping a span
// inside another.
func findings(text string, spans []span) []Finding {
	slices.SortFunc(spans, func(a, b span) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end
	})
	var out []Finding
	seen := map[string]bool{}
	end := -1
	for _, s := range spans {
		if s.end <= end {
			continue
		}
		end = s.end
		token := text[s.start:s.end]
		if !seen[token] {
			seen[token] = true
			out = append(out, Finding{Token: token, Kind: s.kind})
		}
	}
	return out
}

// exact is where text holds s, which with whole set is not part of a
// longer run of letters and digits.
func exact(text, s string, whole bool) []int {
	var out []int
	for from := 0; ; {
		i := strings.Index(text[from:], s)
		if i < 0 {
			return out
		}
		at := from + i
		from = at + 1
		if whole && (at > 0 && alnum(text[at-1]) || at+len(s) < len(text) && alnum(text[at+len(s)])) {
			continue
		}
		out = append(out, at)
	}
}

func alnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// pathAround widens text[start:end] to the path it is part of, without
// the punctuation that closes a sentence.
func pathAround(text string, start, end int) (int, int) {
	stop := func(b byte) bool { return b <= ' ' || strings.IndexByte("<>\"'`()[]{},;", b) >= 0 }
	for start > 0 && !stop(text[start-1]) {
		start--
	}
	for end < len(text) && !stop(text[end]) {
		end++
	}
	for end > start && strings.IndexByte(".:!?", text[end-1]) >= 0 {
		end--
	}
	return start, end
}

// keyLike reports whether s looks like the tail of base64 rather than a
// word: both cases, and a digit or case changing back and forth, but not a
// number and its unit (64GiB) or a name and its version (iOS17).
func keyLike(s string) bool {
	if unitOrVersion.MatchString(s) {
		return false
	}
	upper, lower, digit, changes := false, false, false, 0
	for i, r := range s {
		upper = upper || unicode.IsUpper(r)
		lower = lower || unicode.IsLower(r)
		digit = digit || unicode.IsDigit(r)
		if i > 0 && unicode.IsLetter(r) && unicode.IsLetter(rune(s[i-1])) && unicode.IsUpper(r) != unicode.IsUpper(rune(s[i-1])) {
			changes++
		}
	}
	return upper && lower && (digit || changes >= 3)
}

// ownID reports whether word is one of ids that is not too easily
// something else: a decision's or question's, one in a part, or one of two
// digits or more. "S3" or "F1" alone is more often a bucket or a key.
func ownID(word string, ids []string) bool {
	if !slices.Contains(ids, word) {
		return false
	}
	digits := strings.TrimLeft(word, "ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	return strings.Contains(word, ".") || word[0] == 'D' || word[0] == 'Q' || len(digits) >= 2
}

// withoutCode is text with its code blocks and spans blanked, keeping
// every other byte where it was.
func withoutCode(text string) string {
	b := []byte(text)
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if b[i] != '\n' {
				b[i] = ' '
			}
		}
	}
	// Fenced blocks.
	fence, at := "", 0
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		switch {
		case fence == "" && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")):
			fence = trimmed[:3]
			blank(at, at+len(line))
		case fence != "":
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			blank(at, at+len(line))
		}
		at += len(line)
	}
	// Spans: a run of backticks to the next run of as many.
	for i := 0; i < len(b); {
		if b[i] != '`' {
			i++
			continue
		}
		n := 0
		for i+n < len(b) && b[i+n] == '`' {
			n++
		}
		close := -1
		for j := i + n; j < len(b); {
			if b[j] != '`' {
				j++
				continue
			}
			m := 0
			for j+m < len(b) && b[j+m] == '`' {
				m++
			}
			if m == n {
				close = j
				break
			}
			j += m
		}
		if close < 0 {
			i += n
			continue
		}
		blank(i, close+n)
		i = close + n
	}
	return string(b)
}

// Names lists findings as `"token" (kind)`, joined as prose.
func Names(fs []Finding) string {
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = fmt.Sprintf("%q (%s)", f.Token, f.Kind)
	}
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// advice is how to write for a reader who has never seen the harness.
const advice = "link a review thread by its URL, a commit by its short SHA and a ticket by its key, and leave out plan IDs, " +
	"the tools behind the work and local paths"

// Problem is fs as a phrase for a list of what is wrong with a text, or ""
// for none.
func Problem(fs []Finding) string {
	if len(fs) == 0 {
		return ""
	}
	return "it names " + Names(fs) + ", which mean nothing to a reader who has never seen the harness that wrote it (" +
		advice + ")"
}

// Refusal is why what, a text bound for GitHub, may not go out as it is,
// ending with then, what to do once it is rewritten; "" when fs is empty.
func Refusal(what string, fs []Finding, then string) string {
	if len(fs) == 0 {
		return ""
	}
	return what + " names " + Names(fs) + ", which mean nothing to a reader who has never seen the harness that wrote it. " +
		"Rewrite it for that reader (" + advice + "), then " + then + "."
}
