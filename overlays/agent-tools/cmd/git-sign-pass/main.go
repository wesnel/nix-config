// Command git-sign-pass re-signs a branch's unsigned commits with the
// hardware key, squashing fixups first. Installed on PATH, it runs as
// `git sign-pass`.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/signpass"
)

const usage = `usage: git sign-pass [--base REF] [--push]
       git sign-pass --resume [--push]

Re-signs the current branch from its oldest unsigned commit (or the oldest
commit a fixup folds into) to its tip, squashing fixups first. Every commit
keeps its files, author and message; branches stacked inside the range move
with it. The unsigned tip is kept under ` + signpass.BackupRef + `BRANCH.

  --base REF   where the branch starts (default: where it left origin's default
               branch, or the local main or master)
  --resume     verify and finish a pass after ` + "`git rebase --continue`" + `
  --push       push every branch that moved to its upstream, guarded by its
               remote-tracking ref
`

func main() {
	flags := flag.NewFlagSet("git-sign-pass", flag.ContinueOnError)
	flags.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	base := flags.String("base", "", "")
	resume := flags.Bool("resume", false, "")
	push := flags.Bool("push", false, "")
	if err := flags.Parse(os.Args[1:]); err != nil || flags.NArg() > 0 {
		flags.Usage()
		os.Exit(2)
	}
	if err := git.CheckConfig(""); err != nil {
		os.Exit(cli.Report("git-sign-pass", nil, err, 0, os.Stdout, os.Stderr))
	}
	result, err := signpass.Run(*base, *resume, *push)
	os.Exit(cli.Report("git-sign-pass", result, err, 0, os.Stdout, os.Stderr))
}
