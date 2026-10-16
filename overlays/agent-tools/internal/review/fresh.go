package review

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// Usage is `git fresh-review -h`.
const Usage = `usage: git fresh-review [--focus ROUTE] --goal FILE [--items FILE] [--prior FILE]
                        [--reviewer auto|codex|subagent] [--model M] [--timeout S] BASE..TIP
       git fresh-review --print-prompt [--focus ROUTE] --goal FILE [--items FILE] [--prior FILE] BASE..TIP
       git fresh-review --check FILE BASE..TIP
       git fresh-review ask --question FILE PATH...   (see git fresh-review ask -h)

Reviews the commits in BASE..TIP with a reviewer that did not write them and
sees only the commits and what the files name: the goal statement (the goal,
and what is not this change's job), the items the change answers (threads,
CI checks, earlier findings and how each was settled), and the earlier
findings of this review.

By default Codex reviews, as a model family other than the author's:
codex exec --ignore-user-config --ephemeral, in a throwaway worktree of TIP
under the git directory's fresh-review/, removed on every exit. Its commands
may read only that worktree, the parts of the git directory git needs, the
Nix store and the system files they need to run; they may not write or
reach the network, and no MCP server, plugin, web search or image viewer
loads. Codex's sandbox cannot start inside another macOS sandbox, so run
this outside one. Where the repository has test settings, git red-check
BASE..TIP and the test command at TIP run in the VM pr-loop review runs
pull requests in, never here, as they run the commits' code; their output
goes in the prompt, and where the VM cannot run them stderr says why.

Each commit whose tests red-check finds pass without the rest of it
(passes-without-change) is an untested finding of the review's own, with
reviewer "red-check" and an id RC-<key>, the key made from that commit's
test files' diff. A finding-settled event with outcome rebut, a reason as
its evidence and --review <round>.RC-<key>, in the plan the receipt goes
to, keeps it out of later rounds until the tests change, where it follows
the receipt of round <round> of the same range as given, which raised it;
the finding's evidence names the round. It is listed under rebutted, and
in the prompt with its reason for the reviewer to contest. --check takes the VM's results from the --print-prompt run for
the same commits in the last week, and runs the VM itself where there is
none.

Where Codex cannot review, or a sub-agent is asked for, review with a fresh
sub-agent instead: give it the prompt --print-prompt (or --reviewer
subagent) prints, save its reply, and check that with --check, which prints
and exits as a review does. The prompt asks for the commit it names as TIP
in the reply, and --check takes TIP from there, refusing a reply without
it, so a HEAD that moved since the prompt was printed is not counted as
reviewed. Where a plan takes the receipt, --print-prompt logs the prompt
as review-issued with a nonce, its focus, range, tip and patch ID, and asks
for the nonce in the reply as issued; --check takes the focus from that
event, never from the reply, and refuses a nonce not issued here, one
checked already, one issued for another range or tip, and one whose tip
the range no longer contains or whose change has another patch ID now.
--print-prompt with --focus is refused where no plan takes the receipt.

A review that ran, by Codex or through --check, logs a review receipt in
the plan its commits are logged in: the newest that names the worktree,
else the one that names the Claude Code session running it
(CLAUDE_CODE_SESSION_ID). The receipt holds BASE..TIP as given, TIP's
commit, and the patch ID
of the change from BASE to TIP. agent-plan stop-check compares it with the
change as it stands at the stop, reading BASE again, so pass BASE as a
name, such as origin/main, that follows a rebase. stderr says where the
receipt went, or why none was logged.

A general review also routes the change to the focused reviews it needs,
each source only adding: a scan of every commit from TIP's merge base with
the default branch (origin/HEAD, else origin/main or origin/master, else
main or master), whatever BASE is, by its changed paths and its added and
removed lines, against built-in rules and those the operator's repos
settings add as routes; the routes the reviewer names; and security where
the plan's REVIEWS lost a route since the last routing. They are printed
as routes, each with why and the commits that need it, stderr says how to
run each, and a routed event logs them beside the receipt.

With --focus ROUTE, the review is that route's focused review: the same
runner, confinement and rules, with the route's checks and checklist in
place of the general ones, no red-check and no routing. A security reply
must name every category of its checklist as checked, or it is not
counted. Its findings' ids start with the route, as security-1, and it
logs a route receipt, which only meets that route and never counts as the
general review, with the IDs of its findings; it prints its round, by
which a finding-settled event names each finding.

  --focus ROUTE    the focused review to run: security
  --goal FILE      the goal statement (required)
  --items FILE      the items the change answers, and how each was settled
  --prior FILE      earlier rounds' findings and how each was settled
  --reviewer R      auto (default): Codex, unless called from inside Codex,
                    where a sub-agent is the fresh reviewer; codex: Codex
                    even then; subagent: print the prompt for a sub-agent,
                    as --print-prompt does, and exit 0
  --model M         the Codex model (default: Codex's own)
  --timeout S       seconds before the review is stopped (default 1800)

Writes JSON on stdout: reviewer, model, range, tip, findings (each with
id, path, line, kind, claim and evidence, and endLine, about, reopens and
reviewer where given), dropped (findings whose path:line does not exist at
TIP, or that do not fit the schema, each with its reason), unverified,
rebutted (red-check's findings left out, each with id, commit, path and
the rebuttal's reason) and, where the change needs any, routes; a
focused review adds focus and round. Exit
status:
  0  no findings
  3  findings
  4  Codex cannot review here: it is not installed or not signed in, its
     sandbox cannot start confined to the worktree, this is a review-loop
     session (PR_LOOP_SESSION or SANDBOX_RELAYED is set), or, with
     --reviewer auto, the caller is Codex (CODEX_SANDBOX is set)
  1  the review could not run; 2 a usage error or refusal
`

