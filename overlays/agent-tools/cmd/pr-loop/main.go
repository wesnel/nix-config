// Command pr-loop pushes, replies and requests reviews for a pull request
// review loop, enforcing the loop's policy on every write to GitHub. See the
// usage text for the commands.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/loop"
	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/redact"
)

const usage = `pr-loop: a review loop's way to write to GitHub.

Policy: apart from open, every write is to the head of an open PR, draft or
ready, by the authenticated user in the same repository and not the default
branch, re-checked before it is made. A ready PR's pushes add only fixups of
its own commits. open publishes a branch GitHub does not have, or takes one
GitHub has at the local tip, heading no open PR, whose commits since GitHub's
other branches are all by user.email; never a default branch; and opens it as
a draft PR based on the default branch or on the head of another open PR by
the authenticated user in the same repository. open
refuses a new commit, and push a fixup, whose message breaks the rules git
sign-pass holds messages to: a subject of at most 72 characters without a
trailing period, no attribution lines, and on a fixup only Review-*
trailers.

A loop runs in a mode, PR_LOOP_MODE, which only the operator sets, by
starting the session with claude-review-loop --bots|--people. In bots
mode, request and reply take bot threads, and push pushes the fixups. In
people mode, draft writes the answers to people's threads into the PR's
review record and push refuses: the operator reads the fixups and drafts,
pushes with git sign-pass --push and posts with pr-loop post. request and
reply refuse outside a bots-mode loop, and draft outside a people-mode one.

Each push, reply, resolve and wait ci's result is logged in the plan of the
session that runs it: the plan naming the harness's session or this
worktree. A failure to log is printed on stderr and fails nothing.

  open --title T --body-file F [--base BASE] [--dry-run] [BRANCH]
                              push BRANCH (default HEAD) through the GitHub API, unless
                              GitHub has it at its tip already, and open it as a draft
                              PR against BASE (default: the default branch),
                              in BASE's PR's GitHub stack, as link puts it there; refuses
                              when BRANCH contains a remote branch BASE lacks, and names
                              the BASE to pass instead
  stack [PR|BRANCH]           the stack of open PRs containing a PR (default HEAD's), as
                              their bases chain them, and GitHub's stack of that PR as
                              "github" (null when GitHub has it in none)
  link PR...                  put the PRs, by number or branch, bottom first, each based on
                              the one before, into one GitHub stack (see below)
  setup [PATH]                run the setup command in the worktree at PATH (default: this one)
  setup-command               the setup command configured for this repository
  push [--dry-run] [--lease BRANCH=SHA]... [BRANCH...]
                              push through the GitHub API, a branch at a time, bottom up
                              (default: HEAD's branch alone; see below)
  threads [--all] PR          unresolved review threads, labelled by bot, every comment
                              read; writes every thread to the PR's review record
  reply THREAD --body-file F  reply inside a bot's review thread (- reads stdin)
  reply --from F              post the replies in a reply file (- reads stdin)
  resolve THREAD              resolve a bot's review thread whose last comment is yours
  draft THREAD --body-file F --outcome OUTCOME
                              write the answer to a thread into the PR's review record,
                              for the operator to post (- reads stdin)
  post PR [THREAD...]         the operator's: show each unposted draft on the terminal and
                              post it once answered y there (see below)
  request [--bot B] [--accept-failure CHECK] [PR...]
                              ask each bot that has not reviewed the head, once CI allows
                              and every thread it started has a reply
  status [PR...]              CI, each bot's review, open threads
  ci [PR...]                  checks on each head, compared with the base
  logs [--lines N] PR CHECK   the end of a failed GitHub Actions job's log
  wait ci [--sha REV] [--grace S] [--timeout S] [--interval S] [PR...]
                              block until CI on each head finishes (exit 3 on timeout,
                              4 when a head has no checks)
  wait reviews [--timeout S] [--interval S] [PR...]
                              block until requested reviews land (exit 3 on timeout)
  sign-check                  squash the fixups of HEAD's stack unsigned in a throwaway
                              worktree and check every branch keeps its own commits and
                              its files, before the stack is handed to the operator's
                              git sign-pass --push
  end [PR...]                 delete your trigger comments, those holding a bot's
                              request comment
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
                              pr-loop wait ci to start
  mcp-status [--hook]         which of this host's mcp-proxy servers are not connected,
                              each with the command that connects it, probing each route
                              without credentials; nothing when all are; --hook prints it
                              as context for Claude Code's SessionStart hook

reply and resolve take only a thread whose every comment is a configured
bot's (its login pattern, on an account GitHub types Bot) or the
authenticated user's, on an open PR by the authenticated user in this
repository; a thread read in part is refused. A person's thread is answered
with draft in a people-mode loop, and posted by the operator.

post reads each draft of the PR's review record once, prints it to
/dev/tty, and posts exactly those bytes when the operator answers y there;
without a terminal it refuses. A thread answered fix or rebut is then
resolved. Once every unresolved thread a person commented in has a posted
answer, those people are asked to review again. Agents never run it.

push takes a branch's head on GitHub from --lease, else from its remote
tracking ref, and refuses when GitHub's head is another commit, so a push
made elsewhere is never overwritten; without a lease the local branch must
contain that head, so a rebased branch is pushed with --lease. Several
branches go in their stack's order, bottom up.

ci, wait, logs, status, request and end take a PR by number or by branch,
each an open PR by the authenticated user in this repository, by default
the current branch's.

link adds the PRs onto the top of the GitHub stack the lowest is in, or the
one the PR it is based on tops, or else makes a new stack of them; PRs
already in it, in order, are left alone. Every open PR in a stack it
changes must be the authenticated user's. When open cannot stack a PR, the
PR stays open and "unstacked" says why and which link to run later.

wait ci prints {"result": R, "prs": [...]}, where R is passed, failed (a check
fails that does not fail on the base), failedOnBase, noChecks or timedOut. It
exits 0 once CI has finished, 3 on timedOut, and 4 on noChecks: nothing
failed, but a head still had no checks --grace seconds (default 180) after
wait ci first saw it as the PR's head, as on a branch no CI runs on. CI that
takes longer than that to report its first check needs a longer --grace; a
--grace beyond --timeout makes such a head time out instead. It waits for the
PR's head to become the commit --sha names, or else the head branch's tip on
GitHub, and never reports an earlier head's checks. Each failed check gives
its state on the base (onBase) and, for a GitHub Actions job, the logs
command that prints its log.

pr-loop may run outside the sandbox, with the operator's login. open's
title and body, each reply, draft and request's trigger comment are
refused, before anything is posted, while redaction would change them: name
a secret without its value. A placeholder such as $VAR, ${VAR}, <token> or
{{ .Values.x }}, or an obvious example value, passes.

A reply file has a section per thread: a line "=== thread THREAD", or
"=== thread THREAD resolve" to resolve the thread after replying, then the
Markdown reply up to the next such line. Every line starting "=== thread" is
a header. Every thread is checked before anything is posted; threads already
resolved are skipped and reported.

A review record that fails its structure check, which only a hand edit
makes, refuses threads, draft and post with exit 5; the operator fixes or
deletes it.

A setup command prepares a fresh checkout, e.g. generating files the
repository does not commit. It is taken from "setup" in the clone's agent
settings, .git/info/agents.json in its common git directory, which the
operator writes, and never from the repository. setup runs it with sh in this process, in the sandbox, printed on
stderr before it runs and followed by its exit status; a failed setup makes
the command exit 1. Nothing runs it on its own: run it in each new worktree.
`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if cli.IsHelp(args[0]) {
		fmt.Fprint(os.Stdout, usage)
		os.Exit(0)
	}
	os.Exit(run(args[0], args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run runs command with args and returns its exit status.
func run(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var (
		dryRun   = flags.Bool("dry-run", false, "")
		all      = flags.Bool("all", false, "")
		bodyFile = flags.String("body-file", "", "")
		base     = flags.String("base", "", "")
		title    = flags.String("title", "", "")
		from     = flags.String("from", "", "")
		outcome  = flags.String("outcome", "", "")
		lines    = flags.Int("lines", 200, "")
		timeout  = flags.Int("timeout", 240, "")
		interval = flags.Int("interval", 30, "")
		sha      = flags.String("sha", "", "")
		grace    = flags.Int("grace", 180, "")
		bots     multi
		accepted multi
		leases   multi
	)
	flags.Var(&bots, "bot", "")
	flags.Var(&accepted, "accept-failure", "")
	flags.Var(&leases, "lease", "")
	if command == "guard" {
		return guard.Main(args, stdin, stdout, stderr)
	}
	if command == "mcp-status" {
		return mcpStatus(args, stdout, stderr)
	}
	if code, ok := cli.Parse(flags, interleave(args), usage, stdout, stderr); !ok {
		return code
	}
	rest := flags.Args()
	if err := git.CheckLocation(); err != nil {
		return cli.Report("pr-loop", nil, err, 0, stdout, stderr)
	}
	if err := git.CheckConfig(""); err != nil {
		return cli.Report("pr-loop", nil, err, 0, stdout, stderr)
	}
	need := func(n int) error {
		if len(rest) != n {
			return fmt.Errorf("%s takes %d argument(s); see pr-loop --help", command, n)
		}
		return nil
	}
	number := func(arg string) (int, error) {
		n, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
		if err != nil {
			return 0, fmt.Errorf("%q is not a PR number", arg)
		}
		return n, nil
	}
	body := func() (string, error) {
		var data []byte
		var err error
		switch *bodyFile {
		case "":
			err = fmt.Errorf("%s needs --body-file", command)
		case "-":
			data, err = io.ReadAll(stdin)
		default:
			data, err = os.ReadFile(*bodyFile) //nolint:gosec // the caller's own file; the guard refuses a credential's path, and redact.Outgoing what would leak
		}
		if err == nil {
			err = redact.Outgoing(string(data))
		}
		return string(data), err
	}

	var result any
	var err error
	code := 0
	switch command {
	case "open":
		if len(rest) > 1 {
			err = errors.New("open takes at most one branch; see pr-loop --help")
			break
		}
		branch := ""
		if len(rest) == 1 {
			branch = rest[0]
		}
		var text string
		if err = redact.Outgoing(*title); err != nil {
			break
		}
		if text, err = body(); err == nil {
			result, err = loop.Open(branch, *base, *title, text, *dryRun)
		}
	case "stack":
		if len(rest) > 1 {
			err = errors.New("stack takes at most one PR or branch; see pr-loop --help")
			break
		}
		target := ""
		if len(rest) == 1 {
			target = rest[0]
		}
		result, err = loop.Stack(target)
	case "link":
		result, err = loop.Link(rest)
	case "push":
		byBranch := map[string]string{}
		for _, l := range leases {
			b, oid, ok := strings.Cut(l, "=")
			if !ok || b == "" || oid == "" {
				err = fmt.Errorf("--lease takes BRANCH=SHA, not %q", l)
				break
			}
			byBranch[b] = oid
		}
		if err == nil {
			result, err = loop.Push(rest, byBranch, *dryRun)
		}
	case "threads":
		if err = need(1); err == nil {
			var n int
			if n, err = number(rest[0]); err == nil {
				result, err = loop.Threads(n, *all)
			}
		}
	case "reply":
		if *from != "" {
			result, err = replyFrom(*from, *bodyFile, rest, stdin)
		} else if err = need(1); err == nil {
			var text string
			if text, err = body(); err == nil {
				result, err = loop.Reply(rest[0], text)
			}
		}
	case "resolve":
		if err = need(1); err == nil {
			result, err = loop.Resolve(rest[0])
		}
	case "draft":
		if err = need(1); err == nil {
			var text string
			if text, err = body(); err == nil {
				result, err = loop.Draft(rest[0], text, *outcome)
			}
		}
	case "post":
		if len(rest) == 0 {
			err = errors.New("post takes the PR, then any of its threads; see pr-loop --help")
			break
		}
		var n int
		if n, err = number(rest[0]); err == nil {
			result, err = loop.Post(n, rest[1:])
		}
	case "request":
		result, err = loop.Request(rest, bots, accepted)
	case "status":
		result, err = loop.Status(rest)
	case "ci":
		result, err = loop.CIStatuses(rest)
	case "logs":
		if err = need(2); err == nil {
			var text string
			if text, err = loop.Logs(rest[0], rest[1], *lines); err == nil {
				fmt.Fprintln(stdout, text)
				return 0
			}
		}
	case "wait":
		if len(rest) == 0 || (rest[0] != "ci" && rest[0] != "reviews") {
			err = errors.New("wait takes ci or reviews")
			break
		}
		target := rest[0]
		rest = rest[1:]
		wait, every := time.Duration(*timeout)*time.Second, time.Duration(*interval)*time.Second
		ciOnly := *sha != ""
		flags.Visit(func(f *flag.Flag) { ciOnly = ciOnly || f.Name == "grace" })
		settled := false
		switch {
		case target == "ci":
			var w *loop.CIWait
			w, settled, err = loop.WaitCI(rest, *sha, wait, every, time.Duration(*grace)*time.Second)
			result = w
			if err == nil && w.Result == "noChecks" {
				code = 4
			}
		case ciOnly:
			err = errors.New("--sha and --grace are for wait ci")
		default:
			result, settled, err = loop.WaitReviews(rest, wait, every)
		}
		if !settled {
			code = 3
		}
	case "setup":
		if len(rest) > 1 {
			err = errors.New("setup takes at most one path; see pr-loop --help")
			break
		}
		if len(rest) == 1 {
			// The repository is identified from where the command runs.
			if err = os.Chdir(rest[0]); err != nil {
				break
			}
			if err = git.CheckConfig(""); err != nil {
				break
			}
		}
		var dir string
		if dir, err = git.Toplevel(); err != nil {
			break
		}
		var setup *loop.Setup
		if setup, err = loop.SetupCommand(); err != nil {
			break
		}
		runs := []loop.SetupRun{}
		if setup.Command != "" {
			runs = loop.RunSetup(setup.Command, []string{dir}, stderr)
		}
		result = map[string]any{"settings": setup.Settings, "command": setup.Command, "runs": runs}
		code = setupFailures(runs, stderr)
	case "setup-command":
		result, err = loop.SetupCommand()
	case "sign-check":
		result, err = loop.SignCheck()
	case "end":
		result, err = loop.End(rest)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}

	var drift *record.DriftError
	if errors.As(err, &drift) {
		fmt.Fprintf(stderr, "pr-loop: refused: %s\n", drift)
		return record.DriftExit
	}
	return cli.Report("pr-loop", result, err, code, stdout, stderr)
}

// replyFrom posts the replies in a reply file (- for stdin).
func replyFrom(path, bodyFile string, rest []string, stdin io.Reader) (any, error) {
	if bodyFile != "" || len(rest) != 0 {
		return nil, errors.New("reply --from takes no THREAD and no --body-file; the file names the threads")
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path) //nolint:gosec // the caller's own file; the guard refuses a credential's path, and redact.Outgoing what would leak
	}
	if err != nil {
		return nil, err
	}
	plan, err := loop.ParseReplies(string(data))
	if err != nil {
		return nil, err
	}
	for _, r := range plan {
		if err := redact.Outgoing(r.Body); err != nil {
			return nil, fmt.Errorf("the reply to %s: %w", r.Thread, err)
		}
	}
	return loop.ReplyAll(plan)
}

// setupFailures names each failed run and returns 1 if there was one.
func setupFailures(runs []loop.SetupRun, stderr io.Writer) int {
	code := 0
	for _, r := range runs {
		if r.Exit != 0 {
			fmt.Fprintf(stderr, "pr-loop: setup failed in %s (exit %d)\n", r.Dir, r.Exit)
			code = 1
		}
	}
	return code
}

// interleave moves flags ahead of positional arguments, since the standard
// flag package stops at the first positional one.
func interleave(args []string) []string {
	takesValue := map[string]bool{"-body-file": true, "--body-file": true, "-from": true, "--from": true, "-lines": true, "--lines": true,
		"-timeout": true, "--timeout": true, "-interval": true, "--interval": true, "-bot": true, "--bot": true,
		"-accept-failure": true, "--accept-failure": true, "-base": true, "--base": true, "-title": true, "--title": true,
		"-sha": true, "--sha": true, "-grace": true, "--grace": true, "-lease": true, "--lease": true,
		"-outcome": true, "--outcome": true}
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if takesValue[a] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}
