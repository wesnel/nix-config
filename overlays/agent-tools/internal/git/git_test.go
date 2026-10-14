package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
	"github.com/wesnel/agent-tools/internal/refusal"
)

func repository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	return dir
}

// A command aimed at a directory acts on the repository there, whatever
// repository the environment names.
func TestTargetedCommandsIgnoreTheEnvironment(t *testing.T) {
	repo := repository(t)
	decoy := gitenvtest.Decoy(t)

	if _, err := Run("-C", repo, "config", "test.c", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := RunEnv([]string{"GIT_AUTHOR_NAME=T"}, "-C", repo, "config", "test.env", "1"); err != nil {
		t.Fatal(err)
	}
	if code := Attached(nil, "-C", repo, "config", "test.attached", "1"); code != 0 {
		t.Fatalf("attached: exit %d", code)
	}
	Dir = repo
	defer func() { Dir = "" }()
	if _, err := Run("config", "test.dir", "1"); err != nil {
		t.Fatal(err)
	}
	if !OK("config", "test.dir") {
		t.Error("OK did not read the repository in Dir")
	}
	got := gitIn(t, repo, "config", "--get-regexp", `^test\.`)
	if got != "test.c 1\ntest.env 1\ntest.attached 1\ntest.dir 1" {
		t.Errorf("config of the repository aimed at:\n%s", got)
	}

	// Discovery honours the environment.
	Dir = ""
	if got, _ := Run("rev-parse", "--absolute-git-dir"); got != filepath.Join(decoy, ".git") {
		resolved, _ := filepath.EvalSymlinks(got)
		want, _ := filepath.EvalSymlinks(filepath.Join(decoy, ".git"))
		if resolved != want {
			t.Errorf("an untargeted command found %s, not the decoy", got)
		}
	}
}

// Toplevel finds the worktree as git does, and refuses one the environment
// gives another git directory.
func TestToplevel(t *testing.T) {
	repo := repository(t)
	sub := filepath.Join(repo, "sub")
	os.Mkdir(sub, 0o755)
	t.Chdir(sub)

	if top, err := Toplevel(); err != nil || top != repo {
		t.Errorf("without GIT_DIR: %q, %v", top, err)
	}
	if err := CheckLocation(); err != nil {
		t.Errorf("without GIT_DIR: %v", err)
	}

	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	t.Setenv("GIT_WORK_TREE", repo)
	if top, err := Toplevel(); err != nil || top != repo {
		t.Errorf("GIT_DIR naming this repository: %q, %v", top, err)
	}

	decoy := gitenvtest.Decoy(t)
	os.Unsetenv("GIT_WORK_TREE")
	_, err := Toplevel()
	if _, ok := err.(refusal.Refusal); !ok || !refusal.SaysInstead(err.Error()) {
		t.Errorf("GIT_DIR naming another repository: %v", err)
	}
	if err := CheckLocation(); err == nil {
		t.Error("CheckLocation accepted GIT_DIR naming another repository")
	}

	// GIT_DIR and GIT_WORK_TREE that agree name a repository to act on.
	t.Setenv("GIT_WORK_TREE", decoy)
	if err := CheckLocation(); err != nil {
		t.Errorf("GIT_DIR and GIT_WORK_TREE naming the decoy: %v", err)
	}

	// Outside a repository there is nothing to check.
	os.Unsetenv("GIT_DIR")
	os.Unsetenv("GIT_WORK_TREE")
	t.Chdir(t.TempDir())
	if err := CheckLocation(); err != nil {
		t.Errorf("outside a repository: %v", err)
	}
}
