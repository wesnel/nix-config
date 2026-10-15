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
	"github.com/wesnel/agent-tools/internal/relay"
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
  new --parent PLAN --part P1 --title T [...]
                     create part P1's plan file beside its parent's
  log PLAN --item ID --kind KIND [--summary S] [--role R] [--agent A] [--branch B]
      [--commit SUBJECT] [--evidence E] [--review ROUND.ID --outcome O]
      [--tokens N] [--model M] [--effort E]
                     append an event about item ID to the plan's log, and
                     write a step's or part's heading keyword from its log
  find [--session ID] [--worktree DIR] [--branch B]
                     this host's plan files that match any one given (default:
                     this session, worktree and branch)
  find --hook        the same for a Claude Code SessionStart hook (reads stdin)
  goal PLAN [--part P1]
                     the goal statement, the text under "** Goal"
  status PLAN [--format org|json]
                     steps without done, done without verified, parts ready
                     to start, steps ready now (no open AFTER item, earlier
                     ORDERED siblings settled, not scheduled for later),
                     what still waits, keyword drift, and tokens
  questions PLAN [--all] [--waiting] [--json | --count]
                     the open questions Wesley can answer now, by priority,
                     with their recommendations; --all adds the parts',
                     --waiting those that wait for an AFTER item: a question
                     until settled, a part or step until logged done,
                     verified or abandoned
  brief PLAN         what an agent needs to go on after a compaction: the goal,
                     the decisions, the steps and parts ready, the questions
                     Wesley can answer now, the steps done but not verified,
                     and the open follow-ups; the hooks add it after one
  decisions PLAN [--all] [--json]
                     the questions settled as DECIDED, with their answers;
                     --all adds the parts'
  ask PLAN --question TEXT --recommendation TEXT [--priority A|B|C]
      [--context TEXT] [--alternative TEXT]... [--after ID]... [--part P1]
                     write a question for Wesley under Questions, in the
                     plan's file or the part's, as its next question ID, in
                     the form questions reads, and log it as asked
  ask PLAN ... --answer TEXT [--role R]
                     write a reversible decision of your own, settled as
                     written, and log it as asked and decided
  decide PLAN QID --answer TEXT [--drop | --supersedes QID] [--role R]
                     settle open question QID in place with Wesley's answer,
                     or with --drop as dropped, why in --answer; --supersedes
                     marks the settled question it replaces SUPERSEDED; log
                     it as decided
  followup PLAN --title T --found-in ID [--context T] [--part P1]
                     record work found while doing item ID, outside its
                     scope, as a TODO under Follow-ups with the next follow-up
                     ID, and log it as discovered; its keyword then follows
                     its log, as a step's does
  reference PLAN URL [--note TEXT]
                     keep URL under References, or set its note: what it was
                     used for and which decisions (Q IDs) it fed; log it as
                     referenced. remind adds every URL of Wesley's own
                     prompts this way, with no note
  postpone PLAN ID (--until 'YYYY-MM-DD[ HH:MM]' | --clear)
                     schedule step or part ID for that day or time, which
                     keeps it out of the ready lists and the agenda until
                     then, or clear its schedule; log it as postponed
  statusline         Claude Code's status line and Emacs's mode line (reads
                     stdin): the session's context and its provider's quota
                     windows as this host last saw them, then "review owed"
                     where the session committed since its plan's last review
                     receipt, then the open questions of the session's plan,
                     else of the plan of the worktree "cwd" is in; or nothing.
                     Keeps Claude Code's rate_limits in
                     $XDG_STATE_HOME/agent-tools/usage. Emacs sends {cwd,
                     provider, context_window{used_percentage}}, or {cwd} alone
                     to see each provider at 75% or more. Never touches the
                     network
  usage [--json]     this host's last reading of each provider's quota windows,
                     and Wesley's choice while it lasts
  usage choose PROVIDER keep|down|postpone [--plan PLAN] [--answer TEXT]
                     record Wesley's answer to the quota ask: keep the usual
                     tiers, start new sub-agents one tier down, or postpone
                     work, until the window that prompted it resets; with a
                     plan, also as a question he decided, his own words in
                     --answer, written once: run again, it changes nothing,
                     and another answer to the same question is refused.
                     Postpone each step or part with postpone
  own PLAN --worktree DIR --agent ID
                     record agent ID, the harness's ID for a sub-agent or
                     "main" for the session's main thread, as the one agent
                     that edits files in the worktree DIR (OWNERS in the plan)
  stop-check         Claude Code's and Codex's Stop hook, and ECA's postRequest
                     hook (reads stdin): blocks a stop once when the session's
                     own commits have no plan, its plan's steps are done but
                     not verified, or the session committed to a full
                     pipeline and no review receipt (which git fresh-review
                     and pr-loop review log) for a range BASE..HEAD, BASE at
                     or below the branch's base in the plan's BRANCHES or
                     origin/<default>, has the patch ID of BASE..HEAD as it
                     stands; the commits as pr-loop guard
                     --post records them (outside any plan, in
                     $XDG_STATE_HOME/agent-tools/commits); ECA gets
                     the reason on stderr with exit 2, once per chat, kept in
                     $XDG_STATE_HOME/agent-tools/stop; never touches the network
  remind             a hook (reads stdin) that asks, in a session with a plan,
                     for the coordinator to check a finished sub-agent's report
                     and log its part (Claude Code's UserPromptSubmit and
                     PostToolUse on Agent, Codex's PostToolUse on wait_agent,
                     ECA's postToolCall on spawn_agent), and for a compaction
                     to keep what the plan lacks (Claude Code's PreCompact) or
                     the plan to be brought up to date after one (Codex's
                     SessionStart, ECA's postCompact); keeps the URLs of
                     Wesley's own prompts as references (Claude Code's and
                     Codex's UserPromptSubmit, ECA's preRequest); and on those
                     prompts, once per host when a quota window of the
                     session's provider passes 75% and again at 90%, asks
                     for Wesley's choice of tiers (see usage choose), having
                     read Codex's quota from its rollout; never blocks
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
                     it; Wesley's to run

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
		return fmt.Sprintf("%q is not served through the relay. A sandbox runs `agent-plan log`, `goal`, `status`, `find`, `questions`, `decisions` and `brief`; put anything else in your report for Wesley", name)
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
		if c, err := plans.Load(); err == nil {
			out = c.Remind(input)
		}
		if ask := quota.Remind(quota.DefaultState(), input, time.Now()); ask != "" {
			out = withContext(out, input, ask)
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
				if reason := c.StopCheckChat(input, plans.StopStateDir(), time.Now()); reason != "" {
					fmt.Fprintln(stderr, reason)
					return 2
				}
			} else if out := c.StopCheck(input, time.Now()); len(out) > 0 {
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
		return report(p.SetOwner(worktree, *agent))
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
	}
	fmt.Fprint(stderr, usage)
	return 2
}

// withContext is the hook output out with text added to its
// additionalContext, or a new output for input's event carrying text:
// hookSpecificOutput for Claude Code's and Codex's events, which name
// hook_event_name, and a bare additionalContext for ECA's.
func withContext(out, input []byte, text string) []byte {
	var event struct {
		Name string `json:"hook_event_name"`
	}
	json.Unmarshal(input, &event)
	var o map[string]any
	if json.Unmarshal(out, &o) != nil {
		o = map[string]any{}
	}
	target := o
	if event.Name != "" {
		specific, _ := o["hookSpecificOutput"].(map[string]any)
		if specific == nil {
			specific = map[string]any{"hookEventName": event.Name}
			o["hookSpecificOutput"] = specific
		}
		target = specific
	}
	if before, _ := target["additionalContext"].(string); before != "" {
		text = before + "\n\n" + text
	}
	target["additionalContext"] = text
	data, _ := json.Marshal(o)
	return data
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
				Answer: answer, Wesley: true, Once: true}, now)
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
	fmt.Fprint(stderr, "agent-plan usage: takes nothing, or choose PROVIDER keep|down|postpone [--plan PLAN] [--answer TEXT]\n")
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
