package diagram

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"testing"
)

var tree = map[string]string{
	"queue/queue.go": `package queue

type State string

const (
	Queued State = "queued"
	Ready  State = "ready"
)

type Queue struct{ entries []*Entry }

func (q *Queue) Next() *Entry { return q.entries[0] }
`,
	"queue/run.go": `package queue

func Run(e *Entry) {
	if review(e) == 0 {
		e.State = Ready
	}
	notify(e)
}

func review(e *Entry) int { return 0 }

func notify(e *Entry) { _ = Entry{State: Queued} }

func isReady(s State) bool { return s == Ready }
`,
	"queue/methods.go": `package queue

type A struct{}

func (A) Run() { helper() }

type B struct{}

func (B) Run() { helper() }

func helper() {}
`,
	"queue/broken.go": "package queue\n\nfunc (\n",
	"module.nix":      "{\n  services.queue.enable = true;\n}\n",
}

func blob(path string) ([]byte, error) {
	if path == "big.txt" {
		return []byte("x\n" + strings.Repeat("y", MaxBlob)), nil
	}
	if s, ok := tree[path]; ok {
		return []byte(s), nil
	}
	return nil, errors.New("no such file")
}

// diagramOf is a diagram of kind whose elements are given as
// "ID[:FROM>TO] PATH:LINE[-END] SYMBOL" or "ID[:FROM>TO] planned ITEM".
func diagramOf(t *testing.T, kind string, elements ...string) *Diagram {
	t.Helper()
	d := &Diagram{Kind: kind, Title: "t", At: at}
	for _, el := range elements {
		f := strings.Fields(el)
		id, ends, _ := strings.Cut(f[0], ":")
		var c *Cite
		var p *string
		if f[1] == "planned" {
			p = &f[2]
		} else {
			path, lines, _ := strings.Cut(f[1], ":")
			start, end, ranged := strings.Cut(lines, "-")
			c = &Cite{Path: path, Line: atoi(start), Symbol: f[2]}
			if ranged {
				e := atoi(end)
				c.EndLine = &e
			}
		}
		if from, to, ok := strings.Cut(ends, ">"); ok {
			d.Edges = append(d.Edges, Edge{ID: id, From: from, To: to, Label: id, Cite: c, Planned: p})
		} else {
			d.Nodes = append(d.Nodes, Node{ID: id, Label: id, Cite: c, Planned: p})
		}
	}
	if _, err := Parse(d.JSON()); err != nil {
		t.Fatal(err)
	}
	return d
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func failed(r Result) []string {
	var out []string
	for _, f := range r.Failures {
		out = append(out, f.Element)
	}
	return out
}

func TestCheck(t *testing.T) {
	plan := func(id string) bool { return id == "P1.S3" }
	for name, tc := range map[string]struct {
		kind     string
		elements []string
		failed   []string
		says     string
	}{
		"a state set": {kind: "state", elements: []string{"N1 queue/queue.go:6 Queued", "N2 queue/queue.go:7 Ready",
			"E1:N1>N2 queue/run.go:5 Ready", "E2:N2>N1 queue/run.go:12 Queued"}},
		"a call flow": {kind: "callflow", elements: []string{"N1 queue/run.go:3-8 Run", "N2 queue/run.go:10 review",
			"N3 queue/run.go:12 notify", "E1:N1>N2 queue/run.go:4 review", "E2:N1>N3 queue/run.go:7 notify"}},
		"a method": {kind: "component", elements: []string{"N1 queue/queue.go:12 Queue.Next", "N2 queue/queue.go:1 queue"}},
		"text":     {kind: "component", elements: []string{"N1 module.nix:2 enable"}},
		"planned":  {kind: "component", elements: []string{"N1 queue/run.go:3 Run", "N2 planned P1.S3", "E1:N1>N2 planned P1.S3"}},

		// Citations to the wrong span.
		"a symbol not in its span": {kind: "state", elements: []string{"N1 queue/queue.go:6 Ready"},
			failed: []string{"N1"}, says: "whole word"},
		"a symbol used, not declared": {kind: "state", elements: []string{"N1 queue/run.go:5 Ready"},
			failed: []string{"N1"}, says: "declare"},
		"a method of another type": {kind: "component", elements: []string{"N1 queue/queue.go:12 State.Next"},
			failed: []string{"N1"}, says: "declare"},
		"a transition that compares": {kind: "state", elements: []string{"N1 queue/queue.go:6 Queued",
			"N2 queue/queue.go:7 Ready", "E1:N1>N2 queue/run.go:14 Ready"}, failed: []string{"E1"}, says: "assign"},
		"a transition to another state": {kind: "state", elements: []string{"N1 queue/queue.go:6 Queued",
			"N2 queue/queue.go:7 Ready", "E1:N1>N2 queue/run.go:12 Queued"}, failed: []string{"E1"}, says: "N2"},
		"a call outside its caller": {kind: "callflow", elements: []string{"N1 queue/run.go:3-8 Run",
			"N3 queue/run.go:12 notify", "E1:N1>N3 queue/run.go:12 notify"}, failed: []string{"E1"}, says: "inside"},
		"a call never made": {kind: "callflow", elements: []string{"N1 queue/run.go:3-8 Run",
			"N2 queue/run.go:10 review", "E1:N1>N2 queue/run.go:5-6 review"}, failed: []string{"E1"}, says: "whole word"},
		"a callee named but not called": {kind: "callflow", elements: []string{"N1 queue/run.go:3-8 Run",
			"N2 queue/run.go:10 review", "E1:N1>N2 queue/run.go:3 Run"}, failed: []string{"E1"}, says: "N2"},
		"a caller in another file": {kind: "callflow", elements: []string{"N1 queue/queue.go:12 Queue.Next",
			"N2 queue/run.go:10 review", "E1:N1>N2 queue/run.go:4 review"}, failed: []string{"E1"}, says: "queue/queue.go"},
		"a missing file": {kind: "component", elements: []string{"N1 queue/gone.go:1 queue"},
			failed: []string{"N1"}, says: "queue/gone.go"},
		"a line past the end": {kind: "component", elements: []string{"N1 queue/queue.go:40 queue"},
			failed: []string{"N1"}, says: "lines"},
		"Go that does not parse": {kind: "component", elements: []string{"N1 queue/broken.go:1 queue"},
			failed: []string{"N1"}, says: "parse"},
		"text without the symbol": {kind: "component", elements: []string{"N1 module.nix:2 disable"},
			failed: []string{"N1"}, says: "whole word"},
		"an item the plan lacks": {kind: "component", elements: []string{"N1 planned P1.S9"},
			failed: []string{"N1"}, says: "P1.S9"},
		"a method named as a function": {kind: "component", elements: []string{"N1 queue/methods.go:5 Run"},
			failed: []string{"N1"}, says: "declare"},
		"a call from another type's method": {kind: "callflow", elements: []string{"N1 queue/methods.go:5 A.Run",
			"N2 queue/methods.go:11 helper", "E1:N1>N2 queue/methods.go:9 helper"}, failed: []string{"E1"}, says: "inside"},
		"a call from its method": {kind: "callflow", elements: []string{"N1 queue/methods.go:9 B.Run",
			"N2 queue/methods.go:11 helper", "E1:N1>N2 queue/methods.go:9 helper"}},
		"a file past the size cap": {kind: "component", elements: []string{"N1 big.txt:1 x"},
			failed: []string{"N1"}, says: "bytes"},
	} {
		t.Run(name, func(t *testing.T) {
			d := diagramOf(t, tc.kind, tc.elements...)
			r := Check(d, blob, plan)
			if got := failed(r); !slices.Equal(got, tc.failed) {
				t.Fatalf("failed %v, want %v: %+v", got, tc.failed, r.Failures)
			}
			if tc.says != "" && !strings.Contains(r.Failures[0].Reason, tc.says) {
				t.Errorf("reason %q does not say %q", r.Failures[0].Reason, tc.says)
			}
		})
	}
}

// Check sets each citation's hash, the lines it covers trimmed, and says
// what it checked of each element for the critic.
func TestCheckHashes(t *testing.T) {
	d := diagramOf(t, "callflow", "N1 queue/run.go:3-8 Run", "N2 queue/run.go:10 review", "E1:N1>N2 queue/run.go:4 review")
	r := Check(d, blob, nil)
	if len(r.Failures) != 0 {
		t.Fatal(r.Failures)
	}
	lines := strings.Split(tree["queue/run.go"], "\n")[2:8]
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	if d.Nodes[0].Cite.Hash != hex.EncodeToString(sum[:]) {
		t.Errorf("hash %s", d.Nodes[0].Cite.Hash)
	}
	if !strings.Contains(r.Checked["N1"], "declared") || !strings.Contains(r.Checked["E1"], "calls review") {
		t.Errorf("checked %v", r.Checked)
	}
}

// A call from a planned function is checked as text alone, and says so.
func TestCheckCallFromPlanned(t *testing.T) {
	d := diagramOf(t, "callflow", "N1 planned S1", "N2 queue/run.go:10 review", "E1:N1>N2 queue/run.go:4 review")
	r := Check(d, blob, func(string) bool { return true })
	if len(r.Failures) != 0 || r.Checked["E1"] != "review is a word in queue/run.go:4-4; its caller is planned, so the call itself is not checked" {
		t.Errorf("%+v", r)
	}
}

// Without a plan's items, planned elements are refused.
func TestCheckPlannedWithoutPlan(t *testing.T) {
	d := diagramOf(t, "component", "N1 planned P1.S3")
	if got := failed(Check(d, blob, nil)); !slices.Equal(got, []string{"N1"}) {
		t.Errorf("failed %v", got)
	}
}
