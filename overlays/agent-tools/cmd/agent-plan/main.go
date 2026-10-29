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
	quota "github.com/wesnel/agent-tools/internal/usage"
)

var usage = `agent-plan: agents' plan files and their append-only event logs.

A plan is files side by side in the plans directory: <time>-<slug>.<host>.org,
<time>-<slug>.<host>.<part>.org for each sub-agent's part, and
<time>-<slug>.<host>.log.org, which only "agent-plan log" writes, <time>
being when new made it, as YYYYMMDDHHMMSS; plans named YYYY-MM-DD-<slug>
are read alike. The plans
directory and host come from "plans" in the user's agent-tools/config.json.
A command that would leave a plan file with structure drift it did not
have (see status) is refused and writes nothing.

  new --title T --slug S [--repo R]... [--branch BASE..BRANCH]... [--worktree DIR]...
      [--session HARNESS:ID]... [--harness H] [--template NAME] [--workstream NAME]
                     create a plan; prints its file, ID and log. --template
                     starts it with the entries of each section of
                     templates/NAME.org in the plans' repository, or
                     else NAME.org in plans.templates, the built-in ones.
                     The plan's WORKSTREAM, an id: link, names the
                     workstream --workstream names (a slug or name that
                     workstream list prints, or its file); without it,
                     that of the newest plan, any host's, whose branch
                     BRANCH or BASE is (main and master aside), or none
  new --parent PLAN --part P1 --title T [...]
                     create part P1's plan file beside its parent's; a part
                     is in its plan's workstream
  workstream new --title T [--summary S]
                     write a workstream, a line of work many plans share,
                     as workstream-<slug>.<host>.org beside the plans, the
                     slug made from T: an org-roam node holding T and the
                     summary alone, for a person to edit. Prints its file,
                     name and ID; where any host has that slug already,
                     prints that one, unchanged, as existed
  workstream list [--json]
                     every host's workstreams, by title
  log PLAN --item ID --kind KIND [--summary S] [--role R] [--agent A] [--branch B]
      [--commit SUBJECT | --revision REF] [--evidence E] [--review ROUND.ID --outcome O]
      [--command CMD [--dir DIR]]
                     append an event about item ID to the plan's log, and
                     write a step's or part's heading keyword from its log.
                     A handed-back event's --command is what the operator
                     runs next, in the absolute --dir if given. --revision
                     records an exact commit and its GitHub origin owner/name
                     from the current worktree, deriving the subject; applies
                     to done and verified. --commit alone records only a subject
  commits [PLAN] [--repo OWNER/NAME] [--revision FULL-OID] [--item ID]
                     JSON exact associations from every readable host's plans,
                     or PLAN, resolved through signing rewrite aliases in all
                     logs. --item requires PLAN. Subject-only events have no
                     exact association. No matches prints []
  resolve-commit --repo OWNER/NAME --revision FULL-OID
                     JSON repository, original_revision and resolved revision,
                     even without an association. No alias leaves it unchanged
  waiting PLAN [--json]
                     the items whose newest event, but planned, property,
                     resealed, referenced and postponed events, is a
                     hand-back with a command, each with its command and
                     directory: what waits on the operator, which Emacs and
                     the dashboard show them
  find [--session ID] [--worktree DIR] [--branch B]
                     this host's plan files that match any one given (default:
                     this session, and the worktree and branch if a linked
                     worktree, never a repository's main checkout)
  find --hook [--harness claude|codex]
                     the same for a SessionStart hook (reads stdin); Codex's
                     returns bounded plan context without changing sessions.
                     Claude is the default: after /clear or compaction it adds
                     brief --resume; as its SessionEnd hook, hands the plan of
                     a session /clear ends to the session it starts in the
                     same directory, and prints nothing
  goal PLAN [--part P1]
                     the goal statement, the text under "** Goal"
  status PLAN [--format org|json]
                     steps without done, done without verified, parts ready
                     to start, steps ready now (no open AFTER item, earlier
                     ORDERED siblings settled, not scheduled for later),
                     what still waits, keyword drift, property drift (a
                     WORKTREES, BRANCHES, PULL_REQUESTS or OWNERS value, or a
                     References entry or Related link, the log does not
                     give), Related links to no file-level ID of an Org file
                     in the plans' repository, and structure
                     drift (a top-level heading but one * Plan; a section
                     not a plan's, or twice; a heading under Goal, Related or
                     Verification; a References entry without a URL; an
                     entry of Steps, Distribution, Questions or Follow-ups
                     without its ID; a question without a question's
                     keyword; an ORDERED or Archive group with text or no
                     entries; a heading skipping a level; a heading line
                     inside a block; a block with no end; once per file,
                     the old form, item IDs in ID rather than CUSTOM_ID or
                     a plan's file without a file-level ID, which stays
                     readable and refuses no write)
  set PLAN NAME [VALUE...] [--part P1]
                     set WORKTREES, BRANCHES or PULL_REQUESTS of the plan's
                     file, or the part's, to the values, and log it; new,
                     own and the hooks log the changes they make. STATUS
                     (planning, reviewing-plan, working, reviewing,
                     handed-back or halted), SKILLS, REPOSITORIES, SESSIONS
                     and HARNESS are set the same way, with no log event
  unfile PLAN NAME VALUE --reason TEXT [--part P1]
                     remove VALUE, which a hook or an agent filed in the
                     wrong plan, from WORKTREES, BRANCHES or PULL_REQUESTS
                     of the plan's file, or the part's, and log the new
                     value with the reason, which names the plan it belongs
                     to; refused for a value the file does not hold
  questions PLAN [--all] [--waiting] [--json | --count]
                     the open questions the operator can answer now, by
                     priority, with their recommendations; --all adds the
                     parts', --waiting those that wait for an AFTER item: a
                     question until settled, a part or step until logged
                     done, verified or abandoned
  open [--kind OKIND] [--since 'YYYY-MM-DD[ HH:MM]'] [--json]
                     the open items of every plan of this host, halted ones
                     included and marked, under a heading per workstream
                     (those of the newest plans first, then "none") and in
                     it per plan, newest plan first: what waits on
                     the operator (a hand-back's command, a sign-pass not
                     yet run), the questions they can answer now, retro
                     proposals, ready steps and open follow-ups, its parts'
                     included; --kind keeps one kind, --since those asked,
                     discovered or handed back since then (a step's time is
                     its first event). A plan that cannot be read is named
                     on stderr, and the exit is 1, after the rest
  brief PLAN         what an agent needs to go on after a compaction: the goal,
                     the decisions, the steps and parts ready, the questions
                     the operator can answer now, the steps done but not
                     verified, the open follow-ups, and any structure drift;
                     Codex's hook adds it after one
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
      [--kind QKIND]
                     write a question for the operator under Questions, in
                     the plan's file or the part's, as its next question ID,
                     in the form questions reads, and log it as asked; a
                     kind is kept as its KIND property, which questions
                     --json shows as kind
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
  step PLAN --title T [--text TEXT] [--after ID]... [--part P1]
                     add a step at the end of Steps, in the plan's file or
                     the part's, as its next step ID, and log it as planned
  part PLAN --title T [--text TEXT] [--after ID]... [--repo R]
      [--branch BASE..BRANCH] [--worktree DIR]
                     add a part at the end of Distribution as the next part
                     ID, its text starting "R: BRANCH on BASE, worktree
                     DIR." when given them, and log it as planned
  edit PLAN ID [--title T] [--text TEXT]
                     change the title or the text of step, part or
                     follow-up ID in place, keeping its keyword, tags,
                     drawers and the headings under it; an empty --text
                     removes the text. Log it as planned
  after PLAN ID ID... | after PLAN ID --clear
                     set the AFTER of step, part or question ID to the IDs
                     it waits for, or with --clear none; log it as planned
  section PLAN Goal|Verification --text TEXT [--part P1]
                     write the whole of the plan's, or the part's, Goal or
                     Verification, the sections that hold text; log it as
                     planned
                     TEXT may hold line breaks, but no line Org reads as a
                     heading, a drawer, a planning line or a #+ keyword
                     other than a block's
  reference PLAN URL [--note TEXT]
                     keep URL under References, or set its note: what it was
                     used for and which decisions (Q IDs) it fed; log it as
                     referenced. remind adds every URL of the operator's
                     own prompts this way, with no note
  link PLAN OTHER --why TEXT
                     write "- [[id:ID][TITLE]]: TEXT" under Related in PLAN, ID
                     and TITLE being OTHER's file-level ID and title, and log
                     it as linked; nothing when PLAN already links OTHER.
                     Refused for PLAN itself, a part's file, and a plan in
                     the old form, which has no file-level ID
  related PLAN [--json]
                     the plans, any host's, related to PLAN, and why, as
                     "- hard|soft [linked] NAME (TITLE): EVIDENCE"; writes
                     nothing. Hard evidence, which links PLAN to the other
                     by itself (as link does, the evidence as TEXT) when
                     new, set, step, part, edit, section or the commit hook
                     gives PLAN some: a branch both work on or one stacks
                     on, a pull request both name, a part or follow-up of
                     one that the other's text names on a line with the
                     plan's name, a commit both logs record on one branch.
                     Softer signs, for an agent to judge and link: a session
                     or linked worktree both name, one's text naming the
                     other. main, master and the main checkout are no
                     evidence. A plan that cannot be read is named on
                     stderr, and the exit is 1, after the rest
  postpone PLAN ID (--until 'YYYY-MM-DD[ HH:MM]' | --clear)
                     schedule step or part ID for that day or time, which
                     keeps it out of the ready lists and the agenda until
                     then, or clear its schedule; log it as postponed
  statusline         Claude Code's status line and Emacs's mode line (reads
                     stdin): the session's context and quota windows from
                     Claude Code's input, then the open questions of the
                     session's plan, else of the plan of the worktree "cwd"
                     is in; or nothing. Never touches the network. Ends with
                     "checkpoint · /clear" where that plan stands at a
                     checkpoint (a hand-back, a sign-pass handed over or
                     signed, or a decision that left no question to ask) and
                     the context holds 200K tokens or more or its cache has
                     gone cold. Records the quota windows in
                     $XDG_STATE_HOME/agent-plan/usage.json for quota-stop,
                     an equal or higher reading of a window replacing a
                     lower one until the window resets
  quota-stop --threshold N
                     Claude Code's PreToolUse hook (reads stdin): while a
                     window statusline recorded is at or above N percent,
                     its reset ahead and seen in the last 5 minutes, denies
                     every call but Read, Grep, Glob, ToolSearch, Skill,
                     AskUserQuestion, TaskStop, SubagentHandback, agent-plan
                     alone on a Bash line, and a ScheduleWakeup, or a
                     CronCreate pinned to one minute ("M H DoM Mon *"),
                     that fires after the reset, naming the window, its
                     share and its reset (the window that resets last, with
                     both over), and the call that schedules the main
                     session's resume just after it; with no record it can
                     read, denies nothing
  quota-stop --harness codex --threshold N
                     the same as Codex's PreToolUse hook, from the windows
                     codex-usage recorded: denies every call but
                     update_plan, request_user_input, wait_agent,
                     close_agent and agent-plan alone on a Bash line, and
                     tells the session to end its turn for the operator to
                     resume, Codex having no tool to schedule that; exits 2
                     where Claude Code's exits 1
  codex-usage       read the Codex account's quota windows through
                     ` + "`codex app-server`" + ` (account/rateLimits/read) and
                     record them in $XDG_STATE_HOME/agent-plan/codex-usage.json,
                     as statusline records Claude's; needs Codex's login and
                     the network, so a timer runs it, never a session
  own PLAN --worktree DIR --agent ID
                     record agent ID, the harness's ID for a sub-agent or
                     "main" for the session's main thread, as the one agent
                     that edits files in the worktree DIR (OWNERS in the plan)
  remind             a hook (reads stdin) that asks, in a session with a plan,
                     for the coordinator to check a finished sub-agent's report
                     and log its part (Claude Code's UserPromptSubmit and
                     PostToolUse on Agent, Codex's PostToolUse on
                     wait_agent), and for a compaction to keep what the plan
                     lacks (Claude Code's PreCompact) or the plan to be
                     brought up to date after one (Codex's SessionStart);
                     keeps the URLs of the operator's own prompts as
                     references (Claude Code's and Codex's
                     UserPromptSubmit); and on those prompts,
                     task-notifications included, adds a line for each result
                     of the session's sign-passes not yet delivered, logging
                     it as sign-delivered, marked stale when over a day old;
                     and on every one of those prompts, with or without a
                     plan, adds the time and how long since the operator's
                     previous message in the session; never blocks
  sign-id [--plan PLAN] [--session ID] [--branch B]... [--worktree DIR] [--base REF]
                     log a sign-pass about to be handed to the operator as
                     sign-requested in the plan of this session (--session,
                     else CLAUDE_CODE_SESSION_ID or CODEX_THREAD_ID) and
                     worktree, and print its ID for
                     ` + "`git sign-pass --notify ID`" + `; refused without a plan.
                     --plan selects an own-host plan file or name under the
                     configured plans directory instead of automatic lookup.
                     The event's WORKTREE is where git sign-pass runs (the
                     worktree given, else the one sign-id runs in), and its
                     BASE the pass's --base, left out for main (or
                     origin/main); BRANCH defaults to the branch of that
                     worktree
  sign-result ID [--check] [--wait [--timeout DURATION]]
                     print each result of sign-pass ID and log those not yet
                     delivered as delivered, as remind does; exit 1 while it
                     has none. --check prints nothing and only exits 0 once
                     one is logged. --wait first waits for one, for at most
                     --timeout (default 24h), and exits 1 if none comes:
                     a session's waiter, run in the background
  init               clone the plans' repository where it is missing and
                     "plans.remote" names it, and point its origin at
                     "plans.remote"; then make the plans directory, with a
                     .gitignore for the lock files, as new does
  sync [--pull] [--settle DURATION]
                     take the other hosts' plans by fast-forwarding the
                     checked-out branch; then, if HEAD holds the remote's
                     branch, commit this host's plan files and workstreams
                     and nothing else
                     onto it, authored by the git user.name and user.email
                     (agent-plan sync <agent-plan@HOST.invalid> where there
                     is no user.email) and committed by agent-plan sync
                     <agent-plan@HOST.invalid>, and push that commit; --pull only takes them. It never
                     rebases or commits on the checked-out branch, and leaves
                     it where it is when it cannot fast-forward, unless the
                     remote is ahead only by this host's plan files, with
                     nothing staged: then it moves the branch and the index
                     to the remote's and keeps the working tree. The watcher
                     runs it, never an agent
  reseal PLAN        accept a log edited outside agent-plan, recording that in
                     it; the operator's to run

find, status, new and workstream first take the other hosts' plans, quietly,
if they can.
Values are single lines but --text and --context, which may span lines as
section says; list values hold no whitespace.
KIND: ` + strings.Join(plans.Kinds, ", ") + `
QKIND: ` + strings.Join(plans.QuestionKinds, ", ") + `
OKIND: ` + strings.Join(plans.OpenKinds, ", ") + `
ROLE: ` + strings.Join(plans.Roles, ", ") + `
OUTCOME: ` + strings.Join(plans.Outcomes, ", ") + `
`

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
	if args[0] == "quota-stop" {
		// Before plans.Load: it runs before every tool call.
		return quotaStop(args[1:], stdin, stdout, stderr)
	}
	if args[0] == "codex-usage" {
		return codexUsage(args[1:], stderr)
	}
	if args[0] == "statusline" {
		// Claude Code shows whatever this prints, so a failure prints
		// nothing.
		input := readAll(stdin)
		if ws, ok := quota.FromClaudeStatus(input, time.Now()); ok {
			_ = quota.Record(quota.File(), ws) // a reading not recorded waits for the next refresh
		}
		var parts []string
		if line := quota.StatusLine(input, time.Now()); line != "" {
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
		if c, err := plans.Load(); err == nil {
			out = c.Remind(input)
		}
		if line := prompttime.Remind(prompttime.DefaultDir(), input, time.Now()); line != "" {
			out = plans.WithContext(out, input, line)
		}
		if len(out) > 0 {
			stdout.Write(append(out, '\n'))
		}
		return 0
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
	return checkoutAt(cwd)
}

// checkoutAt is the top level and branch of the worktree dir is in.
func checkoutAt(dir string) (top, branch string) {
	top, err := git.Run("-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ""
	}
	branch, _ = git.Run("-C", dir, "branch", "--show-current")
	return top, branch
}

func run(command string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	var (
		title    = flags.String("title", "", "")
		slug     = flags.String("slug", "", "")
		harness  = flags.String("harness", "", "")
		template = flags.String("template", "", "")
		stream   = flags.String("workstream", "", "")
		parent   = flags.String("parent", "", "")
		part     = flags.String("part", "", "")
		item     = flags.String("item", "", "")
		kind     = flags.String("kind", "", "")
		summary  = flags.String("summary", "", "")
		role     = flags.String("role", "", "")
		agent    = flags.String("agent", "", "")
		commit   = flags.String("commit", "", "")
		revision = flags.String("revision", "", "")
		evidence = flags.String("evidence", "", "")
		review   = flags.String("review", "", "")
		outcome  = flags.String("outcome", "", "")
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
		since    = flags.String("since", "", "")
		foundIn  = flags.String("found-in", "", "")
		note     = flags.String("note", "", "")
		why      = flags.String("why", "", "")
		unset    = flags.Bool("clear", false, "")
		check    = flags.Bool("check", false, "")
		wait     = flags.Bool("wait", false, "")
		timeout  = flags.Duration("timeout", 24*time.Hour, "")
		resume   = flags.Bool("resume", false, "")
		text     = flags.String("text", "", "")
		run      = flags.String("command", "", "")
		runDir   = flags.String("dir", "", "")
		signBase = flags.String("base", "", "")
		signPlan = flags.String("plan", "", "")
		reason   = flags.String("reason", "", "")
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
	if *unset && command != "postpone" && command != "after" {
		fmt.Fprintf(stderr, "agent-plan %s: --clear is postpone's and after's\n", command)
		return 2
	}
	given := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { given[f.Name] = true })
	if given["plan"] {
		if command != "sign-id" {
			fmt.Fprintf(stderr, "agent-plan %s: --plan is sign-id's\n", command)
			return 2
		}
		if *signPlan == "" {
			fmt.Fprint(stderr, "agent-plan sign-id: --plan needs a plan file or name; pass a known plan or omit --plan for automatic selection\n")
			return 2
		}
	}
	if given["workstream"] && command != "new" {
		fmt.Fprintf(stderr, "agent-plan %s: --workstream is new's\n", command)
		return 2
	}
	if given["base"] && command != "sign-id" {
		fmt.Fprintf(stderr, "agent-plan %s: --base is sign-id's\n", command)
		return 2
	}
	if given["reason"] && command != "unfile" {
		fmt.Fprintf(stderr, "agent-plan %s: --reason is unfile's\n", command)
		return 2
	}
	if given["text"] && command != "step" && command != "part" && command != "edit" && command != "section" {
		fmt.Fprintf(stderr, "agent-plan %s: --text is step's, part's, edit's and section's\n", command)
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
	// reportLinking reports a command that may have given p new evidence
	// of related plans, then links p to them, saying so on stderr.
	reportLinking := func(p plans.Plan) func(any, error) int {
		return func(result any, err error) int {
			code := report(result, err)
			if err != nil {
				return code
			}
			linked, err := c.LinkEvidence(p.File(), time.Now())
			for _, r := range linked {
				fmt.Fprintf(stderr, "agent-plan: linked %s to %s: %s\n", p.Name, r.Plan, strings.Join(r.Evidence, " "))
			}
			if err != nil {
				fmt.Fprintf(stderr, "agent-plan: linking related plans: %s\n", err)
			}
			return code
		}
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
	if command == "new" || command == "status" || command == "workstream" || (command == "find" && !*hook) {
		c.Pull()
	}
	switch command {
	case "workstream":
		switch {
		case len(positional) == 1 && positional[0] == "new" && !*asJSON:
			return report(c.NewWorkstream(*title, *summary))
		case len(positional) == 1 && positional[0] == "list" && *title == "" && *summary == "":
			ws, err := c.Workstreams()
			if err != nil || *asJSON {
				return report(ws, err)
			}
			for _, w := range ws {
				fmt.Fprintf(stdout, "- %s (%s)\n", w.Title, w.Name)
			}
			return 0
		}
		fmt.Fprint(stderr, "agent-plan workstream: takes new --title T [--summary S], or list [--json]\n")
		return 2
	case "new":
		if len(positional) != 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		o := plans.NewOptions{Title: *title, Slug: *slug, Repos: repos, Branches: branches, Worktrees: trees,
			Sessions: sessions, Harness: *harness, Parent: *parent, Part: *part, Template: *template, Workstream: *stream}
		if len(o.Sessions) == 0 {
			if h, id := plans.EnvSession(); id != "" {
				o.Sessions = []string{h + ":" + id}
				if o.Harness == "" {
					o.Harness = h
				}
			}
		}
		if len(o.Worktrees) == 0 {
			if top, _ := checkout(); top != "" && !strings.ContainsAny(top, " \t") {
				o.Worktrees = []string{top}
			}
		}
		made, err := c.New(o, time.Now())
		if made.Note != "" {
			fmt.Fprintf(stderr, "agent-plan: %s\n", made.Note)
		}
		if made.Unlinked != "" {
			fmt.Fprintf(stderr, "agent-plan: linking related plans: %s\n", made.Unlinked)
		}
		return report(made, err)
	case "log":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		e := plans.Event{Kind: *kind, Item: *item, Summary: *summary, Role: *role, Agent: *agent,
			Branch: branch, Commit: *commit, Evidence: *evidence, Review: *review, Outcome: *outcome,
			Command: *run, Dir: *runDir}
		if *revision != "" {
			top, _ := checkout()
			identity, err := plans.CommitIdentity(top, *revision)
			if err != nil {
				return report(nil, err)
			}
			e.Repository, e.Revision, e.Worktree = identity.Repository, identity.Revision, identity.Worktree
			if e.Commit != "" && e.Commit != identity.Commit {
				return report(nil, fmt.Errorf("--commit does not match --revision's subject"))
			}
			e.Commit = identity.Commit
		}
		return report(p.Log(e, time.Now()))
	case "commits":
		if len(positional) > 1 || len(repos) > 1 {
			return report(nil, fmt.Errorf("commits takes at most one PLAN and --repo"))
		}
		plan := ""
		if len(positional) == 1 {
			plan = positional[0]
		}
		return report(c.Commits(plans.CommitOptions{Plan: plan, Repository: one(repos), Revision: *revision, Item: *item}))
	case "resolve-commit":
		if len(positional) != 0 || len(repos) != 1 {
			return report(nil, fmt.Errorf("resolve-commit takes --repo and --revision, without PLAN"))
		}
		return report(c.ResolveCommit(one(repos), *revision))
	case "waiting":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		ws, err := p.Waiting()
		if err != nil || *asJSON {
			return report(ws, err)
		}
		for _, w := range ws {
			line := "- " + w.Item + ": `" + w.Command + "`"
			if w.Dir != "" {
				line += " in " + w.Dir
			}
			if w.Summary != "" {
				line += " (" + w.Summary + ")"
			}
			fmt.Fprintln(stdout, line)
		}
		return 0
	case "find":
		if *hook {
			h := *harness
			if h == "" {
				h = "claude"
			}
			input := readAll(stdin)
			var event struct {
				Name string `json:"hook_event_name"`
			}
			if json.Unmarshal(input, &event) == nil && event.Name == "SessionEnd" && h == "claude" {
				return hookReport(nil, c.SessionEndHook(input, time.Now()), stdout, stderr)
			}
			out, err := c.SessionHook(input, h)
			return hookReport(out, err, stdout, stderr)
		}
		o := plans.FindOptions{Session: session, Worktree: worktree, Branch: branch}
		if o == (plans.FindOptions{}) {
			session := ""
			if h, id := plans.EnvSession(); id != "" {
				session = h + ":" + id
			}
			cwd, _ := os.Getwd()
			if o = plans.Here(session, cwd); o == (plans.FindOptions{}) {
				return report([]plans.Found{}, nil)
			}
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
	case "open":
		if len(positional) != 0 {
			fmt.Fprint(stderr, "agent-plan open: takes [--kind KIND] [--since TIME] [--json], and no PLAN\n")
			return 2
		}
		f := plans.OpenFilter{Kind: *kind}
		if *since != "" {
			if f.Since, err = plans.ParseSince(*since, time.Local); err != nil {
				return report(nil, err)
			}
		}
		items, err := c.OpenItems(f, time.Now())
		if items == nil {
			return report(nil, err)
		}
		if *asJSON {
			report(items, nil)
		} else {
			io.WriteString(stdout, plans.OpenItemsOrg(items))
		}
		if err != nil {
			fmt.Fprintf(stderr, "agent-plan: %s\n", err)
			return 1
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
			Recommendation: *advice, Answer: *answer, Role: *role, Kind: *kind, Alternatives: alternatives, After: after},
			time.Now()))
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
	case "step", "part":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		o := plans.EntryOptions{Section: "Steps", Part: *part, Title: *title, Text: *text, After: after}
		if command == "part" {
			if *part != "" || len(repos) > 1 {
				fmt.Fprint(stderr, "agent-plan part: takes PLAN --title T and one --repo, and no --part: a part's file has no Distribution\n")
				return 2
			}
			o = plans.EntryOptions{Section: "Distribution", Title: *title, Text: *text, After: after, Repo: one(repos),
				Branch: branch, Worktree: worktree}
		} else if len(repos)+len(branches)+len(trees) > 0 {
			fmt.Fprint(stderr, "agent-plan step: a step takes no --repo, --branch or --worktree; a part does\n")
			return 2
		}
		return reportLinking(p)(p.AddEntry(o, time.Now()))
	case "edit":
		if len(positional) != 2 {
			fmt.Fprint(stderr, "agent-plan edit: takes PLAN ID [--title T] [--text TEXT]\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		var o plans.EditOptions
		if given["title"] {
			o.Title = title
		}
		if given["text"] {
			o.Text = text
		}
		return reportLinking(p)(p.EditEntry(positional[1], o, time.Now()))
	case "after":
		if len(positional) < 2 || (len(positional) == 2) != *unset {
			fmt.Fprint(stderr, "agent-plan after: takes PLAN ID ID..., or PLAN ID --clear\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		return report(p.SetAfter(positional[1], positional[2:], time.Now()))
	case "section":
		if len(positional) != 2 || !given["text"] {
			fmt.Fprint(stderr, "agent-plan section: takes PLAN Goal|Verification --text TEXT [--part P1]\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		return reportLinking(p)(p.SetSection(*part, positional[1], *text, time.Now()))
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
	case "link":
		if len(positional) != 2 {
			fmt.Fprint(stderr, "agent-plan link: takes PLAN OTHER --why TEXT\n")
			return 2
		}
		return report(c.Link(positional[0], positional[1], *why, time.Now()))
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
		return reportLinking(p)(map[string]any{"property": name, "values": values}, nil)
	case "related":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		rs, err := c.Related(p.File())
		if *asJSON {
			report(rs, nil)
		} else {
			for _, r := range rs {
				state := "soft"
				if r.Hard {
					state = "hard"
				}
				if r.Linked {
					state += " linked"
				}
				fmt.Fprintf(stdout, "- %s %s (%s): %s\n", state, r.Plan, r.Title, strings.Join(r.Evidence, " "))
			}
		}
		if err != nil {
			fmt.Fprintf(stderr, "agent-plan: %s\n", err)
			return 1
		}
		return 0
	case "unfile":
		if len(positional) != 3 {
			fmt.Fprint(stderr, "agent-plan unfile: takes PLAN NAME VALUE --reason TEXT [--part P1]\n")
			return 2
		}
		p, err := c.Open(positional[0])
		if err != nil {
			return report(nil, err)
		}
		name, value := positional[1], positional[2]
		if err := p.RemoveFromProperty(*part, name, value, *reason, time.Now()); err != nil {
			return report(nil, err)
		}
		return report(map[string]any{"property": name, "removed": value, "reason": *reason}, nil)
	case "reseal":
		p, ok := wantPlan()
		if !ok {
			return 2
		}
		return report(p.Reseal(time.Now()))
	case "sign-id":
		if len(positional) != 0 {
			fmt.Fprint(stderr, "agent-plan sign-id: takes [--plan PLAN] [--session ID] [--branch B]... [--worktree DIR] [--base REF]\n")
			return 2
		}
		if session == "" {
			_, session = plans.EnvSession()
		}
		top, current := checkout()
		if worktree != "" {
			top = worktree
			_, current = checkoutAt(worktree)
		}
		if len(branches) == 0 && current != "" {
			branches = multi{current}
		}
		id, err := c.RequestSign(plans.SignRequest{Plan: *signPlan, Session: session, Worktree: top, Branches: branches, Base: *signBase}, time.Now())
		if err != nil {
			return report(nil, err)
		}
		fmt.Fprintln(stdout, id)
		return 0
	case "sign-result":
		if len(positional) != 1 {
			fmt.Fprint(stderr, "agent-plan sign-result: takes ID [--check] [--wait [--timeout DURATION]]\n")
			return 2
		}
		if *wait {
			deadline := time.Now().Add(*timeout)
			for {
				ds, err := c.SignResults(positional[0], false, time.Now())
				if err != nil {
					return report(nil, err)
				}
				left := time.Until(deadline)
				if len(ds) > 0 || left <= 0 {
					break
				}
				time.Sleep(min(signPoll, left))
			}
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

// signPoll is how often sign-result --wait reads the log for a result.
const signPoll = time.Second

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
