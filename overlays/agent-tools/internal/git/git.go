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
	"syscall"
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

// commandContext is command, killed when ctx is done, with the environment
// WithEnv gave ctx added last.
func commandContext(ctx context.Context, env []string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append(append([]string{}, hardened...), args...)...)
	cmd.Dir = Dir
	base := os.Environ()
	if Dir != "" || (len(args) > 1 && args[0] == "-C" && args[1] != "") {
		base = gitenv.Without(base)
	}
	extra, _ := ctx.Value(envKey{}).([]string)
	cmd.Env = append(append(base, env...), extra...)
	return cmd
}

type envKey struct{}

// WithEnv makes every command run under ctx get env after the caller's
// own, so env overrides what the caller passes.
func WithEnv(ctx context.Context, env ...string) context.Context {
	extra, _ := ctx.Value(envKey{}).([]string)
	return context.WithValue(ctx, envKey{}, append(append([]string{}, extra...), env...))
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

type treeKey struct{}

// KillingTree marks ctx so that a command AttachedStderr or AttachedQuiet
// runs under it runs in a process group of its own, which the end of ctx
// kills whole: git and whatever git started, such as a signer waiting on a
// locked key, which killing git alone leaves running. Use it only for a
// command run without a terminal: the group is not the terminal's
// foreground group, so a program in it that reads the terminal is stopped.
func KillingTree(ctx context.Context) context.Context {
	return context.WithValue(ctx, treeKey{}, true)
}

// bound readies cmd for the end of ctx: with a deadline or KillingTree, a
// program git started that outlives git, holding its pipes, does not keep
// the caller waiting; with KillingTree, the end kills that program too.
func bound(ctx context.Context, cmd *exec.Cmd) {
	tree := ctx.Value(treeKey{}) != nil
	if _, ok := ctx.Deadline(); ok || tree {
		cmd.WaitDelay = 2 * time.Second
	}
	if tree {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	}
}

// Started and Ended, when set, are called with the process ID of each
// command AttachedStderr and AttachedQuiet run, once it has started and
// once it has ended.
var Started, Ended func(pid int)

// run runs cmd, readied by bound. A command in a process group of its own
// gets a watchdog in that group, which kills the group once this process's
// end of a pipe closes: when cmd ends, and when this process dies, however
// it dies, SIGKILL included, which no handler here sees.
func run(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if Started != nil {
		Started(cmd.Process.Pid)
	}
	if Ended != nil {
		defer Ended(cmd.Process.Pid)
	}
	if ctx.Value(treeKey{}) == nil {
		return cmd.Wait()
	}
	r, w, err := os.Pipe()
	if err == nil {
		dog := exec.Command("sh", "-c", "read -r line; kill -KILL 0")
		dog.Stdin = r
		dog.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: cmd.Process.Pid}
		// A git that has already ended takes its group with it, and the
		// watchdog cannot join; there is nothing left for it to kill.
		if dog.Start() == nil {
			defer dog.Wait()
		}
		r.Close()
		defer w.Close()
	}
	return cmd.Wait()
}

// AttachedStderr is Attached, also returning what the command wrote to
// stderr, which reaches this process's stderr as it is written. Git sees a
// pipe there rather than the terminal, so it prints no progress meters. See
// bound for how the end of ctx ends it.
func AttachedStderr(ctx context.Context, env []string, args ...string) (int, string) {
	cmd := commandContext(ctx, env, args...)
	bound(ctx, cmd)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, io.MultiWriter(os.Stderr, &stderr)
	if err := run(ctx, cmd); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), stderr.String()
		}
		return 1, stderr.String() + err.Error()
	}
	return 0, stderr.String()
}

// AttachedQuiet is Attached with standard output discarded and standard
// error written to stderr alone, for a caller that decides what of it to
// show. It returns the exit code; a failure to run git at all is written to
// stderr too. See bound for how the end of ctx ends it.
func AttachedQuiet(ctx context.Context, env []string, stderr io.Writer, args ...string) int {
	cmd := commandContext(ctx, env, args...)
	bound(ctx, cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, io.Discard, stderr
	if err := run(ctx, cmd); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		io.WriteString(stderr, err.Error())
		return 1
	}
	return 0
}
