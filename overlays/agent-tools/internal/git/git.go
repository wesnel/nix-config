// Package git runs the git on PATH, so the user's own configuration (signing
// key, SSH) applies to everything these tools do.
package git

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Dir is the working directory for every command; empty means the process's.
var Dir string

// hardened keeps the repository from running programs through these tools. A
// sandboxed agent can write the repository's .git, and these tools run git on
// the host with the user's credentials, so hooks and the fsmonitor are off
// for every command. Repository config that names other programs is refused
// by CheckConfig instead, since a command-line value cannot clear it.
var hardened = []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}

func command(args ...string) *exec.Cmd {
	cmd := exec.Command("git", append(append([]string{}, hardened...), args...)...)
	cmd.Dir = Dir
	return cmd
}

// Bytes runs git and returns its stdout.
func Bytes(stdin []byte, args ...string) ([]byte, error) {
	cmd := command(args...)
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

// Run runs git and returns its stdout with surrounding whitespace removed.
func Run(args ...string) (string, error) {
	out, err := Bytes(nil, args...)
	return strings.TrimSpace(string(out)), err
}

// OK reports whether git exits successfully.
func OK(args ...string) bool {
	cmd := command(args...)
	return cmd.Run() == nil
}

// Attached runs git connected to this process's terminal, for commands that
// ask for a key touch or a passphrase. It returns the exit code.
func Attached(env []string, args ...string) int {
	cmd := command(args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), env...)
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode()
		}
		return 1
	}
	return 0
}
