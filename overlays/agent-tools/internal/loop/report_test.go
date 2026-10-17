package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
)

// The report's tables are generated from GitHub and the loop's state, so a
// hand-back pastes real thread URLs instead of composing them.
func TestReport(t *testing.T) {
	const config = `{"bots": {
		"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"},
			"verdicts": {"approval": "Approval recommended", "closer-look": "closer look"}},
		"qodo": {"login": "^qodo-code-review", "request": {"comment": "/agentic_review"}}}}`
	f := reviewRepo(t, config,
		fakeReviewThread{id: "T1", first: copilotLogin, last: copilotLogin, path: "a.go", line: 3, url: "https://github.com/o/r/pull/1#discussion_r1"},
		fakeReviewThread{id: "T2", first: qodoLogin, last: "me", path: "b.go", url: "https://github.com/o/r/pull/1#discussion_r2", resolved: true},
		fakeReviewThread{id: "T3", first: "josh", last: "josh", path: "a.go", line: 7, url: "https://github.com/o/r/pull/1#discussion_r3"})
	f.reviews = []fakeReviewNode{{copilotLogin, "a1", "### Approval recommended"}}
	os.WriteFile(filepath.Join(git.Dir, "a"), []byte("a\n"), 0o644)
	gitIn(t, git.Dir, "add", "a")
	gitIn(t, git.Dir, "commit", "-qm", "Add a")
	os.WriteFile(filepath.Join(git.Dir, "a"), []byte("a\nb\n"), 0o644)
	gitIn(t, git.Dir, "commit", "-qam", "fixup! Add a")
	state := &State{Repo: "o/r", Order: []string{"feat"}, Branches: map[string]*Branch{"feat": {PR: 1, Base: "main", BaselineLines: 1,
		Commits: []Commit{{Message: "Add a\n", Author: "T <t@e>"}}}}}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}

	report, err := Report(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## #1 (feat)",
		"CI: passed",
		"Changed lines: 1 when the loop began, 2 now",
		"Commits: 1 of the PR's own, 1 fixup added",
		"| copilot | yes | approval | 1 | 1 |",
		"| qodo | no |  | 0 | 0 |",
		"| T1 | copilot | a.go:3 | open, the bot has the last word | https://github.com/o/r/pull/1#discussion_r1 |",
		"| T2 | qodo | b.go | resolved | https://github.com/o/r/pull/1#discussion_r2 |",
		"| T3 | human | a.go:7 | open, the operator's | https://github.com/o/r/pull/1#discussion_r3 |",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report lacks %q:\n%s", want, report)
		}
	}
}
