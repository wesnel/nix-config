package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

func TestMirrorReminder(t *testing.T) {
	setUp(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "owner")
	file := os.Getenv("AGENT_TOOLS_CONFIG")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	config["agentPlans"] = map[string]bool{"enable": true}
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := plans.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.New(plans.NewOptions{Title: "Reminder", Slug: "reminder", Goal: "Show the owning session its plan.", Sessions: []string{"codex:owner"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, event string
		want        bool
	}{
		{"owner", `{"hook_event_name":"UserPromptSubmit","session_id":"owner"}`, true},
		{"other session", `{"hook_event_name":"UserPromptSubmit","session_id":"other"}`, false},
		{"subagent", `{"hook_event_name":"UserPromptSubmit","session_id":"owner","agent_id":"child"}`, false},
		{"tool completion", `{"hook_event_name":"PostToolUse","session_id":"owner"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			mirrorRemind(strings.NewReader(tc.event), &out)
			if got := strings.Contains(out.String(), "plan-mirrors"); got != tc.want {
				t.Fatalf("reminder=%t want %t: %s", got, tc.want, out.String())
			}
		})
	}
}
