package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runCodexQuotaStop runs the Codex hook at threshold 90, in the Env of ctx,
// on a call of tool with input, in the form Codex sends PreToolUse; agent
// is the sub-agent's ID, or "" for the main session.
func runCodexQuotaStop(ctx context.Context, t *testing.T, tool, input, agent string) (int, string, string) {
	t.Helper()
	id := ""
	if agent != "" {
		id = fmt.Sprintf(`"agent_id":%q,"agent_type":"worker",`, agent)
	}
	event := fmt.Sprintf(`{"session_id":"s-1","turn_id":"t-1",%s"cwd":"/nowhere","hook_event_name":"PreToolUse","model":"gpt-5",`+
		`"permission_mode":"default","tool_name":%q,"tool_input":%s,"tool_use_id":"c-1","transcript_path":null}`, id, tool, input)
	return agentPlan(ctx, t, event, "quota-stop", "--harness", "codex", "--threshold", "90")
}

// The Codex quota stop refuses from what codex-usage recorded, and Claude's
// does not.
func TestCodexQuotaStopFromCodexUsage(t *testing.T) {
	resets := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	bash := `{"command":"go test ./..."}`

	t.Run("over the threshold", func(t *testing.T) {
		ctx := fakeCodexOnPath(t, 95, resets)
		if code, out, errs := agentPlan(ctx, t, "", "codex-usage"); code != 0 {
			t.Fatalf("codex-usage: %d %q %q", code, out, errs)
		}
		code, out, errs := runCodexQuotaStop(ctx, t, "Bash", bash, "")
		if code != 0 {
			t.Fatalf("quota-stop: %d %q %q", code, out, errs)
		}
		reason := denial(t, out)
		for _, want := range []string{"Codex account's 5h window is at 95%", resets.Format("15:04"), "wgn.home.codex.quotaStop = null",
			"end your turn", "cannot schedule"} {
			if !strings.Contains(reason, want) {
				t.Errorf("reason %q does not say %q", reason, want)
			}
		}
		for _, absent := range []string{"ScheduleWakeup", "CronCreate", "SubagentHandback", "wgn.home.claude"} {
			if strings.Contains(reason, absent) {
				t.Errorf("reason %q names %q, which Codex lacks", reason, absent)
			}
		}
		// Claude's stop reads only Claude's record.
		if code, out, errs := runQuotaStop(ctx, t, "Bash", bash); code != 0 || out != "" {
			t.Errorf("Claude's stop on Codex's record: %d %q %q", code, out, errs)
		}
	})

	t.Run("below the threshold", func(t *testing.T) {
		ctx := fakeCodexOnPath(t, 89, resets)
		if code, out, errs := agentPlan(ctx, t, "", "codex-usage"); code != 0 {
			t.Fatalf("codex-usage: %d %q %q", code, out, errs)
		}
		if code, out, errs := runCodexQuotaStop(ctx, t, "Bash", bash, ""); code != 0 || out != "" {
			t.Errorf("stopped below the threshold: %d %q %q", code, out, errs)
		}
	})
}

// Over the threshold, the Codex stop lets through only agent-plan alone on
// a shell line and Codex's tools that plan, ask the operator, wait for or
// close a sub-agent; a sub-agent is told to end its turn with its report.
func TestCodexQuotaStop(t *testing.T) {
	t.Parallel()
	resets := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	bash := func(command string) string { return fmt.Sprintf(`{"command":%q}`, command) }
	for _, c := range []struct {
		name, tool, input string
		stops             bool
	}{
		{"a command", "Bash", bash("go test ./..."), true},
		{"a patch", "apply_patch", `{"command":"*** Begin Patch"}`, true},
		{"starting a sub-agent", "spawn_agent", `{"message":"go"}`, true},
		{"a message to a sub-agent", "send_input", `{"id":"a1","message":"go on"}`, true},
		{"an MCP call", "mcp__linear__save_issue", `{}`, true},
		{"agent-plan", "Bash", bash("agent-plan log /p.org --item P1 --kind blocked"), false},
		{"agent-plan in a pipeline", "Bash", bash("agent-plan status /p.org | head"), true},
		{"the plan tool", "update_plan", `{"plan":[]}`, false},
		{"a question to the operator", "request_user_input", `{"questions":[]}`, false},
		{"waiting for a sub-agent", "wait_agent", `{"ids":["a1"]}`, false},
		{"closing a sub-agent", "close_agent", `{"id":"a1"}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			ctx, _ := setup(t)
			writeCodexUsage(ctx, t, 97.5, resets)
			code, out, errs := runCodexQuotaStop(ctx, t, c.tool, c.input, "")
			if code != 0 {
				t.Fatalf("quota-stop: %d %q %q", code, out, errs)
			}
			if reason := denial(t, out); (reason != "") != c.stops {
				t.Errorf("stops %v, want %v: %q", reason != "", c.stops, reason)
			}
		})
	}

	t.Run("a sub-agent", func(t *testing.T) {
		t.Parallel()
		ctx, _ := setup(t)
		writeCodexUsage(ctx, t, 97.5, resets)
		_, out, _ := runCodexQuotaStop(ctx, t, "Bash", bash("go test ./..."), "a1")
		if reason := denial(t, out); !strings.Contains(reason, "sub-agent") || !strings.Contains(reason, "report") {
			t.Errorf("sub-agent told %q", reason)
		}
	})

	// Codex runs a tool whose PreToolUse hook exits 1; exit 2 with a
	// reason on stderr blocks it.
	for _, args := range [][]string{{"--harness", "codex"}, {"--harness", "codex", "--threshold", "0"},
		{"--harness", "codex", "--threshold", "ninety"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			ctx, _ := setup(t)
			code, out, errs := agentPlan(ctx, t, `{"tool_name":"Bash","tool_input":{}}`, append([]string{"quota-stop"}, args...)...)
			if code != 2 || out != "" || errs == "" {
				t.Errorf("%d %q %q", code, out, errs)
			}
		})
	}
	t.Run("an unknown harness", func(t *testing.T) {
		t.Parallel()
		ctx, _ := setup(t)
		if code, _, errs := agentPlan(ctx, t, `{}`, "quota-stop", "--harness", "eca", "--threshold", "90"); code == 0 || errs == "" {
			t.Errorf("%d %q", code, errs)
		}
	})
}

// writeCodexUsage writes codex-usage.json, in the state directory of ctx's
// Env, with the 5h window at used, observed now.
func writeCodexUsage(ctx context.Context, t *testing.T, used float64, resets time.Time) {
	t.Helper()
	os.MkdirAll(filepath.Dir(codexUsageFile(ctx)), 0o700)
	data := fmt.Sprintf(`{"five_hour":{"used_percentage":%g,"resets_at":%d,"observed_at":%d}}`, used, resets.Unix(), time.Now().Unix())
	if err := os.WriteFile(codexUsageFile(ctx), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}
