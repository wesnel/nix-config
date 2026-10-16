package plans

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
)

// StopCheck is the Stop hook of Claude Code and Codex, and of ECA through
// StopCheckChat: before the agent stops, it blocks the stop once, with the
// reason, when the work is not in the state the plan-work and review-change
// skills leave it in. A session answers only for its own plan and its own
// commits, as `pr-loop guard --post` records them, never for another
// session's work in the same worktree; a session that committed has a
// plan, since that records its first commit in a plan made for it where
// none was. It reads local files and git alone, never the network, and
// returns nothing to let the stop through, on any error too.
func (c Config) StopCheck(input []byte, now time.Time) []byte {
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Active    bool   `json:"stop_hook_active"`
	}
	if json.Unmarshal(input, &in) != nil || in.Active {
		return nil
	}
	if _, reason := c.stopReason(in.SessionID, in.Cwd); reason != "" {
		out, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
		return out
	}
	return nil
}

// stopReason is the plan session answers for, as it stops in cwd, and why
// the stop should be blocked, or "".
func (c Config) stopReason(session, cwd string) (Plan, string) {
	top, _ := checkout(cwd)
	p, found := c.sessionPlan(session, top)
	if !found {
		return Plan{}, ""
	}
	items, err := p.items()
	if err != nil {
		return p, ""
	}
	props := items[p.Name].Props
	events, err := p.Events()
	if err != nil {
		return p, ""
	}
	state, pipeline := props["STATUS"], p.pipeline(events)
	if state == "halted" || state == "planning" || strings.HasPrefix(pipeline, "opted-out") {
		return p, ""
	}
	status, err := p.Status()
	if err != nil {
		return p, ""
	}
	if state == "working" && len(status.DoneNotVerified) > 0 {
		return p, fmt.Sprintf("Plan %s has steps done but not verified: %s. Verify each and log it with "+
			"`agent-plan log PLAN --item ID --kind verified --evidence '...'`, or say in your report why it cannot be.",
			p.File(), strings.Join(status.DoneNotVerified, ", "))
	}
	if pipeline != defaultPipeline {
		return p, ""
	}
	committed := slices.ContainsFunc(events, func(e Event) bool { return e.Kind == "committed" && e.Session == session })
	ref := defaultRef(top)
	planBase := branchBase(top, props["BRANCHES"])
	if !committed || reviewed(top, ref, planBase, events) {
		return p, ""
	}
	base := planBase
	if base == "" {
		base = ref
	}
	if base == "" {
		base = "BASE"
	}
	return p, fmt.Sprintf("Plan %s runs the full review pipeline, this session committed to it, and no review receipt matches the change %s..HEAD as it stands. "+
		"Review it with `git fresh-review --goal FILE %s..HEAD`, as the review-change skill says, which logs the receipt; or say in your report why it cannot be reviewed.",
		p.File(), base, base)
}

// branchBase is the base the plan's BRANCHES give the branch checked out
// at top, from its `base..branch` entry, or "".
func branchBase(top, branches string) string {
	if top == "" {
		return ""
	}
	current, err := git.Run("-C", top, "branch", "--show-current")
	if err != nil || current == "" {
		return ""
	}
	for _, v := range strings.Fields(branches) {
		if base, branch, ok := strings.Cut(v, ".."); ok && branch == current && base != "" {
			return base
		}
	}
	return ""
}

