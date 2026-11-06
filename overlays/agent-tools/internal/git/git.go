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

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Dir aims the functions that take no context (Run, OK, Bytes, RunEnv,
// RunLong, Toplevel, CheckLocation, CheckConfig) and WithDir at a
// directory, as At does; empty means the process's own. Nothing but
// pr-loop's tests sets it, since pr-loop runs git through those functions;
// In never reads it.
var Dir string

// Where is where git runs: an Env, in whose directory each command runs, and
// whether the caller aimed git there (At or Dir).
type Where struct {
	env env.Env
	at  bool
}

type atKey struct{}

// In is where a command run under ctx runs: in ctx's Env (env.From). It
// reads the Env and nothing else of ctx: the commands of the Where end at
// their own timeout (Timeout, LongTimeout), not when ctx does. BytesContext,
// Attached, AttachedStderr and AttachedQuiet take a ctx of their own to be
// ended by.
func In(ctx context.Context) Where {
	return Where{env: env.From(ctx), at: ctx.Value(atKey{}) != nil}
}

// At is ctx with git aimed at dir: its Env in dir, without gitenv.Location,
// which git sets for the commands it starts (hooks, aliases, `rebase
// --exec`) and which would send git to the caller's repository instead.
func At(ctx context.Context, dir string) context.Context {
	return context.WithValue(env.With(ctx, aimed(env.From(ctx), dir)), atKey{}, true)
}

// aimed is e in dir without gitenv.Location.
func aimed(e env.Env, dir string) env.Env { return e.Without(gitenv.Location...).In(dir) }

// process is In for a caller with no context: this process's environment,
// aimed at Dir when it is set.
func process() Where {
	e := env.Process()
	if Dir != "" {
		return Where{env: aimed(e, Dir), at: true}
	}
	return Where{env: e}
}

// WithDir is ctx aimed at Dir (At) where it is set, else ctx, for pr-loop,
// which runs git both through the functions that take no context and
// through code that takes one, so that both act on one repository.
func WithDir(ctx context.Context) context.Context {
	if Dir == "" {
		return ctx
	}
	return At(ctx, Dir)
}

// hardened keeps the repository from running programs through these tools. A
// sandboxed agent can write the repository's .git, and these tools run git on
// the host with the user's credentials, so hooks and the fsmonitor are off
// for every command. Repository config that names other programs is refused
// by CheckConfig instead, since a command-line value cannot clear it.
var hardened = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}

// Timeout bounds each command Run, RunEnv, Bytes and OK run, and LongTimeout
// each RunLong runs: these tools run git in repositories an agent can write,
// where a FIFO in place of a file git reads stalls it. A command that may
// take longer by nature, such as a rebase or a worktree's checkout, goes
// through RunLong; one that waits on the operator takes a context.
var (
	Timeout     = time.Minute
	LongTimeout = 10 * time.Minute
)

// commandContext is w.command, with the environment WithEnv gave ctx added
// after extra.
func commandContext(ctx context.Context, extra []string, args ...string) *exec.Cmd {
	more, _ := ctx.Value(envKey{}).([]string)
	return In(ctx).command(ctx, append(append([]string{}, extra...), more...), args...)
}

// command is the git on the PATH of w's Env with args, killed when ctx is
// done, with extra added to w's environment. A command aimed at a
// directory by a leading `-C DIR` runs without gitenv.Location, as one
// under At does. Other commands find the repository as git does, honouring
// the environment; Toplevel checks that the two agree.
func (w Where) command(ctx context.Context, extra []string, args ...string) *exec.Cmd {
	cmd := env.Command(env.With(ctx, w.env), "git", append(append([]string{}, hardened...), args...)...)
	base := w.env.Environ()
	if len(args) > 1 && args[0] == "-C" && args[1] != "" {
		base = gitenv.Without(base)
	}
	cmd.Env = append(base, extra...)
	return cmd
}

type envKey struct{}

// WithEnv makes every command run under ctx get env after the caller's
// own, so env overrides what the caller passes.
func WithEnv(ctx context.Context, env ...string) context.Context {
	extra, _ := ctx.Value(envKey{}).([]string)
	return context.WithValue(ctx, envKey{}, append(append([]string{}, extra...), env...))
}

// Bytes is Where.Bytes in this process's environment.
func Bytes(stdin []byte, args ...string) ([]byte, error) { return process().Bytes(stdin, args...) }

// RunEnv is Where.RunEnv in this process's environment.
func RunEnv(extra []string, args ...string) (string, error) { return process().RunEnv(extra, args...) }

// RunLong is Where.RunLong in this process's environment.
func RunLong(extra []string, args ...string) (string, error) {
	return process().RunLong(extra, args...)
}

// Bytes runs git and returns its stdout, within Timeout.
func (w Where) Bytes(stdin []byte, args ...string) ([]byte, error) {
	return w.within(Timeout, nil, stdin, args...)
}

