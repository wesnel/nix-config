package loop

import (
	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Refusal is a failed policy check.
type Refusal = refusal.Refusal

// Commit is a commit as the signing pass compares it.
type Commit = history.Commit

func refuse(format string, args ...any) error { return refusal.New(format, args...) }

// Refusals shared by several commands, as formats for refuse.
const (
	notHead   = "%s is not the head of an open pull request by %s; name a branch that is, or ask Wesley to open one"
	notInLoop = "#%d is not part of this loop; `pr-loop status` lists the loop's pull requests"
)

var (
	commitsBetween = history.Between
	subject        = history.Subject
	fold           = history.Fold
	matches        = history.Match
	couldNotApply  = history.CouldNotApply
	noEditor       = history.NoEditor
	// graphQL is replaced by tests that fake GitHub.
	graphQL = gh.GraphQL
)
