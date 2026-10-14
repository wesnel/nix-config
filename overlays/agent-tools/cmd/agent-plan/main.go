// Command agent-plan keeps agents' plan files and their append-only event
// logs. See the usage text.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/relay"
)

var usage = `agent-plan: agents' plan files and their append-only event logs.

A plan is files side by side in the plans directory: <date>-<slug>.<host>.org,
<date>-<slug>.<host>.<part>.org for each sub-agent's part, and
<date>-<slug>.<host>.log.org, which only "agent-plan log" writes. The plans
directory and host come from "plans" in the user's agent-tools/config.json.

  new --title T --slug S [--repo R]... [--branch BASE..BRANCH]... [--worktree DIR]...
      [--session HARNESS:ID]... [--harness H]
                     create a plan; prints its file, ID and log
  new --parent PLAN --part P1 --title T [...]
                     create part P1's plan file beside its parent's
  log PLAN --item ID --kind KIND [--summary S] [--role R] [--agent A] [--branch B]
      [--commit SUBJECT] [--evidence E] [--review ROUND.ID --outcome O]
      [--tokens N] [--model M] [--effort E]
                     append an event about item ID to the plan's log
  find [--session ID] [--worktree DIR] [--branch B]
                     this host's plan files that match any one given (default:
                     this session, worktree and branch)
  find --hook        the same for a Claude Code SessionStart hook (reads stdin)
  goal PLAN [--part P1]
                     the goal statement, the text under "** Goal"
  status PLAN [--format org|json]
                     steps without done, done without verified, and tokens
  tokens [--format org|json]
                     every plan's tokens by event kind, model and effort
  import-claude      Claude Code's PostToolUse hook on ExitPlanMode (reads stdin):
                     records the approved plan in the session's plan
  init               make the plans directory, with a .gitignore for the logs'
                     lock files, as new does

PLAN is one of a plan's files, or its name, <date>-<slug>.<host>. Values are single lines; list
values hold no whitespace.
KIND: ` + strings.Join(plans.Kinds, ", ") + `
ROLE: ` + strings.Join(plans.Roles, ", ") + `
OUTCOME: ` + strings.Join(plans.Outcomes, ", ") + `
EFFORT: ` + strings.Join(plans.Efforts, ", ") + `

With ` + relay.URLEnv + ` set, every command but the hooks is sent to that
sandbox-relay, which serves only log, goal, status and find.
`

// relayed are the commands a sandbox may run through the relay: reading
// plans and appending to their logs.
var relayed = map[string]bool{"log": true, "goal": true, "status": true, "find": true}

// clientSide reports whether args run in this process even with a relay:
// the hooks, which Claude Code runs on the host.
func clientSide(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "import-claude" {
		return true
	}
	if args[0] == "find" {
		for _, a := range args[1:] {
			if a == "--hook" || a == "-hook" {
				return true
			}
		}
	}
	return false
}

// relayRefusal is why the relay's caller may not run args, or "".
func relayRefusal(args []string) string {
	if len(args) == 0 || !relayed[args[0]] || clientSide(args) {
		name := strings.Join(args[:min(len(args), 2)], " ")
		return fmt.Sprintf("%q is not served through the relay. A sandbox runs `agent-plan log`, `goal`, `status` and `find`; put anything else in your report for Wesley", name)
	}
	return ""
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() { os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

// Main runs agent-plan with args and returns its exit code.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	if cli.IsHelp(args[0]) {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if url := os.Getenv(relay.URLEnv); url != "" && !clientSide(args) {
		r, err := relay.Call(url, "agent-plan", args, nil)
		if err != nil {
			fmt.Fprintf(stderr, "agent-plan: %s\n", err)
			return 1
		}
		io.WriteString(stdout, r.Stdout)
		io.WriteString(stderr, r.Stderr)
		return r.Code
	}
	if os.Getenv(relay.RelayEnv) == "1" {
		if reason := relayRefusal(args); reason != "" {
			fmt.Fprintf(stderr, "agent-plan: refused: %s\n", reason)
			return 2
		}
	}
	return run(args[0], args[1:], stdin, stdout, stderr)
}

// parse parses flags wherever they fall among the positional arguments,
// which it returns.
func parse(flags *flag.FlagSet, args []string, stdout, stderr io.Writer) ([]string, int, bool) {
	var positional []string
	for {
		if code, ok := cli.Parse(flags, args, usage, stdout, stderr); !ok {
			return nil, code, false
		}
		rest := flags.Args()
		if len(rest) == 0 {
			return positional, 0, true
		}
		positional, args = append(positional, rest[0]), rest[1:]
	}
}

// checkout is the top level and branch of the worktree the caller is in.
func checkout() (top, branch string) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", ""
	}
	top, err = git.Run("-C", cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ""
	}
	branch, _ = git.Run("-C", cwd, "branch", "--show-current")
	return top, branch
}

// claudeSession is the Claude Code session running this command, if any.
func claudeSession() string {
	if id := os.Getenv("CLAUDE_CODE_SESSION_ID"); id != "" {
		return "claude:" + id
	}
	return ""
}

