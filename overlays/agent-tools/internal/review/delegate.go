package review

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	shared "github.com/wesnel/agent-tools/internal/availability"
	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// Usage is `git delegate -h`.
const Usage = `usage: git delegate [--reviewer TYPE]... [--plan PLAN [--part P]] [--items FILE] [--red-check FILE]
                    [--description FILE] [--model M] [--timeout S] SOURCE
       git delegate ask [--reviewer TYPE] --question FILE PATH...   (see git delegate ask -h)
       git delegate availability [--disable claude|codex | --clear claude|codex]
       git delegate show SOURCE                                      (see git delegate show -h)
       git delegate settle SOURCE FINDING OUTCOME [--target X] [--reason-file F]
       git delegate --print-prompt [--reviewer TYPE] [--plan PLAN [--part P]] [--items FILE]
                    [--red-check FILE] [--description FILE] SOURCE

Reviews SOURCE with reviewers that did not write it, and keeps what they
found in its review record, under $XDG_STATE_HOME/agent-tools/reviews/
OWNER/NAME/, which git delegate show prints and settle changes. SOURCE is
a range BASE..TIP of this repository, its record named by origin's GitHub
repository, or a pull request: OWNER/NAME#N, or N for origin's. A pull
request is fetched, pinned to the head GitHub reports, with neither the
user's nor the system's git configuration, no hook, no LFS filter and no
submodule. Run from a clone of OWNER/NAME whose config names no program,
it is fetched into that clone as refs/pr-review/N/head and base, and
reviewed in a worktree of the clone with nothing checked out, under
$XDG_CACHE_HOME/pr-review/OWNER/NAME/N/, so the clone's agent settings
apply; the worktree is removed when the review ends, and the refs stay for
the next round (git update-ref -d removes them). Anywhere else it is
fetched into a bare clone of its own there, which has no agent settings,
as stderr says. Its range is merge-base(base,
head)..head, its author's title, description and closing issues go in as
claims, and its threads, written to its pr-N.org record as pr-loop threads
writes them, as items. Every reviewer reads a checkout of TIP of its own,
made the same way under that cache directory and removed after it.
Reviewers run one at a time, and nothing here runs the change's code.

TYPEs (--reviewer, repeated):
  codex     the general review, by Codex:
            codex exec --json --ignore-user-config --ephemeral, its
            commands reading only the checkout, the parts of the git
            directory git needs and the Nix store, with no writes, network,
            MCP server, plugin, web search or image viewer. Codex's sandbox
            cannot start inside another macOS sandbox, so run this outside
            one. It also routes the change to the focused reviews it needs:
            a scan of every commit from TIP's merge base with the default
            branch, by its paths and lines, against built-in rules and the
            operator's repos routes, and the routes it names.
  claude    the same general review and routing contract, by Claude Code's
            adversarial-reviewer agent, confined as security below.
  security  the security review, by Claude Code's security-reviewer agent,
            headless: claude -p with --setting-sources "", no MCP server,
            CLAUDE_CODE_DISABLE_CLAUDE_MDS=1, its agent from the
            review-agents.json and its sandbox from the reviewer-settings.json
            agent-tools' config names, so nothing of the user's or the
            project's settings, hooks or agents reaches it. Its prompt adds
            the operator's threat notes and the open branches that change the
            same files. Its reply must name every category of its checklist
            as checked.
  compat    the compatibility review, by Claude Code's compat-reviewer
            agent, headless as security is: what outlives the commit
            (stored state, flags, output others parse, config keys and
            options, what another host or branch reads).
            Its prompt adds the open branches that change the same files,
            and the ## Reverting section of the pull request description,
            the file --description names or a pull request's own, without
            which it does not run. Its reply must name every category of its
            checklist as checked.
  upgrade   the upgrade review, by Claude Code's upgrade-reviewer agent,
            headless as security is: what the change's version bumps change
            here, against the upstream text gathered for them on this host,
            as below. Its prompt adds the open branches that change the same
            files. Its reply must name every category of its checklist as
            checked, and say of each moved pin whether it read its text.
  guide     the walkthrough, by Claude Code's review-guide agent, headless
            as security is: the change's hunks in chunks, in the order to
            read them, each hunk cited as file:PATH::LINE@SHA. A citation of
            no hunk, and a hunk no chunk cites, are dropped. It is selected
            last in every review, and its walkthrough replaces the record's.
  diagram   at most three diagrams, run only when named: drawn by Claude
            Code's diagrammer agent, headless as security is, as JSON each
            element of which cites the lines at TIP it stands for; each
            citation held to TIP's blobs, Go parsed and never built; then
            critiqued element by element by Codex, confined as its review
            is. A critique that faults nothing and leaves nothing
            unverified passes. One the critic faults is redrawn once, then
            checked and critiqued again. A diagram whose critique passes is
            kept with verdict holds, and a redraw whose critique passes with
            corrected; otherwise the first drawing is kept as unresolved,
            the critique's findings as the round's. Each is kept as its JSON
            alone; show generates its Mermaid. A run that keeps a diagram
            replaces the record's, one with an earlier one's title keeping
            its ID.
With no --reviewer, the general review is claude for a Codex caller
(CODEX_SANDBOX or CODEX_THREAD_ID set), or codex where claude cannot run,
and codex for any other caller. Each type
the route scan picks runs too; stderr says what it picked and why.

A type whose provider is held does not run (git delegate availability -h).
A run that fails for a rate limit holds its provider for five minutes, and
one that fails for exhausted quota holds it until the reset Claude's quota
windows show, else until availability --clear; no later stage or command
calls it meanwhile, a retry, walkthrough, diagram or ask included.

A reply is taken from the structured output Claude Code returns, else
the last fenced json block of its result, else its last JSON object, and
held to the type's schema, each finding to a line of TIP's tree. A reply
that does not fit is asked for once more, with why; a second is kept
under the round's Dropped.

Each commit whose tests red-check finds pass without the rest of it, in
the JSON --red-check gives (git red-check BASE..TIP's, which the session
runs where the change's tests run, written under its working directory),
is an untested finding of the review's own, with ID RC-<key>; the JSON is
refused unless its commits are the range's and it ran the repository's
test settings, and the round records RED_CHECK session, or absent. A
printed prompt, whose reply delegate never sees, lists those findings
instead, for the reviewer to report itself.

An upgrade prompt adds the pins BASE..TIP sets, moves or drops in lock
files (flake.lock nodes, by each node's locked and original objects
whole, and its input paths, as hm/nixpkgs, that resolve to another node;
go.mod's and go.work's requirements, replacements, and go and toolchain
lines, as golang.org/x/mod reads them; go.sum hashes that moved under one
version; niv's and npins' sources; other lock files whole), a change to
a flake.nix as one pin of the file, whose diff the reviewer reads
(nothing evaluates it here), where its flake.lock's pins move or it may
set nixConfig, and otherwise as any Nix file; each Nix derivation or fetcher set's
version, rev, tag and hash attributes, and each occurrence of an action a
workflow or composite action runs. The Nix and workflow scanners fail
closed: a changed line naming what may pin a version that no pin covers
makes one more pin of its file (nix-unread, actions-unread). Those two
and flake-nix have no upstream: reading the diff settles them, as the
reply's read says. A flake node renamed from the same source, and a Go
module's next major version, are one pin moved, one repository moved
between the same versions in several places is one pin standing for all
of them, and keys that collide take an index. For each moved pin it adds
what changed upstream, gathered here and never by the reviewer: from a
local clone under ~/git/github.com/OWNER/REPO that has both commits, is
not shallow, and whose config names no program, read with no replace
refs or grafts; from the changelog files of both versions' source trees,
which nix flake prefetch fetches from GitHub over HTTPS and which must
have the NAR hash the pin names, or which go mod download fetches from
the public module proxy into a module cache it removes after, never
through a symbolic link in the tree; and from GitHub's releases and
comparison over HTTPS with the gh login, at most 2 calls a pin. Nothing
goes over SSH; a flake node of another type than github is requested
from nowhere, and a private module, one GOPRIVATE, GONOPROXY or GONOSUMDB
matches (in the environment or go env) or review.privateModules of the
config (github.com/ngrok/*, github.com/ngrok-private/* and
go.ngrok.com/* where it names none, all matched ignoring case), is
gathered only from a local clone or GitHub's API. A GitHub repository,
which the gh login, a local clone and nix's tokens could reach where it
is private, is read only where GitHub says it is public, or, for the
operator's own change (a range, or a pull request whose author is the
gh login's user), where those patterns name it, as github.com/OWNER/REPO;
any other's pin is left unread, and its finding says why. At most 30
pins are gathered, each text cut at 16 KiB and all of them at 120 KiB, kept by
source and versions under $XDG_CACHE_HOME/agent-tools/upgrade, a
directory of the user's with mode 0700 or none (stderr says where it is
refused), so that a pin gathered again is not fetched again. Each moved
pin nothing was gathered for (only its hash moved, say), or whose text
the reply's upgrades does not say it read at its two versions, and each
pin added or dropped with no pair, is an unread-upgrade finding of the
review's own, with reviewer "upgrade" and ID UP-<key>, the key made from
the pin and its two versions, settled as any finding is. A printed
upgrade prompt, whose reply delegate never sees, lists those pins
instead, for the reviewer to report as such findings itself.

Known secret shapes are taken out of the goal, the items, the earlier
findings, red-check's output, the threat notes, the Reverting section and
the upstream text before a reviewer sees them. For a pull request the
earlier findings include those of its newest review at another head.

  --reviewer TYPE   a type to run, as above; repeat for more
  --plan PLAN       the plan, as agent-plan takes it, whose Goal is the goal
                    statement: the goal, and what is not this change's job.
                    Required for a range; for a pull request it adds the
                    operator's goal to the one delegate writes
  --part P          read the Goal of PLAN's part P, as P1, instead
  --items FILE      the items the change answers, and how each was settled
  --red-check FILE  git red-check BASE..TIP's JSON
  --description FILE
                    the pull request description, whose ## Reverting section
                    the compat review checks; for a pull request, its own
                    unless this names another
  --model M         the Codex model (default: Codex's own)
  --timeout S       seconds before a reviewer is stopped (default 1800)
  --print-prompt    print the selected type's prompt for a sub-agent the
                    caller starts, and run no provider; diagram exports
                    both drawing and critique tasks

Every file argument is read as given: write it under the working
directory, never $TMPDIR, which differs inside and outside the sandbox.

Writes JSON on stdout: source, range, tip, the record's path, each type's
review (reviewer, model, findings with their record IDs, dropped and
unverified, and an upgrade review's upgrades), those that could not run
and why, the routes, and the record's counts. Exit status:
  0  no open findings
  3  open findings
  4  a type could not run: Codex is not installed or signed in, its
     sandbox cannot start, this is a review-loop session (PR_LOOP_SESSION
     is set); its provider is held; claude or agent-tools'
     delegate config is missing; a compat
     review the scan routed has no description with a Reverting section;
     an upgrade review cannot read the change's pins; or a reply did not
     fit twice
  5  the record fails its structure check
  1  the review could not run; 2 a usage error or refusal
`

