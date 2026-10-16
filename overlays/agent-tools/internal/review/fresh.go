package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"github.com/wesnel/agent-tools/internal/repos"
)

// Usage is `git fresh-review -h`.
const Usage = `usage: git fresh-review --goal FILE [--items FILE] [--prior FILE] [--reviewer auto|codex|subagent]
                        [--model M] [--timeout S] BASE..TIP
       git fresh-review --print-prompt --goal FILE [--items FILE] [--prior FILE] BASE..TIP
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

Where Codex cannot review, or a sub-agent is asked for, review with a fresh
sub-agent instead: give it the prompt --print-prompt (or --reviewer
subagent) prints, save its reply, and check that with --check, which prints
and exits as a review does. The prompt asks for the commit it names as TIP
in the reply, and --check takes TIP from there, refusing a reply without
it, so a HEAD that moved since the prompt was printed is not counted as
reviewed.

A review that ran, by Codex or through --check, logs a review receipt in
the plan its commits are logged in: the newest that names the worktree,
else the one that names the Claude Code session running it
(CLAUDE_CODE_SESSION_ID). The receipt holds BASE..TIP as given, TIP's
commit, and the patch ID
of the change from BASE to TIP. agent-plan stop-check compares it with the
change as it stands at the stop, reading BASE again, so pass BASE as a
name, such as origin/main, that follows a rebase. stderr says where the
receipt went, or why none was logged.

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
id, path, line, kind, claim and evidence, and endLine, about and reopens
where given), dropped (findings whose path:line does not exist at TIP, or
that do not fit the schema, each with its reason) and unverified. Exit
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
	if err := git.CheckConfig(""); err != nil {
		return report(nil, err, 0)
	}
	r, err := resolve("", positional[0])
	if err != nil {
		return report(nil, err, 0)
	}

	if *check != "" {
		reply, err := readFile(*check, stdin)
		if err != nil {
			return report(nil, err, 0)
		}
		tip, err := promptTip(reply, r)
		if err != nil {
			return report(nil, err, 0)
		}
		if tip != "" {
			r.tip = tip
		}
		out, err := validate(reply, r, "")
		if err != nil {
			return report(nil, err, 0)
		}
		out.Reviewer = "subagent"
		logReceipt(r, out, stderr)
		return report(out, nil, out.exit())
	}

	in := inputs{r: r}
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

	noCodex := func(why string) int {
		fmt.Fprintf(stderr, "%s: Codex cannot review here: %s. Review with a fresh `reviewer` sub-agent instead: "+
			"give it the prompt `git fresh-review --print-prompt` prints for the same files and range, "+
			"save its reply, and check it with `git fresh-review --check FILE %s`.\n", name, why, r.given)
		return 4
	}
	if !*printPrompt {
		if why := unavailable(*reviewer == "auto"); why != "" {
			return noCodex(why)
		}
	}
	in.redCheck = redCheck(ctx, r, stderr)
	if *printPrompt {
		in.where = fmt.Sprintf("Read the change in the repository at %s: `git diff %s %s`, `git log -p %s..%s`, "+
			"and `git show %s:PATH` for a file as it is at TIP. That checkout's own files may hold work that is not "+
			"part of the change, so judge only the commits. Put `\"tip\": \"%s\"` in the JSON object you reply with, "+
			"beside findings and unverified, so that the check of your reply knows which commit you reviewed.",
			r.top, r.base, r.tip, r.base, r.tip, r.tip, r.tip)
		fmt.Fprint(stdout, prompt(in))
		if subagent {
			fmt.Fprintf(stderr, "%s: give this prompt to a new `reviewer` sub-agent, save its reply, "+
				"and check it with `git fresh-review --check FILE %s`.\n", name, r.given)
		}
		return 0
	}

	out, err := codexReview(ctx, in, *model, time.Duration(*timeout)*time.Second, stderr)
	var unconfined Unconfined
	if errors.As(err, &unconfined) {
		return noCodex(string(unconfined))
	}
	if err != nil {
		return report(nil, err, 0)
	}
	logReceipt(r, out, stderr)
	return report(out, nil, out.exit())
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
func logReceipt(r *span, out *Output, log io.Writer) {
	id, err := git.PatchID([]string{"-C", r.top}, r.base, r.tip)
	file := ""
	if err == nil {
		model := ""
		if out.Model != nil {
			model = *out.Model
		}
		file, err = plans.WriteReceipt(os.Getenv("CLAUDE_CODE_SESSION_ID"), r.top, plans.Receipt{Range: r.given, Tip: r.tip,
			PatchID: id, Reviewer: out.Reviewer, Model: model, Findings: len(out.Findings)}, time.Now())
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

// RedCheckInVM runs git red-check on base..tip of the repository at top in
// the review sandbox's VM, and returns its results for the prompt; nil where
// no VM can. git-fresh-review sets it.
var RedCheckInVM func(ctx context.Context, top, base, tip string, log io.Writer) (string, error)

// redCheck is git red-check's results for the prompt, where the repository
// has test settings, from RedCheckInVM. red-check runs the commits' code,
// which may be anyone's, so it never runs here; where the VM cannot run it,
// log says so and the review has no red-check results.
func redCheck(ctx context.Context, r *span, log io.Writer) string {
	git.Dir = r.top
	settings, err := repos.Lookup()
	git.Dir = ""
	if err != nil || settings.Test == "" || len(settings.TestFiles) == 0 {
		return ""
	}
	why := "no VM is available here"
	if RedCheckInVM != nil {
		text, err := RedCheckInVM(ctx, r.top, r.base, r.tip, log)
		if err == nil {
			return text
		}
		why = err.Error()
	}
	fmt.Fprintf(log, "%s: git red-check runs only in the review sandbox, so this review has no red-check results: %s\n", name, why)
	return ""
}

type inputs struct {
	r                            *span
	goal, items, prior, redCheck string
	where                        string
}

// prompt is the contract, then the range, the goal statement, the items, the
// earlier findings and red-check's JSON: nothing of the author's beyond them.
func prompt(in inputs) string {
	var b strings.Builder
	b.WriteString(Contract)
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
	return validateWith(reply, given, tip, blob, wt)
}

// codexReview runs Codex read-only in a throwaway worktree of TIP, which it
// removes however the review ends.
func codexReview(ctx context.Context, in inputs, model string, timeout time.Duration, log io.Writer) (*Output, error) {
	parent := filepath.Join(in.r.common, "fresh-review")
	if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // holds checkouts of the repository, in its git directory
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
	reply, err := CodexRun(ctx, Codex{Checkout: wt, Prompt: prompt(in), Schema: Schema, Model: model, Timeout: timeout, Log: log})
	if err != nil {
		return nil, err
	}
	out, err := validate(reply.Text, in.r, wt)
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
	if err := os.WriteFile(schema, c.Schema, 0o644); err != nil { //nolint:gosec // a JSON schema compiled into this program
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
// made relative to it.
func validate(reply string, r *span, wt string) (*Output, error) {
	blob := func(path string) ([]byte, error) {
		return git.Bytes(nil, "-C", r.top, "cat-file", "blob", r.tip+":"+path)
	}
	return validateWith(reply, r.given, r.tip, blob, wt)
}

func validateWith(reply, given, tip string, blob func(path string) ([]byte, error), wt string) (*Output, error) {
	text := strings.TrimSpace(reply)
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, errors.New("the reply holds no JSON object; ask the reviewer again for the JSON the contract describes")
	}
	var parsed struct {
		Findings   *[]json.RawMessage `json:"findings"`
		Unverified []string           `json:"unverified"`
	}
	if err := json.Unmarshal([]byte(text[start:end+1]), &parsed); err != nil || parsed.Findings == nil {
		return nil, fmt.Errorf("the reply is not the JSON the contract describes (%v); ask the reviewer again", err)
	}
	out := &Output{Range: given, Tip: tip, Findings: []*Finding{}, Dropped: []Dropped{}, Unverified: []string{}}
	if parsed.Unverified != nil {
		out.Unverified = parsed.Unverified
	}
	seen := map[string]bool{}
	lines := map[string]int{}
	for _, raw := range *parsed.Findings {
		f, why := normalise(raw, blob, wt, lines)
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
func normalise(raw json.RawMessage, blob func(path string) ([]byte, error), wt string, lines map[string]int) (*Finding, string) {
	var f Finding
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, "does not fit the schema: " + err.Error()
	}
	switch {
	case strings.TrimSpace(f.ID) == "":
		return nil, "has no id"
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
