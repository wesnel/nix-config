// Package prcli dispatches PR commands for agent-pr and agent-ops MCP tools.
package prcli

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
	"github.com/wesnel/agent-tools/internal/loop"
	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/redact"
)

const Usage = `agent-pr: pull request publication, CI, stacks and discussions.

Policy: apart from open, every write is to the head of an open PR, draft or
ready, by the authenticated user in the same repository and not the default
branch, re-checked before it is made. A ready PR's pushes add only fixups of
its own commits. open publishes a branch GitHub does not have, or takes one
GitHub has at the local tip, heading no open PR, whose commits beyond GitHub's
base have verified signatures by the protected operator key; never a default branch; and opens it as
a draft PR based on the default branch or on the head of another open PR by
the authenticated user in the same repository. open
refuses a new commit, and push a fixup, whose message breaks the rules git
sign-pass holds messages to: a subject of at most 72 characters without a
trailing period, no attribution lines, and on a fixup only Review-*
trailers.

A loop runs in a mode, PR_LOOP_MODE, which only the operator sets, by
starting the session with review-loop --harness claude|codex
--bots|--people. In bots
mode, request and reply take bot threads, and push pushes the fixups. In
people mode, draft writes the answers to people's threads into the PR's
review record and push refuses: the operator reads the fixups and drafts,
pushes with git sign-pass --push and posts with agent-pr post. request and
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
  next [--json] [BRANCH...]   the single next step for each BRANCH (default HEAD), reading
                              only: nothing; open (open's draft PR, for a branch GitHub
                              lacks, or has at its tip with no PR and verified operator
                              signatures); sign
                              (the operator's git sign-pass --push or --set-upstream,
                              with the agent-plan sign-id that mints its --notify ID);
                              push (a signed branch GitHub is behind); or blocked,
                              including divergent histories, saying what to fix
  stack [PR|BRANCH]           the stack of open PRs containing a PR (default HEAD's), as
                              their bases chain them, and GitHub's stack of that PR as
                              "github" (null when GitHub has it in none)
  link PR...                  put the PRs, by number or branch, bottom first, each based on
                              the one before, into one GitHub stack (see below)
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
  merge [--method M] [PR|BRANCH]
                              the operator's: merge the PR (default HEAD's) and every open
                              PR below it in its GitHub stack, by M (merge, the default,
                              squash or rebase), once answered y on the terminal (see below)
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

merge lists on /dev/tty the PRs GitHub's merge-async endpoint would merge,
the PR and every open PR below it in its GitHub stack, and asks GitHub once
the operator answers y there; without a terminal it refuses. It refuses a
draft among them, or one not the authenticated user's. The PR's head must
still be the one shown, or GitHub refuses. GitHub merges in the background:
"status" is GitHub's answer (pending, merged, enqueued or failed) and "uuid"
the request's. Agents never run it.

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

agent-pr may run outside the sandbox, with the operator's login. open's
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


`

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// Main handles agent-pr arguments and streams, returning its exit status.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, Usage)
		return 2
	}
	if cli.IsHelp(args[0]) {
		fmt.Fprint(stdout, Usage)
		return 0
	}
	return Run(args[0], args[1:], stdin, stdout, stderr)
}

// Run dispatches one PR command and returns its exit status.
func Run(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
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
		asJSON   = flags.Bool("json", false, "")
		method   = flags.String("method", "", "")
		bots     multi
		accepted multi
		leases   multi
	)
	flags.Var(&bots, "bot", "")
	flags.Var(&accepted, "accept-failure", "")
	flags.Var(&leases, "lease", "")
	if code, ok := cli.Parse(flags, interleave(args), Usage, stdout, stderr); !ok {
		return code
	}
	rest := flags.Args()
	if err := git.CheckLocation(); err != nil {
		return cli.Report("agent-pr", nil, err, 0, stdout, stderr)
	}
	if err := git.CheckConfig(""); err != nil {
		return cli.Report("agent-pr", nil, err, 0, stdout, stderr)
	}
	need := func(n int) error { return needArgs(command, rest, n) }
	body := func() (string, error) { return commandBody(command, *bodyFile, stdin) }

	var result any
	var err error
	code := 0
	switch command {
	case "open":
		result, err = openCommand(rest, *base, *title, *dryRun, body)
	case "next":
		var steps []loop.NextStep
		if steps, err = loop.Next(rest); err == nil && !*asJSON {
			fmt.Fprint(stdout, loop.FormatNext(steps))
			return 0
		}
		result = steps
	case "stack":
		result, err = stackCommand(rest)
	case "link":
		result, err = loop.Link(rest)
	case "merge":
		result, err = mergeCommand(rest, *method)
	case "push":
		result, err = pushCommand(rest, leases, *dryRun)
	case "threads":
		result, err = threadsCommand(rest, *all)
	case "reply":
		result, err = replyCommand(rest, *from, *bodyFile, stdin, body)
	case "resolve":
		if err = need(1); err == nil {
			result, err = loop.Resolve(rest[0])
		}
	case "draft":
		result, err = draftCommand(rest, *outcome, body)
	case "post":
		result, err = postCommand(rest)
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
		result, err, code = waitCommand(flags, rest, *sha, *timeout, *interval, *grace)
	case "sign-check":
		result, err = loop.SignCheck()
	case "end":
		result, err = loop.End(rest)
	default:
		fmt.Fprint(stderr, Usage)
		return 2
	}

	var drift *record.DriftError
	if errors.As(err, &drift) {
		fmt.Fprintf(stderr, "agent-pr: refused: %s\n", drift)
		return record.DriftExit
	}
	return cli.Report("agent-pr", result, err, code, stdout, stderr)
}