// reviewerType is a TYPE: the engine that runs it and, for a focused
// review, its focus. The guide writes the walkthrough, and finds nothing.
type reviewerType struct {
	claude bool
	// agent is its agent in review-agents.json, and a focused review's is
	// its route's ROUTE-reviewer, which the Claude Code module builds from
	// agent-tools' focusPrompts.
	agent string
	focus string
	guide bool
	// diagram is drawn by Claude's diagrammer and critiqued by Codex.
	diagram bool
}

// provider is the account the type's reviewer runs on; a diagram's
// critique runs on Codex.
func (ty reviewerType) provider() shared.Provider {
	if ty.claude {
		return shared.Claude
	}
	return shared.Codex
}

// reviewerTypes are the TYPEs, by name. Each route of routes.Known names
// one.
var reviewerTypes = map[string]reviewerType{
	"codex":    {},
	"claude":   {claude: true, agent: "adversarial-reviewer"},
	"security": {claude: true, agent: "security-reviewer", focus: "security"},
	"compat":   {claude: true, agent: "compat-reviewer", focus: "compat"},
	"upgrade":  {claude: true, agent: "upgrade-reviewer", focus: "upgrade"},
	"guide":    {claude: true, agent: "review-guide", guide: true},
	"diagram":  {diagram: true},
}

