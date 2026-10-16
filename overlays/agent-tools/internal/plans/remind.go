package plans

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	notifiedAgent  = regexp.MustCompile(`<summary>Agent "(.*)" finished</summary>`)
	notifiedTokens = regexp.MustCompile(`<subagent_tokens>([0-9]+)</subagent_tokens>`)
)

// Remind is `agent-plan remind`, a hook that asks for the judgement the
// plan needs at two moments, in the session's plan alone:
//
//   - When a sub-agent has finished, it asks the coordinator to check the
//     report and log the part's outcome: on Claude Code's UserPromptSubmit
//     carrying a background sub-agent's task-notification, its PostToolUse
//     of a foreground Agent call, Codex's PostToolUse of wait_agent, and
//     ECA's postToolCall of spawn_agent. Claude Code's and Codex's
//     SubagentStop speak only to the sub-agent, so they are not used.
//   - Around a compaction, it asks for what the plan does not yet record:
//     on Claude Code's PreCompact, whose output becomes the compaction's
//     instructions, as text for the summary to keep; and after one, where
//     the harness cannot add context before it, on Codex's SessionStart
//     from a compaction and ECA's postCompact, with the plan's brief.
//
// On any other UserPromptSubmit of the main thread, and on ECA's
// preRequest of a chat that is not a sub-agent's, the prompt is the operator's
// own: it keeps each http(s) address in it as a reference of the plan,
// and prints nothing. Codex sends a sub-agent's turns with agent_id.
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
		Type         string          `json:"hook_type"` // ECA's
		SessionID    string          `json:"session_id"`
		AgentID      string          `json:"agent_id"` // Codex's, for a sub-agent
		ChatID       string          `json:"chat_id"`  // ECA's
		ParentChatID string          `json:"parent_chat_id"`
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
		session := in.SessionID
		if in.ChatID != "" {
			session = in.ChatID
		}
		top, _ := checkout(in.Cwd)
		if p, _, ok := c.Locate(session, top); ok {
			found, file = p, p.File()
		}
		return file
	}
	finished := func(what, tokens string) string {
		log := "`done` once it passes, or `blocked` or `abandoned`"
		if tokens != "" {
			log += ", with `--tokens " + tokens + "`"
		}
		return fmt.Sprintf("Sub-agent %sfinished. Check its report as the coordinate-subagents skill's \"Check every report\" says "+
			"before relaying any of it, then log its part's outcome in %s: %s.", what, plan(), log)
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
	var event, context string
	switch {
	case in.Event == "UserPromptSubmit" && strings.HasPrefix(in.Prompt, "<task-notification>"):
		m := notifiedAgent.FindStringSubmatch(in.Prompt)
		if m == nil {
			return nil
		}
		tokens := ""
		if t := notifiedTokens.FindStringSubmatch(in.Prompt); t != nil {
			tokens = t[1]
		}
		event, context = in.Event, finished(fmt.Sprintf("%q ", m[1]), tokens)
	case (in.Event == "UserPromptSubmit" && in.AgentID == "") || (in.Type == "preRequest" && in.ParentChatID == ""):
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
			Description string `json:"description"`
		}
		json.Unmarshal(in.ToolResponse, &response)
		json.Unmarshal(in.ToolInput, &call)
		if response.IsAsync {
			return nil
		}
		what := ""
		if call.Description != "" {
			what = fmt.Sprintf("%q ", call.Description)
		}
		event, context = in.Event, finished(what, "")
	case in.Event == "PostToolUse" && in.ToolName == "wait_agent":
		event, context = in.Event, finished("", "")
	case in.Type == "postToolCall" && in.ToolName == "spawn_agent":
		context = finished("", "")
	case in.Event == "PreCompact":
		if p := plan(); p != "" {
			return []byte(fmt.Sprintf("Keep in the summary the path of the plan, %s, and everything since it was last brought "+
				"up to date that it does not yet record: steps done or verified, decisions, findings, sub-agent outcomes and "+
				"questions for the operator, so it can be brought up to date first thing after the compaction.", p))
		}
		return nil
	case in.Event == "SessionStart" && in.Source == "compact":
		event, context = in.Event, compacted()
	case in.Type == "postCompact":
		context = compacted()
	default:
		return nil
	}
	if plan() == "" {
		return nil
	}
	var out any = map[string]string{"additionalContext": context}
	if event != "" {
		out = map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": context}}
	}
	data, _ := json.Marshal(out)
	return data
}
