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

const usage = `usage: git sign-pass [--base REF] [--push | --set-upstream]
       git sign-pass --resume [--push | --set-upstream]

Re-signs the current branch from its oldest unsigned commit (or the oldest
commit a fixup folds into) to its tip, squashing fixups first. Every commit
keeps its files, author and message; branches stacked inside the range move
with it, and so does any other worktree one is checked out in, which must be
clean (ignored files aside). The unsigned tip is kept under ` + signpass.BackupRef + `BRANCH.

  --base REF        where the branch starts (default: where it left origin's
                    default branch, or the local main or master)
  --resume          verify and finish the current branch's pass after
                    ` + "`git rebase --continue`" + `, or retry its push once it is
                    signed; a pass in progress blocks only passes that would
                    move its branches
  --push            push every branch that moved to the branch of the same
                    name on its upstream's remote, guarded by its
                    remote-tracking ref; refused, before any key touch, for a
                    branch whose upstream is another branch and for the
                    remote's default branch (REMOTE/HEAD, or main and master)
  --set-upstream    --push, and in the same push publish every branch that
                    moved and has no upstream to its push remote
                    (branch.NAME.pushRemote, remote.pushDefault, the only
                    remote, or origin) and make that its upstream; refused for a
                    branch the remote already has
`

func main() {
	flags := flag.NewFlagSet("git-sign-pass", flag.ContinueOnError)
	base := flags.String("base", "", "")
	resume := flags.Bool("resume", false, "")
	push := flags.Bool("push", false, "")
	setUpstream := flags.Bool("set-upstream", false, "")
	if code, ok := cli.Parse(flags, os.Args[1:], usage, os.Stdout, os.Stderr); !ok {
		os.Exit(code)
	}
	if flags.NArg() > 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err := git.CheckConfig(""); err != nil {
		os.Exit(cli.Report("git-sign-pass", nil, err, 0, os.Stdout, os.Stderr))
	}
	result, err := signpass.Run(*base, *resume, *push, *setUpstream)
	os.Exit(cli.Report("git-sign-pass", result, err, 0, os.Stdout, os.Stderr))
}
