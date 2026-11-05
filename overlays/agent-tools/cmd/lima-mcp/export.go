package main

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/wesnel/agent-tools/internal/limaimport"
)

const exportUsage = "usage: lima-mcp export --outbox DIR NAME, run in the repository to import: " +
	"writes DIR/NAME.bundle for lima_dev_import, NAME a lowercase letter or digit then up to 63 of a-z, 0-9, ., _ and -"

// exportMain is lima-mcp export --outbox DIR NAME, which a session runs, as
// lima-dev-export NAME, in the repository holding dir: it writes the
// bundle that lima_dev_import NAME carries into the managed VM.
func exportMain(ctx context.Context, args []string, dir string) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	outbox := flags.String("outbox", "", "")
	if err := flags.Parse(args); err != nil || *outbox == "" || flags.NArg() != 1 {
		return errors.New(exportUsage)
	}
	name, err := limaimport.ParseName(flags.Arg(0))
	if err != nil {
		return err
	}
	return limaimport.Export(ctx, "git", dir, *outbox, name)
}
