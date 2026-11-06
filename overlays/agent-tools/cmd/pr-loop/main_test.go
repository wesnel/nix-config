package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
)

func TestRun(t *testing.T) {
	t.Parallel()
	// A command run from a worktree that GIT_DIR, set by a git hook or
	// `rebase --exec` elsewhere, does not name is refused before it reads or
	// writes either repository.
	t.Run("GIT_DIR names another repository", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cmd := exec.Command("git", "init", "-q", "-b", "main")
		cmd.Dir, cmd.Env = dir, gitenv.Without(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		_, decoy := gitenvtest.DecoyEnv(t)
		ctx := env.With(t.Context(), env.Process().Set(decoy...).Without("GIT_WORK_TREE").In(dir))
		var stdout, stderr bytes.Buffer
		if code := run(ctx, "end", nil, strings.NewReader(""), &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "GIT_DIR") {
			t.Errorf("exit %d\n%s", code, stderr.String())
		}
	})

	// setup PATH looks the command up in the clone at PATH, and leaves this
	// process in its own directory.
	t.Run("setup in another worktree", func(t *testing.T) {
		t.Parallel()
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "init", "-q", "-b", "main")
		cmd.Dir, cmd.Env = dir, gitenv.Without(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		before, _ := os.Getwd()
		var stdout, stderr bytes.Buffer
		code := run(env.With(t.Context(), env.Process().In(t.TempDir())), "setup", []string{dir}, strings.NewReader(""), &stdout, &stderr)
		if code != 0 || !strings.Contains(stdout.String(), filepath.Join(dir, ".git", "info", "agents.json")) {
			t.Errorf("exit %d, printed %s\n%s; want the settings of %s", code, stdout.String(), stderr.String(), dir)
		}
		if after, _ := os.Getwd(); after != before {
			t.Errorf("setup moved this process from %s to %s", before, after)
		}
	})

	// merge reads --method wherever it is given, and refuses before asking
	// anything of GitHub or the terminal.
	for name, c := range map[string]struct {
		args []string
		code int
		want string
	}{
		"a method after the PR": {[]string{"3", "--method", "fast-forward"}, 2, `"fast-forward" is not a merge method`},
		"two PRs":               {[]string{"3", "4"}, 1, "merge takes at most one PR"},
	} {
		t.Run("merge/"+name, func(t *testing.T) {
			t.Parallel()
			ctx, _, calls := posting(t)
			var stdout, stderr bytes.Buffer
			if code := run(ctx, "merge", c.args, strings.NewReader(""), &stdout, &stderr); code != c.code || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("exit %d\n%s", code, stderr.String())
			}
			if _, err := os.Stat(calls); err == nil {
				t.Errorf("merge ran gh")
			}
		})
	}
}

// posting makes a checkout of a GitHub repository, and a gh that records
// each call in a file and fails. It returns a context to run in, in the
// checkout in a bots-mode loop with that gh on its PATH, the checkout, and
// the file.
func posting(t *testing.T) (context.Context, string, string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/o/r.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, gitenv.Without(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bin, calls := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\necho \"$*\" >> \"$FAKE_GH_CALLS\"\nexit 1\n"), 0o755)
	e := env.Process()
	e = e.Set("FAKE_GH_CALLS="+calls, "PATH="+bin+string(os.PathListSeparator)+e.Get("PATH"), "PR_LOOP_MODE=bots").In(dir)
	return env.With(t.Context(), e), dir, calls
}

// pr-loop may run outside the sandbox with the operator's login, so text
// it posts holds no secret's shape.
func TestOutgoingText(t *testing.T) {
	t.Parallel()
	ghCalled := func(calls string) bool {
		_, err := os.Stat(calls)
		return err == nil
	}
	pr := func(ctx context.Context, args ...string) (int, string) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, args[0], args[1:], strings.NewReader(""), &stdout, &stderr)
		return code, stderr.String()
	}
	// The fake gh fails, so the reply gets as far as GitHub and no further.
	t.Run("a reply of placeholders is posted", func(t *testing.T) {
		t.Parallel()
		ctx, dir, _ := posting(t)
		file := filepath.Join(dir, "reply.md")
		os.WriteFile(file, []byte("Set NGROK_AUTHTOKEN=$NGROK_AUTHTOKEN and send `Authorization: Bearer <token>`.\n"), 0o644)
		if code, stderr := pr(ctx, "reply", "T", "--body-file", file); code != 1 || strings.Contains(stderr, "refused") {
			t.Errorf("exit %d, %s", code, stderr)
		}
	})
	token := "ghp_" + strings.Repeat("a", 36)
	for name, args := range map[string]func(file string) []string{
		"a reply":         func(file string) []string { return []string{"reply", "T", "--body-file", file} },
		"a reply file":    func(file string) []string { return []string{"reply", "--from", file} },
		"a draft":         func(file string) []string { return []string{"draft", "T", "--outcome", "fix", "--body-file", file} },
		"an open's body":  func(file string) []string { return []string{"open", "--title", "T", "--body-file", file} },
		"an open's title": func(string) []string { return []string{"open", "--title", "Drop " + token, "--body-file", "body.md"} },
	} {
		t.Run("refused/a token in "+name, func(t *testing.T) {
			t.Parallel()
			ctx, dir, calls := posting(t)
			file := filepath.Join(dir, "token.md")
			os.WriteFile(file, []byte("=== thread T\nThe token "+token+" is gone.\n"), 0o644)
			code, stderr := pr(ctx, args(file)...)
			if code != 2 || !strings.Contains(stderr, "GitHub token") || strings.Contains(stderr, token) {
				t.Errorf("exit %d, %s", code, stderr)
			}
			if ghCalled(calls) {
				t.Error("gh was called")
			}
		})
	}

	t.Run("refused/a lease without its SHA", func(t *testing.T) {
		t.Parallel()
		ctx, _, calls := posting(t)
		if code, stderr := pr(ctx, "push", "--lease", "x", "x"); code != 1 || !strings.Contains(stderr, "BRANCH=SHA") || ghCalled(calls) {
			t.Errorf("exit %d, %s", code, stderr)
		}
	})
}
