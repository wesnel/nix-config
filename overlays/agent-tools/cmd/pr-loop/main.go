// Command pr-loop pushes, replies and requests reviews for an unattended pull
// request review loop, enforcing the loop's policy on every write to GitHub.
// See the usage text for the commands.
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
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/relay"
)

const usage = `pr-loop: the review loop's only way to write to GitHub.

Policy: apart from reply and resolve outside a loop (see below), only branches
registered with begin are touched, each the head of an open PR, draft or
ready, by the authenticated user in the same repository and not the default
branch; this is re-checked before every write. Everything the loop commits
must be a fixup of the PR's own commits. The other exception is open, which
publishes a branch GitHub does not have, never a default branch, as a draft
PR based on the default branch or on the head of another open PR by the
authenticated user in the same repository.

  open --title T --body-file F [--base BASE] [--dry-run] [BRANCH]
                              push BRANCH (default HEAD) through the GitHub API and open
                              it as a draft PR against BASE (default: the default branch)
  stack [BRANCH]              the stack of open PRs containing BRANCH (default HEAD)
  begin [BRANCH...]           register the branches of one stack (default: the current stack)
  worktrees [--remove]        a worktree per PR under .pr-loop/, where commits are unsigned
  push [--dry-run] [BRANCH...]  push through the GitHub API, atomically
  threads [--all] PR          unresolved review threads, labelled by bot
  reply THREAD --body-file F  reply inside a bot's thread (- reads stdin)
  resolve THREAD              resolve a bot's thread whose last comment is yours
  request [--bot B] [--accept-failure CHECK] [PR...]
                              ask each bot to review the head, once CI allows
  status [PR...]              CI, each bot's review, open threads, diff growth
  ci [PR...]                  checks on each head, compared with the base
  logs [--lines N] PR CHECK   the end of a failed GitHub Actions job's log
  wait ci|reviews [--timeout S] [--interval S] [PR...]
                              block until CI or requested reviews land (exit 3 on timeout)
  sign [--resume] [--no-push] squash the loop's fixups, sign with the hardware key, and
                              push the stack over SSH (outside a loop, use git sign-pass)
  end [--prune]               delete trigger comments and loop state
  guard                       PreToolUse hook for Claude Code and ECA (reads stdin)

reply and resolve take a thread a review bot started on an open PR by the
authenticated user in this repository, and need no begin. In a loop session
(relayed, or with PR_LOOP_SESSION=1), and on a PR registered with begin, they
take only the loop's PRs.

With ` + relay.URLEnv + ` set, every command except guard is sent to that
sandbox-relay, where sign and end are refused: run those outside the session.
`

// relayed are the commands a sandbox may run through the relay. sign needs
// the hardware key and end discards state, so a person runs those on the host.
var relayed = map[string]bool{
	"open": true, "stack": true, "begin": true, "push": true, "threads": true, "reply": true, "resolve": true,
	"request": true, "status": true, "ci": true, "logs": true, "wait": true, "worktrees": true,
}

// relayRefusal is why the relay's caller may not run argv, or "".
func relayRefusal(argv []string) string {
	if len(argv) == 0 || !relayed[argv[0]] {
		name := ""
		if len(argv) > 0 {
			name = argv[0]
		}
		return fmt.Sprintf("%q is only run on the host", name)
	}
	// The client sends a --body-file itself; a path here would name a file on
	// the host, which the sandbox must not be able to read out through GitHub.
	for i, a := range argv {
		if (a == "--body-file" && (i+1 >= len(argv) || argv[i+1] != "-")) ||
			(strings.HasPrefix(a, "--body-file=") && a != "--body-file=-") || a == "-body-file" || strings.HasPrefix(a, "-body-file=") {
			return "a relayed command takes its body on stdin"
		}
	}
	return ""
}

// forward sends argv to the relay, reading a --body-file here, where the
// sandbox can see it, and passing it on stdin.
func forward(url string, argv []string) int {
	var stdin []byte
	for i, a := range argv {
		if a == "--body-file" && i+1 < len(argv) {
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
	if url := os.Getenv(relay.URLEnv); url != "" && command != "guard" {
		os.Exit(forward(url, args))
	}
	if os.Getenv(relay.RelayEnv) == "1" {
		if reason := relayRefusal(args); reason != "" {
			fmt.Fprintf(os.Stderr, "pr-loop: refused: %s\n", reason)
			os.Exit(2)
		}
	}
	os.Exit(run(command, args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var (
		dryRun   = flags.Bool("dry-run", false, "")
		all      = flags.Bool("all", false, "")
		bodyFile = flags.String("body-file", "", "")
		base     = flags.String("base", "", "")
		title    = flags.String("title", "", "")
		lines    = flags.Int("lines", 200, "")
		timeout  = flags.Int("timeout", 240, "")
		interval = flags.Int("interval", 30, "")
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
		return guard.Main(stdin, stdout)
	}
	if code, ok := cli.Parse(flags, interleave(args), usage, stdout, stderr); !ok {
		return code
	}
	rest := flags.Args()
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
		branch := ""
		if len(rest) > 0 {
			branch = rest[0]
		}
		result, err = loop.Stack(branch)
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
		if err = need(1); err == nil {
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
		var ns []int
		if ns, err = numbers(); err == nil {
			result, err = loop.CIStatuses(ns)
		}
	case "logs":
		if err = need(2); err == nil {
			var n int
			if n, err = strconv.Atoi(strings.TrimPrefix(rest[0], "#")); err == nil {
				var text string
				if text, err = loop.Logs(n, rest[1], *lines); err == nil {
					fmt.Fprintln(stdout, text)
					return 0
				}
			}
		}
	case "wait":
		if len(rest) == 0 || (rest[0] != "ci" && rest[0] != "reviews") {
			err = errors.New("wait takes ci or reviews")
			break
		}
		target := rest[0]
		rest = rest[1:]
		var ns []int
		if ns, err = numbers(); err == nil {
			var settled bool
			result, settled, err = loop.Wait(target, ns, time.Duration(*timeout)*time.Second, time.Duration(*interval)*time.Second)
			if !settled {
				code = 3
			}
		}
	case "worktrees":
		result, err = loop.Worktrees(*remove)
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

// interleave moves flags ahead of positional arguments, since the standard
// flag package stops at the first positional one.
func interleave(args []string) []string {
	takesValue := map[string]bool{"-body-file": true, "--body-file": true, "-lines": true, "--lines": true,
		"-timeout": true, "--timeout": true, "-interval": true, "--interval": true, "-bot": true, "--bot": true,
		"-accept-failure": true, "--accept-failure": true, "-base": true, "--base": true, "-title": true, "--title": true}
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
