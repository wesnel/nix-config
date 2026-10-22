// Package gitenvtest points a test's git environment at a repository the code
// under test must leave alone.
package gitenvtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// Decoy makes a repository standing in for a caller's, and sets GIT_DIR and
// GIT_WORK_TREE to it for the rest of the test, as git does for the commands
// it starts (rebase --exec, hooks, aliases). The test fails if the decoy's
// config, refs, worktree or git directory's files change. Setup that must
// not reach the decoy runs git with gitenv.Without(os.Environ()).
func Decoy(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false",
		"commit", "-q", "--allow-empty", "-m", "decoy")
	before := snapshot(t, dir)
	t.Setenv("GIT_DIR", filepath.Join(dir, ".git"))
	t.Setenv("GIT_WORK_TREE", dir)
	t.Cleanup(func() {
		if after := snapshot(t, dir); after != before {
			t.Errorf("the repository GIT_DIR names changed:\n--- before\n%s\n--- after\n%s", before, after)
		}
	})
	return dir
}

func snapshot(t testing.TB, dir string) string {
	t.Helper()
	// First, as status may write the index.
	state := run(t, dir, "for-each-ref") + run(t, dir, "worktree", "list", "--porcelain") +
		run(t, dir, "status", "--porcelain", "--untracked-files=all")
	config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(dir, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return state + string(config) + strings.Join(names, " ") + "\n"
}

func run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
