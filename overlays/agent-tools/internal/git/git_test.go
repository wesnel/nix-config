package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/env"
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

// useDir points the package's commands at dir for the rest of the test,
// which therefore cannot run in parallel with another that runs git here.
func useDir(t *testing.T, dir string) {
	t.Helper()
	Dir = dir
	t.Cleanup(func() { Dir = "" })
}

// under is the test's context with an Env of its own: this process's, as
// TestMain left it, in dir and with vars set.
func under(t *testing.T, dir string, vars ...string) context.Context {
	t.Helper()
	return env.With(t.Context(), env.Process().In(dir).Set(vars...))
}

func TestRun(t *testing.T) {
	t.Parallel()
	// A command aimed at a directory acts on the repository there, whatever
	// repository the environment names.
	t.Run("aimed at a directory ignores the environment", func(t *testing.T) {
		t.Parallel()
		repo := repository(t)
		_, decoy := gitenvtest.DecoyEnv(t)
		ctx := under(t, t.TempDir(), decoy...)

		if _, err := In(ctx).Run("-C", repo, "config", "test.c", "1"); err != nil {
			t.Fatal(err)
		}
		if _, err := In(ctx).RunEnv([]string{"GIT_AUTHOR_NAME=T"}, "-C", repo, "config", "test.env", "1"); err != nil {
			t.Fatal(err)
		}
		if code := Attached(ctx, nil, "-C", repo, "config", "test.attached", "1"); code != 0 {
			t.Fatalf("attached: exit %d", code)
		}
		ctx = At(ctx, repo)
		if _, err := In(ctx).Run("config", "test.dir", "1"); err != nil {
			t.Fatal(err)
		}
		if !In(ctx).OK("config", "test.dir") {
			t.Error("OK did not read the repository At aimed at")
		}
		got := gitIn(t, repo, "config", "--get-regexp", `^test\.`)
		if got != "test.c 1\ntest.env 1\ntest.attached 1\ntest.dir 1" {
			t.Errorf("config of the repository aimed at:\n%s", got)
		}
	})

	// A command attached to the terminal reads and writes the streams its
	// caller gives, not this process's.
	t.Run("attached to the caller's streams", func(t *testing.T) {
		t.Parallel()
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\ncat >&2\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx := under(t, repository(t), "PATH="+bin+string(os.PathListSeparator)+env.Process().Get("PATH"))
		var stderr bytes.Buffer
		if code, got := AttachedStderr(ctx, nil, strings.NewReader("typed\n"), &stderr, "status"); code != 0 || got != "typed\n" || stderr.String() != "typed\n" {
			t.Errorf("AttachedStderr: exit %d, returned %q, wrote %q; want what was typed in both", code, got, stderr.String())
		}
		stderr.Reset()
		if code := AttachedQuiet(ctx, nil, strings.NewReader("typed\n"), &stderr, "status"); code != 0 || stderr.String() != "typed\n" {
			t.Errorf("AttachedQuiet: exit %d, wrote %q; want what was typed", code, stderr.String())
		}
	})

	// The git that runs is the one on the Env's PATH, not the process's.
	t.Run("found on the Env's PATH", func(t *testing.T) {
		t.Parallel()
		bin := t.TempDir()
		fake := "#!/bin/sh\necho fake git \"$#\"\necho fake >&2\n"
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
		ctx := under(t, repository(t), "PATH="+bin+string(os.PathListSeparator)+env.Process().Get("PATH"))
		if got, err := In(ctx).Run("status"); err != nil || got != "fake git 5" {
			t.Errorf("Run ran %q, %v; want the fake git on the Env's PATH", got, err)
		}
		var stderr bytes.Buffer
		if code := AttachedQuiet(ctx, nil, os.Stdin, &stderr, "status"); code != 0 || stderr.String() != "fake\n" {
			t.Errorf("AttachedQuiet: exit %d, %q; want the fake git on the Env's PATH", code, stderr.String())
		}
		missing := under(t, t.TempDir(), "PATH="+t.TempDir())
		if _, err := In(missing).Run("status"); !errors.Is(err, exec.ErrNotFound) {
			t.Errorf("with no git on the Env's PATH: %v", err)
		}
	})

	t.Run("discovery honours the environment", func(t *testing.T) {
		t.Parallel()
		decoy, vars := gitenvtest.DecoyEnv(t)
		ctx := under(t, t.TempDir(), vars...)
		if got, _ := In(ctx).Run("rev-parse", "--absolute-git-dir"); got != filepath.Join(decoy, ".git") {
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
	t.Parallel()
	// inSub makes a repository and a directory below its top, and returns
	// the repository and that directory.
	inSub := func(t *testing.T) (repo, sub string) {
		t.Helper()
		repo = repository(t)
		sub = filepath.Join(repo, "sub")
		os.Mkdir(sub, 0o755)
		return repo, sub
	}

	t.Run("without GIT_DIR", func(t *testing.T) {
		t.Parallel()
		repo, sub := inSub(t)
		ctx := under(t, sub)
		if top, err := In(ctx).Toplevel(); err != nil || top != repo {
			t.Errorf("without GIT_DIR: %q, %v", top, err)
		}
		if err := In(ctx).CheckLocation(); err != nil {
			t.Errorf("without GIT_DIR: %v", err)
		}
	})

	t.Run("GIT_DIR naming this repository", func(t *testing.T) {
		t.Parallel()
		repo, sub := inSub(t)
		ctx := under(t, sub, "GIT_DIR="+filepath.Join(repo, ".git"), "GIT_WORK_TREE="+repo)
		if top, err := In(ctx).Toplevel(); err != nil || top != repo {
			t.Errorf("GIT_DIR naming this repository: %q, %v", top, err)
		}
	})

	t.Run("GIT_DIR naming another repository", func(t *testing.T) {
		t.Parallel()
		_, sub := inSub(t)
		_, decoy := gitenvtest.DecoyEnv(t)
		ctx := env.With(t.Context(), env.From(under(t, sub, decoy...)).Without("GIT_WORK_TREE"))
		_, err := In(ctx).Toplevel()
		if _, ok := err.(refusal.Refusal); !ok || !refusal.SaysInstead(err.Error()) {
			t.Errorf("GIT_DIR naming another repository: %v", err)
		}
		if err := In(ctx).CheckLocation(); err == nil {
			t.Error("CheckLocation accepted GIT_DIR naming another repository")
		}
	})

	// A repository an agent can write puts its worktree elsewhere with its own
	// config, and no variable in the environment: that is a git directory other
	// than the worktree's own, as GIT_DIR naming another repository is.
	t.Run("config naming another worktree", func(t *testing.T) {
		t.Parallel()
		repo, other := repository(t), repository(t)
		cfg, err := os.OpenFile(filepath.Join(repo, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(cfg, "[core]\n\tworktree = %s\n", other)
		cfg.Close()
		ctx := under(t, repo)
		top, err := In(ctx).Toplevel()
		if _, ok := err.(refusal.Refusal); !ok {
			t.Errorf("a config naming the worktree %s: %q, %v; want a refusal", other, top, err)
		}
		if err := In(ctx).CheckLocation(); err == nil {
			t.Error("CheckLocation accepted a config naming another worktree")
		}
	})

	// Aimed at a directory, git finds the worktree there, whatever
	// repository the caller's environment names.
	t.Run("aimed at a directory", func(t *testing.T) {
		t.Parallel()
		repo, sub := inSub(t)
		_, decoy := gitenvtest.DecoyEnv(t)
		ctx := At(env.With(t.Context(), env.From(under(t, t.TempDir(), decoy...)).Without("GIT_WORK_TREE")), sub)
		if top, err := In(ctx).Toplevel(); err != nil || top != repo {
			t.Errorf("aimed at %s: %q, %v", sub, top, err)
		}
		if err := In(ctx).CheckLocation(); err != nil {
			t.Errorf("aimed at %s: %v", sub, err)
		}
	})

	// GIT_DIR and GIT_WORK_TREE that agree name a repository to act on.
	t.Run("GIT_DIR and GIT_WORK_TREE agreeing", func(t *testing.T) {
		t.Parallel()
		_, sub := inSub(t)
		_, decoy := gitenvtest.DecoyEnv(t)
		if err := In(under(t, sub, decoy...)).CheckLocation(); err != nil {
			t.Errorf("GIT_DIR and GIT_WORK_TREE naming the decoy: %v", err)
		}
	})

	// Outside a repository there is nothing to check.
	t.Run("outside a repository", func(t *testing.T) {
		t.Parallel()
		ctx := env.With(t.Context(), env.From(under(t, t.TempDir())).Without("GIT_DIR", "GIT_WORK_TREE"))
		if err := In(ctx).CheckLocation(); err != nil {
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
