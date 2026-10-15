package plans

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// Locate is the plan file that what a session did in a worktree belongs
// to: of this host's plan files that name the worktree, the newest, else
// of those that name the session, the newest. part is the part whose file
// it is, or "" for the plan's own.
func (c Config) Locate(session, worktree string) (p Plan, part string, ok bool) {
	if session == "" && worktree == "" {
		return Plan{}, "", false
	}
	found, err := c.Find(FindOptions{Session: session, Worktree: worktree})
	if err != nil || len(found) == 0 {
		return Plan{}, "", false
	}
	pick := found[0]
	for _, f := range found {
		if slices.Contains(f.Matched, "worktree") {
			pick = f
			break
		}
	}
	p, err = c.Open(pick.Plan)
	return p, pick.Part, err == nil
}

// Receipt is what a review that ran leaves in the plan's log, for
// StopCheck to compare with the change as it then stands.
type Receipt struct {
	// Range is the range reviewed as it was given, BASE..TIP, so that
	// its base is read again at the stop, wherever it has moved.
	Range string
	// Tip is the commit TIP named.
	Tip string
	// PatchID is git.PatchID of the range's change.
	PatchID string
	// Reviewer is codex or subagent, and Model the model, where known.
	Reviewer, Model string
	Findings        int
}

// receiptKind is the kind of the event Receipt logs, which Log refuses
// from anyone else.
const receiptKind = "review-receipt"

// Receipt logs r about part, or about the plan itself when part is "".
func (p Plan) Receipt(part string, r Receipt, now time.Time) (Event, error) {
	return p.Record(part, Event{Kind: receiptKind, Range: r.Range, Tip: r.Tip, PatchID: r.PatchID, Agent: r.Reviewer,
		Model: r.Model, Summary: fmt.Sprintf("%s: %d findings", r.Range, r.Findings), receipt: true}, now)
}

// WriteReceipt logs r in the plan Locate finds for session and the
// worktree at top, and returns that plan's file, or "" where no plan names
// either.
func WriteReceipt(session, top string, r Receipt, now time.Time) (string, error) {
	c, err := Load()
	if err != nil {
		return "", err
	}
	p, part, ok := c.Locate(session, top)
	if !ok {
		return "", nil
	}
	if _, err := p.Receipt(part, r, now); err != nil {
		return "", err
	}
	return p.File(), nil
}

// Record logs e about part, or about the plan itself when part is "".
func (p Plan) Record(part string, e Event, now time.Time) (Event, error) {
	e.Item = part
	if part == "" {
		e.Item = p.Name
	}
	return p.Log(e, now)
}

// AddToProperty adds value to the list property name of the `* Plan`
// heading in the plan's file, or the part's, unless it is there, and
// reports whether it added it.
func (p Plan) AddToProperty(part, name, value string) (bool, error) {
	if err := p.checkOwn(); err != nil {
		return false, err
	}
	if err := checkWords(strings.ToLower(name), []string{value}); err != nil {
		return false, err
	}
	added := false
	err := p.editPlanProperty(p.file(part), name, func(values []string) []string {
		added = !slices.Contains(values, value)
		if !added {
			return values
		}
		return append(values, value)
	})
	return added, err
}

// editPlanProperty sets the list property name of file's `* Plan` heading
// to what edit makes of its values, adding the property at the end of the
// drawer if it has none. edit may run more than once, each time on the
// values the file then holds.
func (p Plan) editPlanProperty(file, name string, edit func(values []string) []string) error {
	return p.withLock(func(l *lockedPlan) error {
		return l.edit(file, func(lines []string) ([]string, error) {
			lines = slices.Clone(lines)
			start := slices.Index(lines, "* Plan")
			if start < 0 {
				return nil, refusal.New("%s has no `* Plan` heading. Restore it as the plan-work skill describes, then try again", file)
			}
			at, end := -1, -1
			var values []string
			for i := start + 1; i < len(lines) && end < 0; i++ {
				switch {
				case lines[i] == ":END:":
					end = i
				case strings.HasPrefix(lines[i], ":"+name+":"):
					at = i
					values = strings.Fields(strings.TrimPrefix(lines[i], ":"+name+":"))
				}
			}
			if end < 0 {
				return nil, refusal.New("%s's `* Plan` heading has no property drawer. Restore it as the plan-work skill describes, then try again", file)
			}
			line := ":" + name + ":"
			if values = edit(values); len(values) > 0 {
				line += " " + strings.Join(values, " ")
			}
			if at >= 0 {
				lines[at] = line
				return lines, nil
			}
			return slices.Insert(lines, end, line), nil
		})
	})
}
