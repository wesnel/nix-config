// Command git-task-worktree creates a task worktree for an agent whose
// sandbox cannot write the repository's .git/worktrees. Installed on PATH,
// it runs as `git task-worktree`; Claude Code's sandbox excludes it, so it
// runs outside the sandbox when it is a command line of its own.
package main

import (
	"fmt"
	"os"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/repos"
	"github.com/wesnel/agent-tools/internal/taskworktree"
)

const usage = `usage: git task-worktree BRANCH BASE
       git task-worktree -h

Creates BRANCH from BASE, without an upstream, and its worktree at
WORKTREES/<repo>/BRANCH, <repo> being the name of the repository's main
checkout directory and WORKTREES the worktrees directory agent-tools'
config names (~/.claude/worktrees). Run it from any worktree of the
repository. BASE is a ref, such as origin/main or a parent branch; a commit
ID is refused.

Where BRANCH already exists at BASE's commit with no worktree, as a
sandboxed ` + "`git worktree add -b`" + ` leaves it, it makes the worktree for
that branch. So for an existing branch, pass it as its own BASE:
git task-worktree topic topic.

It refuses a worktree path inside the main checkout, one that exists, one
reached through a symbolic link under WORKTREES, and a BRANCH checked out
elsewhere or at another commit. Git runs without the repository's hooks or
fsmonitor, and not at all where the repository's own config names a
program. LFS files stay pointers (GIT_LFS_SKIP_SMUDGE=1).

Writes JSON on stdout: branch, base (the full ref), commit, worktree and
recovered. Move a Claude Code session into the worktree with EnterWorktree's
path. Exit status: 0 made, 2 refused or a usage error, 1 otherwise.
`

func main() {
	args := os.Args[1:]
	if len(args) == 1 && cli.IsHelp(args[0]) {
		fmt.Fprint(os.Stdout, usage)
		os.Exit(0)
	}
	if len(args) != 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var result taskworktree.Result
	dir, err := os.Getwd()
	root := ""
	if err == nil {
		root, err = taskworktree.Root(repos.Path())
	}
	if err == nil {
		result, err = taskworktree.Create(dir, root, args[0], args[1])
	}
	os.Exit(cli.Report("git-task-worktree", result, err, 0, os.Stdout, os.Stderr))
}
