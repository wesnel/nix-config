package prompttime

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Every prompt of a session's main thread carries the time and how long
// it has been since Wesley's previous message, which only his own prompts
// move; a sub-agent's prompts carry nothing.
func TestRemind(t *testing.T) {
	event := func(fields map[string]any) []byte {
		data, _ := json.Marshal(fields)
		return data
	}
	claude := func(session, prompt string) []byte {
		return event(map[string]any{"session_id": session, "hook_event_name": "UserPromptSubmit", "prompt": prompt})
	}
	zone := time.FixedZone("PDT", -7*3600)
	start := time.Date(2026, 10, 15, 9, 0, 0, 0, zone)
	dir := t.TempDir()
	steps := []struct {
		name  string
		input []byte
		after time.Duration
		want  []string
	}{
		{"the first prompt", claude("s1", "Start"), 0,
			[]string{"It is 2026-10-15 Thu 09:00 PDT", "Wesley's first message in this session"}},
		{"a reply two hours later", claude("s1", "Carry on"), 2*time.Hour + 5*time.Minute,
			[]string{"It is 2026-10-15 Thu 11:05 PDT", "2h 5m since Wesley's previous message"}},
		{"a sub-agent's notification", claude("s1", "<task-notification>\n<summary>Agent \"x\" finished</summary>\n</task-notification>"),
			2*time.Hour + 15*time.Minute, []string{"It is 2026-10-15 Thu 11:15 PDT", "10m since Wesley's previous message"}},
		{"his next prompt counts from his own", claude("s1", "And then?"), 2*time.Hour + 35*time.Minute,
			[]string{"30m since Wesley's previous message"}},
		{"another session", claude("s2", "Hello"), 3 * time.Hour, []string{"Wesley's first message in this session"}},
		{"a Codex prompt", event(map[string]any{"session_id": "s3", "hook_event_name": "UserPromptSubmit", "turn_id": "t",
			"prompt": "Go"}), 3 * time.Hour, []string{"It is 2026-10-15 Thu 12:00 PDT"}},
		{"an ECA prompt a day later", event(map[string]any{"hook_type": "preRequest", "chat_id": "c1", "prompt": "Go"}),
			27 * time.Hour, []string{"It is 2026-10-16 Fri 12:00 PDT", "first message"}},
		{"the ECA chat's next prompt", event(map[string]any{"hook_type": "preRequest", "chat_id": "c1", "prompt": "Go on"}),
			27*time.Hour + 45*time.Second, []string{"45s since Wesley's previous message"}},
		{"the ECA chat two days on", event(map[string]any{"hook_type": "preRequest", "chat_id": "c1", "prompt": "Back"}),
			75*time.Hour + 45*time.Second + 3*time.Hour, []string{"2d 3h since Wesley's previous message"}},
		{"a Codex sub-agent", event(map[string]any{"session_id": "s3", "hook_event_name": "UserPromptSubmit", "agent_id": "a1",
			"prompt": "Go"}), 4 * time.Hour, nil},
		{"an ECA sub-agent", event(map[string]any{"hook_type": "preRequest", "chat_id": "c2", "parent_chat_id": "c1",
			"prompt": "Go"}), 4 * time.Hour, nil},
		{"another event", event(map[string]any{"session_id": "s1", "hook_event_name": "PostToolUse", "tool_name": "Bash"}),
			4 * time.Hour, nil},
		{"a session ID that is no file name", claude("../x", "Hi"), 4 * time.Hour, nil},
		{"not JSON", []byte("{"), 4 * time.Hour, nil},
	}
	for _, s := range steps {
		got := Remind(dir, s.input, start.Add(s.after))
		if s.want == nil {
			if got != "" {
				t.Errorf("%s: %q, want nothing", s.name, got)
			}
			continue
		}
		for _, w := range s.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q, want %q in it", s.name, got, w)
			}
		}
	}
}
