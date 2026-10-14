// Command git-fresh-review reviews a range of commits with a reviewer that
// did not write them. Installed on PATH, it runs as `git fresh-review`.
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
