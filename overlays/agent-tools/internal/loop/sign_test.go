package loop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOpenPRs answers the GraphQL that finds the repository and the
// viewer's open PRs, for the viewer "me" in o/r: bases maps each PR's head
// branch to its base.
type fakeOpenPRs struct{ bases map[string]string }

func (f *fakeOpenPRs) graphQL(query string, vars map[string]any, out any) error {
	var data any
	switch {
	case strings.Contains(query, "viewer { login }"):
		data = map[string]any{"viewer": map[string]any{"login": "me"},
			"repository": map[string]any{"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": map[string]any{"name": "main"}}}
	case strings.Contains(query, "search(query"):
		nodes := []any{}
		n := 0
		for head, base := range f.bases {
			n++
			nodes = append(nodes, map[string]any{"number": n, "headRefName": head, "baseRefName": base,
				"author": map[string]any{"login": "me"}})
		}
		data = map[string]any{"search": map[string]any{"nodes": nodes}}
	default:
		return errors.New("unexpected query: " + query)
	}
	encoded, _ := json.Marshal(data)
	return json.Unmarshal(encoded, out)
}

// checkStack is newFixedStack in a checkout of o/r, whose default branch is
// main, with lower and upper open PRs and upper checked out.
func checkStack(t *testing.T) (*fixedStack, *fakeOpenPRs) {
	t.Helper()
	s := newFixedStack(t)
	gitIn(t, s.dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, s.dir, "update-ref", "refs/remotes/origin/main", "main")
	gitIn(t, s.dir, "switch", "-q", "upper")
	f := &fakeOpenPRs{bases: map[string]string{"lower": "main", "upper": "lower"}}
	old := graphQL
	graphQL = f.graphQL
	t.Cleanup(func() { graphQL = old })
	return s, f
}

// TestSignCheck checks that pr-loop sign-check accepts the stack containing
// HEAD when its fixups squash into each branch's own commits without moving
// a branch, and refuses one where they do not, or that is not linear.
func TestSignCheck(t *testing.T) {
	t.Run("clean stack", func(t *testing.T) {
		s, _ := checkStack(t)
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

	// A fixup of lower's commit, committed on upper, squashes into lower and
	// changes its files.
	t.Run("a fixup in the wrong branch", func(t *testing.T) {
		s, _ := checkStack(t)
		os.WriteFile(filepath.Join(s.dir, "a"), []byte("a fixed again\n"), 0o644)
		gitIn(t, s.dir, "commit", "-qa", "--fixup=lower~2")
		if _, err := SignCheck(); err == nil {
			t.Error("a fixup in the wrong branch was accepted")
		} else if _, ok := err.(Refusal); !ok {
			t.Errorf("not a refusal: %v", err)
		}
	})

	t.Run("a stack that is not linear", func(t *testing.T) {
		s, f := checkStack(t)
		gitIn(t, s.dir, "branch", "side", "lower")
		f.bases["side"] = "lower"
		if _, err := SignCheck(); err == nil || !strings.Contains(err.Error(), "linear") {
			t.Errorf("a stack that is not linear: %v", err)
		}
	})

	t.Run("HEAD heads no PR", func(t *testing.T) {
		s, _ := checkStack(t)
		gitIn(t, s.dir, "switch", "-q", "main")
		if _, err := SignCheck(); !refused(err) {
			t.Errorf("main: %v", err)
		}
	})
}
