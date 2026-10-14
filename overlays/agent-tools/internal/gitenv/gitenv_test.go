package gitenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// git runs git in dir with the test process's environment, as a package's own
// setup helper does.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return run(t, dir, os.Environ(), args...)
}

// bare runs git in dir without the variables that locate a repository.
func bare(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(Location, name) {
			env = append(env, kv)
		}
	}
	return run(t, dir, append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), args...)
}

func run(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// decoy makes a repository standing in for the caller's and points every
// git command of the test process at it, as git does for `rebase --exec`.
func decoy(t *testing.T) (snapshot func() string) {
	t.Helper()
	dir := t.TempDir()
	bare(t, dir, "init", "-q", "-b", "main")
	bare(t, dir, "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false",
		"commit", "-q", "--allow-empty", "-m", "decoy")
	snapshot = func() string {
		config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
		if err != nil {
			t.Fatal(err)
		}
		return string(config) + bare(t, dir, "for-each-ref") + bare(t, dir, "status", "--porcelain")
	}
	t.Setenv("GIT_DIR", filepath.Join(dir, ".git"))
	t.Setenv("GIT_WORK_TREE", dir)
	return snapshot
}

func TestIsolate(t *testing.T) {
	// The sequence is what a package's test setup does.
	t.Run("keeps setup out of the caller's repository", func(t *testing.T) {
		snapshot := decoy(t)
		before := snapshot()
		t.Setenv("GIT_CONFIG_GLOBAL", "/nonexistent")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
		Isolate()

		scratch := t.TempDir()
		git(t, scratch, "init", "-q", "-b", "main")
		git(t, scratch, "init", "-q", "--bare", filepath.Join(scratch, "fork.git"))
		git(t, scratch, "remote", "add", "fork", "https://example.invalid/fork.git")
		git(t, scratch, "-c", "user.name=T", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "x")
		git(t, scratch, "config", "core.bare", "true")

		if after := snapshot(); after != before {
			t.Errorf("the caller's repository changed:\n--- before\n%s\n--- after\n%s", before, after)
		}
		if got := os.Getenv("GIT_CONFIG_GLOBAL"); got != "/dev/null" {
			t.Errorf("GIT_CONFIG_GLOBAL = %q", got)
		}
		if got := os.Getenv("GIT_CONFIG_NOSYSTEM"); got != "1" {
			t.Errorf("GIT_CONFIG_NOSYSTEM = %q", got)
		}
		if remotes := git(t, scratch, "remote"); strings.TrimSpace(remotes) != "fork" {
			t.Errorf("remotes of the scratch repository: %q", remotes)
		}
	})

	// Packages must be isolated by their own TestMain, not by setup helpers
	// that remember to be careful.
	t.Run("in each package's TestMain", func(t *testing.T) {
		if testing.Short() {
			t.Skip("runs other packages' tests")
		}
		snapshot := decoy(t)
		before := snapshot()
		cmd := exec.Command("go", "test", "-count=1", "../git", "../repos", "../pushguard")
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Logf("go test: %v\n%s", err, out)
		}
		if after := snapshot(); after != before {
			t.Errorf("the caller's repository changed:\n--- before\n%s\n--- after\n%s", before, after)
		}
	})
}

func TestWithout(t *testing.T) {
	env := []string{"GIT_DIR=/r/.git", "PATH=/bin", "GIT_WORK_TREE=/r", "GIT_DIRECTORY=x", "GIT_CONFIG_GLOBAL=/dev/null"}
	if got := strings.Join(Without(env), " "); got != "PATH=/bin GIT_DIRECTORY=x GIT_CONFIG_GLOBAL=/dev/null" {
		t.Errorf("Without: %s", got)
	}
}
