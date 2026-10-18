package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
	quota "github.com/wesnel/agent-tools/internal/usage"
)

// checkpointHint is why the status line should suggest /clear: the
// session's plan stands at a checkpoint and the context its input carries
// is heavy; else "".
func checkpointHint(c plans.Config, input []byte, now time.Time) string {
	ctx, ok := quota.FromClaudeContext(input, now)
	if !ok {
		return ""
	}
	why := ctx.Heavy(now)
	if why == "" {
		return ""
	}
	var in struct {
		Cwd string `json:"cwd"`
	}
	json.Unmarshal(input, &in)
	if _, ok := c.Checkpoint(ctx.Session, in.Cwd); !ok {
		return ""
	}
	return why
}

// checkpointNudge is the Stop hook's output telling the operator, once per
// checkpoint, that the context is large enough to clear, which a session
// cannot do itself: only with nothing in flight, and never a block. The cache was just written by the turn
// that is stopping, so its warmth is the status line's to show.
func checkpointNudge(c plans.Config, s quota.State, input []byte) []byte {
	var in struct {
		SessionID string            `json:"session_id"`
		Cwd       string            `json:"cwd"`
		Active    bool              `json:"stop_hook_active"`
		Tasks     []json.RawMessage `json:"background_tasks"`
	}
	if json.Unmarshal(input, &in) != nil || in.Active || len(in.Tasks) > 0 {
		return nil
	}
	ctx, ok := s.Context(in.SessionID)
	if !ok || ctx.Tokens < quota.LargeContext {
		return nil
	}
	cp, ok := c.Checkpoint(in.SessionID, in.Cwd)
	if !ok || s.Nudged(in.SessionID, cp.Key) || s.MarkNudged(in.SessionID, cp.Key) != nil {
		return nil
	}
	out, _ := json.Marshal(map[string]string{"systemMessage": fmt.Sprintf(
		"Checkpoint (%s) in plan %s, and the context holds %dK tokens. Type /clear to start again from the plan: "+
			"its resume brief is added to the new session.", cp.Kind, cp.Plan, ctx.Tokens/1000)})
	return out
}
