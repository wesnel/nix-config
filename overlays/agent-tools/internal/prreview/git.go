package prreview

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// transport is the one protocol git may fetch over; tests serve a local
// remote over "file".
var transport = "https"

// safe is the configuration every git command on a review clone, or a
// checkout or copy of it, runs with. The pull request chooses what is in
// them, so nothing it names may run on this side: no hook or fsmonitor, no
// link that a reader would follow out of the checkout, no submodule and no
// transport but transport, with every object checked as it arrives.
func safe() []string {
	return []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "core.symlinks=false",
		"-c", "submodule.recurse=false",
		"-c", "protocol.allow=never",
		"-c", "protocol." + transport + ".allow=always",
		"-c", "transfer.fsckObjects=true",
	}
}

// environment is the process's environment without anything that points
// git at a repository or at configuration, and with neither the user's nor
// the system's config or attributes: the user's defines filters, such as Git
// LFS's, which a pull request's .gitattributes would otherwise run here, and
// includes a secret. extra is added last.
func environment(extra []string) []string {
	var env []string
	for _, kv := range gitenv.Without(os.Environ()) {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_CONFIG") || strings.HasPrefix(name, "GIT_ATTR") ||
			name == "GIT_TERMINAL_PROMPT" || name == "GIT_ASKPASS" || name == "SSH_ASKPASS" || name == "GIT_EXEC_PATH" {
			continue
		}
		env = append(env, kv)
	}
	return append(append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_ATTR_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1"), extra...)
}

// gitCommand is git in dir with the safe configuration. env is added to
// environment's, and is where a credential helper goes, never argv.
func gitCommand(ctx context.Context, dir string, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append(safe(), args...)...)
	cmd.Dir = dir
	cmd.Env = environment(env)
	return cmd
}

// gitOut runs git as gitCommand does and returns its stdout.
func gitOut(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := gitCommand(ctx, dir, env, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// gitRun is gitOut without surrounding whitespace.
func gitRun(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := gitOut(ctx, dir, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// Checkout makes a detached worktree of the clone at rev in dir, with the
// safe configuration: the pull request's attributes find no filter to run,
// and its links become files holding their targets.
func Checkout(ctx context.Context, clone, dir, rev string) error {
	_, err := gitRun(ctx, clone, "worktree", "add", "--quiet", "--detach", dir, rev+"^{commit}")
	return err
}

// RemoveCheckout removes a checkout Checkout made, and the clone's record
// of it.
func RemoveCheckout(clone, dir string) {
	ctx := context.Background()
	if _, err := gitRun(ctx, clone, "worktree", "remove", "--force", "--force", dir); err != nil {
		os.RemoveAll(dir)
		gitRun(ctx, clone, "worktree", "prune")
	}
}
