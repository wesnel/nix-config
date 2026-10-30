package guard

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// excludedNotAlone is why a command line is refused that names a command
// Claude Code's sandbox excludes anywhere but as the whole line, with %s
// the command.
const excludedNotAlone = "`%s` is sure to run outside the sandbox only as a command line of its own: piped, chained, " +
	"redirected, after cd or an assignment, or inside another command, it may run sandboxed, where it fails as if " +
	"it were missing. " +
	"Run it bare, as a command of its own in a call of its own, with absolute paths and its own arguments " +
	"in place of cd: `-R OWNER/NAME`, a PR number or branch, or the range, which `git delegate` resolves from " +
	"any worktree of the repository."

// excludedSandboxed is why a line naming an excluded command is refused
// for holding sandboxedSyntax, with %s the command and then the syntax
// found.
const excludedSandboxed = "`%s` runs outside the sandbox only when Claude Code reads its line as that command alone, " +
	"and it reads a line holding %s as more, even inside quotes, so the line may run sandboxed, where it fails as if " +
	"it were missing. Where the value has a file form, as agent-plan's `--text-file FILE` takes the place of --text " +
	"and --context, put it in a file under $TMPDIR and pass that; otherwise word the value without that syntax."

// sandboxedSyntax is the syntax Claude Code was seen to read as more than
// one command even inside quotes, which sends an excluded command alone on
// its line into the sandbox. It is what was seen, not documented, and a
// Claude Code upgrade may change it.
var sandboxedSyntax = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"a backtick", regexp.MustCompile("`")},
	{"$(", regexp.MustCompile(`\$\(`)},
	{"<(", regexp.MustCompile(`<\(`)},
	{">(", regexp.MustCompile(`>\(`)},
	// After a space only, and not ==: a value starting with =, (=x=) and
	// a == b were seen running outside.
	{"a word starting with =", regexp.MustCompile(`\s=[^\s=]`)},
}

// sandboxedBy is the name of each sandboxedSyntax text holds.
func sandboxedBy(text string) []string {
	var found []string
	for _, s := range sandboxedSyntax {
		if s.pattern.MatchString(text) {
			found = append(found, s.name)
		}
	}
	return found
}

// checkExcludedAlone refuses a Claude Code command line that names one of
// the commands its sandbox excludes (excludedCommands in the guard's
// settings, which the Claude Code module sets to its sandbox's list)
// anywhere but as the whole line. Claude Code matches an exclusion against
// the text of the line: a pattern `NAME SUB *` takes a line that starts
// with those words and holds one command, with no redirection. Some other
// lines run outside the sandbox too, but not every one, and one that runs
// in it fails; the command alone always runs outside it, so every other
// line is refused, and so is the command alone holding sandboxedSyntax,
// whose refusal names the syntax wherever the line holds it.
func checkExcludedAlone(text string) string {
	patterns := excludedPatterns()
	if len(patterns) == 0 {
		return ""
	}
	named := firstCall(text, 0, func(c call) []string { return excludedCall(c, patterns) })
	if named == nil {
		return ""
	}
	if found := sandboxedBy(text); len(found) > 0 {
		return fmt.Sprintf(excludedSandboxed, strings.Join(named, " "), strings.Join(found, ", "))
	}
	if ExcludedAlone(text, named) {
		return ""
	}
	return fmt.Sprintf(excludedNotAlone, strings.Join(named, " "))
}

// excludedPatterns are the words of each excludedCommands pattern, its
// trailing ` *` left off.
func excludedPatterns() [][]string {
	var patterns [][]string
	for _, p := range loadSettings().ExcludedCommands {
		if words := strings.Fields(strings.TrimSuffix(p, " *")); len(words) > 0 {
			patterns = append(patterns, words)
		}
	}
	return patterns
}

// plainGh is why a Claude Code command line running one of gh's reads is
// refused where the sandbox excludes gh-read: gh there cannot read its
// login, and fails with an error that reads as if there were no gh at all.
const plainGh = "`gh` runs in the sandbox, which hides its login, so it cannot reach GitHub from here. " +
	"Run `gh-read` with the same arguments, as a command of its own; it runs gh's reads of pull requests, runs " +
	"and repositories, and says which when it refuses one. Make changes on GitHub with `pr-loop`, or put them in " +
	"your report for the operator."

// checkPlainGh refuses a Claude Code command line that runs one of gh's
// reads (ghReadOnly), where the sandbox excludes gh-read. It runs after
// the other rules, so a gh write meets its own refusal first.
func checkPlainGh(text string) string {
	if !slices.ContainsFunc(excludedPatterns(), func(p []string) bool { return slices.Equal(p, []string{"gh-read"}) }) {
		return ""
	}
	gh := firstCall(text, 0, func(c call) []string {
		if len(c.words) < 3 || c.expands[0] || base(c.words[0]) != "gh" || !ghReadOnly[c.words[1]+" "+c.words[2]] {
			return nil
		}
		return c.words[:1]
	})
	if gh == nil {
		return ""
	}
	return plainGh
}