const name = "git-fresh-review"

// Main runs `git fresh-review` with args and returns its exit status.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "ask" {
		return ask(ctx, args[1:], stdin, stdout, stderr)
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	goal := flags.String("goal", "", "")
	items := flags.String("items", "", "")
	prior := flags.String("prior", "", "")
	reviewer := flags.String("reviewer", "auto", "")
	model := flags.String("model", "", "")
	timeout := flags.Int("timeout", 1800, "")
	printPrompt := flags.Bool("print-prompt", false, "")
	check := flags.String("check", "", "")
	focusName := flags.String("focus", "", "")

	// Flags may follow the range, as with git's own commands.
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
	if len(positional) != 1 || (*reviewer != "auto" && *reviewer != "codex" && *reviewer != "subagent") ||
		(*check != "" && *printPrompt) {
		fmt.Fprint(stderr, Usage)
		return 2
	}
	subagent := *reviewer == "subagent" && *check == ""
	*printPrompt = *printPrompt || subagent
	report := func(result any, err error, code int) int {
		return cli.Report(name, result, err, code, stdout, stderr)
	}
	if *check != "" && *focusName != "" {
		return report(nil, refusal.New("a reply's focus is the one its prompt was printed with, never the check's; "+
			"run `git fresh-review --check FILE %s` without --focus", positional[0]), 0)
	}
	var fc *focus
	if *focusName != "" {
		f, err := focusNamed(*focusName)
		if err != nil {
			return report(nil, err, 0)
		}
		fc = &f
	}
	if err := git.CheckConfig(""); err != nil {
		return report(nil, err, 0)
	}
	r, err := resolve("", positional[0])
	if err != nil {
		return report(nil, err, 0)
	}
	if err := usableSettings(r); err != nil {
		return report(nil, err, 0)
	}

	if *check != "" {
		reply, err := readFile(*check, stdin)
		if err != nil {
			return report(nil, err, 0)
		}
		current := r.tip
		tip, err := promptTip(reply, r)
		if err != nil {
			return report(nil, err, 0)
		}
		if tip != "" {
			r.tip = tip
		}
		issued, err := issuedFor(reply, r, current)
		if err != nil {
			return report(nil, err, 0)
		}
		if issued.Focus != "" {
			f := focuses[issued.Focus]
			fc, *focusName = &f, issued.Focus
		}
		out, err := validate(reply, r, "", *focusName, fc)
		if err != nil {
			return report(nil, err, 0)
		}
		if fc == nil {
			testAdequacy(ctx, r, true, stderr).add(out)
		}
		out.Reviewer = "subagent"
		return finish(r, out, *focusName, issued.Nonce, report, stderr)
	}

	in := inputs{r: r}
	if fc != nil {
		in.contract = fc.contract()
	}
	if *goal == "" {
		return report(nil, refusal.New("a review is judged against a goal statement; write the goal, and what is not this change's job, "+
			"to a file and pass `--goal FILE`"), 0)
	}
	if in.goal, err = readFile(*goal, stdin); err != nil {
		return report(nil, err, 0)
	}
	if strings.TrimSpace(in.goal) == "" {
		return report(nil, refusal.New("the goal statement %s is empty; write the goal, and what is not this change's job, "+
			"into it and pass `--goal %s` again", *goal, *goal), 0)
	}
	for _, f := range []struct {
		path string
		into *string
	}{{*items, &in.items}, {*prior, &in.prior}} {
		if f.path != "" {
			if *f.into, err = readFile(f.path, stdin); err != nil {
				return report(nil, err, 0)
			}
		}
	}

	focusFlag := ""
	if fc != nil {
		focusFlag = "--focus " + *focusName + " "
	}
	noCodex := func(why string) int {
		fmt.Fprintf(stderr, "%s: Codex cannot review here: %s. Review with a fresh `reviewer` sub-agent instead: "+
			"give it the prompt `git fresh-review %s--print-prompt` prints for the same files and range, "+
			"save its reply, and check it with `git fresh-review --check FILE %s`.\n", name, why, focusFlag, r.given)
		return 4
	}
	if !*printPrompt {
		if why := unavailable(*reviewer == "auto"); why != "" {
			return noCodex(why)
		}
	}
	tests := &adequacy{}
	if fc == nil {
		tests = testAdequacy(ctx, r, false, stderr)
		in.redCheck, in.adequacy = tests.text, tests.prompt()+sharedPrompt(r)
	}
	if *printPrompt {
		nonce, err := issue(r, *focusName)
		if err != nil {
			return report(nil, err, 0)
		}
		in.where = fmt.Sprintf("Read the change in the repository at %s: `git diff %s %s`, `git log -p %s..%s`, "+
			"and `git show %s:PATH` for a file as it is at TIP. That checkout's own files may hold work that is not "+
			"part of the change, so judge only the commits. Put `\"tip\": \"%s\"` in the JSON object you reply with, "+
			"beside findings and unverified, so that the check of your reply knows which commit you reviewed.",
			r.top, r.base, r.tip, r.base, r.tip, r.tip, r.tip)
		if nonce != "" {
			in.where += fmt.Sprintf(" Put `\"issued\": \"%s\"` beside it too, which names this prompt.", nonce)
		}
		fmt.Fprint(stdout, prompt(in))
		if subagent {
			fmt.Fprintf(stderr, "%s: give this prompt to a new `reviewer` sub-agent, save its reply, "+
				"and check it with `git fresh-review --check FILE %s`.\n", name, r.given)
		}
		return 0
	}

	out, err := codexReview(ctx, in, *focusName, fc, *model, time.Duration(*timeout)*time.Second, stderr)
	var unconfined Unconfined
	if errors.As(err, &unconfined) {
		return noCodex(string(unconfined))
	}
	if err != nil {
		return report(nil, err, 0)
	}
	tests.add(out)
	return finish(r, out, *focusName, "", report, stderr)
}

