package plans

import (
	"os"
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
	err := editPlanProperty(p.file(part), name, func(values []string) []string {
		if slices.Contains(values, value) {
			return values
		}
		added = true
		return append(values, value)
	})
	return added, err
}

// editPlanProperty sets the list property name of file's `* Plan` heading
// to what edit makes of its values, adding the property at the end of the
// drawer if it has none.
func editPlanProperty(file, name string, edit func(values []string) []string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	start := slices.Index(lines, "* Plan")
	if start < 0 {
		return refusal.New("%s has no `* Plan` heading. Restore it as the plan-work skill describes, then try again", file)
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
		return refusal.New("%s's `* Plan` heading has no property drawer. Restore it as the plan-work skill describes, then try again", file)
	}
	line := ":" + name + ":"
	if values = edit(values); len(values) > 0 {
		line += " " + strings.Join(values, " ")
	}
	if at >= 0 {
		if lines[at] == line {
			return nil
		}
		lines[at] = line
	} else {
		lines = append(lines[:end], append([]string{line}, lines[end:]...)...)
	}
	return replaceFile(file, strings.Join(lines, "\n"))
}
