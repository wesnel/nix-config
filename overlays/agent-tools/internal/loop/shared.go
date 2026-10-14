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
