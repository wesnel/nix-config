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

// useDir points the package's commands at dir for the rest of the test.
func useDir(t *testing.T, dir string) {
	t.Helper()
	Dir = dir
	t.Cleanup(func() { Dir = "" })
}

func TestRun(t *testing.T) {
	// A command aimed at a directory acts on the repository there, whatever
	// repository the environment names.
	t.Run("aimed at a directory ignores the environment", func(t *testing.T) {
		repo := repository(t)
		gitenvtest.Decoy(t)

		if _, err := Run("-C", repo, "config", "test.c", "1"); err != nil {
			t.Fatal(err)
		}
		if _, err := RunEnv([]string{"GIT_AUTHOR_NAME=T"}, "-C", repo, "config", "test.env", "1"); err != nil {
			t.Fatal(err)
		}
		if code := Attached(nil, "-C", repo, "config", "test.attached", "1"); code != 0 {
			t.Fatalf("attached: exit %d", code)
		}
		useDir(t, repo)
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
	})

	t.Run("discovery honours the environment", func(t *testing.T) {
		decoy := gitenvtest.Decoy(t)
		Dir = ""
		if got, _ := Run("rev-parse", "--absolute-git-dir"); got != filepath.Join(decoy, ".git") {
			resolved, _ := filepath.EvalSymlinks(got)
			want, _ := filepath.EvalSymlinks(filepath.Join(decoy, ".git"))
			if resolved != want {
				t.Errorf("an untargeted command found %s, not the decoy", got)
			}
		}
	})
}

// Toplevel finds the worktree as git does, and refuses one the environment
// gives another git directory.
func TestToplevel(t *testing.T) {
	// inSub makes a repository and moves into a directory below its top.
	inSub := func(t *testing.T) string {
		t.Helper()
		repo := repository(t)
		sub := filepath.Join(repo, "sub")
		os.Mkdir(sub, 0o755)
		t.Chdir(sub)
		return repo
	}

	t.Run("without GIT_DIR", func(t *testing.T) {
		repo := inSub(t)
		if top, err := Toplevel(); err != nil || top != repo {
			t.Errorf("without GIT_DIR: %q, %v", top, err)
		}
		if err := CheckLocation(); err != nil {
			t.Errorf("without GIT_DIR: %v", err)
		}
	})

	t.Run("GIT_DIR naming this repository", func(t *testing.T) {
		repo := inSub(t)
		t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
		t.Setenv("GIT_WORK_TREE", repo)
		if top, err := Toplevel(); err != nil || top != repo {
			t.Errorf("GIT_DIR naming this repository: %q, %v", top, err)
		}
	})

	t.Run("GIT_DIR naming another repository", func(t *testing.T) {
		inSub(t)
		gitenvtest.Decoy(t)
		os.Unsetenv("GIT_WORK_TREE")
		_, err := Toplevel()
		if _, ok := err.(refusal.Refusal); !ok || !refusal.SaysInstead(err.Error()) {
			t.Errorf("GIT_DIR naming another repository: %v", err)
		}
		if err := CheckLocation(); err == nil {
			t.Error("CheckLocation accepted GIT_DIR naming another repository")
		}
	})

	// GIT_DIR and GIT_WORK_TREE that agree name a repository to act on.
	t.Run("GIT_DIR and GIT_WORK_TREE agreeing", func(t *testing.T) {
		inSub(t)
		decoy := gitenvtest.Decoy(t)
		os.Unsetenv("GIT_WORK_TREE")
		t.Setenv("GIT_WORK_TREE", decoy)
		if err := CheckLocation(); err != nil {
			t.Errorf("GIT_DIR and GIT_WORK_TREE naming the decoy: %v", err)
		}
	})

	// Outside a repository there is nothing to check.
	t.Run("outside a repository", func(t *testing.T) {
		// t.Setenv first, so the environment is restored after the test.
		for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE"} {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
		t.Chdir(t.TempDir())
		if err := CheckLocation(); err != nil {
			t.Errorf("outside a repository: %v", err)
		}
	})
}
