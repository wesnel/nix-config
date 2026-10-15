package signpass

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// refusedByKey matches what ssh, ssh-keygen and gpg print when the hardware
// key refuses to sign, as it does when its touch is missed.
var refusedByKey = regexp.MustCompile(`agent refused operation|signing failed|sign_and_send_pubkey|failed to sign the data`)

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
		b, berr := os.Stat(exe)
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
