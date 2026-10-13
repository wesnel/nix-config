// Package history reads commits the way the signing passes compare them.
package history

import (
	"regexp"
	"strings"

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
	out, err := git.Run("log", "--reverse", "--format=%B%x00%an <%ae>%x01", base+".."+tip)
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

// FixupTarget is the subject a "fixup! ..." commit folds into, or "" for any
// other commit. Nested fixups fold into the same commit.
func FixupTarget(message string) string {
	s := Subject(message)
	if !strings.HasPrefix(s, "fixup! ") {
		return ""
	}
	for strings.HasPrefix(s, "fixup! ") {
		s = strings.TrimPrefix(s, "fixup! ")
	}
	return s
}

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
