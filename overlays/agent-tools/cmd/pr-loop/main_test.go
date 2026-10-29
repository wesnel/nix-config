package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
)

func TestRun(t *testing.T) {
	// A command run from a worktree that GIT_DIR, set by a git hook or
	// `rebase --exec` elsewhere, does not name is refused before it reads or
	// writes either repository.
	t.Run("GIT_DIR names another repository", func(t *testing.T) {
		dir := t.TempDir()
		cmd := exec.Command("git", "init", "-q", "-b", "main")
		cmd.Dir, cmd.Env = dir, gitenv.Without(os.Environ())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v\n%s", err, out)
		}
		gitenvtest.Decoy(t)
		os.Unsetenv("GIT_WORK_TREE")
		t.Chdir(dir)
		var stdout, stderr bytes.Buffer
		if code := run("end", nil, strings.NewReader(""), &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "GIT_DIR") {
			t.Errorf("exit %d\n%s", code, stderr.String())
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
			_, calls := posting(t)
			var stdout, stderr bytes.Buffer
			if code := run("merge", c.args, strings.NewReader(""), &stdout, &stderr); code != c.code || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("exit %d\n%s", code, stderr.String())
			}
			if _, err := os.Stat(calls); err == nil {
				t.Errorf("merge ran gh")
			}
		})
	}
}

// posting makes a checkout of a GitHub repository to run in, in a bots-mode
// loop, and a gh on PATH that records each call in the file it returns and
// fails.
func posting(t *testing.T) (string, string) {
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
	t.Setenv("FAKE_GH_CALLS", calls)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PR_LOOP_MODE", "bots")
	t.Chdir(dir)
	return dir, calls
}

// pr-loop may run outside the sandbox with the operator's login, so text
// it posts holds no secret's shape.
func TestOutgoingText(t *testing.T) {
	ghCalled := func(calls string) bool {
		_, err := os.Stat(calls)
		return err == nil
	}
	pr := func(args ...string) (int, string) {
		var stdout, stderr bytes.Buffer
		code := run(args[0], args[1:], strings.NewReader(""), &stdout, &stderr)
		return code, stderr.String()
	}
	// The fake gh fails, so the reply gets as far as GitHub and no further.
	t.Run("a reply of placeholders is posted", func(t *testing.T) {
		dir, _ := posting(t)
		file := filepath.Join(dir, "reply.md")
		os.WriteFile(file, []byte("Set NGROK_AUTHTOKEN=$NGROK_AUTHTOKEN and send `Authorization: Bearer <token>`.\n"), 0o644)
		if code, stderr := pr("reply", "T", "--body-file", file); code != 1 || strings.Contains(stderr, "refused") {
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
			dir, calls := posting(t)
			file := filepath.Join(dir, "token.md")
			os.WriteFile(file, []byte("=== thread T\nThe token "+token+" is gone.\n"), 0o644)
			code, stderr := pr(args(file)...)
			if code != 2 || !strings.Contains(stderr, "GitHub token") || strings.Contains(stderr, token) {
				t.Errorf("exit %d, %s", code, stderr)
			}
			if ghCalled(calls) {
				t.Error("gh was called")
			}
		})
	}

	t.Run("refused/a lease without its SHA", func(t *testing.T) {
		_, calls := posting(t)
		if code, stderr := pr("push", "--lease", "x", "x"); code != 1 || !strings.Contains(stderr, "BRANCH=SHA") || ghCalled(calls) {
			t.Errorf("exit %d, %s", code, stderr)
		}
	})
}
