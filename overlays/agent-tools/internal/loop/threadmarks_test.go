package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
)

// In a loop, `pr-loop threads` marks whether a thread sits on lines the PR
// changed, and on code a loop fixup touched last, which triage needs to
// tell a finding the PR introduced from one that predates it, and one the
// loop's own fixups brought.
func TestThreadMarks(t *testing.T) {
	reviewRepo(t, `{"bots": {"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}}}}`,
		fakeReviewThread{id: "old", first: copilotLogin, last: copilotLogin, path: "a.go", line: 1},
		fakeReviewThread{id: "pr", first: copilotLogin, last: copilotLogin, path: "a.go", line: 2},
		fakeReviewThread{id: "fixup", first: copilotLogin, last: copilotLogin, path: "a.go", line: 4},
		fakeReviewThread{id: "removed", first: copilotLogin, last: copilotLogin, path: "a.go", line: 3, side: "LEFT"},
		fakeReviewThread{id: "outdated", first: copilotLogin, last: copilotLogin, path: "a.go", outdated: true},
		fakeReviewThread{id: "operator", first: copilotLogin, last: copilotLogin, path: "a.go", line: 6},
	)
	dir := git.Dir
	write := func(lines ...string) {
		os.WriteFile(filepath.Join(dir, "a.go"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	}
	gitIn(t, dir, "switch", "-q", "main")
	write("1", "2", "3", "4", "5")
	gitIn(t, dir, "add", "a.go")
	gitIn(t, dir, "commit", "-qm", "Add a.go")
	gitIn(t, dir, "switch", "-q", "feat")
	gitIn(t, dir, "reset", "-q", "--hard", "main")
	write("1", "two", "4", "5")
	gitIn(t, dir, "commit", "-qam", "Change a.go")
	// The operator's own fixup, there when the loop began.
	write("1", "two", "4", "5", "six")
	gitIn(t, dir, "commit", "-qam", "fixup! Change a.go", "-m", "The operator's own.")
	write("1", "two", "4", "five", "5", "six")
	gitIn(t, dir, "commit", "-qam", "fixup! Change a.go")
	tip := gitIn(t, dir, "rev-parse", "HEAD")
	// A later local commit the loop has not pushed changes nothing.
	write("1", "two", "4", "five", "5", "six", "7")
	gitIn(t, dir, "commit", "-qam", "fixup! Change a.go")
	state := &State{Repo: "o/r", Order: []string{"feat"}, Branches: map[string]*Branch{"feat": {PR: 1, Base: "main", RemoteOid: tip,
		Commits: []Commit{{Message: "Change a.go", Author: "T <t@e>"},
			{Message: "fixup! Change a.go\n\nThe operator's own.", Author: "T <t@e>"}}}}}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}

	out, err := Threads(1, false)
	if err != nil {
		t.Fatal(err)
	}
	mark := func(b *bool) string {
		if b == nil {
			return "-"
		}
		if *b {
			return "y"
		}
		return "n"
	}
	got := map[string]string{}
	for _, th := range out.([]Thread) {
		got[th.ID] = mark(th.OnChangedLines) + mark(th.OnLoopFixup)
	}
	// The pushed head's lines are 1, two (the PR's), 4, five (the loop
	// fixup's), 5 and six (the operator's fixup's).
	want := map[string]string{"old": "nn", "pr": "yn", "fixup": "yy", "removed": "y-", "outdated": "--", "operator": "yn"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: changed, fixup = %s, want %s", id, got[id], w)
		}
	}
}
