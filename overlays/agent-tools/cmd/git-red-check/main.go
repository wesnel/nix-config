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

const usage = `usage: git red-check [--test CMD (--test-files GLOB | --inline-test-files GLOB)... [--build CMD]] [--lines N] [REV]
       git red-check [options] [--fixups] BASE..TIP
       git red-check -h

Checks that the tests a commit changes test the commit. A commit runs each
test suite whose test files it changes: in a throwaway worktree at the
commit's parent plus only that suite's tests, the suite's test command
must fail; at the commit itself, it must pass. REV defaults to HEAD; a range
checks each non-merge commit in it against its own parent.

  --test CMD         the test command, run with sh at the top of the checkout
  --build CMD        a command that builds the tests without running them,
                     e.g. go vet ./...; run without the change, it tells a
                     build failure from a failed assertion, and without it
                     no commit can exit 0
  --test-files GLOB  a git pathspec glob for files that hold only tests,
                     e.g. '**/*_test.go' (repeatable), copied whole
  --inline-test-files GLOB
                     a glob, as --test-files, for source files that hold
                     their own tests (repeatable); only a file's tests are
                     copied onto the parent's code, split by its extension:
                     .rs, its one trailing top-level #[cfg(test)] mod NAME
                     { ... }, preserving every other parent byte. Other
                     extensions, overlapping --test-files selection and
                     layouts it cannot split are refused. Build Rust tests
                     with cargo test --no-run, not cargo build
  --fixups           check suites only for fixup!, amend! and squash! commits;
                     named checks still run once at TIP
  --lines N          lines kept from the end of each run's output (default 40)

Every changed path that no suite takes as a test file, and every inline
test file whose code outside its tests changed, is the change.

The suites come from "suites" in the clone's agent settings,
.git/info/agents.json in its common git directory, which the operator
writes, and never from the repository. --test, --test-files,
--inline-test-files and --build make one suite, named flags, in their
place, and need --test and test file globs both. Named checks come from
"checks": [{"name":"lint","command":"make lint"}] in the same protected
settings. Each runs once at resolved TIP in its own fresh checkout, even
after another check fails. Suite flags do not override checks or bypass
settings validation. Checks-only clones work; without suites or checks,
nothing runs. A command runs the commit's code: run it in a
sandbox when the commit is not trusted. Nothing is committed, signed or
pushed, and no ref or file of the caller's worktrees changes; each
throwaway worktree, under the git directory's red-check/, is removed
afterwards.

Writes JSON on stdout: the suites, and each commit's verdict, the worst of
its suites', with each suite it ran: that suite's verdict, why it falls
short if it does, its test files, and the exit status and output of each
run. The optional tipChecks envelope holds resolved base and tip hashes
and every named check's name, command, exit and output. Output is redacted
before line trimming and capped at 16 KiB per run. An interrupted command
or execution error produces no completed evidence. Pass the JSON with
git delegate --red-check FILE BASE..TIP; delegate never runs checks.
Exit status:
  0  every suite of every commit builds its tests and fails without the
     change and passes with it (fails-as-asserted), and all named checks pass
  3  a suite of a commit passes without its change (passes-without-change)
     or fails with it (fails-with-change), or a named check exits nonzero
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

// args are the command line, parsed.
type args struct {
	rev                    string
	test, build            string
	testFiles, inlineFiles globs
	fixups                 bool
	lines                  int
}

func main() {
	a := parse(os.Args[1:])
	if err := git.CheckConfig(""); err != nil {
		os.Exit(cli.Report("git-red-check", nil, err, 0, os.Stdout, os.Stderr))
	}
	settings, err := repos.Lookup()
	if err != nil {
		os.Exit(cli.Report("git-red-check", nil, err, 0, os.Stdout, os.Stderr))
	}
	suites, err := a.suites(func() (*repos.Settings, error) { return settings, nil })
	if err != nil {
		os.Exit(cli.Report("git-red-check", nil, err, 0, os.Stdout, os.Stderr))
	}
	opts := redcheck.Options{Suites: suites, Checks: settings.Checks, Fixups: a.fixups, Lines: a.lines, Log: os.Stderr}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	result, err := redcheck.Do(ctx, a.rev, opts)
	code := 0
	if err == nil {
		code = result.Exit()
		printVerdicts(result, a.rev)
	}
	os.Exit(cli.Report("git-red-check", result, err, code, os.Stdout, os.Stderr))
}

// parse reads the command line, exiting on -h or a usage error. Flags may
// follow the revision, as with git's own commands.
func parse(argv []string) args {
	flags := flag.NewFlagSet("git-red-check", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.Usage = func() {}
	a := args{rev: "HEAD"}
	flags.StringVar(&a.test, "test", "", "")
	flags.StringVar(&a.build, "build", "", "")
	flags.Var(&a.testFiles, "test-files", "")
	flags.Var(&a.inlineFiles, "inline-test-files", "")
	flags.BoolVar(&a.fixups, "fixups", false, "")
	flags.IntVar(&a.lines, "lines", redcheck.DefaultLines, "")

	var positional []string
	for {
		err := flags.Parse(argv)
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
		argv = flags.Args()[1:]
	}
	if len(positional) > 1 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if len(positional) == 1 {
		a.rev = positional[0]
	}
	return a
}

// suites are the suites a run checks. Where a flag of a suite is given,
// they are one suite named flags, which needs --test and test file globs
// both; otherwise they are those of the clone's settings, which lookup is
// called for only then, so a caller who gives the flags does not need the
// settings to load.
func (a args) suites(lookup func() (*repos.Settings, error)) ([]redcheck.Suite, error) {
	if a.test == "" && a.build == "" && len(a.testFiles) == 0 && len(a.inlineFiles) == 0 {
		config, err := lookup()
		if err != nil {
			return nil, err
		}
		return config.Suites, nil
	}
	if a.test == "" || len(a.testFiles)+len(a.inlineFiles) == 0 {
		return nil, refusal.New("--test and --test-files or --inline-test-files make one suite in place of the configured ones, " +
			"so each needs the other; pass both, as `--test CMD --test-files GLOB`, or neither to run the configured suites")
	}
	return []redcheck.Suite{{Name: "flags", Test: a.test, Build: a.build, TestFiles: a.testFiles, InlineTestFiles: a.inlineFiles}}, nil
}

// printVerdicts says on stderr why each commit, or each suite of one,
// falls short.
func printVerdicts(result *redcheck.Result, rev string) {
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
