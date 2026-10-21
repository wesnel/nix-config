package review

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/diagram"
	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/redact"
)

// maxDiagrams is the most diagrams one review draws.
const maxDiagrams = 3

// diagramChecks are a diagram critique's checks, checklist and output,
// which replace the general contract's.
//
//go:embed diagram.md
var diagramChecks string

// drawContract is what the diagrammer is told: the diagrams to draw, and
// the reply's shape.
const drawContract = `You draw diagrams of one change for the operator reviewing it: the commits
in a range BASE..TIP, judged from the code at TIP. Only read.

Draw at most three diagrams, each of one thing a reader of the change needs
to see whole: how its parts call each other (callflow or component), the
order things happen in (sequence), or the states something moves through
(state). Draw none where the change has nothing such.

Every node and edge cites the lines at TIP it stands for, with a symbol
that is a whole word in them: a node's declaration, a call edge's callee,
a transition's target state; an element you cannot cite does not go in.
Nothing is planned: each element's "planned" is null. Each diagram's "at"
is TIP's full commit ID. Another model checks every citation and critiques
each diagram against the code; one that does not hold is dropped.
`

// drawSchema is the JSON Schema of the diagrammer's reply: diagrams, each
// one diagram.Schema describes.
func drawSchema() []byte {
	var one map[string]any
	if err := json.Unmarshal(diagram.Schema, &one); err != nil {
		panic(err)
	}
	defs := one["$defs"]
	delete(one, "$defs")
	out, err := json.Marshal(map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"diagrams", "unverified"},
		"properties": map[string]any{
			"diagrams":   map[string]any{"type": "array", "description": "At most three.", "items": one},
			"unverified": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"$defs": defs,
	})
	if err != nil {
		panic(err)
	}
	return out
}

// drawPrompt is the diagrammer's prompt for r, with what to redraw where
// redraw is not "".
func drawPrompt(in inputs, redraw string) string {
	var b strings.Builder
	b.WriteString(drawContract)
	fmt.Fprintf(&b, "\n# This change\n\n## Range\n\nBASE..TIP is %s..%s. Its commits, oldest first:\n\n%s\n\n%s\n",
		in.r.base, in.r.tip, in.r.commits, in.where)
	if g := strings.TrimSpace(in.goal); g != "" {
		fmt.Fprintf(&b, "\n## Goal statement\n\n%s\n", redact.Secrets(g))
	}
	if redraw != "" {
		fmt.Fprintf(&b, "\n## Redraw\n\nA critic found this diagram untrue to the code. Redraw it, alone, so that each "+
			"finding no longer holds:\n\n%s\n", redraw)
	}
	b.WriteString("\nReply with the JSON object the schema describes, and nothing else.\n")
	return b.String()
}

// critiqueInputs are what a critic is given.
type critiqueInputs struct {
	r       *span
	d       *diagram.Diagram
	checked diagram.Result
	where   string
}