// finish routes a general review's change, or numbers a focused review's
// round, logs the review's receipt, and reports out.
func finish(r *span, out *Output, focusName, nonce string, report func(any, error, int) int, log io.Writer) int {
	if focusName == "" {
		routeChange(r, out, log)
	} else {
		out.Focus = focusName
		out.Round = plans.RouteRound(os.Getenv("CLAUDE_CODE_SESSION_ID"), r.top, focusName, r.given)
		if len(out.Findings) > 0 {
			var reviews []string
			for _, f := range out.Findings {
				reviews = append(reviews, fmt.Sprintf("`--review %d.%s`", out.Round, f.ID))
			}
			fmt.Fprintf(log, "%s: settle each finding as the review-change skill says, as %s; "+
				"the gates wait for each while its receipt covers the change\n", name, strings.Join(reviews, ", "))
		}
	}
	logReceipt(r, out, focusName, nonce, log)
	return report(out, nil, out.exit())
}

// issue logs the prompt about to be printed for r, with focus, in the plan
// a receipt of its reply would go to, and returns the nonce the reply must
// carry; "" where no plan takes it. A focused review's receipt is what
// meets its route, so its prompt is refused where there is no plan.
func issue(r *span, focusName string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(b[:])
	id, err := git.PatchID([]string{"-C", r.top}, r.base, r.tip)
	if err != nil {
		return "", err
	}
	file, err := plans.WriteIssued(os.Getenv("CLAUDE_CODE_SESSION_ID"), r.top,
		plans.Issued{Nonce: nonce, Focus: focusName, Range: r.given, Tip: r.tip, PatchID: id}, time.Now())
	switch {
	case focusName != "" && err != nil:
		return "", err
	case focusName != "" && file == "":
		return "", refusal.New("a %s review's receipt goes in the plan its commits are logged in, and no plan names this "+
			"session or worktree. Start one with `agent-plan new`, as the plan-work skill says, then print the prompt again", focusName)
	case err != nil || file == "":
		return "", nil
	}
	return nonce, nil
}

