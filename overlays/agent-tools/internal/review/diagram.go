package review

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/diagram"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/redact"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/relay"
)

// DiagramUsage is `git fresh-review diagram -h`.
const DiagramUsage = `usage: git fresh-review diagram --diagram FILE [--prior FILE] [--model M] [--timeout S] BASE..TIP
       git fresh-review diagram --print-prompt --diagram FILE [--prior FILE] BASE..TIP
       git fresh-review diagram --check REPLY --diagram FILE BASE..TIP

Critiques a diagram of the code at TIP with a model family other than the
one that drew it. The diagram's
at must be TIP's commit; BASE..TIP is the change it is drawn for, and may
be empty (TIP..TIP) at a plan's design review, where a planned element
names an item of the plan of this session, worktree or branch.

First the diagram is held to its schema and to the code at TIP: one that
fails is refused, naming each element and why, for its drafter to
redraw, and no critic is asked.

Who drew it is what ` + "`agent-plan diagram check`" + ` recorded, by the harness
of the session that first checked it (diagram-drafted), in the plan of
this session, worktree or branch; a diagram with no record is refused.
Codex critiques a diagram Claude drew, confined as a review is (see git
fresh-review -h). A diagram Codex drew is critiqued by a new sub-agent of
the session running this, which must be marked as another family's
(Claude Code): --print-prompt prints its prompt, logged as review-issued
with a nonce, the diagram's hash and its drafter, and --check checks its
reply against the prompt issued for that diagram and range, once.

The critic goes through every element, and its reply names each as
checked or it is not counted. Its findings are untrue-doc, each about an
element, or null for something the diagram leaves out; their ids start
diagram-. A critique logs diagram-critiqued (the diagram's hash, the
findings' IDs, the drafter's and critic's families) in the plan a review's
receipt would go to; it is never a review receipt and meets no route.
Not served through a sandbox's relay.

  --diagram FILE    the diagram's JSON (required)
  --prior FILE      earlier critiques' findings and how each was settled
  --model M         the Codex model (default: Codex's own)
  --timeout S       seconds before the critique is stopped (default 1800)

Writes JSON on stdout as a review does, with focus "diagram" and the
diagram's hash. Exit status: 0 no findings, 3 findings, 4 Codex cannot
critique here, 1 the critique could not run, 2 a usage error or refusal.
`

// diagramChecks are a diagram critique's checks, checklist and output,
// which replace the general contract's.
//
//go:embed diagram.md
var diagramChecks string

