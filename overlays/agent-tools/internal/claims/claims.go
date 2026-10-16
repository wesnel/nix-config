// Package claims reads the verification claims of a pull request's
// description or a sub-agent's report: what the author says was run and
// seen, beyond what CI shows. `git fresh-review audit` judges them against
// the evidence log, and the gates on opening or editing a pull request
// look for its receipt by their hash.
package claims

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// Heading is the heading of a description's verification claims, which
// the pull-requests skill names.
const Heading = "Verification beyond CI"

var (
	heading = regexp.MustCompile(`^(#{1,6})[ \t]+(.*?)[ \t]*#*[ \t]*$`)
	fence   = regexp.MustCompile("^[ \t]{0,3}(```|~~~)")
	// item is a top-level list item's marker, with the space after it.
	item = regexp.MustCompile(`^(?:[-*+]|[0-9]{1,9}[.)])[ \t]+`)
	// comment is an HTML comment, as a template leaves under a heading.
	comment = regexp.MustCompile(`(?s)<!--.*?-->`)
)

// placeholders are what a section says when it has no claims, once
// emphasis, code marks and a full stop are off and in lower case.
var placeholders = map[string]bool{"none": true, "n/a": true, "na": true, "nothing": true, "-": true, "—": true}

// Section is the text under text's Heading, of any level and in any case,
// up to the next heading of its level or above outside a code fence, and
// whether text has the heading.
func Section(text string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	level, start := 0, -1
	inFence := false
	for i, line := range lines {
		if fence.MatchString(line) {
			inFence = !inFence
			continue
		}
		m := heading.FindStringSubmatch(line)
		if inFence || m == nil {
			continue
		}
		switch {
		case start < 0 && strings.EqualFold(m[2], Heading):
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

// Parse is the claims of text: each top-level list item, with the lines
// that continue it, less its marker and the indent its content takes, and
// each other paragraph or fenced block. A heading, an HTML comment and a
// placeholder such as "None." are none.
func Parse(text string) []string {
	var out, cur []string
	indent := 0 // the content indent of the item being read, 0 outside one
	inFence, blank := false, false
	flush := func() {
		for len(cur) > 0 && strings.TrimSpace(cur[len(cur)-1]) == "" {
			cur = cur[:len(cur)-1]
		}
		if c := strings.Join(cur, "\n"); len(cur) > 0 && !placeholders[strings.ToLower(strings.Trim(c, "_*`. \t"))] {
			out = append(out, c)
		}
		cur, indent = nil, 0
	}
	text = comment.ReplaceAllString(strings.ReplaceAll(text, "\r\n", "\n"), "")
	for _, line := range strings.Split(text, "\n") {
		lead := len(line) - len(strings.TrimLeft(line, " \t"))
		switch {
		case inFence:
			cur = append(cur, dedent(line, indent))
			inFence = !fence.MatchString(strings.TrimLeft(line, " \t"))
			continue
		case strings.TrimSpace(line) == "":
			if indent == 0 {
				flush()
			} else {
				cur = append(cur, "")
			}
			blank = true
			continue
		case lead == 0 && heading.MatchString(line):
			flush()
		case lead == 0 && item.MatchString(line):
			flush()
			marker := item.FindString(line)
			cur, indent = []string{line[len(marker):]}, len(marker)
		case indent > 0 && lead >= indent:
			cur = append(cur, dedent(line, indent))
		case indent > 0 && !blank:
			// A lazy continuation of the item's paragraph.
			cur = append(cur, strings.TrimLeft(line, " \t"))
		default:
			if indent > 0 || blank {
				flush()
			}
			cur = append(cur, line)
		}
		inFence = fence.MatchString(strings.TrimLeft(line, " \t")) && !inFence
		blank = false
	}
	flush()
	return out
}

// dedent is line less up to n columns of leading spaces.
func dedent(line string, n int) string {
	for i := 0; i < n && strings.HasPrefix(line, " "); i++ {
		line = line[1:]
	}
	return line
}

// Of is the claims an audit of text takes: those of its Heading's section,
// where it has one, else all of it, as a sub-agent's report gives them.
func Of(text string) []string {
	if section, ok := Section(text); ok {
		return Parse(section)
	}
	return Parse(text)
}

// Gate refuses a pull request description, body, for the change at tip in
// the repository at top, whose Heading section holds claims that no audit
// on this host holds for that change with every claim supported
// (plans.Config.ClaimsAudited); in a review loop, whose calls are in no
// evidence log here, it refuses the section whatever audits there are. A
// description without claims under the heading passes.
func Gate(top, tip, body string, loop bool) error {
	section, ok := Section(body)
	if !ok {
		return nil
	}
	list := Parse(section)
	if len(list) == 0 {
		return nil
	}
	if loop {
		return refusal.New("the description's `## %s` makes claims an audit must back, and a review loop's commands are in "+
			"no evidence log on this host. Leave the section out, and put the claims in your report for the operator", Heading)
	}
	if err := git.CheckConfig(top); err != nil {
		return err
	}
	c, err := plans.Load()
	if err != nil {
		return err
	}
	audited, unsupported, err := c.ClaimsAudited(top, tip, Hash(list))
	if err != nil || audited {
		return err
	}
	base := strings.TrimPrefix(strings.TrimPrefix(routes.DefaultRef(top), "refs/remotes/"), "refs/heads/")
	if base == "" {
		base = "BASE"
	}
	why := "no audit on this host holds them, as they stand, for the change as it stands"
	if len(unsupported) > 0 {
		why = "their audit left " + strings.Join(unsupported, ", ") + " unsupported"
	}
	return refusal.New("the description's `## %s` claims are not backed: %s. Run `cd WT && git fresh-review audit "+
		"--claims FILE %s..HEAD`, WT being the worktree of the pull request's branch and FILE the description, and, where "+
		"another session's calls back them (one before /clear), `--session ID` for each session, this one too; take out "+
		"each claim it leaves unsupported, or run what shows it and audit again, then retry", Heading, why, base)
}

// Hash is the hash of claims, in order, each with its whitespace
// collapsed, so that rewrapping a description keeps it and changing what a
// claim says does not.
func Hash(claims []string) string {
	h := sha256.New()
	for _, c := range claims {
		h.Write([]byte(strings.Join(strings.Fields(c), " ")))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