func run(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var (
		title    = flags.String("title", "", "")
		slug     = flags.String("slug", "", "")
		harness  = flags.String("harness", "", "")
		parent   = flags.String("parent", "", "")
		part     = flags.String("part", "", "")
		item     = flags.String("item", "", "")
		kind     = flags.String("kind", "", "")
		summary  = flags.String("summary", "", "")
		role     = flags.String("role", "", "")
		agent    = flags.String("agent", "", "")
		commit   = flags.String("commit", "", "")
		evidence = flags.String("evidence", "", "")
		review   = flags.String("review", "", "")
		outcome  = flags.String("outcome", "", "")
		tokens   = flags.Int64("tokens", 0, "")
		model    = flags.String("model", "", "")
		effort   = flags.String("effort", "", "")
		format   = flags.String("format", "org", "")
		hook     = flags.Bool("hook", false, "")
		// Repeatable for new; log and find take one each.
		repos    multi
		branches multi
		trees    multi
		sessions multi
	)
	flags.Var(&repos, "repo", "")
	flags.Var(&branches, "branch", "")
	flags.Var(&trees, "worktree", "")
	flags.Var(&sessions, "session", "")
	positional, code, ok := parse(flags, args, stdout, stderr)
	if !ok {
		return code
	}
	one := func(values multi) string {
		if len(values) == 0 {
			return ""
		}
		return values[len(values)-1]
	}
	if command != "new" && (len(branches) > 1 || len(trees) > 1 || len(sessions) > 1) {
		fmt.Fprintf(stderr, "agent-plan %s: --branch, --worktree and --session are given once\n", command)
		return 2
	}
	branch, session, worktree := one(branches), one(sessions), one(trees)
	report := func(result any, err error) int {
		return cli.Report("agent-plan", result, err, 0, stdout, stderr)
	}
	c, err := plans.Load()
	if err != nil {
		return report(nil, err)
	}
	wantPlan := func() (plans.Plan, bool) {
		if len(positional) != 1 {
			fmt.Fprintf(stderr, "agent-plan %s: takes one PLAN\n", command)
			return plans.Plan{}, false
		}
		p, err := c.Open(positional[0])
		if err != nil {
			report(nil, err)
			return plans.Plan{}, false
		}
		return p, true
	}
	switch command {
	case "new":
		if len(positional) != 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		o := plans.NewOptions{Title: *title, Slug: *slug, Repos: repos, Branches: branches, Worktrees: trees,
			Sessions: sessions, Harness: *harness, Parent: *parent, Part: *part}
		if len(o.Sessions) == 0 {
			if s := claudeSession(); s != "" {
				o.Sessions = []string{s}
				if o.Harness == "" {
					o.Harness = "claude"
				}
			}
		}
		if len(o.Worktrees) == 0 {
			if top, _ := checkout(); top != "" && !strings.ContainsAny(top, " \t") {
				o.Worktrees = []string{top}
			}
		}
		return report(c.New(o, time.Now()))
	case "log":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		return report(p.Log(plans.Event{Kind: *kind, Item: *item, Summary: *summary, Role: *role, Agent: *agent,
			Branch: branch, Commit: *commit, Evidence: *evidence, Review: *review, Outcome: *outcome,
			Tokens: *tokens, Model: *model, Effort: *effort}, time.Now()))
	case "find":
		if *hook {
			out, err := c.SessionHook(readAll(stdin))
			return hookReport(out, err, stdout, stderr)
		}
		o := plans.FindOptions{Session: session, Worktree: worktree, Branch: branch}
		if o == (plans.FindOptions{}) {
			o.Session = claudeSession()
			o.Worktree, o.Branch = checkout()
		}
		return report(c.Find(o))
	case "goal":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		goal, err := p.Goal(*part)
		if err != nil {
			return report(nil, err)
		}
		io.WriteString(stdout, goal)
		return 0
	case "status":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		s, err := p.Status()
		if err != nil || *format == "json" {
			return report(s, err)
		}
		io.WriteString(stdout, s.Org())
		return 0
	case "init":
		return report(c.Init())
	case "tokens":
		rows, err := c.Tokens()
		if err != nil || *format == "json" {
			return report(rows, err)
		}
		io.WriteString(stdout, plans.TokensOrg(rows))
		return 0
	case "import-claude":
		out, err := c.ImportClaude(readAll(stdin), time.Now())
		return hookReport(out, err, stdout, stderr)
	}
	fmt.Fprint(stderr, usage)
	return 2
}

func readAll(r io.Reader) []byte {
	data, _ := io.ReadAll(r)
	return data
}

// hookReport writes a hook's output; a failure reaches Wesley on stderr
// without blocking the session.
func hookReport(out []byte, err error, stdout, stderr io.Writer) int {
	if err != nil {
		fmt.Fprintf(stderr, "agent-plan: %s\n", err)
		return 1
	}
	if len(out) > 0 {
		stdout.Write(append(out, '\n'))
	}
	return 0
}
