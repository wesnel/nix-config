// Package history reads commits the way the signing passes compare them.
package history

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Commit is a commit as a signing pass compares it: message and author,
// since rebasing changes everything else.
type Commit struct {
	Message string `json:"message"`
	Author  string `json:"author"`
}

// CouldNotApply finds the commit a stopped rebase names.
var CouldNotApply = regexp.MustCompile(`could not apply ([0-9a-f]+)\.\.\. (.*)`)

// Between lists base..tip oldest first.
func Between(base, tip string) ([]Commit, error) {
	return Log(base + ".." + tip)
}

// Log lists the commits git log selects with revs, oldest first.
func Log(revs ...string) ([]Commit, error) {
	out, err := git.Run(append([]string{"log", "--reverse", "--format=%B%x00%an <%ae>%x01"}, revs...)...)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, entry := range strings.Split(out, "\x01") {
		entry = strings.TrimLeft(entry, "\n")
		if entry == "" {
			continue
		}
		message, author, _ := strings.Cut(entry, "\x00")
		commits = append(commits, Commit{Message: strings.TrimSpace(message), Author: strings.TrimSpace(author)})
	}
	return commits, nil
}

// Subject is a message's first line.
func Subject(message string) string {
	s, _, _ := strings.Cut(message, "\n")
	return s
}

// Kind is what `git rebase --autosquash` does with a commit.
type Kind int

const (
	Kept   Kind = iota // stays a commit of its own
	Fixup              // "fixup! ": its changes fold in and its message is dropped
	Amend              // "amend! " (--fixup=amend: or reword:): its body replaces the target's message
	Squash             // "squash! ": its body is appended to the target's message
)

var foldPrefixes = []struct {
	prefix string
	kind   Kind
}{{"fixup! ", Fixup}, {"amend! ", Amend}, {"squash! ", Squash}}

// Fold is how --autosquash treats a commit, and the subject of the commit it
// folds into ("" when Kept). git strips nested prefixes of every kind to find
// the target; the outermost one decides the kind.
func Fold(message string) (Kind, string) {
	s, kind := Subject(message), Kept
	for stripped := true; stripped; {
		stripped = false
		for _, p := range foldPrefixes {
			if rest, ok := strings.CutPrefix(s, p.prefix); ok {
				if kind == Kept {
					kind = p.kind
				}
				s, stripped = rest, true
				break
			}
		}
	}
	if kind == Kept {
		return Kept, ""
	}
	return kind, s
}

// Squashed predicts the commits that remain after `git rebase -i --autosquash`
// with GIT_EDITOR=: over commits (oldest first), with the messages git leaves
// them. A folded commit joins the oldest earlier kept commit with the subject
// it names, and that commit keeps its author. An amend! replaces the message
// with its own body unless a squash! has already joined it; a squash!, or an
// amend! after one, appends its body. Its subject line is dropped either way.
// A fold with no target stays a commit of its own, as in git.
func Squashed(commits []Commit) []Commit {
	var kept []Commit
	var subjects []string
	joined := map[int]bool{}
	for _, c := range commits {
		kind, target := Fold(c.Message)
		at := -1
		if kind != Kept {
			for i, s := range subjects {
				if s == target {
					at = i
					break
				}
			}
		}
		body := strings.TrimSpace(strings.TrimPrefix(c.Message, Subject(c.Message)))
		switch {
		case at < 0:
			kept = append(kept, c)
			subjects = append(subjects, Subject(c.Message))
		case kind == Fixup:
		case kind == Amend && !joined[at]:
			kept[at].Message = body
		default:
			joined[at] = joined[at] || kind == Squash
			if body != "" {
				kept[at].Message += "\n\n" + body
			}
		}
	}
	return kept
}

// MaxSubject is the longest subject a message may have, in characters,
// counting any `type(scope):` prefix.
const MaxSubject = 72

var (
	// attribution matches a line that credits a tool or links its session.
	attribution = regexp.MustCompile(`(?i)^\s*co-authored-by:|generated (with|by) \[?(claude|codex|eca|chatgpt)|https?://(claude\.ai/code|chatgpt\.com/codex)`)
	trailer     = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]*):\s`)
)

// LintMessage lists what is wrong with a commit message, each as a phrase
// naming the problem; nil when nothing is. A kept commit's subject is at most
// MaxSubject characters, without a trailing period. No line credits a tool
// or links a session. A fixup! carries only Review-* trailers. A fold's
// subject is not a subject the history keeps, so a caller that can lints
// the messages Squashed leaves.
func LintMessage(message string) []string {
	var problems []string
	subject := Subject(message)
	kind, _ := Fold(message)
	if kind == Kept {
		if n := utf8.RuneCountInString(subject); n > MaxSubject {
			problems = append(problems, fmt.Sprintf("its subject is %d characters, over %d", n, MaxSubject))
		}
		if strings.HasSuffix(subject, ".") {
			problems = append(problems, "its subject ends with a period")
		}
	}
	body := strings.TrimSpace(strings.TrimPrefix(message, subject))
	for _, line := range strings.Split(body, "\n") {
		if attribution.MatchString(line) {
			problems = append(problems, fmt.Sprintf("the line %q is attribution", strings.TrimSpace(line)))
		}
	}
	if kind == Fixup {
		paragraphs := strings.Split(body, "\n\n")
		for _, line := range strings.Split(paragraphs[len(paragraphs)-1], "\n") {
			if m := trailer.FindStringSubmatch(line); m != nil && !strings.HasPrefix(m[1], "Review-") && !attribution.MatchString(line) {
				problems = append(problems, fmt.Sprintf("the trailer %q is not a Review-* trailer", m[1]))
			}
		}
	}
	return problems
}

// NoEditor keeps a rebase from opening an editor: for the todo list, and for
// the combined message of a squash!, which git then cleans up as Squashed
// predicts. Both variables override the matching config.
var NoEditor = []string{"GIT_SEQUENCE_EDITOR=:", "GIT_EDITOR=:"}

// Match refuses unless base..tip holds exactly the wanted commits.
func Match(want []Commit, base, tip, branch string) error {
	got, err := Between(base, tip)
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return refusal.New("%s would end with %d commits, not its original %d", branch, len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			return refusal.New("%s's commit %q would become %q", branch, Subject(want[i].Message), Subject(got[i].Message))
		}
	}
	return nil
}
