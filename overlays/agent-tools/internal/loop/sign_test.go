package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkStack is newFixedStack with its loop registered for o/r, whose
// default branch is main, as pr-loop begin leaves it.
func checkStack(t *testing.T) *fixedStack {
	t.Helper()
	s := newFixedStack(t)
	gitIn(t, s.dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, s.dir, "update-ref", "refs/remotes/origin/main", "main")
	s.state.Repo = "o/r"
	for i, b := range s.state.Order {
		s.state.Branches[b].PR = i + 1
	}
	if err := s.state.save(); err != nil {
		t.Fatal(err)
	}
	old := graphQL
	graphQL = (&fakeGitHub{}).graphQL
	t.Cleanup(func() { graphQL = old })
	return s
}

// TestSignCheck checks that pr-loop sign-check accepts a stack whose
// fixups squash into each branch's own commits without moving a branch,
// and refuses one that would keep a commit of its own or is not linear.
func TestSignCheck(t *testing.T) {
	t.Run("clean stack", func(t *testing.T) {
		s := checkStack(t)
		branches := gitIn(t, s.dir, "for-each-ref", "refs/heads/")
		if _, err := SignCheck(); err != nil {
			t.Fatalf("a clean stack: %v", err)
		}
		if got := gitIn(t, s.dir, "for-each-ref", "refs/heads/"); got != branches {
			t.Errorf("sign-check moved branches:\nbefore:\n%s\nafter:\n%s", branches, got)
		}
		if got := gitIn(t, s.dir, "worktree", "list"); strings.Count(got, "\n") != 0 {
			t.Errorf("sign-check left a worktree behind:\n%s", got)
		}
	})

	t.Run("a commit that is no fixup", func(t *testing.T) {
		s := checkStack(t)
		gitIn(t, s.dir, "switch", "-q", "upper")
		os.WriteFile(filepath.Join(s.dir, "d"), []byte("d\n"), 0o644)
		gitIn(t, s.dir, "add", "d")
		gitIn(t, s.dir, "commit", "-qm", "U2 add d")
		gitIn(t, s.dir, "switch", "-q", "main")
		if _, err := SignCheck(); err == nil {
			t.Error("a stack with a commit of the loop's own was accepted")
		} else if _, ok := err.(Refusal); !ok {
			t.Errorf("not a refusal: %v", err)
		}
	})

	t.Run("a stack that is not linear", func(t *testing.T) {
		s := checkStack(t)
		s.state.Branches["upper"].Base = "main"
		if err := s.state.save(); err != nil {
			t.Fatal(err)
		}
		if _, err := SignCheck(); err == nil || !strings.Contains(err.Error(), "linear") {
			t.Errorf("a stack that is not linear: %v", err)
		}
	})
}
