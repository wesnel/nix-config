// Command agent-plan keeps agents' plan files and their append-only event
// logs. See the usage text.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/prompttime"
	"github.com/wesnel/agent-tools/internal/relay"
	"github.com/wesnel/agent-tools/internal/routes"
	quota "github.com/wesnel/agent-tools/internal/usage"
)

var usage = `agent-plan: agents' plan files and their append-only event logs.

A plan is files side by side in the plans directory: <date>-<slug>.<host>.org,
<date>-<slug>.<host>.<part>.org for each sub-agent's part, and
<date>-<slug>.<host>.log.org, which only "agent-plan log" writes. The plans
directory and host come from "plans" in the user's agent-tools/config.json.

  new --title T --slug S [--repo R]... [--branch BASE..BRANCH]... [--worktree DIR]...
      [--session HARNESS:ID]... [--harness H] [--template NAME]
                     create a plan; prints its file, ID and log. --template
                     starts it with the entries of each section of
                     templates/NAME.org in the plans' repository
  new --parent PLAN --part P1 --title T [--from FOLLOWUP] [...]
                     create part P1's plan file beside its parent's; --from
                     links P1 to the follow-up it builds, as link does
  log PLAN --item ID --kind KIND [--summary S] [--role R] [--agent A] [--branch B]
      [--commit SUBJECT] [--evidence E] [--review ROUND.ID --outcome O]
      [--tokens N] [--model M] [--effort E]
                     append an event about item ID to the plan's log, and
                     write a step's or part's heading keyword from its log;
                     done or verified for a step or part that builds a
                     follow-up (see link) logs the same for the follow-up
  find [--session ID] [--worktree DIR] [--branch B]
                     this host's plan files that match any one given (default:
                     this session, worktree and branch)
  find --hook        the same for a Claude Code SessionStart hook (reads stdin),
                     with brief --resume after /clear or a compaction; as its
                     SessionEnd hook, hands the plan of a session /clear ends
                     to the session it starts in the same directory, and
                     prints nothing
  goal PLAN [--part P1]
                     the goal statement, the text under "** Goal"
  status PLAN [--format org|json]
                     steps without done, done without verified, parts ready
                     to start, steps ready now (no open AFTER item, earlier
                     ORDERED siblings settled, not scheduled for later),
                     what still waits, keyword drift, property drift (a
                     WORKTREES, BRANCHES, PULL_REQUESTS, OWNERS, REVIEWS or
                     PIPELINE value, an entry's BUILDS, or a References
                     entry, the log does not give), link drift (a follow-up
                     a step or part builds that the plan does not hold, that
                     two of them build, or that is open though its builder
                     is done or verified), structure drift (a top-level
                     heading but one
                     * Plan and * Approved plan; a section not a plan's, or
                     twice; a heading under Goal or Verification; a
                     References entry without a URL; an entry of Steps,
                     Distribution, Questions or Follow-ups without its ID;
                     a question without a question's keyword; an ORDERED
                     or Archive group with text or no entries; a heading
                     skipping a level; a heading line inside a block; a
                     block with no end), the review routes logged and
                     waived, and tokens
  set PLAN NAME [VALUE...] [--part P1]
                     set WORKTREES, BRANCHES, PULL_REQUESTS, REVIEWS (review
                     routes the change needs: ` + strings.Join(routes.Known, ", ") + `) or PIPELINE
                     (full or opted-out:<reason>) of the plan's file, or the
                     part's, to the values, and log it; new, own and the
                     hooks log the changes they make. A route REVIEWS ever
                     named stays owed, and PIPELINE is what its log last
                     gave, whatever the file says
  waive PLAN --route ROUTE --why TEXT
                     log that the plan's change needs no review of ROUTE,
                     and why; stop-check and the gates stop asking for it,
                     and git sign-pass shows the waiver before signing
  questions PLAN [--all] [--waiting] [--json | --count]
                     the open questions the operator can answer now, by
                     priority, with their recommendations; --all adds the
                     parts', --waiting those that wait for an AFTER item: a
                     question until settled, a part or step until logged
                     done, verified or abandoned
  brief PLAN         what an agent needs to go on after a compaction: the goal,
                     the decisions, the steps and parts ready, the questions
                     the operator can answer now, the steps done but not
                     verified, the open follow-ups, and any structure drift;
                     Codex's and ECA's hooks add it after one
  brief PLAN --resume
                     what a session needs to go on after /clear or a
                     compaction, within 10,000 bytes: the goal, the open
                     questions, the steps under way and ready, what waits on
                     what, branches and pull requests, any structure drift,
                     the decisions since
                     the last hand-back or sign-pass, and the IDs and
                     commands for the rest; Claude Code's SessionStart hook
                     adds it after either
  decisions PLAN [--all] [--json]
                     the questions settled as DECIDED, with their answers;
                     --all adds the parts'
  ask PLAN --question TEXT --recommendation TEXT [--priority A|B|C]
      [--context TEXT] [--alternative TEXT]... [--after ID]... [--part P1]
                     write a question for the operator under Questions, in
                     the plan's file or the part's, as its next question ID,
                     in the form questions reads, and log it as asked
  ask PLAN ... --answer TEXT [--role R]
                     write a reversible decision of your own, settled as
                     written, and log it as asked and decided
  decide PLAN QID --answer TEXT [--drop | --supersedes QID] [--role R]
                     settle open question QID in place with the operator's
                     answer, or with --drop as dropped, why in --answer;
                     --supersedes marks the settled question it replaces
                     SUPERSEDED; log it as decided
  followup PLAN --title T --found-in ID [--context T] [--part P1]
                     record work found while doing item ID, outside its
                     scope, as a TODO under Follow-ups with the next follow-up
                     ID, and log it as discovered; its keyword then follows
                     its log, as a step's does
  link PLAN ID FOLLOWUP | link PLAN ID --clear
                     record that step or part ID builds FOLLOWUP (F3, or
                     P1.F2 in part P1's file), or with --clear none, log it
                     as builds and write the entry's BUILDS from the log.
                     FOLLOWUP is TODO or STARTED, and built by no other
                     entry; a done or verified ID changes its link only
                     once cleared. Logging done or verified for ID then
                     logs it for FOLLOWUP too, at once for an ID that is
                     already. Linking ID to its own link again logs no link
                     and settles a FOLLOWUP an older agent-plan left behind
                     ID. Abandoning ID, or FOLLOWUP, releases the link for
                     good; nothing ID logs changes an abandoned FOLLOWUP
  reference PLAN URL [--note TEXT]
                     keep URL under References, or set its note: what it was
                     used for and which decisions (Q IDs) it fed; log it as
                     referenced. remind adds every URL of the operator's
                     own prompts this way, with no note
  postpone PLAN ID (--until 'YYYY-MM-DD[ HH:MM]' | --clear)
                     schedule step or part ID for that day or time, which
                     keeps it out of the ready lists and the agenda until
                     then, or clear its schedule; log it as postponed
  statusline         Claude Code's status line and Emacs's mode line (reads
                     stdin): the session's context and its provider's quota
                     windows as this host last saw them, then "review owed"
                     where the session committed since its plan's last review
                     receipt, "security review owed" where the plan's log
                     routes the change there with no route receipt or waiver
                     since, then the open questions of the session's plan,
                     else of the plan of the worktree "cwd" is in; or nothing.
                     Keeps Claude Code's rate_limits in
                     $XDG_STATE_HOME/agent-tools/usage. Emacs sends {cwd,
                     provider, context_window{used_percentage}}, or {cwd} alone
                     to see each provider at 75% or more. Never touches the
                     network. Ends with "checkpoint · /clear" where that
                     plan stands at a checkpoint (a hand-back, a sign-pass
                     handed over or signed, or a decision that left no
                     question to ask) and the context holds 200K tokens or
                     more or its cache has gone cold, and keeps each
                     session's context size and cache warmth beside the
                     rate_limits for stop-check: a Claude Code stop it lets
                     through with no background task running, at such a
                     checkpoint with 200K tokens of context or more, shows
                     the operator a suggestion to /clear, once per
                     checkpoint
  usage [--json]     this host's last reading of each provider's quota windows,
                     and the operator's choice while it lasts
  usage choose PROVIDER keep|postpone [--plan PLAN] [--answer TEXT]
                     record the operator's answer to the quota ask: keep
                     going, or postpone the task, until the window that
                     prompted it resets; with a plan, also as a question the
                     operator decided, their own words in --answer, written
                     once: run again, it changes nothing, and another answer
                     to the same question is refused. Postpone each step or
                     part with postpone
  own PLAN --worktree DIR --agent ID
                     record agent ID, the harness's ID for a sub-agent or
                     "main" for the session's main thread, as the one agent
                     that edits files in the worktree DIR (OWNERS in the plan)
  stop-check         Claude Code's and Codex's Stop hook, and ECA's postRequest
                     hook (reads stdin): blocks a stop once when the session's
                     plan has steps done but not verified, or the session
                     committed to a full pipeline and no review receipt
                     (which git fresh-review and pr-loop review log) for a
                     range BASE..HEAD, BASE at or below the branch's base in
                     the plan's BRANCHES or origin/<default>, has the patch ID
                     of BASE..HEAD as it stands, or a route the plan owes (see
                     set and waive) has commits no route receipt of it covers;
                     the commits as pr-loop guard
                     --post logs them, in a plan it makes for a commit no
                     plan takes; ECA gets the reason on stderr with exit 2,
                     logged as stop-blocked, and the chat's stops pass while
                     the log holds nothing newer but stops blocked; never
                     touches the network
  remind             a hook (reads stdin) that asks, in a session with a plan,
                     for the coordinator to check a finished sub-agent's report
                     and log its part (Claude Code's UserPromptSubmit and
                     PostToolUse on Agent, Codex's PostToolUse on wait_agent,
                     ECA's postToolCall on spawn_agent), and for a compaction
                     to keep what the plan lacks (Claude Code's PreCompact) or
                     the plan to be brought up to date after one (Codex's
                     SessionStart, ECA's postCompact); keeps the URLs of
                     the operator's own prompts as references (Claude Code's and
                     Codex's UserPromptSubmit, ECA's preRequest); and on those
                     prompts, once per host when a quota window of the
                     session's provider passes 75% and again at 90%, asks
                     whether to keep going or postpone (see usage choose), having
                     read Codex's quota from its rollout, and logs the ask as
                     usage-asked in the session's plan; and on those
                     prompts, task-notifications included, adds a line for
                     each result of the session's sign-passes not yet
                     delivered, logging it as sign-delivered, marked stale
                     when over a day old; and on every one of those prompts,
                     with or without a plan, adds the time and how long since
                     the operator's previous message in the session; never
                     blocks
  sign-id [--session ID] [--branch B]...
                     log a sign-pass about to be handed to the operator as
                     sign-requested in the plan of this session (--session,
                     else CLAUDE_CODE_SESSION_ID, CODEX_THREAD_ID or
                     ECA_CHAT_ID) and worktree, and print its ID for
                     ` + "`git sign-pass --notify ID`" + `; refused without a plan
  sign-result ID [--check]
                     print each result of sign-pass ID and log those not yet
                     delivered as delivered, as remind does; exit 1 while it
                     has none. --check prints nothing and only exits 0 once
                     one is logged
  tokens [--format org|json]
                     every plan's tokens by event kind, model and effort
  import-claude      Claude Code's PostToolUse hook on ExitPlanMode (reads stdin):
                     records the approved plan in the session's plan
  init               clone the plans' repository where it is missing and
                     "plans.remote" names it, then make the plans directory,
                     with a .gitignore for the lock files, as new does
  sync [--pull] [--settle DURATION]
                     commit this host's plan files and nothing else, take the
                     other hosts' from the remote, push, and tell each peer to
                     pull over an existing ssh master connection; --pull only
                     takes the other hosts' plans. The watcher runs it, never
                     an agent
  reseal PLAN        accept a log edited outside agent-plan, recording that in
                     it; the operator's to run

find, status and new first take the other hosts' plans, quietly, if they can.
Values are single lines; list values hold no whitespace.
KIND: ` + strings.Join(plans.Kinds, ", ") + `
ROLE: ` + strings.Join(plans.Roles, ", ") + `
OUTCOME: ` + strings.Join(plans.Outcomes, ", ") + `
EFFORT: ` + strings.Join(plans.Efforts, ", ") + `

With ` + relay.URLEnv + ` set, every command but the hooks is sent to that
sandbox-relay, which serves only log, goal, status, find, questions,
decisions and brief.
`

