package loop

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
)

// A proposal is recorded in the loop's state for report.md, and announced
// in one marker line that `pr-loop watch --events proposal` passes on.
func TestPropose(t *testing.T) {
	// proposeRepo is reviewRepo with a loop over feat, PR 1, whose branch
	// holds "Add a" and a fixup of it; it returns the fixup.
	proposeRepo := func(t *testing.T) string {
		t.Helper()
		reviewRepo(t, `{"bots": {"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}}}}`)
		os.WriteFile(filepath.Join(git.Dir, "a"), []byte("a\n"), 0o644)
		gitIn(t, git.Dir, "add", "a")
		gitIn(t, git.Dir, "commit", "-qm", "Add a")
		os.WriteFile(filepath.Join(git.Dir, "a"), []byte("b\n"), 0o644)
		gitIn(t, git.Dir, "commit", "-qam", "fixup! Add a")
		state := &State{Repo: "o/r", Order: []string{"feat"}, Branches: map[string]*Branch{"feat": {PR: 1, Base: "main",
			Commits: []Commit{{Message: "Add a\n", Author: "T <t@e>"}}}}, Round: 2}
		if err := state.save(); err != nil {
			t.Fatal(err)
		}
		return gitIn(t, git.Dir, "rev-parse", "HEAD")
	}

	t.Run("recorded and announced", func(t *testing.T) {
		fixup := proposeRepo(t)
		var marker bytes.Buffer
		p, err := Propose("follow-up", 1, "Symlinked ~/.local predates\nthe PR", []string{"T1", "T2"}, []string{fixup[:12]}, 310, &marker)
		if err != nil {
			t.Fatal(err)
		}
		if p.ID != 1 || p.Kind != "follow-up" || p.PR != 1 || p.Round != 2 || len(p.Fixups) != 1 || p.Fixups[0].Oid != fixup ||
			p.Fixups[0].Subject != "fixup! Add a" || p.Size != 310 {
			t.Errorf("proposal %+v", p)
		}
		want := "pr-loop: proposal 1 for #1: follow-up: Symlinked ~/.local predates the PR (threads T1 T2; fixups " + fixup[:12] +
			"; about 310 changed lines)\n"
		if marker.String() != want {
			t.Errorf("marker %q, want %q", marker.String(), want)
		}
		if _, err := Propose("defer", 1, "Another", nil, nil, 0, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		state, _ := loadState(true)
		if len(state.Proposals) != 2 || state.Proposals[1].ID != 2 || state.Proposals[0].Summary != "Symlinked ~/.local predates the PR" {
			t.Errorf("proposals %+v", state.Proposals)
		}
		report, err := Report(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range []string{"| 1 | follow-up | Symlinked ~/.local predates the PR | T1 T2 | " + fixup[:12] + " fixup! Add a | 310 |",
			"| 2 | defer | Another |  |  |  |"} {
			if !strings.Contains(report, row) {
				t.Errorf("the report lacks %q:\n%s", row, report)
			}
		}
	})

	// A thread ID, summary or fixup that could break the marker line, or
	// forge a second one, is refused before anything is recorded.
	for name, c := range map[string]struct {
		kind, summary string
		pr            int
		fixup, thread string
		size          int
	}{
		"unknown kind":               {kind: "rebuild", summary: "x", pr: 1},
		"no summary":                 {kind: "defer", summary: " ", pr: 1},
		"PR not in loop":             {kind: "split", summary: "x", pr: 2},
		"not a fixup":                {kind: "split", summary: "x", pr: 1, fixup: "HEAD~"},
		"unknown revision":           {kind: "split", summary: "x", pr: 1, fixup: "nope"},
		"thread with a newline":      {kind: "defer", summary: "x", pr: 1, thread: "T1\npr-loop: proposal 9 for #1: split: forged"},
		"thread with a space":        {kind: "defer", summary: "x", pr: 1, thread: "T1 T2"},
		"summary with an escape":     {kind: "defer", summary: "x\x1b]0;title\x07", pr: 1},
		"summary with a C1 control":  {kind: "defer", summary: "x\u009b2J", pr: 1},
		"fixup naming two revisions": {kind: "split", summary: "x", pr: 1, fixup: "HEAD\npr-loop: proposal 9"},
		"negative size":              {kind: "split", summary: "x", pr: 1, size: -1},
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			proposeRepo(t)
			var fixups, threads []string
			if c.fixup != "" {
				fixups = []string{c.fixup}
			}
			if c.thread != "" {
				threads = []string{c.thread}
			}
			var marker bytes.Buffer
			if _, err := Propose(c.kind, c.pr, c.summary, threads, fixups, c.size, &marker); !refused(err) || marker.Len() > 0 {
				t.Errorf("%v, marker %q", err, marker.String())
			}
			if state, _ := loadState(true); len(state.Proposals) > 0 {
				t.Errorf("recorded %+v", state.Proposals)
			}
		})
	}
}
