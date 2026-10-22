package guard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// An event or tool input the guard cannot decode blocks the call: an
// event with exit 2, Claude Code's status for a blocked call, and a tool
// input with a denial, where it would otherwise be judged as empty.
func TestUnreadable(t *testing.T) {
	var out, errs bytes.Buffer
	if code := Main([]string{"--baseline"}, strings.NewReader("{not json"), &out, &errs); code != 2 ||
		!strings.Contains(errs.String(), "cannot read") || out.Len() != 0 {
		t.Errorf("an undecodable event: exit %d, stdout %q, stderr %q", code, out.String(), errs.String())
	}
	for name, event := range map[string]map[string]any{
		"Bash with a number for its command": {"tool_name": "Bash", "tool_input": map[string]any{"command": 7}},
		"Monitor with a list":                {"tool_name": "Monitor", "tool_input": map[string]any{"command": []string{"gh pr merge 1"}}},
		"Write with an object for its path":  {"tool_name": "Write", "tool_input": map[string]any{"file_path": map[string]any{}, "content": "x"}},
		"ExitWorktree with a string flag":    {"tool_name": "ExitWorktree", "tool_input": map[string]any{"action": "remove", "discard_changes": "yes"}},
		"local_shell with a number":          {"tool_name": "local_shell", "tool_input": map[string]any{"command": 3}},
	} {
		t.Run(caseName(true, name), func(t *testing.T) {
			data, _ := json.Marshal(event)
			var out bytes.Buffer
			if code := Main([]string{"--baseline"}, bytes.NewReader(data), &out, &bytes.Buffer{}); code != 0 {
				t.Fatalf("exit %d", code)
			}
			var got struct {
				Out struct {
					Decision string `json:"permissionDecision"`
					Reason   string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			json.Unmarshal(out.Bytes(), &got)
			if got.Out.Decision != "deny" || got.Out.Reason != unreadable || !saysInstead(got.Out.Reason) {
				t.Errorf("%s", out.String())
			}
		})
	}
}
