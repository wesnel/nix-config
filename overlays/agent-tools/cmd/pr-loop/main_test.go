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
		if code := run("end", nil, "", false, strings.NewReader(""), &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "GIT_DIR") {
			t.Errorf("exit %d\n%s", code, stderr.String())
		}
	})
}

// posting makes a checkout of a GitHub repository to run in, and a gh on
// PATH that records each call in the file it returns and fails.
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
	t.Setenv("PR_LOOP_SESSION", "")
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
		code := run(args[0], args[1:], "", false, strings.NewReader(""), &stdout, &stderr)
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
}

func TestRelayRefusal(t *testing.T) {
	for _, argv := range [][]string{
		{},
		{"end"},
		{"guard"},
		{"reply", "T", "--body-file", "/etc/passwd"},
		{"reply", "T", "--body-file=/etc/passwd"},
		{"reply", "T", "-body-file", "-"},
		{"reply", "T", "--body-file"},
		{"open", "--title", "T", "--body-file", "/etc/passwd", "x"},
		{"reply", "--from", "/etc/passwd"},
		{"reply", "--from=/etc/passwd"},
		{"reply", "-from", "-"},
		{"reply", "--from"},
		{"setup"},
		{"setup", "/x"},
		// A relayed review names only its own pull request, never a path,
		// another repository, Codex's model or a run of the code.
		{"review"},
		{"review", "o/r#7"},
		{"review", "--run", "7"},
		{"review", "--model", "m", "7"},
		{"review", "--timeout", "5", "7"},
		{"review", "--reviewer", "codex"},
		{"review", "--reviewer", "other", "7"},
		{"review", "7", "8"},
		{"review", "--check", "/etc/passwd", "7"},
		{"review", "settle", "7", "x"},
		{"review", "--check", "settle", "7"},
	} {
		t.Run("refused/"+argvName(argv), func(t *testing.T) {
			if relayRefusal(argv) == "" {
				t.Errorf("%v was accepted", argv)
			}
		})
	}
	for _, argv := range [][]string{
		{"reply", "T", "--body-file", "-"},
		{"reply", "--from", "-"},
		{"status", "12"},
		{"open", "--title", "T", "--body-file", "-", "--base", "parent", "x"},
		{"setup-command"},
		{"stack", "12"},
		{"link", "12", "13"},
		{"review", "7"},
		{"review", "--reviewer", "subagent", "7"},
		{"review", "--reviewer=codex", "7"},
		{"review", "--check", "7"},
		{"review", "settle", "7"},
		{"sign-check"},
	} {
		t.Run("accepted/"+argvName(argv), func(t *testing.T) {
			if reason := relayRefusal(argv); reason != "" {
				t.Errorf("%v was refused: %s", argv, reason)
			}
		})
	}
}

// argvName names a subtest after the arguments it relays.
func argvName(argv []string) string {
	if len(argv) == 0 {
		return "no command"
	}
	return strings.Join(argv, " ")
}
