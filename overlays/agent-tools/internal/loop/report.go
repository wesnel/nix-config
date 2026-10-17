package loop

import (
	"fmt"
	"strings"
)

// Report is the part of report.md the loop's data can give, as Markdown,
// per PR: whether the loop is done with it, CI, changed lines, the commits
// the loop added, each bot's standing, and every review thread, resolved or
// not, with its URL. It only reads.
func Report(numbers []int) (string, error) {
	slug, numbers, state, err := loopNumbers(numbers)
	if err != nil {
		return "", err
	}
	bots, err := botsFor(slug)
	if err != nil {
		return "", err
	}
	statuses, err := Status(numbers)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, entry := range statuses.([]map[string]any) {
		n := entry["pr"].(int)
		branch := entry["branch"].(string)
		fmt.Fprintf(&b, "## #%d (%s)\n\n", n, branch)
		if waiting, _ := entry["waitingOn"].([]string); len(waiting) == 0 {
			b.WriteString("- Done\n")
		} else {
			fmt.Fprintf(&b, "- Waiting on: %s\n", strings.Join(waiting, "; "))
		}
		if paused, _ := entry["paused"].(bool); paused {
			fmt.Fprintf(&b, "- Paused: %s\n", entry["pausedBecause"])
		}
		ci := entry["ci"].(*CI)
		fmt.Fprintf(&b, "- CI: %s", ci.State)
		for i, c := range ci.Failed {
			sep := ", "
			if i == 0 {
				sep = "; failing: "
			}
			fmt.Fprintf(&b, "%s%s (on the base: %s)", sep, c.Name, c.OnBase)
		}
		b.WriteString("\n")
		if lines, ok := entry["changedLines"].(map[string]int); ok {
			fmt.Fprintf(&b, "- Changed lines: %d when the loop began, %d now\n", lines["baseline"], lines["now"])
		}
		if state != nil && state.Branches[branch] != nil {
			own := len(state.Branches[branch].Commits)
			if parent, err := state.parentRef(&Repo{Slug: state.Repo}, branch); err == nil {
				if commits, err := commitsBetween(parent, "refs/heads/"+branch); err == nil {
					added := len(commits) - own
					plural := "s"
					if added == 1 {
						plural = ""
					}
					fmt.Fprintf(&b, "- Commits: %d of the PR's own, %d fixup%s added\n", own, added, plural)
				}
			}
		}
		rewrites, _ := entry["needsOperator"].([]string)
		for _, r := range rewrites {
			fmt.Fprintf(&b, "- Needs the operator: %s\n", r)
		}

		b.WriteString("\n| Bot | Reviewed the head | Verdict | Open threads | Awaiting an answer |\n|---|---|---|---|---|\n")
		review := entry["bots"].(map[string]*BotReview)
		for _, name := range botNames(&Review{Bots: review}) {
			r := review[name]
			reviewed := "no"
			if r.ReviewedHead {
				reviewed = "yes"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %d |\n", cell(name), reviewed, cell(r.Verdict), r.OpenThreads, r.UnansweredThreads)
		}

		found, err := Threads(n, true)
		if err != nil {
			return "", err
		}
		threads := found.([]Thread)
		if len(threads) > 0 {
			b.WriteString("\n| Thread | Bot | Where | State | Link |\n|---|---|---|---|---|\n")
		}
		for _, t := range threads {
			where := t.Path
			if t.Line != nil {
				where += fmt.Sprintf(":%d", *t.Line)
			}
			if t.Outdated {
				where += " (outdated)"
			}
			link := ""
			if len(t.Comments) > 0 {
				link = t.Comments[0].URL
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", cell(t.ID), cell(t.Reviewer), cell(where), threadState(t, bots), cell(link))
		}
		if state != nil {
			writeProposals(&b, state, n)
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

// writeProposals writes the table of the proposals the loop made on PR n,
// if any, each awaiting the operator's decision.
func writeProposals(b *strings.Builder, state *State, n int) {
	header := false
	for _, p := range state.Proposals {
		if p.PR != n {
			continue
		}
		if !header {
			b.WriteString("\nProposals for the operator to decide:\n\n| # | Kind | Summary | Threads | Fixups | Changed lines |\n|---|---|---|---|---|---|\n")
			header = true
		}
		var fixups []string
		for _, f := range p.Fixups {
			fixups = append(fixups, f.Oid[:min(12, len(f.Oid))]+" "+f.Subject)
		}
		size := ""
		if p.Size > 0 {
			size = fmt.Sprint(p.Size)
		}
		fmt.Fprintf(b, "| %d | %s | %s | %s | %s | %s |\n", p.ID, p.Kind, cell(p.Summary), cell(strings.Join(p.Threads, " ")),
			cell(strings.Join(fixups, "; ")), size)
	}
}

// threadState says where a thread stands: resolved; open and the
// operator's, when a person started it; or open with the bot or the loop
// having the last word.
func threadState(t Thread, bots []*Bot) string {
	switch {
	case t.Resolved:
		return "resolved"
	case t.Reviewer == "human":
		return "open, the operator's"
	case len(t.Comments) > 0 && botNamed(bots, t.Comments[len(t.Comments)-1].Author) != nil:
		return "open, the bot has the last word"
	}
	return "open, answered"
}

// cell is s fit for one Markdown table cell.
func cell(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), "|", `\|`)
}