// issuedFor is the issued prompt a sub-agent's reply names, checked: the
// reply must carry the nonce of a prompt printed for the same range and
// tip, not yet checked, whose tip current, the range's tip now, still
// contains, and whose change still has the patch ID it was printed with.
// A reply naming none is a general review's, as replies were before
// prompts carried nonces.
func issuedFor(reply string, r *span, current string) (plans.Issued, error) {
	text := strings.TrimSpace(reply)
	var parsed struct {
		Issued string `json:"issued"`
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start || json.Unmarshal([]byte(text[start:end+1]), &parsed) != nil || parsed.Issued == "" {
		return plans.Issued{}, nil
	}
	again := fmt.Sprintf("print the prompt again with `git fresh-review --print-prompt` for %s, give it to a new reviewer, "+
		"and check that reply", r.given)
	i, checked, found := plans.FindIssued(os.Getenv("CLAUDE_CODE_SESSION_ID"), r.top, parsed.Issued)
	switch {
	case !found:
		return plans.Issued{}, refusal.New("the reply's issued %q names no prompt printed in this session's or worktree's plan; %s",
			parsed.Issued, again)
	case checked:
		return plans.Issued{}, refusal.New("the prompt %s was checked already, and a prompt's reply counts once; %s", i.Nonce, again)
	case i.Range != r.given || i.Tip != r.tip:
		return plans.Issued{}, refusal.New("the prompt %s was printed for %s at %s, not %s at %s; check the reply with "+
			"`git fresh-review --check FILE %s`, or %s", i.Nonce, i.Range, i.Tip, r.given, r.tip, i.Range, again)
	}
	id, err := git.PatchID([]string{"-C", r.top}, r.base, i.Tip)
	if err != nil {
		return plans.Issued{}, err
	}
	if id != i.PatchID || !git.OK("-C", r.top, "merge-base", "--is-ancestor", i.Tip, current) {
		return plans.Issued{}, refusal.New("the change has moved since the prompt %s was printed, so its reply reviews "+
			"what is no longer there; %s", i.Nonce, again)
	}
	return i, nil
}

// promptTip is the commit a sub-agent's reply says it reviewed: the tip
// the --print-prompt prompt it was given names, which HEAD may have moved
// past since. It must be a commit above r's base. A reply that holds no
// JSON object gives "", for validate to refuse.
func promptTip(reply string, r *span) (string, error) {
	text := strings.TrimSpace(reply)
	var parsed struct {
		Tip string `json:"tip"`
	}
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start || json.Unmarshal([]byte(text[start:end+1]), &parsed) != nil {
		return "", nil
	}
	again := fmt.Sprintf("give the reviewer the prompt `git fresh-review --print-prompt` prints for %s, which names the commit "+
		"and asks for it in the reply, and check its reply again", r.given)
	if parsed.Tip == "" {
		return "", refusal.New("the reply does not say which commit it reviewed; %s", again)
	}
	tip, err := git.Run("-C", r.top, "rev-parse", "--verify", "-q", parsed.Tip+"^{commit}")
	if err != nil || tip == "" {
		return "", refusal.New("the reply's tip %q is no commit here; %s", parsed.Tip, again)
	}
	if tip == r.base || !git.OK("-C", r.top, "merge-base", "--is-ancestor", r.base, tip) {
		return "", refusal.New("the reply's tip %s is not above the range's base %s; %s", tip, r.base, again)
	}
	return tip, nil
}

// logReceipt logs a receipt of the review out of r in the plan Locate
// finds for r's worktree and the Claude Code session running it, which the
// session's Stop hook compares with the change as it then stands; log
// says where, or why none was logged.
func logReceipt(r *span, out *Output, focusName, nonce string, log io.Writer) {
	id, err := git.PatchID([]string{"-C", r.top}, r.base, r.tip)
	file := ""
	if err == nil {
		model := ""
		if out.Model != nil {
			model = *out.Model
		}
		var raised, ids []string
		for _, f := range out.Findings {
			ids = append(ids, f.ID)
			if f.Reviewer == "red-check" {
				raised = append(raised, f.ID)
			}
		}
		receipt := plans.Receipt{Range: r.given, Tip: r.tip, PatchID: id, Reviewer: out.Reviewer, Model: model,
			Findings: len(out.Findings), Raised: raised, Nonce: nonce}
		if focusName != "" {
			receipt.Focus, receipt.FindingIDs = focusName, ids
			receipt.Branch, _ = git.Run("-C", r.top, "branch", "--show-current")
		}
		file, err = plans.WriteReceipt(os.Getenv("CLAUDE_CODE_SESSION_ID"), r.top, receipt, time.Now())
	}
	switch {
	case err != nil:
		fmt.Fprintf(log, "%s: no receipt of this review was logged: %v\n", name, err)
	case file == "":
		fmt.Fprintf(log, "%s: no plan names this session or worktree, so no receipt of this review was logged\n", name)
	default:
		fmt.Fprintf(log, "%s: logged a receipt of this review of %s in %s\n", name, r.given, file)
	}
}

// span is the range under review: as given, and resolved.
type span struct {
	given, base, tip, top, common string
	commits                       string
}

// resolve resolves the range given in the repository at dir, or, for "", the
// one containing the process's directory.
func resolve(dir, given string) (*span, error) {
	base, tip, ok := strings.Cut(given, "..")
	if !ok || strings.HasPrefix(tip, ".") || base == "" || tip == "" {
		return nil, refusal.New("%q is not a range BASE..TIP; pass the range whose commits make up the change, "+
			"such as `origin/main..HEAD`", given)
	}
	r := &span{given: given, top: dir}
	var err error
	if r.top == "" {
		if r.top, err = git.Toplevel(); err != nil {
			return nil, err
		}
	}
	if r.common, err = git.Run("-C", r.top, "rev-parse", "--path-format=absolute", "--git-common-dir"); err != nil {
		return nil, err
	}
	for _, p := range []struct {
		rev  string
		into *string
	}{{base, &r.base}, {tip, &r.tip}} {
		if *p.into, err = git.Run("-C", r.top, "rev-parse", "--verify", "--end-of-options", p.rev+"^{commit}"); err != nil {
			return nil, err
		}
	}
	if r.commits, err = git.Run("-C", r.top, "log", "--reverse", "--format=%H %s", r.base+".."+r.tip); err != nil {
		return nil, err
	}
	if r.commits == "" {
		return nil, refusal.New("%s has no commits to review; pass the range whose commits make up the change, "+
			"such as `origin/main..HEAD`", given)
	}
	return r, nil
}

// readFile reads path, or stdin for "-".
func readFile(path string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	return string(data), err
}

// unavailable says why Codex may not or cannot review from here, or "".
func unavailable(auto bool) string {
	switch {
	case os.Getenv("PR_LOOP_SESSION") != "":
		return "this is a review-loop session (PR_LOOP_SESSION is set), whose only way out is pr-loop"
	case os.Getenv("SANDBOX_RELAYED") != "":
		return "this runs behind sandbox-relay (SANDBOX_RELAYED is set)"
	case auto && os.Getenv("CODEX_SANDBOX") != "":
		return "the caller is Codex (CODEX_SANDBOX is set), so a sub-agent is already a fresh reviewer"
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return "codex is not on PATH"
	}
	if err := exec.Command("codex", "login", "status").Run(); err != nil {
		return "codex is not signed in (`codex login status` fails)"
	}
	return ""
}

type inputs struct {
	r                            *span
	goal, items, prior, redCheck string
	// adequacy is what the reviewer is told of test adequacy, beside
	// red-check's results.
	adequacy string
	where    string
	// contract is a focused review's instructions, in place of Contract.
	contract string
}

// prompt is the contract, then the range, the goal statement, the items, the
// earlier findings, red-check's JSON and the findings it raised or that were
// rebutted: nothing of the author's beyond them.
func prompt(in inputs) string {
	var b strings.Builder
	if in.contract != "" {
		b.WriteString(in.contract)
	} else {
		b.WriteString(Contract)
	}
	fmt.Fprintf(&b, "\n# This review\n\n## Range\n\nBASE..TIP is %s..%s (given as `%s`). Its commits, oldest first:\n\n%s\n\n%s\n",
		in.r.base, in.r.tip, in.r.given, in.r.commits, in.where)
	fmt.Fprintf(&b, "\n## Goal statement\n\n%s\n", strings.TrimSpace(in.goal))
	if s := strings.TrimSpace(in.items); s != "" {
		fmt.Fprintf(&b, "\n## Items\n\n%s\n", s)
	}
	if s := strings.TrimSpace(in.prior); s != "" {
		fmt.Fprintf(&b, "\n## Earlier findings\n\nAn earlier finding still standing is reported again, with `reopens` naming it.\n\n%s\n", s)
	}
	if in.redCheck != "" {
		fmt.Fprintf(&b, "\n## git red-check %s..%s\n\n%s\n", in.r.base, in.r.tip, in.redCheck)
	}
	if in.adequacy != "" {
		fmt.Fprintf(&b, "\n## Test adequacy\n\n%s", in.adequacy)
	}
	b.WriteString("\nReply with the JSON object the Output section describes, and nothing else.\n")
	return b.String()
}

// Inputs are a review's prompt for a caller that resolves the range itself.
// Commits is `git log --reverse --format='%H %s' BASE..TIP`; Where says where
// the reviewer reads the change.
type Inputs struct {
	Given, Base, Tip, Commits           string
	Goal, Items, Prior, RedCheck, Where string
}

// Prompt is the prompt a review with in gives its reviewer.
func Prompt(in Inputs) string {
	return prompt(inputs{r: &span{given: in.Given, base: in.Base, tip: in.Tip, commits: in.Commits},
		goal: in.Goal, items: in.Items, prior: in.Prior, redCheck: in.RedCheck, where: in.Where})
}

// Validate is validate for a caller that resolves the range itself: blob
// returns a file as it is at TIP, or an error where there is none.
func Validate(reply, given, tip string, blob func(path string) ([]byte, error), wt string) (*Output, error) {
	return validateWith(reply, given, tip, blob, wt, "", nil)
}

// codexReview runs Codex read-only in a throwaway worktree of TIP, which it
// removes however the review ends.
func codexReview(ctx context.Context, in inputs, focusName string, fc *focus, model string, timeout time.Duration,
	log io.Writer) (*Output, error) {
	parent := filepath.Join(in.r.common, "fresh-review")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, in.r.tip[:12]+"-")
	if err != nil {
		return nil, err
	}
	wt := filepath.Join(dir, "tree")
	defer func() {
		removeWorktree(in.r, wt)
		os.RemoveAll(dir)
		// Left in place while another review is using it.
		os.Remove(parent)
	}()
	if _, err := git.Run("-C", in.r.top, "worktree", "add", "--quiet", "--detach", wt, in.r.tip); err != nil {
		return nil, err
	}
	in.where = "The working directory is a checkout of TIP holding nothing but the commits; `git diff " +
		in.r.base + " " + in.r.tip + "` shows the change."
	schema := Schema
	if fc != nil {
		schema = fc.schema()
	}
	reply, err := CodexRun(ctx, Codex{Checkout: wt, Prompt: prompt(in), Schema: schema, Model: model, Timeout: timeout, Log: log})
	if err != nil {
		return nil, err
	}
	out, err := validate(reply.Text, in.r, wt, focusName, fc)
	if err != nil {
		return nil, err
	}
	out.Reviewer = "codex"
	if reply.Model != "" {
		out.Model = &reply.Model
	}
	return out, nil
}

