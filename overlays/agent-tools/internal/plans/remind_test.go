package plans

import (
	"encoding/json"
	"strings"
	"testing"
)

// The coordinator is asked to check a finished sub-agent's report and
// record its part, and a compaction to carry what the plan lacks, in each
// harness's form, and only in a session with a plan.
func TestRemind(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	// As Claude Code 2.1.300 sent it when a background sub-agent finished.
	notification := "<task-notification>\n<task-id>ac1a150c85cc1d92f</task-id>\n<tool-use-id>toolu_01</tool-use-id>\n" +
		"<status>completed</status>\n<summary>Agent \"Run echo hi and report output\" finished</summary>\n" +
		"<result>The Bash command `echo hi` ran and returned `hi`.</result>\n" +
		"<usage><subagent_tokens>20253</subagent_tokens><tool_uses>1</tool_uses><duration_ms>2177</duration_ms></usage>\n</task-notification>"
	event := func(fields map[string]any) string {
		data, _ := json.Marshal(fields)
		return string(data)
	}
	verify := []string{"Run echo hi and report output", "Check every report", p.File(), "--tokens 20253"}
	for _, c2 := range []struct {
		name, event, format string
		want                []string
	}{
		{"a background sub-agent in Claude Code", event(map[string]any{"session_id": "abc", "cwd": "/nowhere",
			"hook_event_name": "UserPromptSubmit", "prompt": notification}), "UserPromptSubmit", verify},
		{"a foreground sub-agent in Claude Code", event(map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse",
			"tool_name": "Agent", "tool_input": map[string]string{"description": "Add the flag"},
			"tool_response": map[string]any{"status": "completed", "content": []any{}}}), "PostToolUse",
			[]string{"Add the flag", "Check every report", p.File()}},
		{"a sub-agent waited for in Codex", event(map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse",
			"tool_name": "wait_agent", "turn_id": "t", "tool_response": "done"}), "PostToolUse", []string{"Check every report", p.File()}},
		{"a sub-agent in ECA", event(map[string]any{"hook_type": "postToolCall", "chat_id": "abc", "server": "eca",
			"tool_name": "spawn_agent", "tool_input": map[string]string{"task": "Add the flag"}, "tool_response": "done"}), "eca",
			[]string{"Check every report", p.File()}},
		{"a compaction in Claude Code", event(map[string]any{"session_id": "abc", "hook_event_name": "PreCompact",
			"trigger": "auto", "custom_instructions": nil}), "text", []string{p.File(), "not yet record"}},
		{"after a compaction in Codex", event(map[string]any{"session_id": "abc", "hook_event_name": "SessionStart",
			"source": "compact"}), "SessionStart", []string{p.File(), "up to date"}},
		{"after a compaction in ECA", event(map[string]any{"hook_type": "postCompact", "chat_id": "abc",
			"triggered": "auto", "compact_summary": "..."}), "eca", []string{p.File(), "up to date"}},

		{"a session with no plan", event(map[string]any{"session_id": "zzz", "hook_event_name": "UserPromptSubmit",
			"prompt": notification}), "", nil},
		{"Wesley's own prompt", event(map[string]any{"session_id": "abc", "hook_event_name": "UserPromptSubmit",
			"prompt": "Carry on"}), "", nil},
		{"a background shell command's notification", event(map[string]any{"session_id": "abc", "hook_event_name": "UserPromptSubmit",
			"prompt": "<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n<summary>Background command \"go test\" completed (exit code 0)</summary>\n</task-notification>"}), "", nil},
		{"a sub-agent started in the background", event(map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse",
			"tool_name": "Agent", "tool_response": map[string]any{"isAsync": true, "status": "async_launched"}}), "", nil},
		{"Codex resuming", event(map[string]any{"session_id": "abc", "hook_event_name": "SessionStart", "source": "resume"}), "", nil},
		{"another tool", event(map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse", "tool_name": "Bash"}), "", nil},
		{"not JSON", "{", "", nil},
	} {
		out := string(c.Remind([]byte(c2.event)))
		if c2.want == nil {
			if out != "" {
				t.Errorf("%s: printed %q", c2.name, out)
			}
			continue
		}
		var got struct {
			Specific struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
			Context string `json:"additionalContext"`
		}
		text := out
		switch c2.format {
		case "text":
			if json.Valid([]byte(out)) {
				t.Errorf("%s: want plain text, got %s", c2.name, out)
			}
		case "eca":
			if json.Unmarshal([]byte(out), &got) != nil || got.Context == "" {
				t.Errorf("%s: want ECA's additionalContext, got %s", c2.name, out)
			}
			text = got.Context
		default:
			if json.Unmarshal([]byte(out), &got) != nil || got.Specific.Event != c2.format {
				t.Errorf("%s: want hookSpecificOutput for %s, got %s", c2.name, c2.format, out)
			}
			text = got.Specific.Context
		}
		for _, w := range c2.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q lacks %q", c2.name, text, w)
			}
		}
	}
}
