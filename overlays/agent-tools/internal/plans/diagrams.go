package plans

import (
	"fmt"
	"strings"
	"time"
)

// critiquedKind is the kind of the event a diagram's critique logs. It is
// never a receipt: a diagram is an aid to reading the change, and owes no
// route.
const critiquedKind = "diagram-critiqued"

// DiagramFocus is the Focus of the prompt issued for a diagram's critique.
const DiagramFocus = "diagram"

// Critique is a diagram's critique by a model family other than the one
// that drew it: the diagram's hash, the range and tip it read, the nonce
// of the prompt a sub-agent's reply answered, and the IDs of its findings.
type Critique struct {
	Diagram, Range, Tip, Nonce, Model string
	Drafter, Critic                   string
	Findings                          []string
}

// WriteCritique logs c in the plan LocateCheckout finds for session and
// the worktree at top, and returns that plan's file, or "" where there is
// none.
func WriteCritique(session, top string, c Critique, now time.Time) (string, error) {
	cfg, err := Load()
	if err != nil {
		return "", err
	}
	p, part, ok := cfg.LocateCheckout(session, top)
	if !ok {
		return "", nil
	}
	if _, err := p.Record(part, Event{Kind: critiquedKind, Diagram: c.Diagram, Range: c.Range, Tip: c.Tip, Nonce: c.Nonce,
		Model: c.Model, Drafter: c.Drafter, Critic: c.Critic, Findings: strings.Join(c.Findings, " "),
		Summary: fmt.Sprintf("diagram %.12s: %d findings by %s", c.Diagram, len(c.Findings), c.Critic)}, now); err != nil {
		return "", err
	}
	return p.File(), nil
}

// CheckoutItems are the item IDs of the plan LocateCheckout finds for
// session and the worktree at top, its parts' too, or nil where there is
// none: what a planned element of a diagram may name.
func CheckoutItems(session, top string) map[string]bool {
	c, err := Load()
	if err != nil {
		return nil
	}
	p, _, ok := c.LocateCheckout(session, top)
	if !ok {
		return nil
	}
	return p.itemIDs()
}

// itemIDs are p's item IDs, or nil where its files cannot be read.
func (p Plan) itemIDs() map[string]bool {
	items, err := p.items()
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for id := range items {
		out[id] = true
	}
	return out
}
