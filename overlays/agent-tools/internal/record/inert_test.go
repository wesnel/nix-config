package record

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// payloads are untrusted text made of Org syntax: links bracketed and
// plain, inline source blocks and calls, macros, export snippets,
// headlines, keywords and blocks, drawers, tables, lists, footnotes,
// targets, LaTeX, planning lines, comments, fixed-width lines, timestamps,
// emphasis and file variables.
var payloads = []string{
	`[[[elisp:(progn (write-region "x" nil "/tmp/rec-pwned") (message "pwned"))][click]]]`,
	`[[elisp:(kill-emacs)][click]]`,
	`[[file:/etc/passwd]]`,
	`shell:touch${IFS}/tmp/rec-pwned`,
	`elisp:ignore`,
	`file:/etc/passwd`,
	`https://example.com/x`,
	`src_emacs-lisp{(write-region "x" nil "/tmp/rec-pwned")}`,
	`src_sh[:results raw]{touch /tmp/rec-pwned}`,
	`call_foo()`,
	`{{{date}}}`,
	`@@html:<script>alert(1)</script>@@`,
	"* TODO a headline",
	"** DONE another",
	"*** OPEN F9",
	",* an escaped headline",
	"#+TITLE: another title",
	"#+begin_src emacs-lisp",
	"#+end_example",
	",#+end_example",
	"  #+end_example",
	"#+begin_example",
	"#+CALL: foo()",
	"#+INCLUDE: \"/etc/passwd\"",
	":PROPERTIES:",
	":END:",
	":LOGBOOK:",
	"| a | b |",
	"|-+-|",
	"- item",
	"1. item",
	"[fn:1] a footnote",
	"<<target>>",
	"<<<radio>>>",
	`\alpha $x$ \(y\)`,
	"CLOCK: [2026-01-01 Thu 10:00]",
	"SCHEDULED: <2026-01-01 Thu>",
	"# a comment",
	": fixed width",
	"<2026-10-14 Wed>",
	"*bold* /italic/ =verbatim= ~code~ a_b^c",
	"-*- mode: emacs-lisp; eval: (kill-emacs) -*-",
	"line one\n* TODO two\n#+begin_src",
	"Local Variables:\neval: (kill-emacs)\nEnd:",
}

// corpusRange holds a payload in every title and text it has.
func corpusRange() *Range {
	r := fixtureRange()
	r.Title = strings.Join(payloads, " ")
	r.Walkthrough, r.Findings, r.Diagrams = nil, nil, nil
	for i, p := range payloads {
		r.Walkthrough = append(r.Walkthrough, Chunk{ID: fmt.Sprintf("C%d", i+1), Title: p, Summary: p,
			Hunks: []Hunk{{Path: "x/a.go", NewStart: i + 1, NewLines: 2}}})
		r.Diagrams = append(r.Diagrams, Diagram{ID: fmt.Sprintf("D%d", i+1), Title: p, DrawnBy: p, CritiquedBy: p, Verdict: p, JSON: p})
		r.Findings = append(r.Findings, Finding{ID: fmt.Sprintf("F%d", i+1), State: Open, Title: p, Reviewer: p, Kind: p,
			Path: "x/a.go", Line: i + 1, Side: "new", OutcomeTarget: p, Evidence: p, Reason: p})
	}
	for i, p := range oddPaths {
		r.Walkthrough = append(r.Walkthrough, Chunk{ID: fmt.Sprintf("C%d", 101+i), Hunks: []Hunk{{Path: p, NewStart: 1, NewLines: 1}}})
		r.Findings = append(r.Findings, Finding{ID: fmt.Sprintf("F%d", 101+i), State: Open, Path: p, Line: 1})
	}
	r.Rounds = []Round{{ID: "R1", Reviewer: "codex", Model: "x", At: at, RedCheck: "session",
		Unverified: payloads, Dropped: []string{strings.Join(payloads, "\n")}}}
	return r
}

func corpusPR() *PR {
	p := &PR{Repo: "owner/repo", Number: 12, Updated: at}
	for i, text := range payloads {
		p.Threads = append(p.Threads, Thread{ID: fmt.Sprintf("PRRT_%d", i), Path: "x/a.go", Line: i + 1,
			Comments: []Comment{{Author: "alice", Type: "User", Body: text}, {Author: "copilot", Type: "Bot", Body: text}},
			Draft:    &Draft{Outcome: "fix", Body: text}})
	}
	for i, path := range oddPaths {
		p.Threads = append(p.Threads, Thread{ID: fmt.Sprintf("PRRT_P%d", i), Path: path, Line: 1,
			Comments: []Comment{{Author: "alice", Type: "User", Body: "x"}}})
	}
	return p
}

// Org itself, run on the corpus's records, finds only the syntax the
// renderer writes: the payloads stay text.
func TestInert(t *testing.T) {
	emacs, err := exec.LookPath("emacs")
	if err != nil {
		t.Skip("emacs is not on PATH, so Org's own check of the rendered corpus did not run")
	}
	type check struct {
		render       func() ([]byte, error)
		items, links int
	}
	// ofRange expects a list item per hunk, and a link per hunk and per
	// finding with a line whose path a link holds.
	ofRange := func(r *Range) check {
		c := check{render: r.Render}
		for _, ch := range r.Walkthrough {
			for _, h := range ch.Hunks {
				c.items++
				if validPath(h.Path) {
					c.links++
				}
			}
		}
		for _, f := range r.Findings {
			if validPath(f.Path) && f.Line > 0 {
				c.links++
			}
		}
		return c
	}
	for name, tc := range map[string]check{
		"corpus range": ofRange(corpusRange()),
		"corpus pr":    {render: corpusPR().Render},
		"golden range": ofRange(fixtureRange()),
		"golden pr":    {render: fixturePR().Render},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := tc.render()
			if err != nil {
				t.Fatal(err)
			}
			blocks := 0
			for _, l := range strings.Split(string(data), "\n") {
				if l == "#+begin_example" {
					blocks++
				}
			}
			file := filepath.Join(t.TempDir(), name+".org")
			if err := os.WriteFile(file, data, 0o600); err != nil {
				t.Fatal(err)
			}
			script, err := filepath.Abs(filepath.Join("testdata", "inert.el"))
			if err != nil {
				t.Fatal(err)
			}
			// -Q with an init directory of its own keeps the operator's
			// init and state out.
			cmd := exec.Command(emacs, "-Q", "--batch", "--init-directory="+t.TempDir(), "-l", script, file,
				strconv.Itoa(blocks), strconv.Itoa(tc.items), strconv.Itoa(tc.links))
			cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), "inert: ok") {
				t.Errorf("inert.el: %v\n%s", err, out)
			}
		})
	}
}
