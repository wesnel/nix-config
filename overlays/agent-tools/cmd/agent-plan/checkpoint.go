package main

import (
	"encoding/json"
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
