package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestPost covers the context added after a tool call that pushed to
// GitHub: Claude Code's and Codex's PostToolUse events.
func TestPost(t *testing.T) {
	claude := func(command, stdout string) string {
		event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash",
			"tool_input": map[string]string{"command": command}, "tool_response": map[string]any{"stdout": stdout, "stderr": "", "interrupted": false}})
		return string(event)
	}
	// Codex names shell calls Bash, puts the command in tool_input.command
	// and the output in tool_response as one string.
	codex := func(command, output string) string {
		event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash", "turn_id": "t",
			"tool_use_id": "call_1", "permission_mode": "default", "agent_id": "main",
			"tool_input": map[string]string{"command": command}, "tool_response": output})
		return string(event)
	}
	opened := `{"pr": 12, "url": "https://github.com/o/r/pull/12", "draft": true, "branch": "feature", "base": "main"}`
	pushed := `{"pushed": {"b": "2222", "a": "1111"}, "round": 2, "apiCalls": 3}`
	for _, c := range []struct {
		name, event, want string
	}{
		{"Claude Code open", claude("pr-loop open --title T --body-file - feature", opened), "`pr-loop wait ci 12`"},
		{"Claude Code push after cd", claude("cd /r && pr-loop push", pushed), "`pr-loop wait ci a b`"},
		{"Claude Code push and more", claude("pr-loop push && echo done", pushed+"\ndone\n"), "`pr-loop wait ci a b`"},
		{"Claude Code sign-pass --push", claude("git sign-pass --push a", `{"branch": "a", "signed": 1, "pushed": {"a": "3333"}}`), "`pr-loop wait ci a`"},
		{"Codex push", codex("pr-loop push", pushed), "`pr-loop wait ci a b`"},
		{"Codex open after cd", codex("cd /r && pr-loop open --title T feature", "Wall time: 1.2 seconds\nOutput:\n"+opened+"\n"), "`pr-loop wait ci 12`"},
		{"Codex dry run", codex("pr-loop push --dry-run", `{"wouldUpdate": {"a": "1111"}, "objects": {}}`), ""},
		{"Codex push without output", codex("pr-loop push", ""), ""},
		{"Claude Code dry run", claude("pr-loop push --dry-run", `{"wouldUpdate": {"a": "1111"}, "objects": {}}`), ""},
		{"Claude Code push of nothing", claude("pr-loop push", `{"pushed": {}}`), ""},
		{"Claude Code push without output", claude("pr-loop push", ""), ""},
		{"Claude Code status", claude("pr-loop status", pushed), ""},
		{"Claude Code sign-pass", claude("git sign-pass", `{"branch": "a", "signed": 1}`), ""},
		{"Claude Code echo of a push", claude("echo '"+pushed+"'", pushed), ""},
		{"Claude Code Edit", `{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":"/x"}}`, ""},
		{"not JSON", `not json`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := Main([]string{"--post"}, strings.NewReader(c.event), &out, io.Discard); code != 0 {
				t.Errorf("%s: exit %d", c.event, code)
			}
			if c.want == "" {
				if out.Len() != 0 {
					t.Errorf("%s: printed %s", c.event, out.String())
				}
				return
			}
			var got struct {
				Specific struct {
					Event   string `json:"hookEventName"`
					Context string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Errorf("%s: %v: %s", c.event, err, out.String())
				return
			}
			context := got.Specific.Context
			if got.Specific.Event != "PostToolUse" {
				t.Errorf("%s: wrong format: %s", c.event, out.String())
			}
			if !strings.Contains(context, c.want) || !strings.Contains(context, "background") {
				t.Errorf("%s: context %q, want %s", c.event, context, c.want)
			}
		})
	}

	t.Run("records facts", testPostRecordsFacts)
	t.Run("links plans on the facts it records", testPostLinksPlans)
	t.Run("hands over the pull-requests skill", testPostPullRequests)
}

// A command that opens or edits a pull request is followed by the line
// that hands over the pull-requests skill; any other leaves it out.
func testPostPullRequests(t *testing.T) {
	claude := func(command string) string {
		event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash",
			"tool_input": map[string]string{"command": command}, "tool_response": map[string]any{"stdout": "", "stderr": ""}})
		return string(event)
	}
	for _, c := range []struct {
		name, event, format string
	}{
		{"gh pr create", claude("gh pr create --draft --title T --body-file b.md"), "hookSpecificOutput"},
		{"gh pr edit after cd", claude("cd /r && gh pr edit 12 --body-file b.md"), "hookSpecificOutput"},
		{"pr-loop open", claude("pr-loop open --title T --body-file b.md feature"), "hookSpecificOutput"},
		{"gh pr view", claude("gh pr view 12"), ""},
		{"pr-loop push", claude("pr-loop push"), ""},
		{"echo of gh pr create", claude("echo gh pr create"), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			Main([]string{"--post"}, strings.NewReader(c.event), &out, io.Discard)
			got := strings.Contains(out.String(), "`pull-requests` skill")
			if got != (c.format != "") || !strings.Contains(out.String(), c.format) {
				t.Errorf("%s: printed %s", c.event, out.String())
			}
		})
	}
}
