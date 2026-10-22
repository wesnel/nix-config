package plans

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ResumeLimit is the most characters a SessionStart hook's context keeps
// whole; Claude Code puts a longer one in a file and passes a preview.
// Briefs are measured in bytes, which are never fewer than characters.
const ResumeLimit = 10000

// checkpointKinds are the events that end a stretch of work: a hand-back
// to the operator, and a sign-pass handed over or signed.
var checkpointKinds = []string{"handed-back", signRequested, "signed"}

// lastCheckpoint is the index in events of the newest checkpoint event, or
// -1.
func lastCheckpoint(events []Event) int {
	for i := len(events) - 1; i >= 0; i-- {
		if slices.Contains(checkpointKinds, events[i].Kind) {
			return i
		}
	}
	return -1
}

// ResumeBrief is what a session needs from p to go on after /clear or a
// compaction, at most limit bytes: the goal, the questions the operator
// can answer now, the steps under way and ready, what waits on what, the
// branches and pull requests, any structure drift, the decisions since
// the last checkpoint spelled out, newest kept first, and the IDs of the
// rest with the commands that show them. Every section is clipped to its share, so a
// plan of any size fits.
func (p Plan) ResumeBrief(now time.Time, limit int) (string, error) {
	s, err := p.StatusAt(now)
	if err != nil {
		return "", err
	}
	events, err := p.Events()
	if err != nil {
		return "", err
	}
	ds, err := p.Decisions(true)
	if err != nil {
		return "", err
	}
	qs, err := p.Questions(true)
	if err != nil {
		return "", err
	}
	items, err := p.items()
	if err != nil {
		return "", err
	}
	props := items[p.Name].Props
	goal, err := p.Goal("")
	if err != nil {
		goal = "none written\n"
	}

	var head strings.Builder
	fmt.Fprintf(&head, "Resume brief of plan %s, %s (status %s). Read the plan's file for anything below that is cut short.\n",
		p.File(), s.Title, orNone(s.State))
	head.WriteString("Goal:\n" + clip(strings.TrimRight(goal, "\n"), limit/4) + "\n")

	askable := Askable(qs)
	head.WriteString("Open questions for the operator:")
	if len(askable) == 0 {
		head.WriteString(" none\n")
	} else {
		head.WriteString("\n")
		var lines []string
		for _, q := range askable {
			lines = append(lines, clip(strings.TrimSuffix(QuestionsOrg([]Question{q}), "\n"), 300))
		}
		head.WriteString(fitLines(lines, limit/5, func(rest []string) string {
			return fmt.Sprintf("- and %d more: `agent-plan questions %s --all`", len(rest), p.File())
		}))
	}

	started := map[string]bool{}
	for _, e := range events {
		if e.Kind == "started" {
			started[e.Item] = true
		}
	}
	var inProgress []string
	for _, st := range s.Steps {
		if started[st.ID] && !st.Done && !st.Abandoned {
			inProgress = append(inProgress, st.ID)
		}
	}
	ready := slices.DeleteFunc(slices.Clone(s.ReadySteps), func(id string) bool { return started[id] })
	var waiting []string
	for _, w := range s.Waiting {
		waiting = append(waiting, w.ID+" on "+strings.Join(w.On, ", "))
	}
	signed := map[string]bool{}
	for _, e := range events {
		if slices.Contains(signResultKinds, e.Kind) {
			signed[e.Sign] = true
		}
	}
	for _, e := range events {
		if e.Kind == signRequested && !signed[e.Sign] {
			on := "sign-pass " + e.Sign + " on the operator"
			if e.Branch != "" {
				on += " (" + e.Branch + ")"
			}
			waiting = append(waiting, on)
		}
	}
	for _, line := range []string{
		"In progress: " + list(inProgress),
		"Done, not verified: " + list(s.DoneNotVerified),
		"Steps ready: " + list(ready),
		"Parts ready to start: " + list(s.Ready),
		"Waiting: " + strings.Join(orNoneList(waiting), "; "),
		"Branches: " + orNone(props["BRANCHES"]),
		"Pull requests: " + orNone(props["PULL_REQUESTS"]),
		fmt.Sprintf("Follow-ups open: %d", len(s.Followups)),
		s.structureLine(),
	} {
		if line != "" {
			head.WriteString(clip(line, 600) + "\n")
		}
	}

	// Decisions: those decided after the last checkpoint, by their decided
	// events; with none logged, every decision is recent.
	cp := lastCheckpoint(events)
	decidedAt := map[string]int{}
	for i, e := range events {
		if e.Kind == "decided" {
			decidedAt[e.Item] = i
		}
	}
	var recent, older []Decision
	for _, d := range ds {
		if i, ok := decidedAt[d.ID]; cp < 0 || (ok && i > cp) {
			recent = append(recent, d)
		} else {
			older = append(older, d)
		}
	}
	var tail strings.Builder
	if cp >= 0 {
		what := events[cp].Kind
		if events[cp].Summary != "" {
			what += ": " + events[cp].Summary
		}
		fmt.Fprintf(&tail, "Decided since the last checkpoint (%s):\n", clip(what, 200))
	} else {
		tail.WriteString("Decided:\n")
	}
	if len(recent) == 0 {
		tail.WriteString("none\n")
	}
	pointer := func(cut []Decision) string {
		var b strings.Builder
		if len(cut) > 0 {
			fmt.Fprintf(&b, "Decided earlier, not shown: %s.\n", idRanges(decidedIDs(cut)))
		}
		if len(older) > 0 {
			fmt.Fprintf(&b, "Decided before it: %s; ", idRanges(decidedIDs(older)))
		}
		if len(older) > 0 || len(cut) > 0 {
			fmt.Fprintf(&b, "`agent-plan decisions %s` lists every decision.\n", p.File())
		}
		return b.String()
	}
	// Newest first into what is left, then printed in the file's order.
	budget := limit - head.Len() - tail.Len()
	keep := map[string]bool{}
	used := 0
	var cut []Decision
	for i := len(recent) - 1; i >= 0; i-- {
		d := recent[i]
		line := clip(fmt.Sprintf("- %s :: %s %s", d.ID, d.Question, d.Answer), 500) + "\n"
		rest := recent[:i]
		if used+len(line)+len(pointer(rest)) > budget {
			cut = append([]Decision{}, recent[:i+1]...)
			break
		}
		keep[d.ID] = true
		used += len(line)
	}
	for _, d := range recent {
		if keep[d.ID] {
			tail.WriteString(clip(fmt.Sprintf("- %s :: %s %s", d.ID, d.Question, d.Answer), 500) + "\n")
		}
	}
	tail.WriteString(pointer(cut))
	out := head.String() + tail.String()
	if len(out) > limit {
		out = clip(out, limit)
	}
	return out, nil
}

