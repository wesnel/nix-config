// Command pr-loop pushes, replies and requests reviews for an unattended pull
// request review loop, enforcing the loop's policy on every write to GitHub.
// See the usage text for the commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/loop"
	"github.com/wesnel/agent-tools/internal/prreview"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/relay"
)

const usage = `pr-loop: the review loop's only way to write to GitHub.

Policy: apart from reply, resolve and link outside a loop (see below), only branches
registered with begin are touched, each the head of an open PR, draft or
ready, by the authenticated user in the same repository and not the default
branch; this is re-checked before every write. Everything the loop commits
must be a fixup of the PR's own commits. The other exception is open, which
publishes a branch GitHub does not have, never a default branch, as a draft
PR based on the default branch or on the head of another open PR by the
authenticated user in the same repository. open refuses a new commit, and
push a loop fixup, whose message breaks the rules git sign-pass holds
messages to: a subject of at most 72 characters without a trailing period,
no attribution lines, and on a fixup only Review-* trailers.

  open --title T --body-file F [--base BASE] [--dry-run] [BRANCH]
                              push BRANCH (default HEAD) through the GitHub API and open
                              it as a draft PR against BASE (default: the default branch),
                              in BASE's PR's GitHub stack, as link puts it there; refuses
                              when BRANCH contains a remote branch BASE lacks, and names
                              the BASE to pass instead
  stack [PR|BRANCH]           the stack of open PRs containing a PR (default HEAD's), as
                              their bases chain them, and GitHub's stack of that PR as
                              "github" (null when GitHub has it in none)
  link PR...                  put the PRs, by number or branch, bottom first, each based on
                              the one before, into one GitHub stack (see below)
  begin [BRANCH...]           register the branches of one stack (default: the current stack)
  worktrees [--remove]        a worktree per PR under .pr-loop/, where commits are unsigned;
                              runs the setup command in each one whose HEAD moved since its last setup
  setup [PATH]                run the setup command in the worktree at PATH (default: this one)
  setup-command               the setup command configured for this repository
  push [--dry-run] [BRANCH...]  push through the GitHub API, atomically
  threads [--all] PR          unresolved review threads, labelled by bot
  reply THREAD --body-file F  reply inside a bot's thread (- reads stdin)
  reply --from F              post the replies in a reply file (- reads stdin)
  resolve THREAD              resolve a bot's thread whose last comment is yours
  request [--bot B] [--accept-failure CHECK] [PR...]
                              ask each bot to review the head, once CI allows
  status [PR...]              CI, each bot's review, open threads, diff growth
  ci [PR...]                  checks on each head, compared with the base
  logs [--lines N] PR CHECK   the end of a failed GitHub Actions job's log
  wait ci [--sha REV] [--grace S] [--timeout S] [--interval S] [PR...]
                              block until CI on each head finishes (exit 3 on timeout,
                              4 when a head has no checks)
  wait reviews [--timeout S] [--interval S] [PR...]
                              block until requested reviews land (exit 3 on timeout)
  sign [--resume] [--no-push] squash the loop's fixups, sign with the hardware key, and
                              push the stack over SSH (outside a loop, use git sign-pass)
  end [--prune]               delete trigger comments and loop state
  guard [--baseline]          PreToolUse hook for Claude Code, Codex and ECA (reads stdin):
                              a loop's rules, or with --baseline every session's, by agent
  guard --post                PostToolUse hook for Claude Code, Codex and ECA (reads stdin):
                              records the shell call's commits, pushes, new worktrees,
                              branches and PRs in the plan of its session or worktree,
                              asking which step a commit completes; after a push to
                              GitHub, names the pr-loop wait ci to start
` + prreview.Usage + `
reply and resolve take a thread a review bot started on an open PR by the
authenticated user in this repository, and need no begin. In a loop session
(relayed, or with PR_LOOP_SESSION=1), and on a PR registered with begin, they
take only the loop's PRs.

ci, wait ci and logs take a PR by number or by branch and need no begin.
Outside a loop session each must be an open PR by the authenticated user in
this repository, by default the current branch's, or the loop's PRs where
HEAD heads none and a loop is registered here. In a loop session they read
the PRs named, or the loop's.

link needs no begin. It adds the PRs onto the top of the GitHub stack the
lowest is in, or the one the PR it is based on tops, or else makes a new
stack of them; PRs already in it, in order, are left alone. Every open PR in
a stack it changes must be the authenticated user's. In a loop session it
takes only the loop's PRs, and open stacks a new PR only on one of them;
when open cannot stack a PR, the PR stays open and "unstacked" says why and
which link to run later.

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

A reply file has a section per thread: a line "=== thread THREAD", or
"=== thread THREAD resolve" to resolve the thread after replying, then the
Markdown reply up to the next such line. Every line starting "=== thread" is
a header. Every thread is checked before anything is posted; threads already
resolved are skipped and reported.

` + prreview.Help + `
A setup command prepares a fresh checkout, e.g. generating files the
repository does not commit. It is taken from "setup" under "repos" in the
user's agent-tools/config.json, keyed by owner/name, and never from the
repository. It
runs with sh in this process, printed on stderr before it runs and followed
by its exit status; a failed setup makes the command exit 1.

With ` + relay.URLEnv + ` set, every command except guard, setup and
worktrees is sent to that sandbox-relay, where sign and end are refused: run
those outside the session. worktrees has the relay create the worktrees, and
setup asks it for the command, then both run the command here, in the sandbox.
`

