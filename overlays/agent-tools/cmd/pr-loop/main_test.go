package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
)

// A command run from a worktree that GIT_DIR, set by a git hook or
// `rebase --exec` elsewhere, does not name is refused before it reads or
// writes either repository.
func TestRepositoryTheEnvironmentNames(t *testing.T) {
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
}

func TestRelayRefusal(t *testing.T) {
	for _, argv := range [][]string{
		{},
		{"sign"},
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
	} {
		if relayRefusal(argv) == "" {
			t.Errorf("%v was accepted", argv)
		}
	}
	for _, argv := range [][]string{
		{"reply", "T", "--body-file", "-"},
		{"reply", "--from", "-"},
		{"status", "12"},
		{"open", "--title", "T", "--body-file", "-", "--base", "parent", "x"},
		{"setup-command"},
	} {
		if reason := relayRefusal(argv); reason != "" {
			t.Errorf("%v was refused: %s", argv, reason)
		}
	}
}
