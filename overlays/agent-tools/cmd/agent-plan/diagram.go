package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/diagram"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// diagramCommand is `agent-plan diagram schema|check|keep`.
func diagramCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("diagram", flag.ContinueOnError)
	disputed := flags.Bool("disputed", false, "")
	findings := flags.String("findings", "", "")
	replace := flags.String("replace", "", "")
	positional, code, ok := parse(flags, args, stdout, stderr)
	if !ok {
		return code
	}
	report := func(result any, err error, code int) int {
		return cli.Report("agent-plan", result, err, code, stdout, stderr)
	}
	sub := ""
	if len(positional) > 0 {
		sub, positional = positional[0], positional[1:]
	}
	if sub == "schema" && len(positional) == 0 {
		stdout.Write(diagram.Schema)
		return 0
	}
	if (sub != "check" && sub != "keep") || len(positional) < 1 || len(positional) > 2 || (sub == "keep" && len(positional) != 2) ||
		(*disputed || *findings != "" || *replace != "") && sub != "keep" || *disputed != (*findings != "") {
		fmt.Fprint(stderr, "agent-plan diagram: takes schema, check PLAN [FILE], or keep PLAN FILE "+
			"[--disputed --findings FILE] [--replace G1]\n")
		return 2
	}
	c, err := plans.Load()
	if err != nil {
		return report(nil, err, 0)
	}
	p, err := c.Open(positional[0])
	if err != nil {
		return report(nil, err, 0)
	}
	top, err := git.Toplevel()
	if err != nil {
		return report(nil, refusal.New("a diagram is held to the code of the repository it is run in, and this is none; "+
			"run `agent-plan diagram %s` from the worktree the plan names", sub), 0)
	}
	if err := git.CheckConfig(top); err != nil {
		return report(nil, err, 0)
	}
	repo := diagram.GitRepo(top)
	if len(positional) == 1 {
		now, err := git.Run("-C", top, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return report(nil, err, 0)
		}
		states, err := p.RefreshDiagrams(repo, now, time.Now())
		code := 0
		for _, s := range states {
			if s.Status == plans.DiagramStale || s.Edited != "" {
				code = 3
			}
		}
		return report(states, err, code)
	}
	data, err := os.ReadFile(positional[1]) //nolint:gosec // the host caller's own file; the relay serves no diagram command
	if err != nil {
		return report(nil, err, 0)
	}
	d, err := diagram.Parse(data)
	if err != nil {
		return report(nil, refusal.New("%v. Redraw it to the schema `agent-plan diagram schema` prints", err), 0)
	}
	items := p.ItemIDs()
	r := diagram.Check(d, func(path string) ([]byte, error) { return repo.Blob(d.At, path) },
		func(id string) bool { return items[id] })
	if sub == "check" {
		if len(r.Failures) > 0 {
			return report(map[string]any{"hash": d.Hash(), "failures": r.Failures, "checked": r.Checked, "diagram": d}, nil, 3)
		}
		// The session that first checks a diagram is taken as the one that
		// drew it, by its harness's own markers, never by what it says.
		drafter, err := p.RecordDrafted(d.Hash(), time.Now())
		if err != nil {
			return report(nil, err, 0)
		}
		return report(map[string]any{"hash": d.Hash(), "failures": r.Failures, "checked": r.Checked, "diagram": d,
			"drafter": drafter}, nil, 0)
	}
	if len(r.Failures) > 0 {
		return report(nil, refusal.New("the diagram does not hold to the code at %s: %v. Redraw it and check it with "+
			"`agent-plan diagram check %s FILE`", d.At, r.Failures, positional[0]), 0)
	}
	if _, err := p.RecordDrafted(d.Hash(), time.Now()); err != nil {
		return report(nil, err, 0)
	}
	root, err := repo.Root(d.At)
	if err != nil {
		return report(nil, err, 0)
	}
	o := plans.KeepOptions{Disputed: *disputed, Replace: *replace, Repo: root}
	if *findings != "" {
		data, err := os.ReadFile(*findings)
		if err != nil {
			return report(nil, err, 0)
		}
		var critique struct {
			Diagram  string            `json:"diagram"`
			Findings []json.RawMessage `json:"findings"`
		}
		if err := json.Unmarshal(data, &critique); err != nil || critique.Diagram != d.Hash() {
			return report(nil, refusal.New("%s is not the output of the critique of this diagram (%.12s); pass the JSON "+
				"`git fresh-review diagram` printed for it as `--findings FILE`", *findings, d.Hash()), 0)
		}
		for _, raw := range critique.Findings {
			var f struct {
				About *string `json:"about"`
				Path  string  `json:"path"`
				Line  int     `json:"line"`
			}
			if json.Unmarshal(raw, &f) == nil {
				at := plans.FindingAt{Path: f.Path, Line: f.Line}
				if f.About != nil {
					at.About = *f.About
				}
				o.Findings = append(o.Findings, at)
			}
		}
	}
	kept, err := p.KeepDiagram(d, o, time.Now())
	return report(kept, err, 0)
}
