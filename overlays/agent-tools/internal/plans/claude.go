package plans

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
)

// claudeHook is the part of a Claude Code hook event these read.
type claudeHook struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	// Source is why SessionStart fired: "compact" after a compaction.
	Source string `json:"source"`
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

// SessionHook names the plans of the session Claude Code's SessionStart
// hook describes, matched by session or by the linked worktree it runs in
// (never a main checkout, which many plans name, as Locate says), the
// plan a /clear handed over by SessionEndHook first made the session's, as
// the hook's output, with the first one's resume brief after /clear or a
// compaction, the whole within ResumeLimit; nothing when there are none.
func (c Config) SessionHook(hook []byte) ([]byte, error) {
	var in claudeHook
	if err := json.Unmarshal(hook, &in); err != nil {
		return nil, err
	}
	o := FindOptions{}
	if in.SessionID != "" {
		o.Session = "claude:" + in.SessionID
		if in.Source == "clear" && in.Cwd != "" {
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
	var lines []string
	for _, f := range found {
		lines = append(lines, fmt.Sprintf("- %s (%s, status %s, matched by %s)", f.File, f.Title, f.State, strings.Join(f.Matched, " and ")))
	}
	context := "Plans for this session:\n" + clip(strings.Join(lines, "\n"), ResumeLimit/5) +
		"\nRead yours, and `agent-plan status PLAN` for its progress, before going on; keep it true as the plan-work skill says."
	if in.Source == "compact" || in.Source == "clear" {
		if p, err := c.Open(found[0].Plan); err == nil {
			if brief, err := p.ResumeBrief(time.Now(), ResumeLimit-len(context)-2); err == nil {
				context += "\n\n" + brief
			}
		}
	}
	return hookOutput("SessionStart", context)
}
