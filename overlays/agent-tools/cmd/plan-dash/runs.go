package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/prreview"
	"github.com/wesnel/agent-tools/internal/tty"
	"github.com/wesnel/agent-tools/internal/usage/runs"
)

// stdin is what usage hook reads Claude Code's hook input from.
var stdin io.Reader = os.Stdin

// hookInputCap bounds how much of the hook's input is read.
const hookInputCap = 1 << 20

// claudeConfigDir is the Claude Code config directory this process's
// sessions use: $CLAUDE_CONFIG_DIR where absolute, else ~/.claude.
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}

// usageHook runs usage hook: Claude Code's SessionStart hook. It prints
// nothing, which would become the session's context, and fails only with
// an exit status Claude Code does not block on.
func usageHook(args []string, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	input, err := io.ReadAll(io.LimitReader(stdin, hookInputCap))
	if err != nil {
		fmt.Fprintf(stderr, "plan-dash: usage hook: %v\n", err)
		return 1
	}
	now := time.Now()
	r, ok, err := runs.FromSessionStart(input, os.Getenv(runs.Env), now, hostname(), claudeConfigDir())
	if err == nil && ok {
		err = runs.Default().Append(r, now)
	}
	if err != nil {
		fmt.Fprintf(stderr, "plan-dash: usage hook: %v\n", err)
		return 1
	}
	return 0
}

// usageRun runs usage run: argv, with this process's standard streams
// and environment, and then the record of its last result. A tag it
// cannot read is reported and the command runs untagged. With --pr-loop,
// the tag is claude-pr-loop's own (prLoopTag), never an inherited one,
// and the command gets it in place of any.
func usageRun(args []string, stdout, stderr io.Writer) int {
	prLoop := false
	if len(args) > 0 && args[0] == "--pr-loop" {
		prLoop, args = true, args[1:]
	}
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	var tag runs.Tag
	tagged := false
	env := os.Environ()
	if prLoop {
		root, _ := prreview.Root()
		cwd, _ := os.Getwd()
		tag, tagged = prLoopTag(cwd, root, os.Getenv(runs.CategoryEnv), stderr), true
		env = slices.DeleteFunc(env, func(kv string) bool {
			return strings.HasPrefix(kv, runs.Env+"=") || strings.HasPrefix(kv, runs.CategoryEnv+"=")
		})
		env = append(env, runs.Env+"="+tag.String())
	} else if s := os.Getenv(runs.Env); s != "" {
		t, err := runs.ParseTag(s)
		if err != nil {
			fmt.Fprintf(stderr, "plan-dash: usage run: %v; running the command unrecorded\n", err)
		}
		tag, tagged = t, err == nil
	}

	cmd := exec.Command(args[1], args[2:]...) //nolint:gosec // the launcher's own command, run as given
	cmd.Stdin, cmd.Stderr, cmd.Env = stdin, stderr, env
	// What the command leaves running may hold its output open; once it
	// has exited, its output is read for this long at most.
	cmd.WaitDelay = waitDelay
	var stream runs.Stream
	// A terminal goes to the command untouched, so an interactive session
	// keeps it; a print session writes no result there anyway.
	if isTerminal(stdout) || !tagged {
		cmd.Stdout = stdout
	} else {
		cmd.Stdout = io.MultiWriter(stdout, &stream)
	}
	// claude-pr-loop's command, sandbox-relay, passes no signal on to the
	// session it runs, so off a terminal it leads a process group of its
	// own, and a signal sent to this process goes to the whole group. Any
	// other command stays in this process's group, which a signal to the
	// caller's group reaches whole. With a terminal it stays in the
	// terminal's foreground group too: a group of its own would be stopped
	// on reading from it, and the terminal's signals reach it without help.
	onTerminal := isTerminal(stdin) || isTerminal(stdout) || isTerminal(stderr)
	group := prLoop && !onTerminal
	cmd.SysProcAttr = childAttr(group)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	// The parent-death signal is tied to the thread that starts the
	// command, which must outlive it.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(stderr, "plan-dash: usage run: %v\n", err)
		if errors.Is(err, fs.ErrPermission) {
			return 126
		}
		return 127
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-signals:
				// The command may have exited already.
				switch {
				case group:
					_ = syscall.Kill(-cmd.Process.Pid, s.(syscall.Signal))
				case s != os.Interrupt || !onTerminal:
					_ = cmd.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	err := cmd.Wait()
	close(done)

	code := 0
	complete := true
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		code = exit.ExitCode()
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
	case errors.Is(err, exec.ErrWaitDelay):
		// The command exited 0, and its status stands: what it wrote before
		// it exited had waitDelay to be read; what it left running held its
		// output after that, which may hold a later result, so none is
		// recorded.
		fmt.Fprintf(stderr, "plan-dash: usage run: what %s left running held its output %s after it exited; "+
			"the rest was not read, and the run is not recorded\n", args[1], waitDelay)
		complete = false
	case err != nil:
		fmt.Fprintf(stderr, "plan-dash: usage run: %v\n", err)
		code = 1
	}
	now := time.Now()
	if r, ok := stream.Record(tag, now, hostname(), claudeConfigDir()); tagged && ok && complete {
		if err := runs.Default().Append(r, now); err != nil {
			fmt.Fprintf(stderr, "plan-dash: usage run: recording the run: %v\n", err)
		}
	}
	return code
}

