package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/watch"
)

// watchLog follows the log named in rest, by default the one the
// review-loop skill writes in the git common directory, until interrupted.
func watchLog(rest []string, fromStart bool, stdout, stderr io.Writer) error {
	if len(rest) > 1 {
		return errors.New("watch takes at most one log; see pr-loop --help")
	}
	log := ""
	if len(rest) == 1 {
		log = rest[0]
	} else {
		common, err := git.Run("rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return err
		}
		log = filepath.Join(common, "pr-loop-session.jsonl")
	}
	// An interrupt stops the watching, never the session.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(stderr, "pr-loop: watching %s; interrupt to stop watching\n", log)
	return watch.Follow(ctx, log, fromStart, stdout, 500*time.Millisecond)
}
