package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// capturer appends each capture to an org file in a git repository and
// commits it there. Commits are unsigned: signing needs a touch that nobody
// is present to give.
type capturer struct {
	repo  string
	inbox string
	now   func() time.Time

	mu sync.Mutex
}

func (c *capturer) capture(ctx context.Context, text string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, err := os.Stat(filepath.Join(c.repo, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(c.repo, 0o700); err != nil {
			return "", err
		}
		if err := c.git(ctx, "init", "--quiet"); err != nil {
			return "", err
		}
	}

	// One line, so nothing said can start a heading or a drawer of its own.
	heading := strings.Join(strings.Fields(text), " ")
	entry := fmt.Sprintf("* TODO %s\n%s\n", heading, c.now().Format("[2006-01-02 Mon 15:04]"))

	path := filepath.Join(c.repo, c.inbox)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(entry); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	if err := c.git(ctx, "add", "--", c.inbox); err != nil {
		return "", err
	}
	// Naming the file commits only it, whatever else is staged.
	if err := c.git(ctx, "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify",
		"-m", "Capture from the ring", "--", c.inbox); err != nil {
		return "", err
	}
	return "Captured: " + heading, nil
}

func (c *capturer) git(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", c.repo}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// asker runs a command that reads a question on standard input and prints
// the answer. Which tools that command may use is its own business.
type asker struct {
	command string
	timeout time.Duration
}

func (a *asker) ask(ctx context.Context, question string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, a.command)
	cmd.Stdin = strings.NewReader(question)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// A child left holding the output open would otherwise keep Run waiting
	// past the timeout.
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("no answer within %s", a.timeout)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %v: %s", a.command, err, strings.TrimSpace(stderr.String()))
	}
	answer := strings.TrimSpace(stdout.String())
	if answer == "" {
		return "", errors.New("the answer was empty")
	}
	return answer, nil
}
