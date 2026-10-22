// Command git-delegate reviews a range of commits or a pull request with
// reviewers that did not write it, and keeps their findings in its review
// record. Installed on PATH, it runs as `git delegate`.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/wesnel/agent-tools/internal/review"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := review.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
