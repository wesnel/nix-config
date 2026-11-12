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
	t.Parallel()
	// As Claude Code 2.1.300 sent it when a background sub-agent finished.
	notification := "<task-notification>\n<task-id>ac1a150c85cc1d92f</task-id>\n<tool-use-id>toolu_01</tool-use-id>\n" +
		"<status>completed</status>\n<summary>Agent \"Run echo hi and report output\" finished</summary>\n" +
		"<result>The Bash command `echo hi` ran and returned `hi`.</result>\n" +
		"<usage><subagent_tokens>20253</subagent_tokens><tool_uses>1</tool_uses><duration_ms>2177</duration_ms></usage>\n</task-notification>"
	event := func(fields map[string]any) string {
		data, _ := json.Marshal(fields)
		return string(data)
	}
	// PLAN in a wanted string stands for the session's plan file.
	verify := []string{"Run echo hi and report output", "Check every report", "PLAN"}
	for _, c2 := range []struct {
		name, event, format string
		want                []string
	}{
		{"a background sub-agent in Claude Code", event(map[string]any{"session_id": "abc", "cwd": "/nowhere",
			"hook_event_name": "UserPromptSubmit", "prompt": notification}), "UserPromptSubmit", verify},
		{"a foreground sub-agent in Claude Code", event(map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse",
			"tool_name": "Agent", "tool_input": map[string]string{"description": "Add the flag"},
			"tool_response": map[string]any{"status": "completed", "content": []any{}}}), "PostToolUse",
			[]string{"Add the flag", "Check every report", "PLAN"}},
		{"a sub-agent waited for in Codex", event(map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse",
			"tool_name": "wait_agent", "turn_id": "t", "tool_response": "done"}), "PostToolUse", []string{"Check every report", "PLAN"}},
		{"a compaction in Claude Code", event(map[string]any{"session_id": "abc", "hook_event_name": "PreCompact",
			"trigger": "auto", "custom_instructions": nil}), "text", []string{"PLAN", "not yet record"}},
		{"after a compaction in Codex", event(map[string]any{"session_id": "abc", "hook_event_name": "SessionStart",
			"source": "compact"}), "SessionStart", []string{"PLAN", "up to date"}},

		{"a session with no plan", event(map[string]any{"session_id": "zzz", "hook_event_name": "UserPromptSubmit",
			"prompt": notification}), "", nil},
		{"the operator's own prompt", event(map[string]any{"session_id": "abc", "hook_event_name": "UserPromptSubmit",
			"prompt": "Carry on"}), "", nil},
		{"a background shell command's notification", event(map[string]any{"session_id": "abc", "hook_event_name": "UserPromptSubmit",
			"prompt": "<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n<summary>Background command \"go test\" completed (exit code 0)</summary>\n</task-notification>"}), "", nil},
		{"a sub-agent started in the background", event(map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse",
			"tool_name": "Agent", "tool_response": map[string]any{"isAsync": true, "status": "async_launched"}}), "", nil},
		{"Codex resuming", event(map[string]any{"session_id": "abc", "hook_event_name": "SessionStart", "source": "resume"}), "", nil},
		{"another tool", event(map[string]any{"session_id": "abc", "hook_event_name": "PostToolUse", "tool_name": "Bash"}), "", nil},
		{"not JSON", "{", "", nil},
	} {
		t.Run(c2.name, func(t *testing.T) {
			c := testConfig(t)
			p := newPlan(t, c, "x")
			out := string(c.Remind([]byte(c2.event)))
			if c2.want == nil {
				if out != "" {
					t.Errorf("%s: printed %q", c2.name, out)
				}
				return
			}
			var got struct {
				Specific struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			text := out
			switch c2.format {
			case "text":
				if json.Valid([]byte(out)) {
					t.Errorf("%s: want plain text, got %s", c2.name, out)
				}
			default:
				if json.Unmarshal([]byte(out), &got) != nil || got.Specific.Event != c2.format {
					t.Errorf("%s: want hookSpecificOutput for %s, got %s", c2.name, c2.format, out)
				}
				text = got.Specific.Context
			}
			for _, w := range c2.want {
				w = strings.Replace(w, "PLAN", p.File(), 1)
				if !strings.Contains(text, w) {
					t.Errorf("%s: %q lacks %q", c2.name, text, w)
				}
			}
		})
	}
	t.Run("names the gaps status shows", testRemindGaps)
}

// On each prompt of the main thread, the operator's or a task
// notification, a plan whose status shows commits no done names or a step
// gone quiet gets a line naming them, so the agent logs them without being
// asked; a plan with neither gets nothing (the cases above).
func testRemindGaps(t *testing.T) {
	t.Parallel()
	event := func(fields map[string]any) []byte {
		data, _ := json.Marshal(fields)
		return data
	}
	subAgent := "<task-notification>\n<task-id>a1</task-id>\n<status>completed</status>\n" +
		"<summary>Agent \"Add the flag\" finished</summary>\n</task-notification>"
	command := "<task-notification>\n<task-id>b1</task-id>\n<status>completed</status>\n" +
		"<summary>Background command \"go test\" completed (exit code 0)</summary>\n</task-notification>"
	for _, c2 := range []struct {
		name  string
		event []byte
		want  []string
	}{
		{"the operator's prompt", event(map[string]any{"session_id": "abc", "hook_event_name": "UserPromptSubmit",
			"prompt": "Carry on"}), nil},
		{"a background command's notification", event(map[string]any{"session_id": "abc",
			"hook_event_name": "UserPromptSubmit", "prompt": command}), nil},
		{"a sub-agent's notification", event(map[string]any{"session_id": "abc", "hook_event_name": "UserPromptSubmit",
			"prompt": subAgent}), []string{"Check every report"}},
	} {
		t.Run(c2.name, func(t *testing.T) {
			c := testConfig(t)
			p := newPlan(t, c, "x")
			addItems(t, p.file(""), "** Steps", "S1", "S2")
			for _, e := range []Event{{Kind: "started", Item: "S1"}, {Kind: "started", Item: "S2"},
				{Kind: "done", Item: "S2", Commit: "Add the run loop"},
				{Kind: "committed", Commit: "fixup! Add the run loop"}, {Kind: "committed", Commit: "Rename the flag"}} {
				if _, err := p.Record(e.Item, e, day); err != nil {
					t.Fatal(err)
				}
			}
			var got struct {
				Specific struct {
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			out := c.Remind(c2.event)
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("output %q: %v", out, err)
			}
			for _, w := range append(c2.want, p.File(), "Commits no step records: Rename the flag\n",
				"Stale, started with nothing logged for a day: S1\n") {
				if !strings.Contains(got.Specific.Context, w) {
					t.Errorf("%q lacks %q", got.Specific.Context, w)
				}
			}
			if strings.Contains(got.Specific.Context, "fixup!") {
				t.Errorf("%q names a fixup of a recorded commit", got.Specific.Context)
			}
		})
	}
	// A Codex sub-agent's turn is not the main thread's.
	c := testConfig(t)
	p := newPlan(t, c, "x")
	if _, err := p.Record("", Event{Kind: "committed", Commit: "Rename the flag"}, day); err != nil {
		t.Fatal(err)
	}
	if out := c.Remind(event(map[string]any{"session_id": "abc", "agent_id": "a", "hook_event_name": "UserPromptSubmit",
		"prompt": "Carry on"})); len(out) != 0 {
		t.Errorf("a sub-agent's prompt: %s", out)
	}
}
