package plans

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// Postpone schedules step or part id for until, a day as 2006-01-02 or a
// time as 2006-01-02 15:04, by its heading's SCHEDULED timestamp, which
// keeps it out of the ready lists and Emacs's agenda until then; an empty
// until clears it. It logs a postponed event.
func (p Plan) Postpone(id, until string, now time.Time) (Event, error) {
	if err := p.checkOwn(); err != nil {
		return Event{}, err
	}
	if !projected(id) {
		return Event{}, refusal.New("%q is not a step or a part, the items a schedule keeps waiting. Pass an ID such as S1 or P1 that `agent-plan status %s` lists", id, p.file(""))
	}
	stamp := ""
	if until != "" {
		var err error
		if stamp, err = orgStamp(until); err != nil {
			return Event{}, err
		}
	}
	var e Event
	err := p.withLock(func(l *lockedPlan) error {
		items, err := p.items()
		if err != nil {
			return err
		}
		it, ok := items[id]
		if !ok {
			return refusal.New("%q is not an item of %s. Use an ID `agent-plan status %s` lists", id, p.Name, p.file(""))
		}
		err = l.edit(it.File, func(lines []string) ([]string, error) {
			at := headingWithID(lines, id)
			if at < 0 {
				return nil, fmt.Errorf("%s is no longer in %s", id, it.File)
			}
			return setScheduled(lines, at, stamp), nil
		})
		if err != nil {
			return err
		}
		summary := "until " + stamp
		if stamp == "" {
			summary = "cleared"
		}
		e, err = l.log(Event{Kind: "postponed", Item: id, Summary: summary}, now)
		return err
	})
	return e, err
}

// orgStamp is until as an Org active timestamp.
func orgStamp(until string) (string, error) {
	if t, err := time.ParseInLocation("2006-01-02 15:04", until, time.Local); err == nil {
		return t.Format("<2006-01-02 Mon 15:04>"), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", until, time.Local); err == nil {
		return t.Format("<2006-01-02 Mon>"), nil
	}
	return "", refusal.New("%q is not a day or a time. Pass `--until 2026-10-20` or `--until '2026-10-20 18:30'`, or `--clear`", until)
}

var scheduledPart = regexp.MustCompile(`\s*SCHEDULED:\s*<[^>]*>`)

// setScheduled sets the SCHEDULED timestamp of the heading at lines[at] to
// stamp, or removes it when stamp is empty, keeping the rest of its
// planning line.
func setScheduled(lines []string, at int, stamp string) []string {
	out := slices.Clone(lines)
	next := at + 1
	planning := next < len(out) && planningLine.MatchString(out[next])
	switch {
	case planning:
		rest := strings.TrimRight(scheduledPart.ReplaceAllString(out[next], ""), " \t")
		if stamp != "" {
			rest = strings.TrimLeft(rest+" SCHEDULED: "+stamp, " ")
		}
		if strings.TrimSpace(rest) == "" {
			return slices.Delete(out, next, next+1)
		}
		out[next] = rest
	case stamp != "":
		out = slices.Insert(out, next, "SCHEDULED: "+stamp)
	}
	return out
}
