// Command git-sign-pass re-signs a branch's unsigned commits with the
// hardware key, squashing fixups first. Installed on PATH, it runs as
// `git sign-pass`.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/signpass"
)

const usage = `usage: git sign-pass [--base REF] [--push | --set-upstream] [--notify ID] [BRANCH...]
       git sign-pass --resume [--push | --set-upstream] [--notify ID] [BRANCH]

Re-signs the current branch from its oldest unsigned commit (or the oldest
commit a fixup!, amend! or squash! folds into) to its tip, folding those in
first. Every commit keeps its files and author, and its message unless an
amend! or squash! rewrites it; branches stacked inside the range move with it,
and so does any other worktree one is checked out in, which must be clean
(ignored files aside). The unsigned tip is kept under ` + signpass.BackupRef + `BRANCH.
Before any touch it refuses a message it would sign, folds squashed in, with
a subject over 72 characters or ending in a period, or a line crediting a
tool or linking its session (Co-Authored-By, "Generated with", a session
URL); an amend! commit rewords one.

Given BRANCHes, it signs each one's stack instead, from wherever the branch is
checked out (or a throwaway worktree), so a handover of several stacks is one
command; naming the top of each stack is enough. Where two named branches
share commits (a fork: one parent, two children), the first in name order
signs the shared commits, and the other's own commits are replayed from their
fork point onto the signed copy and signed in the same rebase. It prints the
plan (branches, commits, replays, pushes and key touches) and then runs it;
everything that can refuse is checked before the first touch.

When the key refuses a signature or an SSH login, as it does at a missed
touch, and standard input is a terminal, it asks to retry that step: Enter
retries, q stops. With no terminal to ask (an agent, or a ` + "`!`" + ` run), it
retries that step once by itself, after --retry-wait, in a process group of
its own, and kills that group, git and the programs git started in it, after
--retry-timeout, at an interrupt or a hangup, or when the pass itself is
killed: one missed touch can lock the key, and the next request then waits
on pinentry for its PIN without end. A killed commit is put back for
` + "`git rebase --continue`" + `, keeping the signatures before it, unless it
is the first. A refusal in a fold of fixup!, amend! or squash! commits, or
a killed first commit, undoes the rebase instead, and --resume signs from
the start; on a terminal, Enter there signs from the start at once. If the
retry misses or is killed, it stops, and says the key may need unlocking.
Stopped, it prints the commands that carry on, with the touches they need,
naming this program by its path unless it is the git-sign-pass on PATH.
--resume never changes a rebase in progress: it says whether that rebase is
one the pass left stopped and what to run, and refuses while a git the pass
started there may still be running.

  --base REF        where the branch starts (default: where it left origin's
                    default branch, or the local main or master); with
                    BRANCHes, only for one
  --resume          verify and finish the pass on BRANCH (default: the current
                    branch) after ` + "`git rebase --continue`" + ` and carry on
                    with the rest of its BRANCHes, or retry its push to the
                    push URLs that lack it once it is signed; a pass in
                    progress blocks only passes that would move its branches
  --push            push every branch that moved to the branch of the same
                    name at each push URL of its upstream's remote in turn,
                    one branch per push, bottom up, over one SSH login per
                    URL, guarded by what its remote-tracking ref named before
                    the first URL's push, and move that ref once every URL
                    has it; refused, before any key touch, for a branch whose
                    upstream is another branch and for the remote's default
                    branch (REMOTE/HEAD, or main and master)
  --brief           print only what it will sign, a progress line every 25
                    signatures, one line per push and the final JSON; git's
                    own account of the rebase and the pushes is left out
                    unless a command fails, and then it is shown in full.
                    The default when standard output is not a terminal, as
                    for an agent or a ` + "`!`" + ` run
  --no-brief        git's own output and the plan in full, as on a terminal
  --retry-wait D   how long to wait before the automatic retry of a missed
                    touch, so there is time to reach the key (default 10s)
  --retry-timeout D how long the retried step may take before it is killed
                    (default 1m)
  --notify ID       when the pass ends, signed, refused or failed, log its
                    outcome, exit status and a one-line summary in the plan
                    whose log holds sign-pass ID (from ` + "`agent-plan sign-id`" + `),
                    for the session that handed it over; a failure to log
                    it is reported on stderr and leaves the exit status as
                    it was
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
	notify := flags.String("notify", "", "")
	brief := flags.Bool("brief", false, "")
	noBrief := flags.Bool("no-brief", false, "")
	flags.DurationVar(&signpass.RetryWait, "retry-wait", signpass.RetryWait, "")
	flags.DurationVar(&signpass.RetryTimeout, "retry-timeout", signpass.RetryTimeout, "")
	if code, ok := cli.Parse(flags, os.Args[1:], usage, os.Stdout, os.Stderr); !ok {
		os.Exit(code)
	}
	if *resume && flags.NArg() > 1 || *brief && *noBrief {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	signpass.Brief = *brief || !*noBrief && !signpass.StdoutIsTerminal()
	if *notify != "" {
		if err := plans.CheckSignID(*notify); err != nil {
			os.Exit(cli.Report("git-sign-pass", nil, err, 0, os.Stdout, os.Stderr))
		}
		signpass.NotifyID = *notify
	}
	result, err := pass(*base, *resume, *push, *setUpstream, flags.Args())
	code := cli.Report("git-sign-pass", result, err, 0, os.Stdout, os.Stderr)
	if *notify != "" {
		if _, nerr := signpass.Notify(*notify, result, err, time.Now()); nerr != nil {
			fmt.Fprintf(os.Stderr, "git-sign-pass: --notify %s: the result is not logged: %s\n", *notify, nerr)
		}
	}
	os.Exit(code)
}

// pass runs the pass the arguments ask for.
func pass(base string, resume, push, setUpstream bool, branches []string) (any, error) {
	if err := git.CheckLocation(); err != nil {
		return nil, err
	}
	if err := git.CheckConfig(""); err != nil {
		return nil, err
	}
	switch {
	case resume && len(branches) == 1:
		return signpass.Resume(branches[0], push, setUpstream)
	case len(branches) > 0:
		return signpass.Plan(base, branches, push, setUpstream)
	default:
		return signpass.Run(base, resume, push, setUpstream)
	}
}
