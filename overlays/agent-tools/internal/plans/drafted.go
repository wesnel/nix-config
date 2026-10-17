package plans

import (
	"os"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// draftedKind is the kind of the event that records which model family's
// session drew a diagram, which its critique's critic must not be.
const draftedKind = "diagram-drafted"

// HarnessFamily is the model family of the harness running this process,
// as its own environment marks it: codex in a Codex session or its
// sandbox, claude in a Claude Code session, or "" where neither is marked,
// as in ECA, which runs models of either.
func HarnessFamily() string {
	switch {
	case os.Getenv("CODEX_THREAD_ID") != "" || os.Getenv("CODEX_SANDBOX") != "":
		return "codex"
	case os.Getenv("CLAUDE_CODE_SESSION_ID") != "":
		return "claude"
	}
	return ""
}

// RecordDrafted records this process's harness, by HarnessFamily, as the
// drafter of the diagram with hash, unless one is recorded already, and
// returns the drafter recorded.
func (p Plan) RecordDrafted(hash string, now time.Time) (string, error) {
	family := HarnessFamily()
	if family == "" {
		if drafter := drafterOf(p, hash); drafter != "" {
			return drafter, nil
		}
		return "", refusal.New("a diagram's drafter is the harness of the session that checks it first, and this " +
			"session's environment marks neither Claude Code nor Codex; run `agent-plan diagram check` in the session " +
			"that drew it, or hand the diagram to the operator")
	}
	return p.RecordDrafter(hash, family, EnvSession(), now)
}

// RecordDrafter records family, in session, as the drafter of the diagram
// with hash, unless one is recorded already, and returns the drafter
// recorded.
func (p Plan) RecordDrafter(hash, family, session string, now time.Time) (string, error) {
	if drafter := drafterOf(p, hash); drafter != "" {
		return drafter, nil
	}
	if _, err := p.Record("", Event{Kind: draftedKind, Diagram: hash, Drafter: family, Session: session,
		Summary: "diagram drawn by " + family}, now); err != nil {
		return "", err
	}
	return family, nil
}

// drafterOf is the drafter p's log records for the diagram with hash, or
// "".
func drafterOf(p Plan, hash string) string {
	events, err := p.Events()
	if err != nil {
		return ""
	}
	for _, e := range events {
		if e.Kind == draftedKind && e.Diagram == hash {
			return e.Drafter
		}
	}
	return ""
}

// DrafterOf is the drafter the plan LocateCheckout finds for session and
// the worktree at top records for the diagram with hash, or "".
func DrafterOf(session, top, hash string) string {
	c, err := Load()
	if err != nil {
		return ""
	}
	p, _, ok := c.LocateCheckout(session, top)
	if !ok {
		return ""
	}
	return drafterOf(p, hash)
}
