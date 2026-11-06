package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
	quota "github.com/wesnel/agent-tools/internal/usage"
)

// checkpointHint is why the status line should suggest /clear: the
// session's plan stands at a checkpoint and the context its input carries
// is heavy; else "". Git runs in ctx's Env.
func checkpointHint(ctx context.Context, c plans.Config, input []byte, now time.Time) string {
	session, ok := quota.FromClaudeContext(input, now)
	if !ok {
		return ""
	}
	why := session.Heavy(now)
	if why == "" {
		return ""
	}
	var in struct {
		Cwd string `json:"cwd"`
	}
	json.Unmarshal(input, &in)
	if _, ok := c.Checkpoint(ctx, session.Session, in.Cwd); !ok {
		return ""
	}
	return why
}