// RunEnv is Run with extra added to w's environment.
func (w Where) RunEnv(extra []string, args ...string) (string, error) {
	out, err := w.within(Timeout, extra, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// RunLong is RunEnv within LongTimeout, for a command long by nature.
func (w Where) RunLong(extra []string, args ...string) (string, error) {
	out, err := w.within(LongTimeout, extra, nil, args...)
	return strings.TrimSpace(string(out)), err
}

// within runs git, killed after d, with an error that wraps
// context.DeadlineExceeded when that ended it.
func (w Where) within(d time.Duration, extra []string, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	cmd := w.command(ctx, extra, args...)
	bound(ctx, cmd)
	out, err := output(cmd, stdin, args)
	if err != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("%w: %w after %s", err, ctx.Err(), d)
	}
	return out, err
}

// BytesContext is Bytes, killed when ctx is done. A program the command
// started that outlives it, such as a signature verifier, gets a second
// more to let go of the command's output.
func BytesContext(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	more, _ := ctx.Value(envKey{}).([]string)
	return In(ctx).bytesContext(ctx, more, stdin, args...)
}

// bytesContext is BytesContext where w runs, with extra added.
func (w Where) bytesContext(ctx context.Context, extra []string, stdin []byte, args ...string) ([]byte, error) {
	cmd := w.command(ctx, extra, args...)
	bound(ctx, cmd)
	cmd.WaitDelay = time.Second
	return output(cmd, stdin, args)
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

// Toplevel is Where.Toplevel in this process's environment.
func Toplevel() (string, error) { return process().Toplevel() }

// Toplevel is the top of the worktree containing w's Env's directory, as
// git finds it. It refuses when the environment gives git another git
// directory than that worktree's own, since the commands aimed at the
// worktree would act on one repository and the others on another. A git
// directory outside its worktree is unsupported for that reason, and so is
// a worktree the repository's own config puts elsewhere. Where the caller
// aimed git at a directory (Aimed) there is nothing to check.
func (w Where) Toplevel() (string, error) {
	top, err := w.Run("rev-parse", "--show-toplevel")
	if err != nil || w.Aimed() {
		return top, err
	}
	named, err := w.Run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	own, err := w.Run("-C", top, "rev-parse", "--absolute-git-dir")
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

// Aimed reports whether the caller aimed git at a directory, with At or Dir:
// git then finds the repository from that directory, and the Env holds none
// of gitenv.Location. A Where from In of a context never aimed is not Aimed,
// whatever its Env holds.
func (w Where) Aimed() bool { return w.at }

// CheckLocation is Where.CheckLocation in this process's environment.
func CheckLocation() error { return process().CheckLocation() }

// CheckLocation is Toplevel for a command that only needs the check. Outside
// a worktree there is nothing to check.
func (w Where) CheckLocation() error {
	if !w.OK("rev-parse", "--show-toplevel") {
		return nil
	}
	_, err := w.Toplevel()
	return err
}

func canonical(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// Run is Where.Run in this process's environment.
func Run(args ...string) (string, error) { return process().Run(args...) }

// OK is Where.OK in this process's environment.
func OK(args ...string) bool { return process().OK(args...) }

// Run runs git and returns its stdout with surrounding whitespace removed.
func (w Where) Run(args ...string) (string, error) {
	out, err := w.Bytes(nil, args...)
	return strings.TrimSpace(string(out)), err
}

// OK reports whether git exits successfully within Timeout.
func (w Where) OK(args ...string) bool {
	_, err := w.within(Timeout, nil, nil, args...)
	return err == nil
}

// Attached runs git connected to this process's terminal, for commands that
// ask for a key touch or a passphrase, killed when ctx is done. It returns
// the exit code.
func Attached(ctx context.Context, extra []string, args ...string) int {
	cmd := commandContext(ctx, extra, args...)
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

// KillingTree marks ctx so that a command BytesContext, AttachedStderr or AttachedQuiet
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

type watchKey struct{}

type watch struct{ started, ended func(pid int) }

// Watching marks ctx so that each command AttachedStderr and AttachedQuiet
// run under it calls started with its process ID once it has started, and
// ended once it has ended. Either may be nil.
func Watching(ctx context.Context, started, ended func(pid int)) context.Context {
	return context.WithValue(ctx, watchKey{}, watch{started, ended})
}

// run runs cmd, readied by bound. A command in a process group of its own
// gets a watchdog in that group, which kills the group once this process's
// end of a pipe closes: when cmd ends, and when this process dies, however
// it dies, SIGKILL included, which no handler here sees.
func run(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	hooks, _ := ctx.Value(watchKey{}).(watch)
	if hooks.started != nil {
		hooks.started(cmd.Process.Pid)
	}
	if hooks.ended != nil {
		defer hooks.ended(cmd.Process.Pid)
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

// AttachedStderr is Attached with the terminal the caller gives, stdin and
// errs, also returning what the command wrote to standard error, which
// reaches errs as it is written. Git sees a pipe there rather than the
// terminal, so it prints no progress meters. A stdin that is not an
// *os.File is copied to the command until it ends, which the command's end
// waits for. See bound for how the end of ctx ends it.
func AttachedStderr(ctx context.Context, extra []string, stdin io.Reader, errs io.Writer, args ...string) (int, string) {
	cmd := commandContext(ctx, extra, args...)
	bound(ctx, cmd)
	var stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, os.Stdout, io.MultiWriter(errs, &stderr)
	if err := run(ctx, cmd); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), stderr.String()
		}
		return 1, stderr.String() + err.Error()
	}
	return 0, stderr.String()
}

// AttachedQuiet is AttachedStderr with standard output discarded and
// standard error written to stderr alone, for a caller that decides what
// of it to show. It returns the exit code; a failure to run git at all is
// written to stderr too. See bound for how the end of ctx ends it.
func AttachedQuiet(ctx context.Context, extra []string, stdin io.Reader, stderr io.Writer, args ...string) int {
	cmd := commandContext(ctx, extra, args...)
	bound(ctx, cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, io.Discard, stderr
	if err := run(ctx, cmd); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		io.WriteString(stderr, err.Error())
		return 1
	}
	return 0
}
