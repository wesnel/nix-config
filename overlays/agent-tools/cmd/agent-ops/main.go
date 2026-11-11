// Command agent-ops provides harness hooks, MCP transport and checkout setup.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/loop"
)

const usage = `agent-ops: harness infrastructure.

  setup [PATH]                run the checkout setup command
  setup-command               print this repository's configured setup command
  guard --baseline            PreToolUse hook for Claude Code and Codex (reads stdin):
                              every session's rules
  guard --reviewer            PreToolUse hook a reviewer agent's definition adds beside
                              --baseline (reads stdin): also keeps the agent to reading
  guard --post                PostToolUse hook for Claude Code and Codex (reads stdin):
                              records the shell call's commits, pushes (but push's,
                              which push logs), new worktrees,
                              branches and PRs in the plan of its session or worktree,
                              each commit with the session, asking which step it
                              completes (a commit no plan takes makes a plan
                              naming both); after a push to GitHub, names the
                              agent-pr wait ci to start
  mcp                         serve the GitHub and review commands an agent runs (open,
                              stack, link, push, threads, reply --from, draft, request,
                              status, ci, logs, wait, sign-check, end; agent-review,
                              ask, settle, show), routine agent-plan operations,
                              and a GitHub user's name and pronouns
                              as typed tools of a stdio MCP server, each run in the
                              worktree its call names, for Codex, whose commands cannot
                              reach the gh login. Codex prompts for non-plan tools
                              outside a review-loop session
  mcp-status [--hook]         which of this host's mcp-proxy servers are not connected,
                              each with the command that connects it, probing each route
                              without credentials; nothing when all are; --hook prints it
                              as Claude Code's SessionStart hook output, as context for
                              the agent and a message shown to the operator

A setup command prepares a fresh checkout, e.g. generating files the
repository does not commit. It is taken from "setup" in the clone's agent
settings, .git/info/agents.json in its common git directory, which the
operator writes, and never from the repository. setup runs it with sh in this process, in the sandbox, printed on
stderr before it runs and followed by its exit status; a failed setup makes
the command exit 1. Nothing runs it on its own: run it in each new worktree.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if cli.IsHelp(args[0]) {
		fmt.Fprint(os.Stdout, usage)
		return
	}
	os.Exit(run(args[0], args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	switch command {
	case "mcp":
		return mcpMain(args, stdin, stdout, stderr)
	case "guard":
		return guard.Main(args, stdin, stdout, stderr)
	case "mcp-status":
		return mcpStatus(args, stdout, stderr)
	case "setup", "setup-command":
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	if code, ok := cli.Parse(flags, args, usage, stdout, stderr); !ok {
		return code
	}
	if err := git.CheckLocation(); err != nil {
		return cli.Report("agent-ops", nil, err, 0, stdout, stderr)
	}
	if err := git.CheckConfig(""); err != nil {
		return cli.Report("agent-ops", nil, err, 0, stdout, stderr)
	}
	var result any
	var err error
	code := 0
	if command == "setup" {
		result, err, code = setupCommand(flags.Args(), stderr)
	} else if flags.NArg() != 0 {
		err = errors.New("setup-command takes no arguments; see agent-ops --help")
	} else {
		result, err = loop.SetupCommand()
	}
	return cli.Report("agent-ops", result, err, code, stdout, stderr)
}

func setupCommand(rest []string, stderr io.Writer) (any, error, int) {
	var err error
	if len(rest) > 1 {
		return nil, errors.New("setup takes at most one path; see agent-ops --help"), 0
	}
	if len(rest) == 1 {
		// The repository is identified from where the command runs.
		if err = os.Chdir(rest[0]); err != nil {
			return nil, err, 0
		}
		if err = git.CheckConfig(""); err != nil {
			return nil, err, 0
		}
	}
	var dir string
	if dir, err = git.Toplevel(); err != nil {
		return nil, err, 0
	}
	var setup *loop.Setup
	if setup, err = loop.SetupCommand(); err != nil {
		return nil, err, 0
	}
	runs := []loop.SetupRun{}
	if setup.Command != "" {
		runs = loop.RunSetup(setup.Command, []string{dir}, stderr)
	}
	result := map[string]any{"settings": setup.Settings, "command": setup.Command, "runs": runs}
	code := setupFailures(runs, stderr)
	return result, nil, code
}

// setupFailures names each failed run and returns 1 if there was one.
func setupFailures(runs []loop.SetupRun, stderr io.Writer) int {
	code := 0
	for _, r := range runs {
		if r.Exit != 0 {
			fmt.Fprintf(stderr, "agent-ops: setup failed in %s (exit %d)\n", r.Dir, r.Exit)
			code = 1
		}
	}
	return code
}