// diagramMain runs `git fresh-review diagram`.
func diagramMain(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	const name = "git-fresh-review diagram"
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	file := flags.String("diagram", "", "")
	prior := flags.String("prior", "", "")
	model := flags.String("model", "", "")
	timeout := flags.Int("timeout", 1800, "")
	printPrompt := flags.Bool("print-prompt", false, "")
	check := flags.String("check", "", "")
	var positional []string
	for {
		if code, ok := cli.Parse(flags, args, DiagramUsage, stdout, stderr); !ok {
			return code
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(positional) != 1 || *file == "" || (*check != "" && *printPrompt) {
		fmt.Fprint(stderr, DiagramUsage)
		return 2
	}
	report := func(result any, err error, code int) int {
		return cli.Report(name, result, err, code, stdout, stderr)
	}
	if os.Getenv(relay.URLEnv) != "" || os.Getenv(relay.RelayEnv) == "1" {
		return report(nil, refusal.New("a diagram's critique is not served through a sandbox's relay; hand the diagram "+
			"to the operator, or critique it outside the sandbox with `git fresh-review diagram`"), 0)
	}
	if err := git.CheckConfig(""); err != nil {
		return report(nil, err, 0)
	}
	r, err := resolveSpan("", positional[0])
	if err != nil {
		return report(nil, err, 0)
	}
	data, err := readFile(*file, stdin)
	if err != nil {
		return report(nil, err, 0)
	}
	d, err := diagram.Parse([]byte(data))
	if err != nil {
		return report(nil, refusal.New("%v. Give this to the diagram's drafter, and critique the redraw with "+
			"`git fresh-review diagram`", err), 0)
	}
	if d.At != r.tip {
		return report(nil, refusal.New("the diagram cites the code at %s, and %s ends at %s; draw it at TIP, or pass the range "+
			"ending at its commit, such as `%s..%s`", d.At, r.given, r.tip, d.At, d.At), 0)
	}
	session := os.Getenv("CLAUDE_CODE_SESSION_ID")
	items := plans.CheckoutItems(session, r.top)
	var planned func(string) bool
	if items != nil {
		planned = func(id string) bool { return items[id] }
	}
	blob := func(path string) ([]byte, error) { return diagram.GitRepo(r.top).Blob(d.At, path) }
	checked := diagram.Check(d, blob, planned)
	if len(checked.Failures) > 0 {
		var why []string
		for _, f := range checked.Failures {
			why = append(why, f.Element+": "+f.Reason)
		}
		return report(nil, refusal.New("the diagram does not hold to the code at %s: %s. Give these to its drafter, then "+
			"critique the redraw with `git fresh-review diagram`", d.At, strings.Join(why, "; ")), 0)
	}
	hash := d.Hash()
	drafter := plans.DrafterOf(session, r.top, hash)
	if drafter == "" {
		return report(nil, refusal.New("no plan of this session, worktree or branch records who drew diagram %.12s; check it "+
			"with `agent-plan diagram check PLAN %s` in the session that drew it, which records its harness, then critique it", hash,
			*file), 0)
	}
	// caller is the family of the session running this, whose sub-agent a
	// printed prompt goes to.
	caller := plans.HarnessFamily()
	fc := &focus{checks: diagramChecks, categories: d.IDs()}
	in := critiqueInputs{r: r, d: d, checked: checked}
	if *prior != "" {
		if in.prior, err = readFile(*prior, stdin); err != nil {
			return report(nil, err, 0)
		}
	}
	finish := func(out *Output, c plans.Critique) int {
		out, dropped := critiqueFindings(out, d)
		out.Dropped = append(out.Dropped, dropped...)
		out.Focus, out.Diagram = plans.DiagramFocus, hash
		c.Diagram, c.Range, c.Tip = hash, r.given, d.At
		for _, f := range out.Findings {
			c.Findings = append(c.Findings, f.ID)
		}
		if out.Model != nil {
			c.Model = *out.Model
		}
		switch file, err := plans.WriteCritique(session, r.top, c, time.Now()); {
		case err != nil:
			fmt.Fprintf(stderr, "%s: the critique was not logged: %v\n", name, err)
		case file == "":
			fmt.Fprintf(stderr, "%s: no plan names this session, worktree or branch, so the critique was not logged\n", name)
		default:
			fmt.Fprintf(stderr, "%s: logged the critique of diagram %.12s in %s\n", name, hash, file)
		}
		return report(out, nil, out.exit())
	}

	if (*check != "" || *printPrompt) && (caller == "" || caller == drafter) {
		return report(nil, refusal.New("diagram %.12s was drawn by %s, and its critic is another family's sub-agent, but this "+
			"session is marked as %q; where Claude drew it, run `git fresh-review diagram --diagram %s %s` so that Codex "+
			"critiques it; where Codex drew it, print the prompt from a Claude Code session, or hand the diagram to the operator",
			hash, drafter, caller, *file, r.given), 0)
	}

	if *check != "" {
		reply, err := readFile(*check, stdin)
		if err != nil {
			return report(nil, err, 0)
		}
		current := r.tip
		issued, err := issuedFor(reply, r, current)
		if err != nil {
			return report(nil, err, 0)
		}
		if issued.Nonce == "" || issued.Focus != plans.DiagramFocus || issued.Diagram != hash {
			return report(nil, refusal.New("the reply names no prompt printed for this diagram's critique; print one with "+
				"`git fresh-review diagram --print-prompt --diagram %s %s`, give it to a new sub-agent, and check "+
				"that reply", *file, r.given), 0)
		}
		out, err := validateWith(reply, r.given, d.At, blob, "", plans.DiagramFocus, fc)
		if err != nil {
			return report(nil, err, 0)
		}
		out.Reviewer = "subagent"
		return finish(out, plans.Critique{Nonce: issued.Nonce, Drafter: drafter, Critic: caller})
	}

	if *printPrompt {
		nonce, err := issueDiagram(r, hash, drafter)
		if err != nil {
			return report(nil, err, 0)
		}
		in.where = fmt.Sprintf("Read the code in the repository at %s: `git show %s:PATH` for a file as it is at TIP, and "+
			"`git diff %s %s` for the change. That checkout's own files may hold work that is not part of it, so judge only "+
			"the commits. Put `\"issued\": \"%s\"` in the JSON object you reply with, beside findings, unverified and checked, "+
			"which names this prompt.", r.top, d.At, r.base, d.At, nonce)
		fmt.Fprint(stdout, critiquePrompt(in))
		fmt.Fprintf(stderr, "%s: give this prompt to a new `reviewer` sub-agent, save its reply, and check it with "+
			"`git fresh-review diagram --check FILE --diagram %s %s`.\n", name, *file, r.given)
		return 0
	}

	noCodex := func(why string) int {
		fmt.Fprintf(stderr, "%s: Codex cannot critique here: %s. Where Codex drew the diagram, critique it with a new Claude "+
			"`reviewer` sub-agent: give it the prompt `git fresh-review diagram --print-prompt --diagram %s %s` "+
			"prints, save its reply, and check it with `git fresh-review diagram --check FILE --diagram %s %s`; where Claude "+
			"drew it, hand the diagram to the operator.\n", name, why, *file, r.given, *file, r.given)
		return 4
	}
	if drafter == "codex" {
		return noCodex("Codex drew the diagram, and its critic is another family")
	}
	if why := unavailable(false); why != "" {
		return noCodex(why)
	}
	var reply *CodexReply
	var out *Output
	err = inTipWorktree(r, d.At, func(wt string) error {
		in.where = "The working directory is a checkout of TIP holding nothing but the commits; `git diff " + r.base + " " +
			d.At + "` shows the change."
		var err error
		if reply, err = CodexRun(ctx, Codex{Checkout: wt, Prompt: critiquePrompt(in), Schema: critiqueSchema(d.IDs()),
			Model: *model, Timeout: time.Duration(*timeout) * time.Second, Log: stderr}); err != nil {
			return err
		}
		out, err = validateWith(reply.Text, r.given, d.At, blob, wt, plans.DiagramFocus, fc)
		return err
	})
	var unconfined Unconfined
	if errors.As(err, &unconfined) {
		return noCodex(string(unconfined))
	}
	if err != nil {
		return report(nil, err, 0)
	}
	out.Reviewer = "codex"
	if reply.Model != "" {
		out.Model = &reply.Model
	}
	return finish(out, plans.Critique{Drafter: drafter, Critic: "codex"})
}

// inTipWorktree runs fn in a throwaway worktree of commit tip, under the
// git directory's fresh-review/ as codexReview's, which it removes however
// fn ends.
func inTipWorktree(r *span, tip string, fn func(wt string) error) error {
	parent := filepath.Join(r.common, "fresh-review")
	if err := os.MkdirAll(parent, 0o755); err != nil { //nolint:gosec // holds checkouts of the repository, in its git directory
		return err
	}
	dir, err := os.MkdirTemp(parent, tip[:12]+"-")
	if err != nil {
		return err
	}
	wt := filepath.Join(dir, "tree")
	defer func() {
		removeWorktree(r, wt)
		os.RemoveAll(dir)
		// Left in place while another review is using it.
		os.Remove(parent)
	}()
	if _, err := git.Run("-C", r.top, "worktree", "add", "--quiet", "--detach", wt, tip); err != nil {
		return err
	}
	return fn(wt)
}

// issueDiagram logs the critique's prompt about to be printed, for the
// diagram with hash, in the plan a critique of its reply would go to, and
// returns the nonce the reply must carry; a prompt with no plan to check
// it against is refused.
func issueDiagram(r *span, hash, drafter string) (string, error) {
	nonce, err := newNonce()
	if err != nil {
		return "", err
	}
	id, err := git.PatchID([]string{"-C", r.top}, r.base, r.tip)
	if err != nil {
		return "", err
	}
	file, err := plans.WriteIssued(os.Getenv("CLAUDE_CODE_SESSION_ID"), r.top, plans.Issued{Nonce: nonce,
		Focus: plans.DiagramFocus, Range: r.given, Tip: r.tip, PatchID: id, Diagram: hash, Drafter: drafter}, time.Now())
	switch {
	case err != nil:
		return "", err
	case file == "":
		return "", refusal.New("a diagram's critique is checked against the prompt its plan logged, and no plan names this " +
			"session, worktree or branch. Start one with `agent-plan new`, as the plan-work skill says, then print the prompt again")
	}
	return nonce, nil
}

// critiqueInputs are what a critic is given.
type critiqueInputs struct {
	r       *span
	d       *diagram.Diagram
	checked diagram.Result
	prior   string
	where   string
}

// critiquePrompt is the reviewer's rules and the diagram's checks, then
// the range, the diagram, what its mechanical check found, and earlier
// critiques' findings.
func critiquePrompt(in critiqueInputs) string {
	var b strings.Builder
	b.WriteString(generalRules())
	b.WriteString(diagramChecks)
	commits := in.r.commits
	if commits == "" {
		commits = "(none: the diagram is of the code at TIP, drawn before the change)"
	}
	fmt.Fprintf(&b, "\n# This critique\n\n## Range\n\nBASE..TIP is %s..%s (given as `%s`). Its commits, oldest first:\n\n%s\n\n%s\n",
		in.r.base, in.d.At, in.r.given, commits, in.where)
	fmt.Fprintf(&b, "\n## Diagram\n\nA %s diagram. An element with `planned` in place of `cite` is not in the code yet: "+
		"the plan item it names adds it; check only that the cited elements leave room for it.\n\n```json\n%s\n```\n",
		in.d.Kind, in.d.JSON())
	b.WriteString("\n## Mechanical check\n\nEach element passed:\n\n")
	for _, id := range in.d.IDs() {
		fmt.Fprintf(&b, "- %s: %s\n", id, in.checked.Checked[id])
	}
	if s := strings.TrimSpace(in.prior); s != "" {
		fmt.Fprintf(&b, "\n## Earlier findings\n\nAn earlier finding still standing is reported again, with `reopens` naming it.\n\n%s\n",
			redact.Secrets(s))
	}
	b.WriteString("\nReply with the JSON object the Output section describes, and nothing else.\n")
	return b.String()
}

// critiqueSchema is the JSON schema of a critic's reply: the general
// one's findings, of kind untrue-doc alone, and unverified, with the
// element IDs it checked in place of routes.
func critiqueSchema(ids []string) []byte {
	var s map[string]any
	if err := json.Unmarshal(Schema, &s); err != nil {
		panic(err)
	}
	props := s["properties"].(map[string]any)
	delete(props, "routes")
	kind := props["findings"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)
	kind["enum"] = []string{"untrue-doc"}
	props["checked"] = map[string]any{"type": "array", "description": "Each element of the diagram gone through.",
		"items": map[string]any{"type": "string", "enum": ids}}
	s["required"] = []string{"findings", "unverified", "checked"}
	out, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return out
}

// critiqueFindings keeps the findings a critique may hold, untrue-doc
// about an element of d or about none, and returns the rest as dropped.
func critiqueFindings(out *Output, d *diagram.Diagram) (*Output, []Dropped) {
	ids := d.IDs()
	var kept []*Finding
	var dropped []Dropped
	for _, f := range out.Findings {
		why := ""
		switch {
		case f.Kind != "untrue-doc":
			why = fmt.Sprintf("kind %q: a diagram's critique reports untrue-doc alone", f.Kind)
		case f.About != nil && !slices.Contains(ids, *f.About):
			why = fmt.Sprintf("about %q is not an element of the diagram", *f.About)
		}
		if why != "" {
			raw, _ := json.Marshal(f)
			dropped = append(dropped, Dropped{Finding: raw, Reason: why})
			continue
		}
		kept = append(kept, f)
	}
	if kept == nil {
		kept = []*Finding{}
	}
	out.Findings = kept
	return out, dropped
}