// relayed are the commands a sandbox may run through the relay. sign needs
// the hardware key and end discards state, so a person runs those on the host.
// setup runs a command, which belongs in the sandbox, never on the host.
var relayed = map[string]bool{
	"open": true, "stack": true, "link": true, "begin": true, "push": true, "threads": true, "reply": true, "resolve": true,
	"request": true, "status": true, "ci": true, "logs": true, "wait": true, "worktrees": true,
	"setup-command": true, "review": true,
}

// reviewRefusal is why a relayed caller may not run `review argv...`, or "".
// It names its pull request by number alone, the relay's working directory
// saying which repository, and gives a reply or outcomes on stdin: no path,
// no other repository and no model.
func reviewRefusal(argv []string) string {
	const narrow = "a relayed review takes only a pull request number, --reviewer codex|subagent, and --check or " +
		"settle with their input on stdin; run anything else on the host, or hand Wesley the command"
	var positional []string
	check, reviewer := false, false
	for i := 0; i < len(argv); i++ {
		switch a := argv[i]; {
		case a == "--check" || a == "-check":
			check = true
		case a == "--reviewer" || a == "-reviewer":
			if i+1 >= len(argv) || (argv[i+1] != "codex" && argv[i+1] != "subagent") {
				return narrow
			}
			reviewer = true
			i++
		case a == "--reviewer=codex" || a == "--reviewer=subagent":
			reviewer = true
		case strings.HasPrefix(a, "-"):
			return narrow
		default:
			positional = append(positional, a)
		}
	}
	settle := len(positional) == 2 && positional[0] == "settle"
	if (len(positional) != 1 && !settle) || ((check || settle) && reviewer) || (check && settle) {
		return narrow
	}
	if _, err := strconv.Atoi(positional[len(positional)-1]); err != nil {
		return narrow
	}
	return ""
}

// clientSide are the commands that run in this process even with a relay:
// each does its part on the host through the relay itself.
var clientSide = map[string]bool{"guard": true, "setup": true, "worktrees": true}

// relayRefusal is why the relay's caller may not run argv, or "".
func relayRefusal(argv []string) string {
	if len(argv) == 0 || !relayed[argv[0]] {
		name := ""
		if len(argv) > 0 {
			name = argv[0]
		}
		if name == "setup" {
			return "setup runs its command in the caller's sandbox, never through the relay; " +
				"run `pr-loop setup` in the sandbox, which asks the relay only for the command"
		}
		return fmt.Sprintf("%q is only run on the host, outside the sandbox; put the exact pr-loop command in the report for Wesley", name)
	}
	if argv[0] == "review" {
		return reviewRefusal(argv[1:])
	}
	// The client sends a --body-file or --from itself; a path here would name a
	// file on the host, which the sandbox must not be able to read out through
	// GitHub.
	for i, a := range argv {
		for _, f := range fileFlags {
			if (a == "--"+f && (i+1 >= len(argv) || argv[i+1] != "-")) ||
				(strings.HasPrefix(a, "--"+f+"=") && a != "--"+f+"=-") || a == "-"+f || strings.HasPrefix(a, "-"+f+"=") {
				return "a relayed command takes its body on stdin; pass `--" + f + " FILE` as a separate argument, or `--" + f + " -`"
			}
		}
	}
	return ""
}

