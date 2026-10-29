package plans

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

type sessionHook struct {
	Event     string `json:"hook_event_name"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Source    string `json:"source"`
}

func hookOutput(event, context string) ([]byte, error) {
	return json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": event, "additionalContext": context}})
}

// checkout is the top level and branch of the worktree containing dir,
// asked of that directory alone.
func checkout(dir string) (top, branch string) {
	if dir == "" {
		return "", ""
	}
	top, err := git.Run("-C", dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", ""
	}
	branch, _ = git.Run("-C", dir, "branch", "--show-current")
	return top, branch
}

// SessionHook returns bounded plan context for a Claude Code or Codex
// session's SessionStart hook, its plans matched by session or by the
// linked worktree it runs in (never a main checkout, which many plans name,
// as Locate says), the whole within ResumeLimit; nothing when there are
// none. Claude clear events first consume its SessionEnd handover. Codex
// events only read: exact session matches take precedence, and ambiguous
// matches get no brief.
func (c Config) SessionHook(hook []byte, harness string) ([]byte, error) {
	var in sessionHook
	if err := json.Unmarshal(hook, &in); err != nil {
		return nil, err
	}
	switch harness {
	case "claude":
	case "codex":
		if in.Event != "SessionStart" || in.SessionID == "" ||
			!slices.Contains([]string{"startup", "resume", "clear", "compact", "fork"}, in.Source) {
			return nil, refusal.New("Codex plan lookup needs a SessionStart event with session_id and a supported source. Pass the Codex SessionStart payload to `find --hook --harness codex`")
		}
		if err := checkWords("session_id", []string{in.SessionID}); err != nil {
			return nil, err
		}
	default:
		return nil, refusal.New("unsupported lifecycle harness %q. Use `find --hook --harness claude` or `find --hook --harness codex`", harness)
	}
	o := FindOptions{}
	if in.SessionID != "" {
		o.Session = harness + ":" + in.SessionID
		if harness == "claude" && in.Source == "clear" && in.Cwd != "" {
			c.takeHandover(o.Session, in.Cwd, time.Now())
		}
	}
	if top, _ := checkout(in.Cwd); linkedWorktree(top) {
		o.Worktree = top
	}
	if o.Session == "" && o.Worktree == "" {
		return nil, nil
	}
	found, err := c.Find(o)
	if err != nil || len(found) == 0 {
		return nil, err
	}
	if harness == "codex" {
		var exact []Found
		for _, f := range found {
			if slices.Contains(f.Matched, "session") {
				exact = append(exact, f)
			}
		}
		if len(exact) > 0 {
			found = exact
		}
	}
	var lines []string
	for _, f := range found {
		lines = append(lines, fmt.Sprintf("- %s (%s, status %s, matched by %s)", f.File, f.Title, f.State, strings.Join(f.Matched, " and ")))
	}
	context := "Plans for this session:\n" + clip(strings.Join(lines, "\n"), ResumeLimit/5) +
		"\nRead yours, and `agent-plan status PLAN` for its progress, before going on; keep it true as the plan-work skill says."
	brief := in.Source == "compact" || in.Source == "clear"
	if harness == "codex" {
		brief = len(found) == 1
	}
	if brief {
		if p, err := c.Open(found[0].Plan); err == nil {
			if brief, err := p.ResumeBrief(time.Now(), ResumeLimit-len(context)-2); err == nil {
				context += "\n\n" + brief
			}
		}
	}
	return hookOutput("SessionStart", context)
}
