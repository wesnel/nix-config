package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

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
		if code := Attached(t.Context(), nil, "-C", repo, "config", "test.attached", "1"); code != 0 {
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

// A repository an agent can write can stall git: a FIFO where git reads its
// config blocks it until something writes there. Each wrapper that takes no
// context ends such a command at its deadline, with an error that says so.
func TestDeadline(t *testing.T) {
	stalled := func(t *testing.T) {
		t.Helper()
		repo := repository(t)
		config := filepath.Join(repo, ".git", "config")
		if err := os.Remove(config); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(config, 0o600); err != nil {
			t.Fatal(err)
		}
		// A git left waiting on the FIFO, as one is before the deadline
		// exists, reads its end once a writer opens and closes it.
		t.Cleanup(func() {
			if f, err := os.OpenFile(config, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				f.Close()
			}
		})
		useDir(t, repo)
	}
	short := func(t *testing.T, d *time.Duration) {
		t.Helper()
		old := *d
		*d = 200 * time.Millisecond
		t.Cleanup(func() { *d = old })
	}
	cases := []struct {
		name  string
		limit *time.Duration
		call  func() error
	}{
		{"Run", &Timeout, func() error { _, err := Run("rev-parse", "HEAD"); return err }},
		{"RunEnv", &Timeout, func() error { _, err := RunEnv([]string{"GIT_AUTHOR_NAME=T"}, "rev-parse", "HEAD"); return err }},
		{"Bytes", &Timeout, func() error { _, err := Bytes(nil, "rev-parse", "HEAD"); return err }},
		{"RunLong", &LongTimeout, func() error { _, err := RunLong(nil, "rev-parse", "HEAD"); return err }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stalled(t)
			short(t, c.limit)
			done := make(chan error, 1)
			go func() { done <- c.call() }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Errorf("a stalled git ended with %v, want its deadline", err)
				}
			// Only a broken test waits this long: the deadline is 200ms.
			case <-time.After(30 * time.Second):
				t.Fatal("a stalled git outlived its deadline")
			}
		})
	}
	t.Run("OK", func(t *testing.T) {
		stalled(t)
		short(t, &Timeout)
		done := make(chan bool, 1)
		go func() { done <- OK("rev-parse", "HEAD") }()
		select {
		case ok := <-done:
			if ok {
				t.Error("a stalled git counted as success")
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a stalled git outlived its deadline")
		}
	})
	// The same command ends well in the same repository without the FIFO,
	// so the cases above fail for the stall.
	t.Run("without the FIFO", func(t *testing.T) {
		useDir(t, repository(t))
		if _, err := Run("rev-parse", "HEAD"); err != nil {
			t.Fatal(err)
		}
	})
}