// fileFlags name files that forward reads in the sandbox and sends as stdin.
var fileFlags = []string{"body-file", "from"}

// forward sends argv to the relay, reading a --body-file or --from here,
// where the sandbox can see it, and passing it on stdin.
func forward(url string, argv []string) int {
	var stdin []byte
	// A review's reply or outcomes come on stdin.
	if argv[0] == "review" && (slices.Contains(argv, "--check") || slices.Contains(argv, "-check") || slices.Contains(argv, "settle")) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pr-loop: %s\n", err)
			return 1
		}
		return relay.Client(url, "pr-loop", argv, data)
	}
	for i, a := range argv {
		if (a == "--body-file" || a == "--from") && i+1 < len(argv) {
			var err error
			if argv[i+1] == "-" {
				stdin, err = io.ReadAll(os.Stdin)
			} else {
				stdin, err = os.ReadFile(argv[i+1])
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "pr-loop: %s\n", err)
				return 1
			}
			argv = append(append(append([]string{}, argv[:i+1]...), "-"), argv[i+2:]...)
			break
		}
	}
	return relay.Client(url, "pr-loop", argv, stdin)
}

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
	command := args[0]
	url := os.Getenv(relay.URLEnv)
	if url != "" && !clientSide[command] {
		os.Exit(forward(url, args))
	}
	relayed := os.Getenv(relay.RelayEnv) == "1"
	if relayed {
		if reason := relayRefusal(args); reason != "" {
			fmt.Fprintf(os.Stderr, "pr-loop: refused: %s\n", reason)
			os.Exit(2)
		}
	}
	os.Exit(run(command, args[1:], url, relayed, os.Stdin, os.Stdout, os.Stderr))
}