// relayed are the commands a sandbox may run through the relay: reading
// plans, appending to their logs, and the keywords log writes.
var relayed = map[string]bool{"log": true, "goal": true, "status": true, "find": true, "questions": true,
	"decisions": true, "brief": true}

// clientSide reports whether args run in this process even with a relay:
// the hooks, which Claude Code runs on the host.
func clientSide(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "import-claude" || args[0] == "statusline" || args[0] == "stop-check" || args[0] == "remind" ||
		args[0] == "usage" {
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
		return fmt.Sprintf("%q is not served through the relay. A sandbox runs `agent-plan log`, `goal`, `status`, `find`, `questions`, `decisions` and `brief`; put anything else in your report for the operator", name)
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
	if args[0] == "statusline" {
		// Claude Code shows whatever this prints, so a failure prints
		// nothing.
		input := readAll(stdin)
		var parts []string
		if line := quota.StatusLine(quota.DefaultState(), input, time.Now()); line != "" {
			parts = append(parts, line)
		}
		if c, err := plans.Load(); err == nil {
			if line := c.StatusLine(input); line != "" {
				parts = append(parts, line)
			}
			if checkpointHint(c, input, time.Now()) != "" {
				parts = append(parts, "checkpoint · /clear")
			}
		}
		if len(parts) > 0 {
			fmt.Fprintln(stdout, strings.Join(parts, " · "))
		}
		return 0
	}
	if args[0] == "remind" {
		// A failure asks for nothing, quietly.
		input := readAll(stdin)
		var out []byte
		c, err := plans.Load()
		if err == nil {
			out = c.Remind(input)
		}
		if ask := quota.Remind(quota.DefaultState(), input, time.Now()); ask != "" {
			if err == nil {
				c.RecordUsageAsk(input, ask, time.Now())
			}
			out = plans.WithContext(out, input, ask)
		}
		if line := prompttime.Remind(prompttime.DefaultDir(), input, time.Now()); line != "" {
			out = plans.WithContext(out, input, line)
		}
		if len(out) > 0 {
			stdout.Write(append(out, '\n'))
		}
		return 0
	}
	if args[0] == "stop-check" {
		// A failure lets the stop through, quietly.
		if c, err := plans.Load(); err == nil {
			input := readAll(stdin)
			var event struct {
				HookType string `json:"hook_type"`
			}
			if json.Unmarshal(input, &event) == nil && event.HookType == "postRequest" {
				if reason := c.StopCheckChat(input, time.Now()); reason != "" {
					fmt.Fprintln(stderr, reason)
					return 2
				}
			} else if out := c.StopCheck(input, time.Now()); len(out) > 0 {
				stdout.Write(append(out, '\n'))
			} else if out := checkpointNudge(c, quota.DefaultState(), input); len(out) > 0 {
				stdout.Write(append(out, '\n'))
			}
		}
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
		template = flags.String("template", "", "")
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
		pull     = flags.Bool("pull", false, "")
		all      = flags.Bool("all", false, "")
		asJSON   = flags.Bool("json", false, "")
		count    = flags.Bool("count", false, "")
		waiting  = flags.Bool("waiting", false, "")
		answer   = flags.String("answer", "", "")
		settle   = flags.Duration("settle", 0, "")
		question = flags.String("question", "", "")
		context  = flags.String("context", "", "")
		advice   = flags.String("recommendation", "", "")
		priority = flags.String("priority", "", "")
		drop     = flags.Bool("drop", false, "")
		replaces = flags.String("supersedes", "", "")
		until    = flags.String("until", "", "")
		foundIn  = flags.String("found-in", "", "")
		note     = flags.String("note", "", "")
		unset    = flags.Bool("clear", false, "")
		planFile = flags.String("plan", "", "")
		check    = flags.Bool("check", false, "")
		route    = flags.String("route", "", "")
		why      = flags.String("why", "", "")
		resume   = flags.Bool("resume", false, "")
		from     = flags.String("from", "", "")
		// Repeatable for new; log and find take one each.
		repos    multi
		branches multi
		trees    multi
		sessions multi
		// Repeatable for ask.
		alternatives multi
		after        multi
	)
	flags.Var(&alternatives, "alternative", "")
	flags.Var(&after, "after", "")
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
	if *from != "" && command != "new" || *unset && command != "postpone" && command != "link" {
		fmt.Fprintf(stderr, "agent-plan %s: --from is new's alone, and --clear postpone's and link's\n", command)
		return 2
	}
	if (command != "new" && (len(trees) > 1 || len(sessions) > 1)) || (command != "new" && command != "sign-id" && len(branches) > 1) {
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
	if command == "new" || command == "status" || (command == "find" && !*hook) {
		c.Pull()
	}
	switch command {
	case "new":
		if len(positional) != 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		o := plans.NewOptions{Title: *title, Slug: *slug, Repos: repos, Branches: branches, Worktrees: trees,
			Sessions: sessions, Harness: *harness, Parent: *parent, Part: *part, Template: *template}
		o.From = *from
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
			input := readAll(stdin)
			var event struct {
				Name string `json:"hook_event_name"`
			}
			if json.Unmarshal(input, &event) == nil && event.Name == "SessionEnd" {
				return hookReport(nil, c.SessionEndHook(input, time.Now()), stdout, stderr)
			}
			out, err := c.SessionHook(input)
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
	case "questions":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		qs, err := p.Questions(*all)
		if !*waiting {
			qs = plans.Askable(qs)
		}
		switch {
		case err != nil || *asJSON:
			return report(qs, err)
		case *count:
			fmt.Fprintln(stdout, len(qs))
		default:
			io.WriteString(stdout, plans.QuestionsOrg(qs))
		}
		return 0
	case "brief":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		brief, err := p.Brief(time.Now())
		if *resume {
			brief, err = p.ResumeBrief(time.Now(), plans.ResumeLimit)
		}
		if err != nil {
			return report(nil, err)
		}
		io.WriteString(stdout, brief)
		return 0
	case "decisions":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		ds, err := p.Decisions(*all)
		if err != nil || *asJSON {
			return report(ds, err)
		}
		io.WriteString(stdout, plans.DecisionsOrg(ds))
		return 0
	case "ask":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		return report(p.Ask(plans.AskOptions{Part: *part, Question: *question, Priority: *priority, Context: *context,
			Recommendation: *advice, Answer: *answer, Role: *role, Alternatives: alternatives, After: after}, time.Now()))
	case "decide":
		if len(positional) != 2 {
			fmt.Fprint(stderr, "agent-plan decide: takes PLAN QID --answer TEXT\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		return report(p.Decide(positional[1], plans.DecideOptions{Answer: *answer, Drop: *drop, Supersedes: *replaces,
			Role: *role}, time.Now()))
	case "followup":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		return report(p.Followup(plans.FollowupOptions{Title: *title, FoundIn: *foundIn, Context: *context, Part: *part},
			time.Now()))
	case "link":
		want := 3
		if *unset {
			want = 2
		}
		if len(positional) != want {
			fmt.Fprint(stderr, "agent-plan link: takes PLAN ID FOLLOWUP, or PLAN ID --clear\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		followup := ""
		if !*unset {
			followup = positional[2]
		}
		return report(p.Link(positional[1], followup, time.Now()))
	case "reference":
		if len(positional) != 2 {
			fmt.Fprint(stderr, "agent-plan reference: takes PLAN URL [--note TEXT]\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		return report(p.Reference(positional[1], *note, time.Now()))
	case "postpone":
		if len(positional) != 2 || (*until == "") == !*unset {
			fmt.Fprint(stderr, "agent-plan postpone: takes PLAN ID and --until TIME or --clear\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		return report(p.Postpone(positional[1], *until, time.Now()))
	case "usage":
		return usageCommand(c, positional, *planFile, *answer, *asJSON, stdout, stderr)
	case "init":
		r, err := c.Init()
		if r.Note != "" {
			fmt.Fprintf(stderr, "agent-plan: %s\n", r.Note)
		}
		return report(r, err)
	case "sync":
		if len(positional) != 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		return report(c.Sync(plans.SyncOptions{PullOnly: *pull, Settle: *settle}))
	case "own":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		return report(p.SetOwner(worktree, *agent, time.Now()))
	case "set":
		if len(positional) < 2 {
			fmt.Fprint(stderr, "agent-plan set: takes PLAN NAME [VALUE...] [--part P1]\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		name, values := positional[1], positional[2:]
		if err := p.SetProperty(*part, name, values, time.Now()); err != nil {
			return report(nil, err)
		}
		return report(map[string]any{"property": name, "values": values}, nil)
	case "waive":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		return report(p.Waive(*route, *why, time.Now()))
	case "reseal":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		return report(p.Reseal(time.Now()))
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
	case "sign-id":
		if len(positional) != 0 {
			fmt.Fprint(stderr, "agent-plan sign-id: takes [--session ID] [--branch B]...\n")
			return 2
		}
		if session == "" {
			session = plans.EnvSession()
		}
		top, current := checkout()
		if len(branches) == 0 && current != "" {
			branches = multi{current}
		}
		id, err := c.RequestSign(session, top, branches, time.Now())
		if err != nil {
			return report(nil, err)
		}
		fmt.Fprintln(stdout, id)
		return 0
	case "sign-result":
		if len(positional) != 1 {
			fmt.Fprint(stderr, "agent-plan sign-result: takes ID [--check]\n")
			return 2
		}
		ds, err := c.SignResults(positional[0], !*check, time.Now())
		if err != nil {
			return report(nil, err)
		}
		if len(ds) == 0 {
			if !*check {
				fmt.Fprintf(stderr, "agent-plan sign-result: no result of sign-pass %s is logged yet\n", positional[0])
			}
			return 1
		}
		if !*check {
			for _, d := range ds {
				line := d.Line()
				if d.Delivered {
					line += " (already delivered)"
				}
				fmt.Fprintln(stdout, line)
			}
		}
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

// usageCommand is `agent-plan usage` and `usage choose`.
func usageCommand(c plans.Config, args []string, planFile, answer string, asJSON bool, stdout, stderr io.Writer) int {
	s, now := quota.DefaultState(), time.Now()
	report := func(result any, err error) int {
		return cli.Report("agent-plan", result, err, 0, stdout, stderr)
	}
	switch {
	case len(args) == 0:
		sums := s.Summary(now)
		if asJSON {
			return report(sums, nil)
		}
		io.WriteString(stdout, quota.SummaryText(sums, now))
		return 0
	case len(args) == 3 && args[0] == "choose":
		provider, choice := args[1], args[2]
		var p plans.Plan
		if planFile != "" {
			var err error
			if p, err = c.Open(planFile); err != nil {
				return report(nil, err)
			}
		}
		if err := quota.CheckChoice(provider, choice); err != nil {
			return report(nil, err)
		}
		reading, known := s.Reading(provider, now)
		reading.Provider = provider
		var result struct {
			Choice   quota.Choice `json:"choice"`
			Question string       `json:"question,omitempty"`
		}
		// The plan first: a question already answered otherwise leaves the
		// choice as it was.
		if planFile != "" {
			q := quota.Question(reading, known, now)
			if answer == "" {
				answer = q.Answers[choice]
			}
			asked, err := p.Ask(plans.AskOptions{Question: q.Text, Recommendation: q.Recommendation, Alternatives: q.Alternatives,
				Answer: answer, Operator: true, Once: true}, now)
			if err != nil {
				return report(nil, err)
			}
			result.Question = asked.ID
		}
		chosen, err := s.Choose(provider, choice, now)
		if err != nil {
			return report(nil, err)
		}
		result.Choice = chosen
		return report(result, nil)
	}
	fmt.Fprint(stderr, "agent-plan usage: takes nothing, or choose PROVIDER keep|postpone [--plan PLAN] [--answer TEXT]\n")
	return 2
}

func readAll(r io.Reader) []byte {
	data, _ := io.ReadAll(r)
	return data
}

// hookReport writes a hook's output; a failure reaches the operator on stderr
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
