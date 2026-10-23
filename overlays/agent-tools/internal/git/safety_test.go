package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// A repository whose config git cannot read in time is refused, not
// passed: git 2.56 reads the alternates file to list the config, and
// either git reads the config file itself, either of which a FIFO nothing
// writes stalls.
func TestCheckConfigTimeout(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	config := filepath.Join(dir, ".git", "config")
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(config, 0o644); err != nil {
		t.Fatal(err)
	}
	old := ConfigTimeout
	ConfigTimeout = time.Second
	t.Cleanup(func() { ConfigTimeout = old })
	done := make(chan error, 1)
	go func() { done <- CheckConfig(dir) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), dir) {
			t.Errorf("CheckConfig: %v", err)
		}
	case <-time.After(15 * time.Second):
		f, _ := os.OpenFile(config, os.O_WRONLY, 0)
		f.Close()
		t.Fatal("CheckConfig hangs on a FIFO")
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// planted makes a repository with what a sandboxed agent could write into
// .git: a hook and an fsmonitor that each create the returned marker file.
// The package's commands act on it for the rest of the test.
func planted(t *testing.T) (dir, marker string) {
	t.Helper()
	dir = t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, dir, "branch", "feature")
	marker = filepath.Join(dir, "RAN")
	hook := filepath.Join(dir, ".git", "hooks", "post-checkout")
	os.WriteFile(hook, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o755)
	gitIn(t, dir, "config", "core.fsmonitor", "touch "+marker+"; false")
	useDir(t, dir)
	return dir, marker
}

// TestHardening checks that this package's git neither runs what an agent
// planted in .git nor proceeds past config that names a program.
func TestHardening(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	t.Run("hooks and the fsmonitor do not run", func(t *testing.T) {
		dir, marker := planted(t)
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
	})

	for _, key := range []string{"core.sshCommand", "filter.x.clean", "diff.x.textconv", "credential.helper", "gpg.program", "include.path", "url.https://evil/.insteadOf", "hook.x.command"} {
		t.Run("refuses "+key, func(t *testing.T) {
			dir, _ := planted(t)
			gitIn(t, dir, "config", key, "x")
			if err := CheckConfig(dir); err == nil {
				t.Errorf("%s was accepted", key)
			}
		})
	}

	t.Run("checks worktree config", func(t *testing.T) {
		dir, _ := planted(t)
		gitIn(t, dir, "config", "extensions.worktreeConfig", "true")
		gitIn(t, dir, "config", "--worktree", "core.sshCommand", "x")
		if err := CheckConfig(dir); err == nil {
			t.Error("worktree config was not checked")
		}
	})
}
