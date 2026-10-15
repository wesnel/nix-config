package plans

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
// session's work in the same worktree. It reads local files and git alone,
// never the network, and returns nothing to let the stop through, on any
// error too.
func (c Config) StopCheck(input []byte, now time.Time) []byte {
	var in struct {
		SessionID string `json:"session_id"`
		ChatID    string `json:"chat_id"`
		Cwd       string `json:"cwd"`
		Active    bool   `json:"stop_hook_active"`
	}
	if json.Unmarshal(input, &in) != nil || in.Active {
		return nil
	}
	session := in.SessionID
	if in.ChatID != "" {
		session = in.ChatID
	}
	top, _ := checkout(in.Cwd)
	unsigned, ref := unsignedAhead(top)
	block := func(reason string) []byte {
		out, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
		return out
	}
	p, found := c.sessionPlan(session, top)
	if !found {
		if unsigned == 0 || !committedUnplanned(session, top) {
			return nil
		}
		return block(fmt.Sprintf("This session committed in this worktree, which has %d unsigned commits ahead of %s, and no plan names this session or worktree. "+
			"Make one with `agent-plan new` as the plan-work skill says, or add this worktree to the plan the work belongs to, before stopping.",
			unsigned, ref))
	}
	items, err := p.items()
	if err != nil {
		return nil
	}
	props := items[p.Name].Props
	state, pipeline := props["STATUS"], props["PIPELINE"]
	if state == "halted" || state == "planning" || strings.HasPrefix(pipeline, "opted-out") {
		return nil
	}
	status, err := p.Status()
	if err != nil {
		return nil
	}
	if state == "working" && len(status.DoneNotVerified) > 0 {
		return block(fmt.Sprintf("Plan %s has steps done but not verified: %s. Verify each and log it with "+
			"`agent-plan log PLAN --item ID --kind verified --evidence '...'`, or say in your report why it cannot be.",
			p.File(), strings.Join(status.DoneNotVerified, ", ")))
	}
	if pipeline != "full" {
		return nil
	}
	events, err := p.Events()
	if err != nil {
		return nil
	}
	// A commit made before the plan was noted outside it.
	committed := committedUnplanned(session, top) ||
		slices.ContainsFunc(events, func(e Event) bool { return e.Kind == "committed" && e.Session == session })
	planBase := branchBase(top, props["BRANCHES"])
	if !committed || reviewed(top, ref, planBase, events) {
		return nil
	}
	base := planBase
	if base == "" {
		base = ref
	}
	if base == "" {
		base = "BASE"
	}
	return block(fmt.Sprintf("Plan %s runs the full review pipeline, this session committed to it, and no review receipt matches the change %s..HEAD as it stands. "+
		"Review it with `git fresh-review --goal FILE %s..HEAD`, as the review-change skill says, which logs the receipt; or say in your report why it cannot be reviewed.",
		p.File(), base, base))
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

// StopCheckChat is StopCheck for ECA's postRequest hook, whose input names
// the chat in chat_id and has no stop_hook_active. It returns the reason the
// stop should be followed up, which the hook prints to stderr with exit
// code 2, or "" to let it through. A file per chat in stateDir makes it
// block once: the stop after a block passes and clears the file, so the
// chat's next stop is checked again. Without a chat id, or a stateDir it
// can write, it lets the stop through.
func (c Config) StopCheckChat(input []byte, stateDir string, now time.Time) string {
	var in struct {
		ChatID string `json:"chat_id"`
	}
	if json.Unmarshal(input, &in) != nil || in.ChatID == "" {
		return ""
	}
	file := filepath.Join(stateDir, stateName(in.ChatID))
	if _, err := os.Stat(file); err == nil {
		os.Remove(file)
		return ""
	}
	var out struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(c.StopCheck(input, now), &out) != nil || out.Reason == "" {
		return ""
	}
	if os.MkdirAll(stateDir, 0o755) != nil || os.WriteFile(file, nil, 0o644) != nil {
		return ""
	}
	return out.Reason
}

// stateName is a session's or chat's ID as a file name: itself where it is
// a plain name, else its SHA-256 in hex.
func stateName(id string) string {
	if safeName.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// stateDir is agent-tools' directory under XDG_STATE_HOME.
func stateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(dir) {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "agent-tools")
}

// StopStateDir is where StopCheckChat keeps its files.
func StopStateDir() string { return filepath.Join(stateDir(), "stop") }

// NoteCommit records that session committed in the worktree at top while
// no plan named either, for StopCheck to ask that session for a plan. A
// file per session under the state directory's commits/ lists the
// worktrees, one to a line.
func NoteCommit(session, top string) error {
	if session == "" || top == "" || strings.ContainsAny(top, "\n\r") {
		return nil
	}
	top = realPath(top)
	if committedUnplanned(session, top) {
		return nil
	}
	dir := filepath.Join(stateDir(), "commits")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, stateName(session)), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(top + "\n")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// committedUnplanned reports whether NoteCommit recorded a commit of
// session in the worktree at top.
func committedUnplanned(session, top string) bool {
	if session == "" || top == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(stateDir(), "commits", stateName(session)))
	return err == nil && slices.Contains(strings.Split(string(data), "\n"), realPath(top))
}

// realPath is path with its symbolic links resolved, or path where they
// cannot be.
func realPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
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

// unsignedAhead counts the commits of the worktree at top that are not on
// its default branch's remote-tracking ref and carry no signature, and
// returns that ref.
func unsignedAhead(top string) (int, string) {
	if top == "" {
		return 0, ""
	}
	ref, err := git.Run("-C", top, "rev-parse", "--abbrev-ref", "origin/HEAD")
	if err != nil {
		ref = ""
		for _, r := range []string{"origin/main", "origin/master"} {
			if git.OK("-C", top, "rev-parse", "--verify", "-q", r) {
				ref = r
				break
			}
		}
	}
	if ref == "" {
		return 0, ""
	}
	raw, err := git.Run("-C", top, "log", "--format=raw", "--max-count=200", ref+"..HEAD")
	if err != nil || raw == "" {
		return 0, ref
	}
	unsigned := 0
	for _, commit := range strings.Split("\n"+raw, "\ncommit ")[1:] {
		header, _, _ := strings.Cut(commit, "\n\n")
		if !strings.Contains(header, "\ngpgsig") {
			unsigned++
		}
	}
	return unsigned, ref
}