// run runs command. url is the relay to do host work through, or "" to do it
// here; relayed means this process is the relay's, on the host, where no
// setup command may run.
func run(command string, args []string, url string, relayed bool, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var (
		dryRun   = flags.Bool("dry-run", false, "")
		all      = flags.Bool("all", false, "")
		bodyFile = flags.String("body-file", "", "")
		base     = flags.String("base", "", "")
		title    = flags.String("title", "", "")
		from     = flags.String("from", "", "")
		lines    = flags.Int("lines", 200, "")
		timeout  = flags.Int("timeout", 240, "")
		interval = flags.Int("interval", 30, "")
		sha      = flags.String("sha", "", "")
		grace    = flags.Int("grace", 180, "")
		resume   = flags.Bool("resume", false, "")
		noPush   = flags.Bool("no-push", false, "")
		prune    = flags.Bool("prune", false, "")
		remove   = flags.Bool("remove", false, "")
		bots     multi
		accepted multi
	)
	flags.Var(&bots, "bot", "")
	flags.Var(&accepted, "accept-failure", "")
	if command == "guard" {
		return guard.Main(args, stdin, stdout, stderr)
	}
	if command == "review" {
		return prreview.Main(context.Background(), args, relayed, stdin, stdout, stderr)
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
	numbers := func() ([]int, error) {
		out := []int{}
		for _, a := range rest {
			n, err := strconv.Atoi(strings.TrimPrefix(a, "#"))
			if err != nil {
				return nil, fmt.Errorf("%q is not a PR number", a)
			}
			out = append(out, n)
		}
		return out, nil
	}
	need := func(n int) error {
		if len(rest) != n {
			return fmt.Errorf("%s takes %d argument(s); see pr-loop --help", command, n)
		}
		return nil
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
			data, err = os.ReadFile(*bodyFile)
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
	case "begin":
		result, err = loop.Begin(rest)
	case "push":
		result, err = loop.Push(rest, *dryRun)
	case "threads":
		if err = need(1); err == nil {
			var ns []int
			if ns, err = numbers(); err == nil {
				result, err = loop.Threads(ns[0], *all)
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
	case "request":
		var ns []int
		if ns, err = numbers(); err == nil {
			result, err = loop.Request(ns, bots, accepted)
		}
	case "status":
		var ns []int
		if ns, err = numbers(); err == nil {
			result, err = loop.Status(ns)
		}
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
			var ns []int
			if ns, err = numbers(); err == nil {
				result, settled, err = loop.WaitReviews(ns, wait, every)
			}
		}
		if !settled {
			code = 3
		}
	case "worktrees":
		if url == "" {
			result, err = loop.Worktrees(*remove)
			if code := cli.Report("pr-loop", result, err, 0, stdout, stderr); code != 0 || *remove || relayed {
				return code
			}
		} else {
			argv := []string{"worktrees"}
			if *remove {
				argv = append(argv, "--remove")
			}
			r, err := relay.Call(url, "pr-loop", argv, nil)
			if err != nil {
				return cli.Report("pr-loop", nil, err, 0, stdout, stderr)
			}
			io.WriteString(stdout, r.Stdout)
			io.WriteString(stderr, r.Stderr)
			if r.Code != 0 || *remove {
				return r.Code
			}
		}
		return setUpWorktrees(url, stderr)
	case "setup":
		if len(rest) > 1 {
			err = errors.New("setup takes at most one path; see pr-loop --help")
			break
		}
		if len(rest) == 1 {
			// The repository is identified, and the relay checks the
			// directory, from where the command runs.
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
		if setup, err = setupCommand(url); err != nil {
			break
		}
		runs := []loop.SetupRun{}
		if setup.Command != "" {
			runs = loop.RunSetup(setup.Command, []string{dir}, stderr)
		}
		result = map[string]any{"repo": setup.Repo, "command": setup.Command, "runs": runs}
		code = setupFailures(runs, stderr)
	case "setup-command":
		result, err = loop.SetupCommand()
	case "sign":
		var active bool
		if active, err = loop.Active(); err == nil {
			if active {
				result, err = loop.Sign(*resume, *noPush)
			} else {
				err = refusal.New("no review loop is running here; sign a branch with `git sign-pass`")
			}
		}
	case "end":
		result, err = loop.End(*prune)
	default:
		fmt.Fprint(stderr, usage)
		return 2
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
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	plan, err := loop.ParseReplies(string(data))
	if err != nil {
		return nil, err
	}
	return loop.ReplyAll(plan)
}

// setupCommand looks the repository's setup command up where the user's
// config is: on the host through the relay at url, or here.
func setupCommand(url string) (*loop.Setup, error) {
	if url == "" {
		return loop.SetupCommand()
	}
	r, err := relay.Call(url, "pr-loop", []string{"setup-command"}, nil)
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("setup-command through the relay exited %d: %s", r.Code, strings.TrimSpace(r.Stderr))
	}
	var setup loop.Setup
	if err := json.Unmarshal([]byte(r.Stdout), &setup); err != nil {
		return nil, fmt.Errorf("setup-command through the relay: %w", err)
	}
	return &setup, nil
}

// setUpWorktrees runs the setup command in each of the loop's worktrees
// whose HEAD moved since its last successful setup, and returns the exit
// code. The worktrees' JSON is already on stdout, so this writes to stderr.
func setUpWorktrees(url string, stderr io.Writer) int {
	setup, err := setupCommand(url)
	if err != nil {
		fmt.Fprintf(stderr, "pr-loop: %s\n", err)
		return 1
	}
	if setup.Command == "" {
		return 0
	}
	paths, err := loop.WorktreePaths()
	if err != nil {
		fmt.Fprintf(stderr, "pr-loop: %s\n", err)
		return 1
	}
	var due []string
	for _, p := range paths {
		if loop.NeedsSetup(p) {
			due = append(due, p)
		}
	}
	return setupFailures(loop.RunSetup(setup.Command, due, stderr), stderr)
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
		"-sha": true, "--sha": true, "-grace": true, "--grace": true}
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