// firstCall is what match says of the first command in text it names,
// once wrappers, assignments and shells are taken off, or nil.
func firstCall(text string, depth int, match func(c call) []string) []string {
	if depth > maxDepth {
		return nil
	}
	var named []string
	var v visitor
	v = visitor{
		call: func(c call) string {
			// A redirection before the program, as `< /dev/null CMD`.
			if i := leadingRedirections(c.words); i > 0 && i < len(c.words) {
				return unwrap(call{words: c.words[i:], expands: c.expands[i:], env: c.env, fed: c.fed}, v)
			}
			if named = match(c); named != nil {
				return "named"
			}
			return ""
		},
		script: func(script string) string {
			if named = firstCall(script, depth+1, match); named != nil {
				return "named"
			}
			return ""
		},
	}
	for _, segment := range segments(text) {
		if words, expands := split(segment); len(words) > 0 && unwrap(call{words: words, expands: expands}, v) != "" {
			return named
		}
	}
	return nil
}

// leadingRedirections is how many of words, from the first, are
// redirections and the targets of those whose target is the next word.
func leadingRedirections(words []string) int {
	i := 0
	for i < len(words) {
		_, _, n := redirection(words, i)
		if n == 0 {
			return i
		}
		i += n
	}
	return i
}

// excludedCall is the pattern whose words c's program and first arguments
// are, with a path's directory and git's own options before its command
// left out, or nil. A program named in $'…' or $"…" quotes, which split
// leaves as $NAME, counts as that name.
func excludedCall(c call, patterns [][]string) []string {
	if len(c.words) == 0 {
		return nil
	}
	program := c.words[0]
	if c.expands[0] {
		name, quoted := strings.CutPrefix(program, "$")
		if !quoted || strings.ContainsAny(name, "$`") {
			return nil
		}
		program = name
	}
	words := append([]string{base(program)}, c.words[1:]...)
	if words[0] == "git" {
		i := 1
		for i < len(words) && strings.HasPrefix(words[i], "-") {
			if gitValueFlags[words[i]] {
				i++
			}
			i++
		}
		words = append([]string{"git"}, words[min(i, len(words)):]...)
	}
	for _, p := range patterns {
		if len(words) >= len(p) && slices.Equal(words[:len(p)], p) {
			return p
		}
	}
	return nil
}

// ExcludedAlone reports whether text is pattern's command alone, as
// Claude Code matches an exclusion against the line's text: one command,
// the line's text starting with the pattern's words, unquoted and one
// space apart, with no redirection. It does not look for sandboxedSyntax,
// which checkExcludedAlone refuses: agent-plan's quota stop lets such an
// agent-plan line through, to the guard's refusal saying what to change.
func ExcludedAlone(text string, pattern []string) bool {
	texts, _, ends := backgroundSegments(text)
	var only string
	commands := 0
	for i, segment := range texts {
		if words, _ := split(segment); len(words) > 0 {
			commands++
			only = segment
			if ends[i] != "" {
				return false
			}
		}
	}
	if commands != 1 || unquotedRedirect(only) {
		return false
	}
	line, prefix := strings.TrimLeft(text, " \t"), strings.Join(pattern, " ")
	return line == prefix || strings.HasPrefix(line, prefix+" ")
}

// unquotedRedirect reports whether a simple command holds a < or > outside
// quotes and before any comment, which the shell reads as a redirection.
// $'…' quotes take backslash escapes, and a # inside ${…} starts no
// comment; a < or > inside ${…} counts, which errs on refusing.
func unquotedRedirect(segment string) bool {
	quote := byte(0) // ', ", or a for $'…'
	braces := 0      // ${ … } open around this point
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		next := byte(0)
		if i+1 < len(segment) {
			next = segment[i+1]
		}
		switch {
		case quote == 'a':
			if c == '\\' {
				i++
			} else if c == '\'' {
				quote = 0
			}
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' {
				i++
			}
		case c == '\\':
			i++
		case c == '$' && next == '\'':
			quote = 'a'
			i++
		case c == '$' && next == '{':
			braces++
			i++
		case c == '}' && braces > 0:
			braces--
		case c == '#' && braces == 0 && (i == 0 || segment[i-1] == ' ' || segment[i-1] == '\t'):
			return false
		case c == '\'' || c == '"':
			quote = c
		case c == '<' || c == '>':
			return true
		}
	}
	return false
}