// ledgerRuns is the run ledger's Claude Code runs since since, by session
// ID, saying on stderr how many of its lines were skipped.
func ledgerRuns(since time.Time, stderr io.Writer) (map[string]runs.Record, error) {
	recorded, unreadable, err := runs.Default().Read(since)
	if unreadable > 0 {
		fmt.Fprintf(stderr, "plan-dash: %d lines of the run ledger are not valid records and were skipped\n", unreadable)
	}
	bySession := map[string]runs.Record{}
	for _, r := range recorded {
		if r.SessionID != "" {
			bySession[r.SessionID] = r
		}
	}
	return bySession, err
}

// reviewDir is a review's directory under the review root,
// OWNER/NAME/N/HEAD12, as pr-loop review makes it.
// OWNER and NAME are as prreview.ValidName takes them.
var reviewDir = regexp.MustCompile(`^([^/]+)/([^/]+)/([1-9][0-9]{0,8})/[0-9a-f]{12}$`)

// prLoopTag is the tag of a claude-pr-loop session started in cwd: in a
// review's directory under root, Review with its pull request, or Triage
// where category, runs.CategoryEnv's value, says so; anywhere else, Loop.
// Another category is reported and ignored.
func prLoopTag(cwd, root, category string, stderr io.Writer) runs.Tag {
	if category != "" && category != runs.Triage {
		fmt.Fprintf(stderr, "plan-dash: usage run: %s takes %s alone, not %q\n", runs.CategoryEnv, runs.Triage, category)
		category = ""
	}
	if root == "" || cwd == "" {
		return runs.Tag{Category: runs.Loop}
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	if c, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = c
	}
	rel, err := filepath.Rel(root, cwd)
	m := reviewDir.FindStringSubmatch(filepath.ToSlash(rel))
	if err != nil || m == nil || !prreview.ValidName(m[1]) || !prreview.ValidName(m[2]) {
		return runs.Tag{Category: runs.Loop}
	}
	n, _ := strconv.Atoi(m[3])
	t := runs.Tag{Category: runs.Review, Repo: m[1] + "/" + m[2], PR: n}
	if category == runs.Triage {
		t.Category = runs.Triage
	}
	return t
}

// terminal reports whether a file is a terminal.
var terminal = tty.IsTerminal

// waitDelay is how long usage run reads a command's output once it has
// exited.
var waitDelay = 10 * time.Second

func isTerminal(s any) bool {
	f, ok := s.(*os.File)
	return ok && terminal(f)
}