// Codex is one confined, read-only Codex run.
type Codex struct {
	// Checkout is a worktree the caller made and removes, whose parent
	// directory is the caller's own and takes the schema and the reply.
	Checkout string
	Prompt   string
	// Schema is the JSON schema the reply must fit.
	Schema  []byte
	Model   string
	Timeout time.Duration
	// Log receives Codex's output.
	Log io.Writer
	// Git runs git in the checkout; nil for the git package's Run, with
	// the user's config.
	Git func(args ...string) (string, error)
}

// CodexReply is what Codex replied, and the model that replied.
type CodexReply struct {
	Text, Model string
}

// CodexRun runs Codex with c's prompt in c's checkout, confined as
// confinement says. It returns an Unconfined where Codex's sandbox cannot
// start confined to the checkout.
func CodexRun(ctx context.Context, c Codex) (*CodexReply, error) {
	dir := filepath.Dir(c.Checkout)
	schema, reply := filepath.Join(dir, "reply.schema.json"), filepath.Join(dir, "reply.json")
	if err := os.WriteFile(schema, c.Schema, 0o644); err != nil {
		return nil, err
	}
	run := c.Git
	if run == nil {
		run = func(args ...string) (string, error) { return git.Run(append([]string{"-C", c.Checkout}, args...)...) }
	}
	head, err := run("rev-parse", "--verify", "HEAD")
	if err != nil {
		return nil, err
	}
	admin, err := run("rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return nil, err
	}
	common, err := run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, err
	}
	// Codex runs helpers in its sandbox by executing itself, from the path
	// it was started by, so start it by a path the sandbox lets it read.
	codex, err := exec.LookPath("codex")
	if err == nil {
		codex, err = filepath.EvalSymlinks(codex)
	}
	if err != nil {
		return nil, err
	}
	confined, err := confinement(common, admin, codex)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	// Codex's sandbox does not start inside another macOS sandbox; Codex
	// must then not run at all, rather than run its commands unconfined.
	env := append(gitenv.Without(os.Environ()), "PATH="+realDirs(os.Getenv("PATH")))
	probe := exec.CommandContext(ctx, codex, append(append([]string{"sandbox"}, confined...),
		"-P", profile, "-C", c.Checkout, "--", "git", "rev-parse", "--verify", "HEAD")...)
	probe.Env = env
	var probeErr bytes.Buffer
	probe.Stderr = &probeErr
	if out, err := probe.Output(); err != nil || strings.TrimSpace(string(out)) != head {
		return nil, Unconfined(fmt.Sprintf("Codex's sandbox, confined to the worktree, could not run git there (%v: %s)",
			err, strings.TrimSpace(probeErr.String())))
	}
	args := append([]string{"exec", "--ignore-user-config", "--ignore-rules", "--ephemeral", "-C", c.Checkout,
		"--output-schema", schema, "-o", reply}, confined...)
	if c.Model != "" {
		args = append(args, "-m", c.Model)
	}
	cmd := exec.CommandContext(ctx, codex, append(args, "-")...)
	cmd.Dir = c.Checkout
	cmd.Env = env
	cmd.Stdin = strings.NewReader(c.Prompt)
	banner := &bannerModel{w: c.Log}
	cmd.Stdout, cmd.Stderr = c.Log, banner
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		stopGroup(cmd.Process.Pid, 5*time.Second)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("codex stopped: %w", ctx.Err())
		}
		return nil, fmt.Errorf("codex: %w", err)
	}
	data, err := os.ReadFile(reply)
	if err != nil {
		return nil, fmt.Errorf("codex left no reply: %w", err)
	}
	out := &CodexReply{Text: string(data), Model: banner.model}
	if out.Model == "" {
		out.Model = c.Model
	}
	return out, nil
}

