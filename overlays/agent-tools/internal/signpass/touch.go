package signpass

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
)

// refusedByKey matches what ssh, ssh-keygen and gpg print when the hardware
// key refuses to sign, as it does when its touch is missed.
var refusedByKey = regexp.MustCompile(`agent refused operation|signing failed|sign_and_send_pubkey|failed to sign the data`)

// unwritten matches what git prints when it cannot write a commit, as when
// its signature fails for any reason.
var unwritten = regexp.MustCompile(`failed to write commit object`)

// With no terminal to ask, a step the key refused is retried Retries times,
// each after RetryWait, and the retried signature, with everything git
// started for it, is killed after RetryTimeout. One missed touch locks the
// key, and the next request then waits on pinentry without end, so a retry
// must never run unbounded, and one is all a locked key allows.
var (
	Retries      = 1
	RetryWait    = 10 * time.Second
	RetryTimeout = 60 * time.Second
)

// retrier decides how a step that git ran again after the key refused it is
// retried, counting the automatic retries of each step on its own.
type retrier struct {
	step    string
	n       int
	retried bool
	// killed says why the last automatic retry was killed, once the function
	// next returned with it has run, or is empty.
	killed string
}

// next reports whether to run step again, and under which context: on a
// terminal the operator decides (retryTouch); otherwise the retry comes
// after RetryWait, and its context kills the command's process group at
// RetryTimeout or when the pass is interrupted or hung up on. That group is
// not the terminal's foreground group, so the command must stop after the
// one step it retries. step must name the same work however often git reschedules
// it.
func (r *retrier) next(ctx context.Context, what, step, stderr string) (context.Context, context.CancelFunc, bool) {
	none := func() {}
	if !refusedByKey.MatchString(stderr) {
		return ctx, none, false
	}
	if isTerminal(os.Stdin) {
		return ctx, none, retryTouch(ctx, what, stderr)
	}
	if step != r.step {
		r.step, r.n = step, 0
	}
	if r.n >= Retries {
		return ctx, none, false
	}
	r.n++
	r.retried = true
	fmt.Fprintf(os.Stderr, "%s: touch the key now (retry %d of %d)\n", what, r.n, Retries)
	select {
	case <-time.After(RetryWait):
	case <-ctx.Done():
		return ctx, none, false
	}
	// An interrupt reaches only the terminal's foreground group, which the
	// retry is not in, so the pass catches it and kills the retry itself; one
	// the pass was started ignoring, as nohup ignores a hangup, it ignores.
	var sigs []os.Signal
	for _, s := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(s) {
			sigs = append(sigs, s)
		}
	}
	interrupt, stop := ctx, context.CancelFunc(func() {})
	if len(sigs) > 0 { // with none, NotifyContext would catch every signal
		interrupt, stop = signal.NotifyContext(ctx, sigs...)
	}
	retry, cancel := context.WithTimeout(interrupt, RetryTimeout)
	r.killed = ""
	return git.KillingTree(retry), func() {
		switch {
		case errors.Is(retry.Err(), context.DeadlineExceeded):
			r.killed = "did not finish within " + RetryTimeout.String()
		case interrupt.Err() != nil:
			r.killed = "was interrupted"
		}
		cancel()
		stop()
	}, true
}

// locked is what to add to the error of a step that failed after an automatic
// retry.
func (r *retrier) locked() string {
	if !r.retried {
		return ""
	}
	return "; a missed touch can lock the key, which then waits on pinentry for its PIN, so unlock it there before resuming"
}

// retryTouch reports whether to retry what the key refused, given the
// stderr of the command that failed: only when that stderr shows the key's
// refusal and standard input is a terminal, on which the operator answers
// Enter to retry or q to stop. A ctx done before the answer stops: where
// SIGINT is caught, it is how Ctrl-C at the question still stops the pass.
func retryTouch(ctx context.Context, what, stderr string) bool {
	if !refusedByKey.MatchString(stderr) || !isTerminal(os.Stdin) {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s: the key refused to sign. Touch missed. Enter to retry, q to stop: ", what)
	answers := make(chan string, 1)
	go func() {
		answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && answer == "" {
			answer = "q"
		}
		answers <- answer
	}()
	select {
	case answer := <-answers:
		return strings.TrimSpace(answer) != "q"
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr)
		return false
	}
}

// NotifyID is the --notify ID the pass runs with, which every command it
// prints for resuming carries.
var NotifyID string

// resumeCommand is the command that resumes the pass on branch, as one line
// to run in fish or a POSIX shell. The branch goes last: flag parsing stops
// at the first argument that is not a flag.
func resumeCommand(branch string, push, setUpstream bool) string {
	cmd := self() + " --resume"
	switch {
	case setUpstream:
		cmd += " --set-upstream"
	case push:
		cmd += " --push"
	}
	if NotifyID != "" {
		cmd += " --notify " + quote(NotifyID)
	}
	return cmd + " " + quote(branch)
}

// self is how to run this program again: `git sign-pass` when the
// git-sign-pass on PATH is this program, and otherwise its path, since an
// older or newer install on PATH may not understand what this one saved.
func self() string {
	exe, err := os.Executable()
	if err != nil {
		if exe, err = exec.LookPath(os.Args[0]); err != nil {
			exe = os.Args[0]
		}
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	if onPath, err := exec.LookPath("git-sign-pass"); err == nil {
		a, aerr := os.Stat(onPath)
		b, berr := os.Stat(exe) //nolint:gosec // this program's own path, only compared with another
		if aerr == nil && berr == nil && os.SameFile(a, b) {
			return "git sign-pass"
		}
	}
	return quote(exe)
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_./@%+=:,-]+$`)

// quote is s as one word in fish or a POSIX shell.
func quote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
