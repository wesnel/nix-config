package plans

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
)

// StopCheck is the Stop hook of Claude Code and Codex: before the agent
// stops, it blocks the stop once, with the reason, when the work is not in
// the state the plan-work and review-change skills leave it in. It reads
// local files and git alone, never the network, and returns nothing to let
// the stop through, on any error too.
func (c Config) StopCheck(input []byte, now time.Time) []byte {
	var in struct {
		SessionID string `json:"session_id"`
		Cwd       string `json:"cwd"`
		Active    bool   `json:"stop_hook_active"`
	}
	if json.Unmarshal(input, &in) != nil || in.Active {
		return nil
	}
	top, _ := checkout(in.Cwd)
	unsigned, ref, last := unsignedAhead(top)
	var found []Found
	if in.SessionID != "" || top != "" {
		found, _ = c.Find(FindOptions{Session: in.SessionID, Worktree: top})
	}
	block := func(reason string) []byte {
		out, _ := json.Marshal(map[string]string{"decision": "block", "reason": reason})
		return out
	}
	if len(found) == 0 {
		if unsigned == 0 {
			return nil
		}
		return block(fmt.Sprintf("This worktree has %d unsigned commits ahead of %s, and no plan names this session or worktree. "+
			"Make one with `agent-plan new` as the plan-work skill says, or add this worktree to the plan the work belongs to, before stopping.",
			unsigned, ref))
	}
	p, err := c.Open(found[0].Plan)
	if err != nil {
		return nil
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
	var committed, reviewed time.Time
	if unsigned > 0 {
		committed = last
	}
	for _, e := range events {
		at, err := time.ParseInLocation("[2006-01-02 Mon 15:04]", e.Time, now.Location())
		if err != nil {
			continue
		}
		switch {
		case e.Commit != "" && at.After(committed):
			committed = at
		case (e.Kind == "reviewed" || e.Kind == "handed-back") && at.After(reviewed):
			reviewed = at
		}
	}
	if committed.IsZero() || !reviewed.Before(committed.Truncate(time.Minute)) {
		return nil
	}
	return block(fmt.Sprintf("Plan %s runs the full review pipeline, and nothing is reviewed or handed back since its last commit. "+
		"Run the rest of the pipeline as the review-change skill says, or log why not with `agent-plan log PLAN --item %s --kind handed-back --summary '...'`.",
		p.File(), p.Name))
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
	name := in.ChatID
	if !chatName.MatchString(name) {
		sum := sha256.Sum256([]byte(name))
		name = hex.EncodeToString(sum[:])
	}
	file := filepath.Join(stateDir, name)
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

var chatName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// StopStateDir is where StopCheckChat keeps its files.
func StopStateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(dir) {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "agent-tools", "stop")
}

// unsignedAhead counts the commits of the worktree at top that are not on
// its default branch's remote-tracking ref and carry no signature, and
// returns that ref and the commit time of HEAD.
func unsignedAhead(top string) (int, string, time.Time) {
	if top == "" {
		return 0, "", time.Time{}
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
		return 0, "", time.Time{}
	}
	raw, err := git.Run("-C", top, "log", "--format=raw", "--max-count=200", ref+"..HEAD")
	if err != nil || raw == "" {
		return 0, ref, time.Time{}
	}
	unsigned := 0
	for _, commit := range strings.Split("\n"+raw, "\ncommit ")[1:] {
		header, _, _ := strings.Cut(commit, "\n\n")
		if !strings.Contains(header, "\ngpgsig") {
			unsigned++
		}
	}
	var last time.Time
	if out, err := git.Run("-C", top, "log", "-1", "--format=%ct", "HEAD"); err == nil {
		if n, err := strconv.ParseInt(out, 10, 64); err == nil {
			last = time.Unix(n, 0)
		}
	}
	return unsigned, ref, last
}