// stopGroup kills the process group pgid until it is empty, or for at most
// bound. One kill is not enough: a process that forks as the kill lands can
// leave a child in the group that the kill missed.
func stopGroup(pgid int, bound time.Duration) {
	deadline := time.Now().Add(bound)
	for syscall.Kill(-pgid, syscall.SIGKILL) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

// Unconfined says why Codex cannot run confined.
type Unconfined string

func (e Unconfined) Error() string { return string(e) }

// profile names the Codex permissions profile confinement defines.
const profile = "fresh-review"

// confinement is the Codex configuration a review runs with: commands may
// read the worktree (Codex's workspace root), what macOS or Linux needs to
// run them (":minimal"), the Nix store, the codex executable and the parts
// of the git directory git needs to show the change, and nothing else; they
// may not write or reach the network. The user's configuration stays out
// with --ignore-user-config, so no MCP server or plugin of theirs loads, and
// the tools that read or reach out outside the sandbox are disabled.
func confinement(common, admin, codex string) ([]string, error) {
	var fs strings.Builder
	fs.WriteString(`{":minimal"="read", ":workspace_roots"={"."="read"}`)
	add := func(path, access string) error {
		if strings.ContainsFunc(path, func(c rune) bool { return c < 0x20 || c == 0x7f }) {
			return fmt.Errorf("%q has a control character, which Codex's configuration cannot hold", path)
		}
		fmt.Fprintf(&fs, `, "%s"="%s"`, tomlEscape.Replace(path), access)
		return nil
	}
	reads := []string{"/nix/store", codex, admin}
	for _, p := range []string{"HEAD", "config", "objects", "refs", "packed-refs", "info", "shallow"} {
		reads = append(reads, filepath.Join(common, p))
	}
	// Temporary directories hold other programs' files, which :minimal
	// would let Codex read and write; a read granted above, inside one,
	// still holds, as the more specific rule.
	denies := []string{"/tmp", "/private/tmp", "/private/var/folders", filepath.Clean(os.TempDir())}
	for _, p := range reads {
		if err := add(p, "read"); err != nil {
			return nil, err
		}
	}
	for _, p := range denies {
		if err := add(p, "deny"); err != nil {
			return nil, err
		}
	}
	fs.WriteString("}")
	args := []string{
		"-c", `default_permissions="` + profile + `"`,
		"-c", "permissions." + profile + ".filesystem=" + fs.String(),
		"-c", `shell_environment_policy.inherit="core"`,
		// Git cannot read the user's configuration in the sandbox, and
		// stops if told to.
		"-c", `shell_environment_policy.set={GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1"}`,
		"-c", `web_search="disabled"`,
	}
	// Each must be a feature `codex features list` knows, or Codex refuses
	// to start.
	for _, feature := range []string{"apps", "browser_use", "browser_use_external", "computer_use", "hooks",
		"image_generation", "in_app_browser", "memories", "multi_agent", "plugins", "remote_plugin",
		"skill_mcp_dependency_install", "tool_suggest", "view_image"} {
		args = append(args, "--disable", feature)
	}
	return args, nil
}

// realDirs resolves the symbolic links in each directory of a PATH. Codex
// grants its sandbox the real paths of what it may read, so it cannot follow
// a link such as nix-darwin's /run, and Codex takes its shell from PATH.
func realDirs(path string) string {
	dirs := filepath.SplitList(path)
	for i, dir := range dirs {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dirs[i] = real
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

// tomlEscape escapes text for the inside of a TOML basic string.
var tomlEscape = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// bannerModel passes Codex's stderr on and picks out the model it names in
// the banner it prints before the prompt, the only place `codex exec` says
// which model its configuration chose.
type bannerModel struct {
	w     io.Writer
	line  []byte
	done  bool
	model string
}

func (b *bannerModel) Write(p []byte) (int, error) {
	for _, c := range p {
		if b.done {
			break
		}
		if c != '\n' {
			b.line = append(b.line, c)
			continue
		}
		line := string(b.line)
		b.line = b.line[:0]
		switch {
		case strings.HasPrefix(line, "model: "):
			b.model, b.done = strings.TrimSpace(strings.TrimPrefix(line, "model: ")), true
		case line == "user":
			b.done = true
		}
	}
	return b.w.Write(p)
}

// removeWorktree deletes the worktree and git's record of it, by hand if
// `git worktree remove` refuses.
func removeWorktree(r *span, wt string) {
	if _, err := os.Stat(wt); err != nil {
		git.Run("-C", r.top, "worktree", "prune")
		return
	}
	admin, _ := git.Run("-C", wt, "rev-parse", "--path-format=absolute", "--git-dir")
	if _, err := git.Run("-C", r.top, "worktree", "remove", "--force", "--force", wt); err != nil {
		os.RemoveAll(wt)
		// Only git's own record of a worktree, never where a damaged
		// worktree's .git file might point.
		if admin != "" && filepath.Dir(admin) == filepath.Join(r.common, "worktrees") {
			os.RemoveAll(admin)
		}
	}
}

// Finding is one problem a reviewer reports.
type Finding struct {
	ID       string  `json:"id"`
	Path     string  `json:"path"`
	Line     int     `json:"line"`
	EndLine  *int    `json:"endLine,omitempty"`
	Kind     string  `json:"kind"`
	Claim    string  `json:"claim"`
	Evidence string  `json:"evidence"`
	About    *string `json:"about,omitempty"`
	Reopens  *string `json:"reopens,omitempty"`
	// Reviewer is "red-check" for a finding the review raised itself from
	// red-check's verdicts, and empty for the reviewer's.
	Reviewer string `json:"reviewer,omitempty"`
}

// Dropped is a finding left out of the result, and why.
type Dropped struct {
	Finding json.RawMessage `json:"finding"`
	Reason  string          `json:"reason"`
}

// Output is what `git fresh-review` prints.
type Output struct {
	Reviewer   string     `json:"reviewer"`
	Model      *string    `json:"model"`
	Range      string     `json:"range"`
	Tip        string     `json:"tip"`
	Findings   []*Finding `json:"findings"`
	Dropped    []Dropped  `json:"dropped"`
	Unverified []string   `json:"unverified"`
	Rebutted   []Rebutted `json:"rebutted"`
	// Routes are the focused reviews the change needs, where a general
	// review found any.
	Routes []routes.Route `json:"routes,omitempty"`
	// Focus is a focused review's route, and Round its round in the
	// plan: what a finding-settled event names its findings by.
	Focus string `json:"focus,omitempty"`
	Round int    `json:"round,omitempty"`
	// proposed are the routes the reviewer's reply named.
	proposed []proposedRoute
}

// proposedRoute is a route a reviewer's reply names.
type proposedRoute struct {
	Route string   `json:"route"`
	Why   string   `json:"why"`
	Paths []string `json:"paths"`
}

func (o *Output) exit() int {
	if len(o.Findings) > 0 {
		return 3
	}
	return 0
}

var kinds = map[string]bool{"goal-unmet": true, "bug": true, "regression": true, "unresolved": true,
	"out-of-scope": true, "untested": true, "untrue-doc": true, "convention": true}

// validate parses a reviewer's reply, which may be wrapped in prose or a
// code fence, and keeps the findings that fit the schema and point at a line
// that exists at TIP. A path inside wt, the checkout the reviewer read, is
// made relative to it. A focused review's reply, of focus fc named
// focusName, may hold its kinds too, must name every category of its
// checklist as checked, and has its findings' IDs prefixed with the
// focus, so that settling one never names a general review's.
func validate(reply string, r *span, wt, focusName string, fc *focus) (*Output, error) {
	blob := func(path string) ([]byte, error) {
		return git.Bytes(nil, "-C", r.top, "cat-file", "blob", r.tip+":"+path)
	}
	return validateWith(reply, r.given, r.tip, blob, wt, focusName, fc)
}

func validateWith(reply, given, tip string, blob func(path string) ([]byte, error), wt, focusName string,
	fc *focus) (*Output, error) {
	text := strings.TrimSpace(reply)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, errors.New("the reply holds no JSON object; ask the reviewer again for the JSON the contract describes")
	}
	var parsed struct {
		Findings   *[]json.RawMessage `json:"findings"`
		Unverified []string           `json:"unverified"`
		Routes     []proposedRoute    `json:"routes"`
		Checked    []string           `json:"checked"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil || parsed.Findings == nil {
		return nil, fmt.Errorf("the reply is not the JSON the contract describes (%v); ask the reviewer again", err)
	}
	allowed := kinds
	if fc != nil {
		if left := fc.unchecked(parsed.Checked); len(left) > 0 {
			return nil, fmt.Errorf("the reply does not name as checked the categories %s of its checklist; "+
				"ask a new reviewer to go through every one", strings.Join(left, ", "))
		}
		allowed = maps.Clone(kinds)
		for _, k := range fc.kinds {
			allowed[k] = true
		}
		parsed.Routes = nil
	}
	out := &Output{Range: given, Tip: tip, Findings: []*Finding{}, Dropped: []Dropped{}, Unverified: []string{},
		Rebutted: []Rebutted{}, proposed: parsed.Routes}
	if parsed.Unverified != nil {
		out.Unverified = parsed.Unverified
	}
	seen := map[string]bool{}
	lines := map[string]int{}
	for _, raw := range *parsed.Findings {
		f, why := normalise(raw, blob, wt, lines, allowed)
		if why == "" && focusName != "" {
			f.ID = focusName + "-" + f.ID
		}
		if why == "" && seen[f.ID] {
			why = "another finding has the id " + f.ID
		}
		if why != "" {
			out.Dropped = append(out.Dropped, Dropped{Finding: raw, Reason: why})
			continue
		}
		seen[f.ID] = true
		out.Findings = append(out.Findings, f)
	}
	return out, nil
}

// normalise checks one finding, returning it cleaned up or why it is
// dropped. lines caches each file's line count at TIP, -1 for no such file.
func normalise(raw json.RawMessage, blob func(path string) ([]byte, error), wt string, lines map[string]int,
	kinds map[string]bool) (*Finding, string) {
	var f Finding
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, "does not fit the schema: " + err.Error()
	}
	f.Reviewer = ""
	switch {
	case strings.TrimSpace(f.ID) == "":
		return nil, "has no id"
	case strings.HasPrefix(f.ID, mechanicalPrefix):
		return nil, "ids starting " + mechanicalPrefix + " are red-check's"
	case !kinds[f.Kind]:
		return nil, fmt.Sprintf("kind %q is not one the contract defines", f.Kind)
	case strings.TrimSpace(f.Claim) == "" || strings.TrimSpace(f.Evidence) == "":
		return nil, "has no claim or no evidence"
	}
	path := f.Path
	if wt != "" && strings.HasPrefix(path, wt+string(filepath.Separator)) {
		path = strings.TrimPrefix(path, wt+string(filepath.Separator))
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) || path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return nil, fmt.Sprintf("%s is not a path in the repository", f.Path)
	}
	f.Path = path
	n, ok := lines[path]
	if !ok {
		n = -1
		if data, err := blob(path); err == nil {
			n = bytes.Count(data, []byte("\n"))
			if len(data) > 0 && data[len(data)-1] != '\n' {
				n++
			}
		}
		lines[path] = n
	}
	switch {
	case n < 0:
		return nil, fmt.Sprintf("%s does not exist at TIP", path)
	case f.Line < 1 || f.Line > n:
		return nil, fmt.Sprintf("%s has %d lines at TIP, so line %d does not exist", path, n, f.Line)
	}
	if f.EndLine != nil && (*f.EndLine < f.Line || *f.EndLine > n) {
		f.EndLine = nil
	}
	for _, p := range []**string{&f.About, &f.Reopens} {
		if *p != nil && strings.TrimSpace(**p) == "" {
			*p = nil
		}
	}
	return &f, ""
}
