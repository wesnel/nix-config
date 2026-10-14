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
	"github.com/wesnel/agent-tools/internal/repos"
)

const usage = `usage: git red-check [--test CMD] [--build CMD] [--test-files GLOB]... [--lines N] [REV]
       git red-check [options] [--fixups] BASE..TIP
       git red-check -h

Checks that the tests a commit changes test the commit. In a throwaway
worktree at the commit's parent plus only its test files, the test command
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

Settings not given as flags come from "repos" in the user's
agent-tools/config.json, keyed by GitHub owner/name, and never from the
repository. The test command runs the commit's code: run it in a sandbox
when the commit is not trusted. Nothing is committed, signed or pushed, and
no ref or file of the caller's worktrees changes; the throwaway worktree,
under the git directory's red-check/, is removed afterwards.

Writes JSON on stdout: each commit's verdict, why it falls short if it
does, its test files, and the exit status and output of each run. Exit
status:
  0  every commit's tests build and fail without its change and pass with
     it (fails-as-asserted)
  3  a commit's tests pass without its change (passes-without-change) or
     fail with it (fails-with-change)
  4  otherwise, a commit lacks evidence: its tests fail only to build
     without the change (fails-to-build-without-change), or fail without
     it with no --build to say whether they built (fails-without-change),
     or it changes no test files (no-test-files), or only test files
     (no-code-change)
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
	opts := redcheck.Options{
		Test: *test, Build: *build, TestFiles: testFiles,
		Fixups: *fixups, Lines: *lines, Log: os.Stderr,
	}
	// The configuration only fills in what the flags leave out, so a caller
	// who gives both required settings does not need it to load.
	config, err := repos.Lookup()
	if err != nil && (opts.Test == "" || len(opts.TestFiles) == 0) {
		os.Exit(cli.Report("git-red-check", nil, err, 0, os.Stdout, os.Stderr))
	}
	if err == nil {
		if opts.Test == "" {
			opts.Test = config.Test
		}
		if opts.Build == "" {
			opts.Build = config.Build
		}
		if len(opts.TestFiles) == 0 {
			opts.TestFiles = config.TestFiles
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	result, err := redcheck.Do(ctx, rev, opts)
	code := 0
	if err == nil {
		code = result.Exit()
		for _, c := range result.Commits {
			if msg := redcheck.Messages[c.Verdict]; msg != "" {
				fmt.Fprintf(os.Stderr, "git-red-check: %.12s %s: %s\n", c.Commit, c.Subject, msg)
			}
		}
		if len(result.Commits) == 0 {
			fmt.Fprintf(os.Stderr, "git-red-check: no commits to check in %s\n", rev)
		}
	}
	os.Exit(cli.Report("git-red-check", result, err, code, os.Stdout, os.Stderr))
}
