package plans

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var notifiedAgent = regexp.MustCompile(`<summary>Agent "(.*)" finished</summary>`)

// Remind is `agent-plan remind`, a hook that asks for the judgement the
// plan needs at two moments, in the session's plan alone:
//
//   - When a sub-agent has finished, it asks the coordinator to check the
//     report and log the part's outcome: on Claude Code's UserPromptSubmit
//     carrying a background sub-agent's task-notification, its PostToolUse
//     of a foreground Agent call, and Codex's PostToolUse of wait_agent.
//     Claude Code's and Codex's SubagentStop speak only to the sub-agent,
//     so they are not used.
//   - Around a compaction, it asks for what the plan does not yet record:
//     on Claude Code's PreCompact, whose output becomes the compaction's
//     instructions, as text for the summary to keep; and after one, where
//     the harness cannot add context before it, on Codex's SessionStart
//     from a compaction, with the plan's brief.
//
// On any other UserPromptSubmit of the main thread the prompt is the
// operator's own: it keeps each http(s) address in it as a reference of the
// plan, and prints nothing. Codex sends a sub-agent's turns with agent_id.
//
// On Claude Code's PostToolUse of an Agent call starting a design-reviewer,
// background or foreground, it records design-review-started in the
// session's plan, which the guard holds edits on (PendingPlanReviewOf).
//
// On every prompt of the main thread, task-notifications included, it
// delivers the results of the session's sign-passes that it has not yet
// received (see DeliverSigns).
//
// It returns the hook's output, or nothing, on any error too.
func (c Config) Remind(input []byte) []byte {
	out := c.remind(input)
	if signs := c.remindSigns(input, time.Now()); signs != "" {
		return WithContext(out, input, signs)
	}
	return out
}

// remind is Remind but for the sign-passes.
func (c Config) remind(input []byte) []byte {
	var in struct {
		Event        string          `json:"hook_event_name"`
		SessionID    string          `json:"session_id"`
		AgentID      string          `json:"agent_id"` // Codex's, for a sub-agent
		Cwd          string          `json:"cwd"`
		Prompt       string          `json:"prompt"`
		ToolName     string          `json:"tool_name"`
		ToolInput    json.RawMessage `json:"tool_input"`
		ToolResponse json.RawMessage `json:"tool_response"`
		Source       string          `json:"source"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	// The plan's file, found once it is needed: most events need none.
	file, looked := "", false
	var found Plan
	plan := func() string {
		if looked {
			return file
		}
		looked = true
		top, _ := checkout(in.Cwd)
		if p, _, ok := c.Locate(in.SessionID, top); ok {
			found, file = p, p.File()
		}
		return file
	}
	finished := func(what string) string {
		return fmt.Sprintf("Sub-agent %sfinished. Check its report as the coordinate-subagents skill's \"Check every report\" says "+
			"before relaying any of it, then log its part's outcome in %s: `done` once it passes, or `blocked` or `abandoned`.",
			what, plan())
	}
	compacted := func() string {
		context := fmt.Sprintf("The conversation was compacted. Bring %s up to date before going on: log the steps done or "+
			"verified, the decisions, findings and sub-agent outcomes it does not yet record.", plan())
		if file == "" {
			return context
		}
		if brief, err := found.Brief(time.Now()); err == nil {
			context += "\n\n" + brief
		}
		return context
	}
	var context string
	switch {
	case in.Event == "UserPromptSubmit" && strings.HasPrefix(in.Prompt, "<task-notification>"):
		m := notifiedAgent.FindStringSubmatch(in.Prompt)
		if m == nil {
			return nil
		}
		context = finished(fmt.Sprintf("%q ", m[1]))
	case in.Event == "UserPromptSubmit" && in.AgentID == "":
		// The operator's own prompt: its addresses are references of the plan.
		if plan() != "" {
			recordReferences(found, in.Prompt, time.Now())
		}
		return nil
	case in.Event == "PostToolUse" && (in.ToolName == "Agent" || in.ToolName == "Task"):
		var response struct {
			IsAsync bool `json:"isAsync"`
		}
		var call struct {
			Description  string `json:"description"`
			SubagentType string `json:"subagent_type"`
		}
		json.Unmarshal(in.ToolResponse, &response)
		json.Unmarshal(in.ToolInput, &call)
		// Recorded at a background review's start, and at a foreground
		// one's end, in a plan whose SESSIONS name the session, never one
		// matched by its worktree alone; the guard holds edits until
		// plan-reviewed is logged (PendingPlanReviewOf).
		if call.SubagentType == "design-reviewer" {
			if p, _, ok := c.Locate(in.SessionID, ""); ok {
				// A summary holds one line; the description's first stands for it.
				summary, _, _ := strings.Cut(call.Description, "\n")
				_, _ = p.Record("", Event{Kind: DesignReviewStarted, Session: in.SessionID, Summary: strings.TrimSpace(summary)}, time.Now())
			}
		}
		if response.IsAsync {
			return nil
		}
		what := ""
		if call.Description != "" {
			what = fmt.Sprintf("%q ", call.Description)
		}
		context = finished(what)
	case in.Event == "PostToolUse" && in.ToolName == "wait_agent":
		context = finished("")
	case in.Event == "PreCompact":
		if p := plan(); p != "" {
			return []byte(fmt.Sprintf("Keep in the summary the path of the plan, %s, and everything since it was last brought "+
				"up to date that it does not yet record: steps done or verified, decisions, findings, sub-agent outcomes and "+
				"questions for the operator, so it can be brought up to date first thing after the compaction.", p))
		}
		return nil
	case in.Event == "SessionStart" && in.Source == "compact":
		context = compacted()
	default:
		return nil
	}
	if plan() == "" {
		return nil
	}
	data, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": in.Event, "additionalContext": context}})
	return data
}
