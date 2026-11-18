package review

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// operatorClone makes a clone of r as the operator's own, with origin on
// GitHub as o/r unless origin names another, and runs the test from it.
func operatorClone(t *testing.T, r *remote, origin string) string {
	t.Helper()
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(parent, "home")
	gitT(t, "", "clone", "-q", "-b", "main", r.bare, home)
	if origin == "" {
		origin = "https://github.com/o/r.git"
	}
	gitT(t, home, "remote", "set-url", "origin", origin)
	t.Chdir(home)
	return home
}

// A pull request reviewed from a clone of its repository is fetched into
// that clone and reviewed in a worktree of it under the cache, so the
// clone's agent settings apply; the clone's own checkout and branches are
// left as they were, and the worktree goes when the review ends.
func TestFetchIntoClone(t *testing.T) {
	t.Run("reviews in a worktree of the clone", func(t *testing.T) {
		r := newRemote(t, nil)
		f := setUpPR(t, r)
		home := operatorClone(t, r, "")
		write(t, filepath.Join(home, ".git", "info", "agents.json"), `{"suites": [{"name": "go", "test": "go test ./...", "testFiles": ["*_test.go"]}]}`)
		os.Chmod(filepath.Join(home, ".git", "info", "agents.json"), 0o644)
		marker := filepath.Join(t.TempDir(), "hook-ran")
		write(t, filepath.Join(home, ".git", "hooks", "reference-transaction"), "#!/bin/sh\ntouch "+marker+"\n")
		os.Chmod(filepath.Join(home, ".git", "hooks", "reference-transaction"), 0o755)
		// Another worktree's entry, left stale, which the review's
		// cleanup must not prune.
		stale := filepath.Join(t.TempDir(), "stale")
		gitT(t, home, "-c", "core.hooksPath=/dev/null", "worktree", "add", "-q", "--detach", stale)
		os.RemoveAll(stale)
		before := gitT(t, home, "rev-parse", "HEAD")

		var log bytes.Buffer
		sp, err := resolveSource(t.Context(), must(parseSource("o/r#7")), true, &log)
		if err != nil {
			t.Fatal(err)
		}
		if sp.tip != r.head || sp.base != r.base {
			t.Errorf("range %s..%s, want %s..%s", sp.base, sp.tip, r.base, r.head)
		}
		if got := gitT(t, home, "rev-parse", "refs/pr-review/7/head"); got != r.head {
			t.Errorf("refs/pr-review/7/head is %s, not %s", got, r.head)
		}
		gitT(t, home, "rev-parse", "--verify", "refs/pr-review/7/base")
		if rel, err := filepath.Rel(Root(), sp.top); err != nil || strings.HasPrefix(rel, "..") {
			t.Errorf("the review's worktree %s is not under the cache %s", sp.top, Root())
		}
		common, _ := filepath.EvalSymlinks(gitT(t, sp.top, "rev-parse", "--path-format=absolute", "--git-common-dir"))
		if common != filepath.Join(home, ".git") {
			t.Errorf("the review's worktree shares %s, not the clone's git directory", common)
		}
		entries, _ := os.ReadDir(sp.top)
		if len(entries) != 1 || entries[0].Name() != ".git" {
			t.Errorf("the review's worktree has the pull request checked out: %v", entries)
		}
		if s, err := sp.settings(); err != nil || len(s.Suites) != 1 || s.Suites[0].Test != "go test ./..." {
			t.Errorf("the clone's settings do not apply: %+v, %v", s, err)
		}
		if after := gitT(t, home, "rev-parse", "HEAD"); after != before {
			t.Errorf("the clone's HEAD moved from %s to %s", before, after)
		}
		if status := gitT(t, home, "status", "--porcelain"); status != "" {
			t.Errorf("the clone's checkout changed:\n%s", status)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Error("a hook of the clone ran")
		}
		if f.mutations != 0 {
			t.Errorf("%d writes reached GitHub", f.mutations)
		}

		// A reviewer's checkout is a worktree of the same clone.
		wt, remove, err := worktree(t.Context(), must(parseSource("o/r#7")), sp)
		if err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(filepath.Join(wt, "r.go")); !strings.Contains(string(data), "return 2") {
			t.Errorf("the reviewer's checkout is not the pull request's head: %q", data)
		}
		remove()
		if _, err := os.Stat(wt); err == nil {
			t.Error("the reviewer's checkout is left")
		}

		sp.release()
		if _, err := os.Stat(sp.top); err == nil {
			t.Error("the review's worktree is left")
		}
		list := gitT(t, home, "worktree", "list", "--porcelain")
		if strings.Contains(list, sp.top) || strings.Contains(list, wt) {
			t.Errorf("the clone still lists the review's worktrees:\n%s", list)
		}
		if !strings.Contains(list, stale) {
			t.Errorf("another worktree's stale entry was pruned:\n%s", list)
		}
		gitT(t, home, "rev-parse", "--verify", "refs/pr-review/7/head")
	})

	// A head other than the one GitHub reports is refused, as for the
	// cache's own clone, and no worktree is made for it.
	t.Run("pins the head", func(t *testing.T) {
		r := newRemote(t, nil)
		f := setUpPR(t, r)
		home := operatorClone(t, r, "")
		f.pr["headRefOid"] = r.base
		var log bytes.Buffer
		if _, err := resolveSource(t.Context(), must(parseSource("o/r#7")), true, &log); err == nil ||
			!strings.Contains(err.Error(), r.base) {
			t.Fatalf("a head that moved was accepted: %v", err)
		}
		if list := gitT(t, home, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
			t.Errorf("a worktree was made for an unpinned head:\n%s", list)
		}
	})

	// Without a clone of the repository here, or from one whose config
	// names a program, the review uses the cache's bare clone, which has
	// no agent settings, and says so.
	for _, c := range []struct {
		name, origin string
		config       []string
		says         string
	}{
		{"another repository's clone", "https://github.com/o/other.git", nil, "no clone of o/r"},
		{"a clone whose config runs a program", "", []string{"filter.x.smudge", "cat"}, "filter.x.smudge"},
	} {
		t.Run("falls back: "+c.name, func(t *testing.T) {
			r := newRemote(t, nil)
			setUpPR(t, r)
			home := operatorClone(t, r, c.origin)
			if c.config != nil {
				gitT(t, home, append([]string{"config"}, c.config...)...)
			}
			var log bytes.Buffer
			sp, err := resolveSource(t.Context(), must(parseSource("o/r#7")), true, &log)
			if err != nil {
				t.Fatal(err)
			}
			defer sp.release()
			if sp.top != filepath.Join(Root(), "o", "r", "7", "clone") {
				t.Errorf("reviewed in %s, not the cache's clone", sp.top)
			}
			if out := log.String(); !strings.Contains(out, c.says) || !strings.Contains(out, "no agent settings") {
				t.Errorf("the fallback is not said: %q", out)
			}
			if err := gitCommand(t.Context(), home, nil, "rev-parse", "--verify", "-q", "refs/pr-review/7/head").Run(); err == nil {
				t.Error("the pull request was fetched into a clone the review did not use")
			}
		})
	}
}
