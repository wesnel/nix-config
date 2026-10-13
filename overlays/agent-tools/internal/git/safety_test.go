package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestHardening plants what a sandboxed agent could write into .git and
// checks that this package's git neither runs it nor proceeds past config
// that names a program.
func TestHardening(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, dir, "branch", "feature")
	marker := filepath.Join(dir, "RAN")
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	gitIn(t, dir, "config", "core.fsmonitor", "touch "+marker+"; false")
	Dir = dir
	defer func() { Dir = "" }()

	if _, err := Run("worktree", "add", "-q", filepath.Join(dir, "wt"), "feature"); err != nil {
		t.Fatal(err)
	}
	Run("status", "--porcelain")
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook or the fsmonitor ran")
	}
	if err := CheckConfig(dir); err != nil {
		t.Errorf("hooks and the fsmonitor are neutralised, not refused: %v", err)
	}

	for _, key := range []string{"core.sshCommand", "filter.x.clean", "diff.x.textconv", "credential.helper", "gpg.program", "include.path", "url.https://evil/.insteadOf", "hook.x.command"} {
		gitIn(t, dir, "config", key, "x")
		if err := CheckConfig(dir); err == nil {
			t.Errorf("%s was accepted", key)
		}
		gitIn(t, dir, "config", "--unset", key)
	}
	gitIn(t, dir, "config", "extensions.worktreeConfig", "true")
	gitIn(t, dir, "config", "--worktree", "core.sshCommand", "x")
	if err := CheckConfig(dir); err == nil {
		t.Error("worktree config was not checked")
	}
}
