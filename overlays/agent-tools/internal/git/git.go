// Package git runs the git on PATH, so the user's own configuration (signing
// key, SSH) applies to everything these tools do.
package git

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Dir is the working directory for every command; empty means the process's.
var Dir string

// hardened keeps the repository from running programs through these tools. A
// sandboxed agent can write the repository's .git, and these tools run git on
// the host with the user's credentials, so hooks and the fsmonitor are off
// for every command. Repository config that names other programs is refused
// by CheckConfig instead, since a command-line value cannot clear it.
var hardened = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}

// command is git with args and env added to this process's environment. A
// command aimed at a directory, through Dir or a leading `-C DIR`, runs
// without gitenv.Location, which git sets for the commands it starts and
// which would otherwise send it to the caller's repository instead. Other
// commands find the repository as git does, honouring the environment;
// Toplevel checks that the two agree.
func command(env []string, args ...string) *exec.Cmd {
	return commandContext(context.Background(), env, args...)
}

// commandContext is command, killed when ctx is done.
func commandContext(ctx context.Context, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, hardened...), args...)...)
	cmd.Dir = Dir
	base := os.Environ()
	if Dir != "" || (len(args) > 1 && args[0] == "-C" && args[1] != "") {
		base = gitenv.Without(base)
	}
	cmd.Env = append(base, env...)
	return cmd
}

// Bytes runs git and returns its stdout.
func Bytes(stdin []byte, args ...string) ([]byte, error) {
	return bytesEnv(nil, stdin, args...)
}

// RunEnv is Run with env added to this process's environment.
func RunEnv(env []string, args ...string) (string, error) {
	out, err := bytesEnv(env, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// BytesContext is Bytes, killed when ctx is done. A program the command
// started that outlives it, such as a signature verifier, gets a second
// more to let go of the command's output.
func BytesContext(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	cmd := commandContext(ctx, nil, args...)
	cmd.WaitDelay = time.Second
	return output(cmd, stdin, args)
}

func bytesEnv(env []string, stdin []byte, args ...string) ([]byte, error) {
	return output(command(env, args...), stdin, args)
}

func output(cmd *exec.Cmd, stdin []byte, args []string) ([]byte, error) {
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// Toplevel is the top of the worktree containing Dir, or the process's
// directory, as git finds it. It refuses when the environment gives git
// another git directory than that worktree's own, since the commands aimed
// at the worktree would act on one repository and the others on another.
// A git directory outside its worktree is unsupported for that reason.
func Toplevel() (string, error) {
	top, err := Run("rev-parse", "--show-toplevel")
	if err != nil || Dir != "" {
		return top, err
	}
	named, err := Run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	own, err := Run("-C", top, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	if canonical(named) != canonical(own) {
		return "", refusal.New("the environment points git at %s, but the worktree at %s has %s; run the command "+
			"where GIT_DIR and GIT_WORK_TREE are unset, which `env -u GIT_DIR -u GIT_WORK_TREE` does, "+
			"rather than from a git hook, alias or `rebase --exec`", named, top, own)
	}
	return top, nil
}

// CheckLocation is Toplevel for a command that only needs the check. Outside
// a worktree there is nothing to check.
func CheckLocation() error {
	if !OK("rev-parse", "--show-toplevel") {
		return nil
	}
	_, err := Toplevel()
	return err
}

func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// Run runs git and returns its stdout with surrounding whitespace removed.
func Run(args ...string) (string, error) {
	out, err := Bytes(nil, args...)
	return strings.TrimSpace(string(out)), err
}

// OK reports whether git exits successfully.
func OK(args ...string) bool {
	return command(nil, args...).Run() == nil
}

// PatchID is `git patch-id --stable` of the change from the merge base of
// base and tip to tip, in the repository repo names as git's leading
// arguments (`-C DIR` or `--git-dir DIR`), or "" for no change. It is the
// same however the change's commits are cut, reworded, signed or rebased,
// and differs for a different change. The diff is spelled out in full
// (prefixes, renames, binary contents) so that diff settings cannot change
// it.
func PatchID(repo []string, base, tip string) (string, error) {
	return PatchIDWith(func(stdin []byte, args ...string) ([]byte, error) {
		return Bytes(stdin, append(append([]string{}, repo...), args...)...)
	}, base, tip)
}

// PatchIDWith is PatchID with run running git in the repository, given
// stdin and the arguments, for a repository this package's commands must
// not run in.
func PatchIDWith(run func(stdin []byte, args ...string) ([]byte, error), base, tip string) (string, error) {
	diff, err := run(nil, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--full-index", "--binary",
		"--src-prefix=a/", "--dst-prefix=b/", "--diff-algorithm=myers", "--end-of-options", base+"..."+tip)
	if err != nil || len(diff) == 0 {
		return "", err
	}
	out, err := run(diff, "patch-id", "--stable")
	if err != nil {
		return "", err
	}
	id, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return id, nil
}

// Attached runs git connected to this process's terminal, for commands that
// ask for a key touch or a passphrase, killed when ctx is done. It returns
// the exit code.
func Attached(ctx context.Context, env []string, args ...string) int {
	cmd := commandContext(ctx, env, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}

// AttachedStderr is Attached, also returning what the command wrote to
// stderr, which reaches this process's stderr as it is written. Git sees a
// pipe there rather than the terminal, so it prints no progress meters.
func AttachedStderr(ctx context.Context, env []string, args ...string) (int, string) {
	cmd := commandContext(ctx, env, args...)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), stderr.String()
		}
		return 1, stderr.String() + err.Error()
	}
	return 0, stderr.String()
}
