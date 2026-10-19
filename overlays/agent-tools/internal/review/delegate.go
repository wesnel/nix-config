package review

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// Usage is `git delegate -h`.
const Usage = `usage: git delegate [--reviewer TYPE]... [--goal FILE] [--items FILE] [--red-check FILE]
                    [--description FILE] [--model M] [--timeout S] SOURCE
       git delegate ask [--reviewer TYPE] --question FILE PATH...   (see git delegate ask -h)
       git delegate show SOURCE                                      (see git delegate show -h)
       git delegate settle SOURCE FINDING OUTCOME [--target X] [--reason-file F]
       git delegate --print-prompt [--reviewer TYPE] [--goal FILE] [--items FILE] [--red-check FILE]
                    [--description FILE] SOURCE

Reviews SOURCE with reviewers that did not write it, and keeps what they
found in its review record, under $XDG_STATE_HOME/agent-tools/reviews/
OWNER/NAME/, which git delegate show prints and settle changes. SOURCE is
a range BASE..TIP of this repository, its record named by origin's GitHub
repository, or a pull request: OWNER/NAME#N, or N for origin's. A pull
request is fetched into a bare clone of its own under
$XDG_CACHE_HOME/pr-review/OWNER/NAME/N/, pinned to the head GitHub
reports, with neither the user's nor the system's git configuration, no
hook, no LFS filter and no submodule; its range is merge-base(base,
head)..head, its author's title, description and closing issues go in as
claims, and its threads, written to its pr-N.org record as pr-loop threads
writes them, as items. Every reviewer reads a checkout of TIP of its own,
made the same way under that cache directory and removed after it.
Reviewers run one at a time, and nothing here runs the change's code.

TYPEs (--reviewer, repeated):
  codex     the general review, by Codex as a model family other than the
            author's: codex exec --ignore-user-config --ephemeral, its
            commands reading only the checkout, the parts of the git
            directory git needs and the Nix store, with no writes, network,
            MCP server, plugin, web search or image viewer. Codex's sandbox
            cannot start inside another macOS sandbox, so run this outside
            one. It also routes the change to the focused reviews it needs:
            a scan of every commit from TIP's merge base with the default
            branch, by its paths and lines, against built-in rules and the
            operator's repos routes, and the routes it names.
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
  guide     the walkthrough, by Claude Code's review-guide agent, headless
            as security is: the change's hunks in chunks, in the order to
            read them, each hunk cited as file:PATH::LINE@SHA. A citation of
            no hunk, and a hunk no chunk cites, are dropped. It always runs,
            last, and its walkthrough replaces the record's.
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
With no --reviewer, codex runs, with each type its route scan picks; it
says on stderr what it picked and why.

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
test settings, and the round records RED_CHECK session, or absent.

Known secret shapes are taken out of the goal, the items, the earlier
findings, red-check's output, the threat notes and the Reverting section
before a reviewer sees them. For a pull request the earlier findings
include those of its newest review at another head.

  --reviewer TYPE   a type to run, as above; repeat for more
  --goal FILE       the goal statement, required for a range: the goal, and
                    what is not this change's job
  --items FILE      the items the change answers, and how each was settled
  --red-check FILE  git red-check BASE..TIP's JSON
  --description FILE
                    the pull request description, whose ## Reverting section
                    the compat review checks; for a pull request, its own
                    unless this names another
  --model M         the Codex model (default: Codex's own)
  --timeout S       seconds before a reviewer is stopped (default 1800)
  --print-prompt    print the prompt of one type (codex by default) for a
                    sub-agent the caller starts, and run nothing

Every file argument is read as given: write it under the working
directory, never $TMPDIR, which differs inside and outside the sandbox.

Writes JSON on stdout: source, range, tip, the record's path, each type's
review (reviewer, model, findings with their record IDs, dropped and
unverified), those that could not run and why, the routes, and the
record's counts. Exit status:
  0  no open findings
  3  open findings
  4  a type could not run: Codex is not installed or signed in, its
     sandbox cannot start, this is a review-loop session (PR_LOOP_SESSION
     is set) or, with no --reviewer, the caller is Codex (CODEX_SANDBOX is
     set); claude or agent-tools' delegate config is missing; a compat
     review the scan routed has no description with a Reverting section;
     or a reply did not fit twice
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

// reviewerTypes are the TYPEs, by name. Each route of routes.Known names
// one.
var reviewerTypes = map[string]reviewerType{
	"codex":    {},
	"security": {claude: true, agent: "security-reviewer", focus: "security"},
	"compat":   {claude: true, agent: "compat-reviewer", focus: "compat"},
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
	goal := flags.String("goal", "", "")
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
	r, err := resolveSource(ctx, s, true)
	if err != nil {
		return report(nil, err, 0)
	}
	if err := usableSettings(r); err != nil {
		return report(nil, err, 0)
	}

	base := inputs{r: r}
	if *goal != "" {
		if base.goal, err = readFile(*goal, stdin); err != nil {
			return report(nil, err, 0)
		}
	}
	if s.number == 0 && strings.TrimSpace(base.goal) == "" {
		if *goal == "" {
			return report(nil, refusal.New("a review is judged against a goal statement; write the goal, and what is not "+
				"this change's job, to a file under the working directory and pass `--goal FILE`"), 0)
		}
		return report(nil, refusal.New("the goal statement %s is empty; write the goal, and what is not this change's job, "+
			"into it and pass `--goal %s` again", *goal, *goal), 0)
	}
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
		t := "codex"
		if len(reviewers) == 1 {
			t = reviewers[0]
		}
		in, err := typeInputs(base, t, tests)
		if err != nil {
			return report(nil, err, 0)
		}
		in.where = where(r)
		if reviewerTypes[t].diagram {
			fmt.Fprint(stdout, drawPrompt(in, ""))
			return 0
		}
		fmt.Fprint(stdout, prompt(in))
		return 0
	}

	// Without --reviewer, codex and the types the scan routes the change to.
	explicit := len(reviewers) > 0
	scanned := routeChange(r, nil, stderr)
	chosen := slices.Clone([]string(reviewers))
	if !explicit {
		chosen = []string{"codex"}
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
	for _, t := range chosen {
		run := runType(ctx, s, r, base, t, tests, !explicit, *model, time.Duration(*timeout)*time.Second, stderr)
		if run.out == nil && run.failures == nil {
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
			fmt.Fprintf(stderr, "%s: the %s review could not run here: %s. Run `git delegate` where it can, outside any "+
				"sandbox and review-loop session, or start %s on what `git delegate --print-prompt "+
				"--reviewer %s %s%s` prints\n", name, t, run.why, reader, t, typeFlags(s, t), s.given)
			continue
		}
		runs = append(runs, run)
		if run.out != nil {
			result.Reviews = append(result.Reviews, run.out)
			if t == "codex" {
				result.Routes = run.out.Routes
			}
		} else {
			result.NotRun = append(result.NotRun, NotRun{Reviewer: t, Why: run.why})
		}
	}
	for _, rt := range result.Routes {
		if !slices.Contains(chosen, rt.Route) {
			fmt.Fprintf(stderr, "%s: this change needs a %s review (%s): run `git delegate --reviewer %s %s%s`\n",
				name, rt.Route, strings.Join(rt.Why, "; "), rt.Route, typeFlags(s, rt.Route), s.given)
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
// adequacy.
func typeInputs(base inputs, t string, tests *adequacy) (inputs, error) {
	in := base
	ty := reviewerTypes[t]
	if ty.guide {
		in.contract, in.prior = guideContract, ""
		return in, nil
	}
	if ty.focus == "" {
		in.redCheck, in.adequacy = tests.text, tests.prompt()+sharedPrompt(in.r)
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
	in.focused += overlapSection(in.r)
	return in, nil
}

// typeFlags are the flags, each followed by a space, that a command run
// for type t of s needs beyond the source, so that a hint runs as given:
// a range needs its goal, and a compat review of a range the description.
func typeFlags(s *source, t string) string {
	if s.number != 0 {
		return ""
	}
	flags := "--goal FILE "
	if focuses[reviewerTypes[t].focus].reverting {
		flags += "--description FILE "
	}
	return flags
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

// runType runs type t once, and once more where its reply does not fit.
// implicit is whether t was chosen without --reviewer: then a Codex caller
// does not review with Codex.
func runType(ctx context.Context, s *source, r *span, base inputs, t string, tests *adequacy, implicit bool, model string,
	timeout time.Duration, log io.Writer) *typeRun {
	ty := reviewerTypes[t]
	if ty.diagram {
		return runDiagrams(ctx, s, r, base, model, timeout, log)
	}
	run := &typeRun{name: t, redCheck: ty.focus == "" && !ty.guide && tests.given}
	if ty.claude {
		run.why = claudeMissing()
	} else {
		run.why = unavailable(implicit)
	}
	if run.why != "" {
		return run
	}
	in, err := typeInputs(base, t, tests)
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
			reply, err = ClaudeRun(ctx, Claude{Checkout: wt, Agent: ty.agent, Prompt: text, Schema: schema, Timeout: timeout, Log: log})
		} else {
			reply, err = CodexRun(ctx, Codex{Checkout: wt, Prompt: text, Schema: schema, Model: model, Timeout: timeout,
				Log: log, Git: worktreeGit(ctx, wt)})
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