// reviewed reports whether a review receipt among events covers the
// change of the worktree at top: whether, for some receipt whose range
// starts at or below planBase or ref, the patch ID of its range's base to
// HEAD is the one it recorded. A reworded, re-signed or rebased change
// keeps it; a review of part of the change does not count. ref is the
// default branch's remote-tracking ref, and planBase the base the plan
// gives the branch; either is "" where there is none, and with neither any
// range counts. With no change ahead of ref there is nothing to review;
// outside a worktree git cannot tell, and nothing is owed.
func reviewed(top, ref, planBase string, events []Event) bool {
	if top == "" {
		return true
	}
	repo := []string{"-C", top}
	if ref != "" {
		if id, err := git.PatchID(repo, ref, "HEAD"); err != nil || id == "" {
			return true
		}
	}
	var bases []string
	for _, b := range []string{planBase, ref} {
		if b != "" {
			bases = append(bases, b)
		}
	}
	covers := func(base string) bool {
		if len(bases) == 0 {
			return true
		}
		return slices.ContainsFunc(bases, func(b string) bool {
			return git.OK("-C", top, "merge-base", "--is-ancestor", base, b)
		})
	}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		base, _, ok := strings.Cut(e.Range, "..")
		if e.Kind != receiptKind || !ok || e.PatchID == "" || !covers(base) {
			continue
		}
		if id, err := git.PatchID(repo, base, "HEAD"); err == nil && id == e.PatchID {
			return true
		}
	}
	return false
}

// stopBlocked is the kind of the event StopCheckChat logs for a stop it
// blocks.
const stopBlocked = "stop-blocked"

// StopCheckChat is StopCheck for ECA's postRequest hook, whose input names
// the chat in chat_id and has no stop_hook_active. It returns the reason the
// stop should be followed up, which the hook prints to stderr with exit
// code 2, or "" to let it through. It blocks once: a block is logged as a
// stop-blocked event of the chat in its plan, and while the log holds
// nothing after the chat's last one but other stops blocked, the chat's
// stops pass. Without a chat id, or a log it can append to, it lets the
// stop through.
func (c Config) StopCheckChat(input []byte, now time.Time) string {
	var in struct {
		ChatID string `json:"chat_id"`
		Cwd    string `json:"cwd"`
	}
	if json.Unmarshal(input, &in) != nil || in.ChatID == "" {
		return ""
	}
	p, reason := c.stopReason(in.ChatID, in.Cwd)
	if reason == "" {
		return ""
	}
	blocked := false
	p.withLock(func(l *lockedPlan) error {
		events, err := p.Events()
		if err != nil {
			return err
		}
		for i := len(events) - 1; i >= 0 && events[i].Kind == stopBlocked; i-- {
			if events[i].Session == in.ChatID {
				return nil
			}
		}
		summary, _, _ := strings.Cut(reason, ". ")
		_, err = l.log(Event{Kind: stopBlocked, Item: p.Name, Session: in.ChatID, Summary: summary}, now)
		blocked = err == nil
		return err
	})
	if !blocked {
		return ""
	}
	return reason
}

// sessionPlan is the plan session answers for: of this host's plans that
// name the session or the worktree at top, the first whose log holds a
// commit of the session's, in Locate's order, since Locate is what put the
// session's commits and receipts there; else the first that names the
// session.
func (c Config) sessionPlan(session, top string) (Plan, bool) {
	if session == "" {
		return Plan{}, false
	}
	found, err := c.Find(FindOptions{Session: session, Worktree: top})
	if err != nil {
		return Plan{}, false
	}
	// Locate's order: where the session's commits and receipts went.
	slices.SortStableFunc(found, func(a, b Found) int {
		return boolOrder(slices.Contains(b.Matched, "worktree")) - boolOrder(slices.Contains(a.Matched, "worktree"))
	})
	for _, f := range found {
		p, err := c.Open(f.Plan)
		if err != nil {
			continue
		}
		events, err := p.Events()
		if err != nil {
			continue
		}
		for _, e := range events {
			if e.Kind == "committed" && e.Session == session {
				return p, true
			}
		}
	}
	for _, f := range found {
		if slices.Contains(f.Matched, "session") {
			p, err := c.Open(f.Plan)
			return p, err == nil
		}
	}
	return Plan{}, false
}

func boolOrder(b bool) int {
	if b {
		return 1
	}
	return 0
}

// defaultRef is the default branch's remote-tracking ref of the worktree
// at top, or "".
func defaultRef(top string) string {
	if top == "" {
		return ""
	}
	if ref, err := git.Run("-C", top, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil {
		return ref
	}
	for _, r := range []string{"origin/main", "origin/master"} {
		if git.OK("-C", top, "rev-parse", "--verify", "-q", r) {
			return r
		}
	}
	return ""
}
