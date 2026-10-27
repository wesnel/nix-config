package guard

import (
	"fmt"
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

// checkExcludedAlone refuses a Claude Code command line that names one of
// the commands its sandbox excludes (excludedCommands in the guard's
// settings, which the Claude Code module sets to its sandbox's list)
// anywhere but as the whole line. Claude Code matches an exclusion against
// the text of the line: a pattern `NAME SUB *` takes a line that starts
// with those words and holds one command, with no redirection. Some other
// lines run outside the sandbox too, but not every one, and one that runs
// in it fails; the command alone always runs outside it, so every other
// line is refused.
func checkExcludedAlone(text string) string {
	patterns := excludedPatterns()
	if len(patterns) == 0 {
		return ""
	}
	named := firstCall(text, 0, func(c call) []string { return excludedCall(c, patterns) })
	if named == nil || ExcludedAlone(text, named) {
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
			if named = match(c); named != nil {
				return "named"
			}
			return ""
		},
		script: func(script string, _ environment) string {
			if named = firstCall(script, depth+1, match); named != nil {
				return "named"
			}
			return ""
		},
	}
	for _, segment := range segments(text) {
		if c := split(segment); len(c.words) > 0 && unwrap(c, v) != "" {
			return named
		}
	}
	return nil
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
// space apart, with no redirection. agent-plan's quota stop lets such an
// agent-plan line through.
func ExcludedAlone(text string, pattern []string) bool {
	texts, _, ends := backgroundSegments(text)
	var only string
	commands := 0
	for i, segment := range texts {
		if c := split(segment); len(c.words) > 0 || len(c.redirects) > 0 {
			commands++
			only = segment
			if ends[i] != "" {
				return false
			}
		}
	}
	if commands != 1 || len(split(only).redirects) > 0 {
		return false
	}
	line, prefix := strings.TrimLeft(text, " \t"), strings.Join(pattern, " ")
	return line == prefix || strings.HasPrefix(line, prefix+" ")
}