// typeList is a repeated --reviewer.
type typeList []string

func (t *typeList) String() string     { return strings.Join(*t, ",") }
func (t *typeList) Set(v string) error { *t = append(*t, v); return nil }

// RunResult is what a review prints.
type RunResult struct {
	Source  string         `json:"source"`
	Range   string         `json:"range"`
	Tip     string         `json:"tip"`
	Record  string         `json:"record"`
	Reviews []*Output      `json:"reviews"`
	NotRun  []NotRun       `json:"notRun"`
	Routes  []routes.Route `json:"routes"`
	Counts  record.Counts  `json:"counts"`
}

// NotRun is a type that could not run, and why.
type NotRun struct {
	Reviewer string `json:"reviewer"`
	Why      string `json:"why"`
}

// Main runs `git delegate` with args and returns its exit status.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "availability":
			return availabilityCommand(args[1:], stdout, stderr)
		case "ask":
			return ask(ctx, args[1:], stdin, stdout, stderr)
		case "show":
			return show(args[1:], stdout, stderr)
		case "settle":
			return settle(args[1:], stdin, stdout, stderr)
		}
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	var reviewers typeList
	flags.Var(&reviewers, "reviewer", "")
	// --goal is defined only to refuse it, naming --plan in its place.
	flags.String("goal", "", "")
	plan := flags.String("plan", "", "")
	part := flags.String("part", "", "")
	items := flags.String("items", "", "")
	redCheckFile := flags.String("red-check", "", "")
	description := flags.String("description", "", "")
	model := flags.String("model", "", "")
	timeout := flags.Int("timeout", 1800, "")
	printPrompt := flags.Bool("print-prompt", false, "")

	// Flags may follow the source, as with git's own commands.
	var positional []string
	for {
		if code, ok := cli.Parse(flags, args, Usage, stdout, stderr); !ok {
			return code
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(positional) != 1 || (*printPrompt && len(reviewers) > 1) {
		fmt.Fprint(stderr, Usage)
		return 2
	}
	report := func(result any, err error, code int) int { return reportRecord(result, err, code, stdout, stderr) }
	goalGiven := false
	flags.Visit(func(f *flag.Flag) { goalGiven = goalGiven || f.Name == "goal" })
	if goalGiven {
		return report(nil, refusal.New("git delegate reads the goal statement from the plan, not a file; pass "+
			"`--plan PLAN` in place of `--goal`, with `--part P1` for a part's goal"), 0)
	}
	if *part != "" && *plan == "" {
		return report(nil, refusal.New("--part names a part of the plan --plan names; pass `--plan PLAN --part %s`",
			*part), 0)
	}
	for _, t := range reviewers {
		if _, ok := reviewerTypes[t]; !ok {
			return report(nil, refusal.New("%q is not a reviewer type; pass `--reviewer` with one of %s", t,
				strings.Join(slices.Sorted(maps.Keys(reviewerTypes)), ", ")), 0)
		}
	}
	s, err := parseSource(positional[0])
	if err != nil {
		return report(nil, err, 0)
	}
	if s.number == 0 {
		if err := git.CheckConfig(""); err != nil {
			return report(nil, err, 0)
		}
	}
	r, err := resolveSource(ctx, s, true, stderr)
	if err != nil {
		return report(nil, err, 0)
	}
	defer r.release()
	if err := usableSettings(r); err != nil {
		return report(nil, err, 0)
	}

	base := inputs{r: r, availability: newAvailability()}
	if *plan != "" {
		if base.goal, err = planGoal(*plan, *part); err != nil {
			return report(nil, err, 0)
		}
	} else if s.number == 0 {
		return report(nil, refusal.New("a review is judged against a goal statement, the Goal of the change's plan; "+
			"pass `--plan PLAN`, with `--part P1` for a part's goal"), 0)
	}
	goal := goalFlags(*plan, *part)
	if *items != "" {
		if base.items, err = readFile(*items, stdin); err != nil {
			return report(nil, err, 0)
		}
	}
	switch {
	case *description != "":
		if base.description, err = readFile(*description, stdin); err != nil {
			return report(nil, err, 0)
		}
		base.descriptionFrom = "the description " + *description
	case s.number != 0:
		base.description = s.pr.Body
		base.descriptionFrom = fmt.Sprintf("the description of %s#%d", s.slug(), s.number)
	}
	if s.number != 0 {
		if err := prInputs(s, &base, stderr); err != nil {
			return report(nil, err, 0)
		}
	}
	if slices.Contains(reviewers, "compat") {
		if _, err := base.reverting(); err != nil {
			return report(nil, err, 0)
		}
	}
	path, err := record.RangeFile(s.owner, s.repo, r.base, r.tip)
	if err != nil {
		return report(nil, err, 0)
	}
	if base.prior, err = priorText(path, s); err != nil {
		return report(nil, err, 0)
	}
	var redCheckJSON string
	if *redCheckFile != "" {
		if redCheckJSON, err = readFile(*redCheckFile, stdin); err != nil {
			return report(nil, err, 0)
		}
	}
	tests, err := testAdequacy(r, *redCheckFile, []byte(redCheckJSON), stderr)
	if err != nil {
		return report(nil, err, 0)
	}

	if *printPrompt {
		t := generalReviewer()
		if len(reviewers) == 1 {
			t = reviewers[0]
		}
		base.printed = true
		in, err := typeInputs(ctx, base, t, tests, stderr)
		if err != nil {
			return report(nil, err, 0)
		}
		in.where = where(r)
		if reviewerTypes[t].diagram {
			fmt.Fprint(stdout, diagramFallback(in))
			return 0
		}
		fmt.Fprint(stdout, prompt(in))
		return 0
	}

	// Without --reviewer, the other family and the scan's focused reviews.
	explicit := len(reviewers) > 0
	scanned := routeChange(r, nil, stderr)
	chosen := slices.Clone([]string(reviewers))
	if !explicit {
		chosen = []string{generalReviewer()}
		for _, rt := range scanned {
			chosen = append(chosen, rt.Route)
			fmt.Fprintf(stderr, "%s: reviewing for %s too: %s\n", name, rt.Route, strings.Join(rt.Why, "; "))
		}
	}
	// The walkthrough comes with every review, after the reviewers.
	chosen = slices.DeleteFunc(chosen, func(t string) bool { return t == "guide" })
	chosen = append(slices.Compact(chosen), "guide")

	result := &RunResult{Source: s.given, Range: r.base + ".." + r.tip, Tip: r.tip, Record: path, Reviews: []*Output{},
		NotRun: []NotRun{}, Routes: scanned}
	var runs []*typeRun
	for i, t := range chosen {
		run := runType(ctx, s, r, base, t, tests, *model, time.Duration(*timeout)*time.Second, stderr)
		// Where Claude cannot review a Codex caller's change, Codex does.
		if i == 0 && !explicit && t == "claude" && run.out == nil && run.failures == nil && !run.refused {
			fmt.Fprintf(stderr, "%s: the claude review could not run here: %s; codex reviews in its place\n", name, run.why)
			t = "codex"
			run = runType(ctx, s, r, base, t, tests, *model, time.Duration(*timeout)*time.Second, stderr)
		}
		if run.out == nil {
			if run.failures != nil {
				runs = append(runs, run)
			}
			result.NotRun = append(result.NotRun, NotRun{Reviewer: t, Why: run.why})
			if run.refused {
				// Its inputs are refused anywhere, --print-prompt too.
				fmt.Fprintf(stderr, "%s: the %s review could not run: %s\n", name, t, run.why)
				continue
			}
			reader := "a sub-agent that only reads"
			if agent := reviewerTypes[t].agent; agent != "" {
				reader = "a new `" + agent + "` sub-agent"
			}
			fmt.Fprintf(stderr, "%s: the %s review could not run here: %s. Instead start %s on what `git delegate --print-prompt "+
				"--reviewer %s %s%s` prints, with a fresh context in the primary harness and none of the author's reasoning. "+
				"Keep review-loop text out of Codex.\n", name, t, run.why, reader, t, typeFlags(s, t, goal), s.given)
			continue
		}
		runs = append(runs, run)
		if run.out != nil {
			result.Reviews = append(result.Reviews, run.out)
			if ty := reviewerTypes[t]; ty.focus == "" && !ty.guide && !ty.diagram {
				result.Routes = run.out.Routes
			}
		}
	}
	for _, rt := range result.Routes {
		if !slices.Contains(chosen, rt.Route) {
			fmt.Fprintf(stderr, "%s: this change needs a %s review (%s): run `git delegate --reviewer %s %s%s`\n",
				name, rt.Route, strings.Join(rt.Why, "; "), rt.Route, typeFlags(s, rt.Route, goal), s.given)
		}
	}
	title := s.given
	if s.pr != nil {
		title = s.pr.Title
	} else if subject, err := git.Run("-C", r.top, "log", "-1", "--format=%s", r.tip); err == nil {
		title = subject
	}
	rec, err := writeRuns(path, s, r, title, runs, time.Now())
	if err != nil {
		return report(nil, err, 0)
	}
	result.Counts = rec.Counts()
	code := 0
	switch {
	case len(result.NotRun) > 0:
		code = 4
	case result.Counts.Open > 0:
		code = 3
	}
	return report(result, nil, code)
}

// typeInputs are base's inputs as type t's reviewer is given them: a
// focused review's contract and sections, and the general review's test
// adequacy. An upgrade review's pins are gathered here, over the network,
// once its reviewer is sure to read them.
func typeInputs(ctx context.Context, base inputs, t string, tests *adequacy, log io.Writer) (inputs, error) {
	in := base
	ty := reviewerTypes[t]
	if ty.guide {
		in.contract, in.prior = guideContract, ""
		return in, nil
	}
	if ty.focus == "" {
		in.redCheck, in.adequacy = tests.text, tests.prompt(in.printed)+sharedPrompt(in.r)
		return in, nil
	}
	fc := focuses[ty.focus]
	in.contract = fc.contract()
	if fc.threatNotes {
		settings, err := in.r.settings()
		if err != nil {
			return in, err
		}
		notes, err := settings.Notes()
		if err != nil {
			return in, err
		}
		in.focused += notesSection(notes)
	}
	if fc.reverting {
		section, err := in.reverting()
		if err != nil {
			return in, err
		}
		in.focused += section
	}
	if fc.upgrades {
		ups, err := gatherUpgrades(ctx, in.r, log)
		if err != nil {
			return in, err
		}
		in.upgrades = ups
		in.focused += ups.section()
		if in.printed {
			in.focused += ups.printedSection(in.r)
		}
	}
	in.focused += overlapSection(in.r)
	return in, nil
}

// typeFlags are the flags, each followed by a space, that a command run
// for type t of s needs beyond the source, so that a hint runs as given:
// goal, the goal's flags as goalFlags gives them, and for a compat review
// of a range the description.
func typeFlags(s *source, t, goal string) string {
	flags := goal
	if s.number == 0 && focuses[reviewerTypes[t].focus].reverting {
		flags += "--description FILE "
	}
	return flags
}

// goalFlags are the --plan and --part flags given, each followed by a
// space, quoted for the shell where they need it.
func goalFlags(plan, part string) string {
	var flags string
	if plan != "" {
		flags += "--plan " + shellWord(plan) + " "
	}
	if part != "" {
		flags += "--part " + shellWord(part) + " "
	}
	return flags
}

var plainWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord is s as one shell word: as it is where nothing in it is special
// to a shell, else single-quoted.
func shellWord(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// planGoal is the goal statement of plan, or of its part: the text under
// its Goal heading, which agent-plan goal prints. plan may name a part's
// file, which Open takes as its plan's, only together with that part.
func planGoal(plan, part string) (string, error) {
	c, err := plans.Load()
	if err != nil {
		return "", err
	}
	p, named, err := c.OpenFile(plan)
	if err != nil {
		return "", err
	}
	if named != "" && named != part {
		return "", refusal.New("%s is part %s's file, and the plan's Goal is not the part's; pass `--plan %s --part %s`",
			plan, named, shellWord(p.File()), named)
	}
	return p.Goal(part)
}

// reverting is the compat prompt's section of in's pull request
// description, or a refusal where there is none to check.
func (in inputs) reverting() (string, error) {
	if in.descriptionFrom == "" {
		return "", refusal.New("a compat review checks the pull request description's `## %s` section; write the "+
			"description to a file under the working directory as the pull-requests skill says and pass "+
			"`--description FILE`", Reverting)
	}
	return revertingSection(in.description, in.descriptionFrom)
}

// runnable says why type ty's reviewer cannot run here, or "".
func runnable(a *availability, ty reviewerType) string {
	if err := a.check(ty.provider()); err != nil {
		return err.Error()
	}
	if ty.claude {
		return claudeMissing()
	}
	return unavailable()
}

// runType runs type t once, and once more where its reply does not fit.
func runType(ctx context.Context, s *source, r *span, base inputs, t string, tests *adequacy, model string,
	timeout time.Duration, log io.Writer) *typeRun {
	ty := reviewerTypes[t]
	if ty.diagram {
		return runDiagrams(ctx, s, r, base, model, timeout, log)
	}
	run := &typeRun{name: t, redCheck: ty.focus == "" && !ty.guide && tests.given}
	if run.why = runnable(base.availability, ty); run.why != "" {
		return run
	}
	in, err := typeInputs(ctx, base, t, tests, log)
	if err != nil {
		var refused refusal.Refusal
		run.why, run.refused = err.Error(), errors.As(err, &refused)
		return run
	}
	var fc *focus
	if ty.focus != "" {
		f := focuses[ty.focus]
		fc = &f
	}
	schema := Schema
	switch {
	case fc != nil:
		schema = fc.schema()
	case ty.guide:
		schema = guideSchema
	}
	in.where = "The working directory is a checkout of TIP holding nothing but the commits; `git diff " +
		r.base + " " + r.tip + "` shows the change."
	text := prompt(in)
	for attempt := 0; attempt < 2; attempt++ {
		wt, remove, err := worktree(ctx, s, r)
		if err != nil {
			run.why = err.Error()
			return run
		}
		var reply *CodexReply
		if ty.claude {
			reply, err = ClaudeRun(ctx, Claude{Checkout: wt, Agent: ty.agent, Prompt: text, Schema: schema, Timeout: timeout, Log: log, availability: base.availability})
		} else {
			reply, err = CodexRun(ctx, Codex{Checkout: wt, Prompt: text, Schema: schema, Model: model, Timeout: timeout,
				Log: log, Git: worktreeGit(ctx, wt), availability: base.availability})
		}
		var out *Output
		var bad badReply
		var unconfined Unconfined
		switch {
		case errors.As(err, &unconfined):
			remove()
			run.why = string(unconfined)
			return run
		case errors.As(err, &bad):
		case err != nil:
			remove()
			run.why = err.Error()
			return run
		case ty.guide:
			run.model = reply.Model
			out, err = validateGuide(reply.Text, r)
		default:
			run.model = reply.Model
			out, err = validate(reply.Text, r, wt, ty.focus, fc)
		}
		remove()
		if err == nil {
			out.Reviewer, out.Focus = t, ty.focus
			if reply.Model != "" {
				out.Model = &reply.Model
			}
			if ty.focus == "" && !ty.guide {
				tests.add(out)
				out.Routes = routeChange(r, out, log)
			}
			if in.upgrades != nil {
				in.upgrades.add(r, out)
			}
			run.out = out
			return run
		}
		failed := "The reply could not be used: " + err.Error()
		if reply != nil {
			failed += "\n\n" + record.Cap(reply.Text, 4<<10)
		}
		run.failures = append(run.failures, failed)
		fmt.Fprintf(log, "%s: the %s reply could not be used: %v\n", name, t, err)
		text = prompt(in) + "\n## Your last reply\n\nYour last reply could not be used: " + err.Error() +
			". Reply again with the JSON the Output section describes, and nothing else.\n"
	}
	run.why = "its reply did not fit twice"
	return run
}