// critiquePrompt is the reviewer's rules and the diagram's checks, then
// the range, the diagram and what its mechanical check found.
func critiquePrompt(in critiqueInputs) string {
	var b strings.Builder
	b.WriteString(generalRules())
	b.WriteString(diagramChecks)
	fmt.Fprintf(&b, "\n# This critique\n\n## Range\n\nBASE..TIP is %s..%s (given as `%s`). Its commits, oldest first:\n\n%s\n\n%s\n",
		in.r.base, in.d.At, in.r.given, in.r.commits, in.where)
	fmt.Fprintf(&b, "\n## Diagram\n\nA %s diagram.\n\n```json\n%s\n```\n", in.d.Kind, in.d.JSON())
	b.WriteString("\n## Mechanical check\n\nEach element passed:\n\n")
	for _, id := range in.d.IDs() {
		fmt.Fprintf(&b, "- %s: %s\n", id, in.checked.Checked[id])
	}
	b.WriteString("\nReply with the JSON object the Output section describes, and nothing else.\n")
	return b.String()
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

// diagramMermaid is a stored diagram's Mermaid, generated when read.
func diagramMermaid(stored string) (string, error) {
	d, err := diagram.Parse([]byte(stored))
	if err != nil {
		return "", err
	}
	return d.Mermaid(), nil
}

// DiagramResult is a diagram a review kept, as it prints it; its record
// holds the diagram itself. Why says what left an unresolved one so.
type DiagramResult struct {
	Title   string `json:"title"`
	Verdict string `json:"verdict"`
	Why     string `json:"why,omitempty"`
}

// runDiagrams draws the change's diagrams with Claude's diagrammer, holds
// each to the code it cites, and has Codex critique each against the code.
// One whose critique passes is kept as holds. One the critic faults is
// redrawn once, checked and critiqued again, and its redraw kept as
// corrected only when that critique passes; otherwise the first drawing
// is kept as unresolved, with the first critique's findings. A drawing
// that does not hold to its citations goes to Dropped. Only the diagrams'
// JSON is kept.
func runDiagrams(ctx context.Context, s *source, r *span, base inputs, model string, timeout time.Duration,
	log io.Writer) *typeRun {
	run := &typeRun{name: "diagram"}
	if run.why = claudeMissing(); run.why == "" {
		run.why = unavailable(false)
	}
	if run.why != "" {
		return run
	}
	in := base
	in.where = "The working directory is a checkout of TIP holding nothing but the commits; `git diff " +
		r.base + " " + r.tip + "` shows the change."
	out := &Output{Reviewer: "diagram", Range: r.given, Tip: r.tip, Findings: []*Finding{}, Dropped: []Dropped{},
		Unverified: []string{}, Diagrams: []DiagramResult{}}
	drop := func(what any, why string) {
		raw, _ := json.Marshal(what)
		out.Dropped = append(out.Dropped, Dropped{Finding: raw, Reason: why})
	}
	blob := func(path string) ([]byte, error) { return diagram.GitRepo(r.top).Blob(r.tip, path) }

	// draw returns the diagrams the diagrammer replied with.
	draw := func(prompt string) ([]json.RawMessage, error) {
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			wt, remove, err := worktree(ctx, s, r)
			if err != nil {
				return nil, err
			}
			reply, err := ClaudeRun(ctx, Claude{Checkout: wt, Agent: "diagrammer", Prompt: prompt, Schema: drawSchema(),
				Timeout: timeout, Log: log})
			remove()
			var bad badReply
			switch {
			case errors.As(err, &bad):
			case err != nil:
				return nil, err
			default:
				run.model = reply.Model
				var parsed struct {
					Diagrams   []json.RawMessage `json:"diagrams"`
					Unverified []string          `json:"unverified"`
				}
				if err = json.Unmarshal([]byte(reply.Text), &parsed); err == nil && parsed.Diagrams != nil {
					out.Unverified = append(out.Unverified, parsed.Unverified...)
					return parsed.Diagrams, nil
				}
				err = fmt.Errorf("the reply is not the diagrams the schema describes (%v)", err)
			}
			lastErr = err
			run.failures = append(run.failures, "The diagrammer's reply could not be used: "+err.Error())
			prompt += "\n## Your last reply\n\nYour last reply could not be used: " + err.Error() +
				". Reply again with the JSON the schema describes, and nothing else.\n"
		}
		return nil, lastErr
	}
	// checked parses and checks one drawn diagram, or says why it is dropped.
	checked := func(raw json.RawMessage) (*diagram.Diagram, diagram.Result, string) {
		d, err := diagram.Parse(raw)
		if err != nil {
			return nil, diagram.Result{}, err.Error()
		}
		if d.At != r.tip {
			return nil, diagram.Result{}, fmt.Sprintf("it is drawn at %s, not TIP %s", d.At, r.tip)
		}
		res := diagram.Check(d, blob, nil)
		if len(res.Failures) > 0 {
			var why []string
			for _, f := range res.Failures {
				why = append(why, f.Element+": "+f.Reason)
			}
			return nil, res, "it does not hold to the code at TIP: " + strings.Join(why, "; ")
		}
		return d, res, ""
	}
	// critique has Codex read the code against d, and returns its findings,
	// and why it is inconclusive when it faults nothing yet passes nothing:
	// it left elements unverified, or each finding it gave was dropped.
	critique := func(d *diagram.Diagram, res diagram.Result) ([]*Finding, string, error) {
		wt, remove, err := worktree(ctx, s, r)
		if err != nil {
			return nil, "", err
		}
		defer remove()
		fc := &focus{checks: diagramChecks, categories: d.IDs()}
		ci := critiqueInputs{r: r, d: d, checked: res, where: in.where}
		reply, err := CodexRun(ctx, Codex{Checkout: wt, Prompt: critiquePrompt(ci), Schema: fc.schema(), Model: model,
			Timeout: timeout, Log: log, Git: worktreeGit(ctx, wt)})
		if err != nil {
			return nil, "", err
		}
		crit, err := validate(reply.Text, r, wt, "diagram", fc)
		if err != nil {
			return nil, "", err
		}
		crit, dropped := critiqueFindings(crit, d)
		out.Dropped = append(out.Dropped, crit.Dropped...)
		out.Dropped = append(out.Dropped, dropped...)
		inconclusive := ""
		switch {
		case len(crit.Findings) > 0:
		case len(crit.Unverified) > 0:
			inconclusive = "the critic could not verify: " + strings.Join(crit.Unverified, "; ")
		case len(crit.Dropped)+len(dropped) > 0:
			inconclusive = "each of the critique's findings was dropped"
		}
		return crit.Findings, inconclusive, nil
	}
	// keep adds d to the record with verdict, and an unresolved one's
	// critique as findings about it.
	keep := func(d *diagram.Diagram, verdict, why string, findings []*Finding) {
		out.diagrams = append(out.diagrams, record.Diagram{Title: d.Title, DrawnBy: "claude", CritiquedBy: "codex",
			Verdict: verdict, JSON: string(d.JSON())})
		out.Diagrams = append(out.Diagrams, DiagramResult{Title: d.Title, Verdict: verdict, Why: why})
		if why != "" {
			out.Unverified = append(out.Unverified, fmt.Sprintf("The diagram %q is unresolved: %s", d.Title, why))
		}
		for _, f := range findings {
			about := fmt.Sprintf("the diagram %q", d.Title)
			if f.About != nil {
				about = fmt.Sprintf("%s of the diagram %q", *f.About, d.Title)
			}
			f.About = &about
			out.Findings = append(out.Findings, f)
		}
	}

	drawn, err := draw(drawPrompt(in, ""))
	if err != nil {
		run.why = err.Error()
		return run
	}
	if len(drawn) > maxDiagrams {
		for _, raw := range drawn[maxDiagrams:] {
			drop(raw, fmt.Sprintf("more than %d diagrams were drawn", maxDiagrams))
		}
		drawn = drawn[:maxDiagrams]
	}
	for _, raw := range drawn {
		d, res, why := checked(raw)
		if why != "" {
			drop(raw, why)
			continue
		}
		findings, inconclusive, err := critique(d, res)
		var unconfined Unconfined
		if errors.As(err, &unconfined) {
			run.why = string(unconfined)
			return run
		}
		if err != nil {
			drop(raw, "its critique could not be used: "+err.Error())
			continue
		}
		if len(findings) == 0 {
			if inconclusive != "" {
				keep(d, "unresolved", inconclusive, nil)
			} else {
				keep(d, "holds", "", nil)
			}
			continue
		}
		text, _ := json.MarshalIndent(findings, "", "  ")
		redrawn, err := draw(drawPrompt(in, "```json\n"+string(d.JSON())+"\n```\n\nThe critic's findings:\n\n```json\n"+
			string(text)+"\n```"))
		if err != nil || len(redrawn) != 1 {
			keep(d, "unresolved", fmt.Sprintf("the critic found it untrue, and it was not redrawn as one diagram (%v)", err),
				findings)
			continue
		}
		d2, res2, why := checked(redrawn[0])
		if why != "" {
			drop(redrawn[0], "the redraw of a diagram the critic found untrue: "+why)
			keep(d, "unresolved", "the critic found it untrue, and its redraw does not hold to the code at TIP", findings)
			continue
		}
		again, inconclusive, err := critique(d2, res2)
		if errors.As(err, &unconfined) {
			run.why = string(unconfined)
			return run
		}
		switch {
		case err != nil:
			keep(d, "unresolved", "the critic found it untrue, and its redraw's critique could not be used: "+err.Error(),
				findings)
		case len(again) > 0:
			drop(redrawn[0], "the critic found the redraw untrue too")
			keep(d, "unresolved", "the critic found it untrue, and its redraw too", findings)
		case inconclusive != "":
			drop(redrawn[0], "the redraw's critique is inconclusive: "+inconclusive)
			keep(d, "unresolved", "the critic found it untrue, and its redraw's critique is inconclusive", findings)
		default:
			keep(d2, "corrected", "", nil)
		}
	}
	run.out = out
	return run
}

// replaceDiagrams is drawn in place of old: a diagram with an old one's
// title takes its ID, and each other the next after the old and taken
// IDs, D1 on.
func replaceDiagrams(old, drawn []record.Diagram) []record.Diagram {
	byTitle := map[string]string{}
	var ids []string
	for _, d := range old {
		if _, ok := byTitle[d.Title]; !ok {
			byTitle[d.Title] = d.ID
		}
		ids = append(ids, d.ID)
	}
	out := make([]record.Diagram, 0, len(drawn))
	for _, d := range drawn {
		if id, ok := byTitle[d.Title]; ok {
			d.ID = id
			delete(byTitle, d.Title)
		} else {
			d.ID = "D" + strconv.Itoa(numbered("D", ids))
			ids = append(ids, d.ID)
		}
		out = append(out, d)
	}
	return out
}