func orNoneList(xs []string) []string {
	if len(xs) == 0 {
		return []string{"none"}
	}
	return xs
}

func decidedIDs(ds []Decision) []string {
	var out []string
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

// clip is s cut to at most n bytes at a character boundary, ending in an
// ellipsis where it was cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	const mark = "…"
	cut := n - len(mark)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut < 0 {
		cut = 0
	}
	return s[:cut] + mark
}

// fitLines is lines, one per line, within n bytes; where they do not all
// fit, the last line is more(rest) for those left out.
func fitLines(lines []string, n int, more func(rest []string) string) string {
	var b strings.Builder
	for i, line := range lines {
		if b.Len()+len(line)+1+len(more(lines[i+1:]))+1 > n && i < len(lines)-1 || b.Len()+len(line)+1 > n {
			b.WriteString(more(lines[i:]) + "\n")
			return b.String()
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

var trailingNumber = regexp.MustCompile(`^(.*?)([0-9]+)$`)

// idRanges is ids with runs of consecutive numbers under one prefix
// written as ranges: Q1, Q2, Q3, Q5 as "Q1–Q3, Q5".
func idRanges(ids []string) string {
	var out []string
	for i := 0; i < len(ids); {
		m := trailingNumber.FindStringSubmatch(ids[i])
		j := i + 1
		if m != nil {
			n, _ := strconv.Atoi(m[2])
			for j < len(ids) {
				next := trailingNumber.FindStringSubmatch(ids[j])
				if next == nil || next[1] != m[1] {
					break
				}
				if k, _ := strconv.Atoi(next[2]); k != n+(j-i) {
					break
				}
				j++
			}
		}
		if j-i > 2 {
			out = append(out, ids[i]+"–"+ids[j-1])
		} else {
			out = append(out, ids[i:j]...)
		}
		i = j
	}
	return strings.Join(out, ", ")
}
