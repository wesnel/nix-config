// Command git-red-check checks that the tests a commit adds or changes fail
// without the commit's other changes and pass with them. Installed on PATH,
// it runs as `git red-check`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/redcheck"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/repos"
)

const usage = `usage: git red-check [--test CMD --test-files GLOB... [--build CMD]] [--lines N] [REV]
       git red-check [options] [--fixups] BASE..TIP
       git red-check -h

Checks that the tests a commit changes test the commit. A commit runs each
test suite whose test files it changes: in a throwaway worktree at the
commit's parent plus only that suite's test files, the suite's test command
must fail; at the commit itself, it must pass. REV defaults to HEAD; a range
checks each non-merge commit in it against its own parent.

  --test CMD         the test command, run with sh at the top of the checkout
  --build CMD        a command that builds the tests without running them,
                     e.g. go vet ./...; run without the change, it tells a
                     build failure from a failed assertion, and without it
                     no commit can exit 0
  --test-files GLOB  a git pathspec glob for test files, e.g. '**/*_test.go'
                     (repeatable); every other changed path is the change
  --fixups           check only the range's fixup!, amend! and squash! commits
  --lines N          lines kept from the end of each run's output (default 40)

The suites come from "suites" in the clone's agent settings,
.git/info/agents.json in its common git directory, which the operator
writes, and never from the repository. --test, --test-files and --build
make one suite, named flags, in their place, and need --test and
--test-files both. Without suites from either, nothing runs. A test command
runs the commit's code: run it in a sandbox when the commit is not
trusted. Nothing is committed, signed or pushed, and no ref or file of the
caller's worktrees changes; each throwaway worktree, under the git
directory's red-check/, is removed afterwards.

Writes JSON on stdout: the suites, and each commit's verdict, the worst of
its suites', with each suite it ran: that suite's verdict, why it falls
short if it does, its test files, and the exit status and output of each
run. Exit status:
  0  every suite of every commit builds its tests and fails without the
     change and passes with it (fails-as-asserted)
  3  a suite of a commit passes without its change (passes-without-change)
     or fails with it (fails-with-change)
  4  otherwise, a commit lacks evidence: a suite's tests fail only to build
     without the change (fails-to-build-without-change), or fail without
     it with no build command to say whether they built
     (fails-without-change), or the commit changes only a suite's test
     files (no-code-change), or no suite's (no-test-files)
  1  the check could not run; 2 a usage error or refusal
`

type globs []string

func (g *globs) String() string     { return strings.Join(*g, ",") }
func (g *globs) Set(v string) error { *g = append(*g, v); return nil }

func main() {
	flags := flag.NewFlagSet("git-red-check", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {}
	test := flags.String("test", "", "")
	build := flags.String("build", "", "")
	var testFiles globs
	flags.Var(&testFiles, "test-files", "")
	fixups := flags.Bool("fixups", false, "")
	lines := flags.Int("lines", redcheck.DefaultLines, "")

	// Flags may follow the revision, as with git's own commands.
	var positional []string
	args := os.Args[1:]
	for {
		err := flags.Parse(args)
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(os.Stdout, usage)
			os.Exit(0)
		}
		if err != nil {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(positional) > 1 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	rev := "HEAD"
	if len(positional) == 1 {
		rev = positional[0]
	}

	if err := git.CheckConfig(""); err != nil {
		os.Exit(cli.Report("git-red-check", nil, err, 0, os.Stdout, os.Stderr))
	}
	opts := redcheck.Options{Fixups: *fixups, Lines: *lines, Log: os.Stderr}
	// The flags make a suite of their own in place of the configured ones,
	// so a caller who gives them does not need the settings to load.
	if *test != "" || *build != "" || len(testFiles) > 0 {
		if *test == "" || len(testFiles) == 0 {
			err := refusal.New("--test and --test-files make one suite in place of the configured ones, so each needs " +
				"the other; pass both, or neither to run the configured suites")
			os.Exit(cli.Report("git-red-check", nil, err, 0, os.Stdout, os.Stderr))
		}
		opts.Suites = []redcheck.Suite{{Name: "flags", Test: *test, Build: *build, TestFiles: testFiles}}
	} else {
		config, err := repos.Lookup()
		if err != nil {
			os.Exit(cli.Report("git-red-check", nil, err, 0, os.Stdout, os.Stderr))
		}
		opts.Suites = config.Suites()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	result, err := redcheck.Do(ctx, rev, opts)
	code := 0
	if err == nil {
		code = result.Exit()
		for _, c := range result.Commits {
			if len(c.Suites) == 0 && c.Why != "" {
				fmt.Fprintf(os.Stderr, "git-red-check: %.12s %s: %s\n", c.Commit, c.Subject, c.Why)
			}
			for _, s := range c.Suites {
				if s.Why != "" {
					fmt.Fprintf(os.Stderr, "git-red-check: %.12s %s: %s: %s\n", c.Commit, c.Subject, s.Suite, s.Why)
				}
			}
		}
		if len(result.Commits) == 0 {
			fmt.Fprintf(os.Stderr, "git-red-check: no commits to check in %s\n", rev)
		}
	}
	os.Exit(cli.Report("git-red-check", result, err, code, os.Stdout, os.Stderr))
}