func parsePRNumber(arg string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(arg, "#"))
	if err != nil {
		return 0, fmt.Errorf("%q is not a PR number", arg)
	}
	return n, nil
}

func openCommand(rest []string, base, title string, dryRun bool, body func() (string, error)) (any, error) {
	if len(rest) > 1 {
		return nil, errors.New("open takes at most one branch; see agent-pr --help")
	}
	branch := ""
	if len(rest) == 1 {
		branch = rest[0]
	}
	var text string
	var err error
	if err = redact.Outgoing(title); err != nil {
		return nil, err
	}
	if text, err = body(); err == nil {
		return loop.Open(branch, base, title, text, dryRun)
	}
	return nil, err
}

func stackCommand(rest []string) (any, error) {
	if len(rest) > 1 {
		return nil, errors.New("stack takes at most one PR or branch; see agent-pr --help")
	}
	target := ""
	if len(rest) == 1 {
		target = rest[0]
	}
	return loop.Stack(target)
}

func mergeCommand(rest []string, method string) (any, error) {
	if len(rest) > 1 {
		return nil, errors.New("merge takes at most one PR or branch, the top of what to merge; see agent-pr --help")
	}
	target := ""
	if len(rest) == 1 {
		target = rest[0]
	}
	return loop.Merge(target, method)
}

func pushCommand(rest, leases []string, dryRun bool) (any, error) {
	byBranch := map[string]string{}
	for _, l := range leases {
		b, oid, ok := strings.Cut(l, "=")
		if !ok || b == "" || oid == "" {
			return nil, fmt.Errorf("--lease takes BRANCH=SHA, not %q", l)
		}
		byBranch[b] = oid
	}
	return loop.Push(rest, byBranch, dryRun)
}

func threadsCommand(rest []string, all bool) (any, error) {
	if err := needArgs("threads", rest, 1); err != nil {
		return nil, err
	}
	n, err := parsePRNumber(rest[0])
	if err != nil {
		return nil, err
	}
	return loop.Threads(n, all)
}

func replyCommand(rest []string, from, bodyFile string, stdin io.Reader, body func() (string, error)) (any, error) {
	if from != "" {
		return replyFrom(from, bodyFile, rest, stdin)
	}
	if err := needArgs("reply", rest, 1); err != nil {
		return nil, err
	}
	text, err := body()
	if err != nil {
		return nil, err
	}
	return loop.Reply(rest[0], text)
}

func draftCommand(rest []string, outcome string, body func() (string, error)) (any, error) {
	if err := needArgs("draft", rest, 1); err != nil {
		return nil, err
	}
	text, err := body()
	if err != nil {
		return nil, err
	}
	return loop.Draft(rest[0], text, outcome)
}

func postCommand(rest []string) (any, error) {
	if len(rest) == 0 {
		return nil, errors.New("post takes the PR, then any of its threads; see agent-pr --help")
	}
	n, err := parsePRNumber(rest[0])
	if err != nil {
		return nil, err
	}
	return loop.Post(n, rest[1:])
}

func waitCommand(flags *flag.FlagSet, rest []string, sha string, timeout, interval, grace int) (any, error, int) {
	var result any
	var err error
	code := 0
	if len(rest) == 0 || (rest[0] != "ci" && rest[0] != "reviews") {
		return nil, errors.New("wait takes ci or reviews"), 0
	}
	target := rest[0]
	rest = rest[1:]
	wait, every := time.Duration(timeout)*time.Second, time.Duration(interval)*time.Second
	ciOnly := sha != ""
	flags.Visit(func(f *flag.Flag) { ciOnly = ciOnly || f.Name == "grace" })
	settled := false
	switch {
	case target == "ci":
		var w *loop.CIWait
		w, settled, err = loop.WaitCI(rest, sha, wait, every, time.Duration(grace)*time.Second)
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
	return result, err, code
}

func needArgs(command string, rest []string, n int) error {
	if len(rest) != n {
		return fmt.Errorf("%s takes %d argument(s); see agent-pr --help", command, n)
	}
	return nil
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

// interleave moves flags ahead of positional arguments, since the standard
// flag package stops at the first positional one.
func interleave(args []string) []string {
	takesValue := map[string]bool{"-body-file": true, "--body-file": true, "-from": true, "--from": true, "-lines": true, "--lines": true,
		"-timeout": true, "--timeout": true, "-interval": true, "--interval": true, "-bot": true, "--bot": true,
		"-accept-failure": true, "--accept-failure": true, "-base": true, "--base": true, "-title": true, "--title": true,
		"-sha": true, "--sha": true, "-grace": true, "--grace": true, "-lease": true, "--lease": true,
		"-outcome": true, "--outcome": true, "-method": true, "--method": true}
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

func commandBody(command, bodyFile string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	switch bodyFile {
	case "":
		err = fmt.Errorf("%s needs --body-file", command)
	case "-":
		data, err = io.ReadAll(stdin)
	default:
		data, err = os.ReadFile(bodyFile) //nolint:gosec // the caller's own file; the guard refuses a credential's path, and redact.Outgoing what would leak
	}
	if err == nil {
		err = redact.Outgoing(string(data))
	}
	return string(data), err
}
