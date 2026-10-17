package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

const page = "https://claude.ai/artifact/DashboardPage1"

// dashboardFixture configures the page and makes the out directory, with a
// link in it to a directory outside, and returns out and that directory.
func dashboardFixture(t *testing.T) (out, outside string) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "agent-plans.json")
	os.WriteFile(config, []byte(`{"url": "`+page+`"}`), 0o600)
	t.Setenv("AGENT_PLANS_CONFIG", config)
	out = agentplans.Out()
	for _, d := range []string{"answers", "dispatch", "plans", "github", "meta"} {
		os.MkdirAll(filepath.Join(out, d), 0o700)
	}
	t.Cleanup(func() { os.RemoveAll(agentplans.Dir()) })
	outside = t.TempDir()
	os.Symlink(outside, filepath.Join(out, "link"))
	os.MkdirAll(agentplans.Session(), 0o700)
	os.MkdirAll(filepath.Dir(agentplans.PageFile()), 0o700)
	os.WriteFile(agentplans.PageFile(), agentplans.Page, 0o600)
	return out, outside
}

// capabilities is agentplans.Capabilities as a tool input holds it.
func capabilities(t *testing.T) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(agentplans.Capabilities), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// hook runs the guard as a hook with args on one call, and returns its
// decision and reason; "" for a call it lets through without a word.
func hook(t *testing.T, args []string, tool string, toolInput any) (string, string) {
	t.Helper()
	raw, err := json.Marshal(toolInput)
	if err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_input": json.RawMessage(raw),
		"cwd": agentplans.Session(), "session_id": "s"})
	var stdout, stderr bytes.Buffer
	if code := Main(args, bytes.NewReader(event), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if stdout.Len() == 0 {
		return "", ""
	}
	var out struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("%s: %v", stdout.String(), err)
	}
	return out.HookSpecificOutput.PermissionDecision, out.HookSpecificOutput.PermissionDecisionReason
}

type toolCall struct {
	tool  string
	input any
}

// allowedCalls are every form the dashboard session's skill uses.
func allowedCalls(t *testing.T, out string) []toolCall {
	caps := capabilities(t)
	return []toolCall{
		{"Bash", map[string]any{"command": "plan-dash export", "description": "Export the page's documents"}},
		{"Bash", map[string]any{"command": "plan-dash page"}},
		{"Bash", map[string]any{"command": "plan-dash page published"}},
		{"Artifact", map[string]any{"action": "read", "url": page}},
		{"Artifact", map[string]any{"url": page, "file_path": agentplans.PageFile(), "capabilities": caps}},
		{"Artifact", map[string]any{"action": "publish", "url": page, "file_path": agentplans.PageFile(),
			"capabilities": map[string]any{"comments": map[string]any{}, "db": map[string]any{"rules": []any{
				map[string]any{"write": "owner", "path": "", "read": "owner"}}}},
			"description": "The plans the agents keep, and the reviews asked of the operator.", "label": "Page update"}},
		{"Bash", map[string]any{"command": "plan-dash answer"}},
		{"Bash", map[string]any{"command": "plan-dash dispatch", "timeout": 120000}},
		{"Monitor", map[string]any{"command": "plan-dash watch", "description": "Agent plans watch", "timeout_ms": 1800000}},
		{"ArtifactData", map[string]any{"action": "list", "url": page, "collection": "answers", "out_dir": out}},
		{"ArtifactData", map[string]any{"action": "list", "url": page, "collection": "dispatch", "out_dir": out,
			"query": map[string]any{"limit": 1000}}},
		{"ArtifactData", map[string]any{"action": "list", "url": page, "collection": "plans"}},
		{"ArtifactData", map[string]any{"action": "get", "url": page, "collection": "meta", "doc_id": "sync"}},
		{"ArtifactData", map[string]any{"action": "set", "url": page, "collection": "meta", "doc_id": "sync",
			"file_path": filepath.Join(out, "meta", "sync.json"), "if_version": 3}},
		{"ArtifactData", map[string]any{"action": "delete", "url": page, "collection": "plans", "doc_id": "2026-10-17-old.box",
			"if_version": 2}},
		{"ArtifactData", map[string]any{"action": "batch", "url": page, "writes": []any{
			map[string]any{"op": "set", "collection": "plans", "doc_id": "2026-10-17-loop.box",
				"file_path": filepath.Join(out, "plans", "2026-10-17-loop.box.json"), "if_version": 4},
			map[string]any{"op": "set", "collection": "answers", "doc_id": "2026-10-17-loop.box~Q1",
				"file_path": filepath.Join(out, "answers", "2026-10-17-loop.box@Q1.json")},
			map[string]any{"op": "delete", "collection": "plans", "doc_id": "2026-10-16-gone.box", "if_version": 1}}}},
		{"ArtifactComments", map[string]any{"action": "watch", "url": page}},
		{"ArtifactComments", map[string]any{"action": "resolve", "url": page, "thread_id": "th_01HZX9K2Q4"}},
		{"Agent", map[string]any{"description": "Update the page", "prompt": "Run the update.", "subagent_type": "general-purpose"}},
		{"Agent", map[string]any{"description": "Update the page", "prompt": "Run the update.", "isolation": ""}},
		{"Skill", map[string]any{"skill": "agent-plans"}},
		{"ToolSearch", map[string]any{"query": "select:ArtifactData,ArtifactComments,Monitor,PushNotification", "max_results": 4}},
		{"PushNotification", map[string]any{"message": "devbox plan sync stalled", "status": "proactive"}},
		{"PushNotification", map[string]any{"message": "Standup digest ready", "status": "proactive"}},
		{"PushNotification", map[string]any{"message": "Demo digest ready"}},
	}
}

// deniedCalls are near misses of allowedCalls and calls that would leave
// the fence: other commands, pages, paths and tools.
func deniedCalls(t *testing.T, out, outside string) []toolCall {
	state := filepath.Dir(out)
	creds := filepath.Join(home, ".claude", ".credentials.json")
	settings := filepath.Join(agentplans.Session(), ".claude", "settings.json")
	caps := capabilities(t)
	publish := func(extra map[string]any) toolCall {
		in := map[string]any{"url": page, "file_path": agentplans.PageFile(), "capabilities": caps}
		for k, v := range extra {
			if v == nil {
				delete(in, k)
			} else {
				in[k] = v
			}
		}
		return toolCall{"Artifact", in}
	}
	calls := []toolCall{
		publish(map[string]any{"capabilities": nil}),
		publish(map[string]any{"capabilities": map[string]any{}}),
		publish(map[string]any{"capabilities": map[string]any{"db": map[string]any{}, "comments": map[string]any{}}}),
		publish(map[string]any{"capabilities": map[string]any{"db": map[string]any{"rules": []any{
			map[string]any{"path": "", "read": "view", "write": "owner"}}}, "comments": map[string]any{}}}),
		publish(map[string]any{"capabilities": map[string]any{"db": map[string]any{"rules": []any{
			map[string]any{"path": "", "read": "owner", "write": "owner"}}}, "comments": map[string]any{},
			"mcp": map[string]any{"servers": []any{map[string]any{"server": "host:x", "tools": []any{"run"}}}}}}),
		publish(map[string]any{"capabilities": "{}"}),
		publish(map[string]any{"file_path": filepath.Join(out, "meta", "sync.json")}),
		publish(map[string]any{"file_path": creds}),
		publish(map[string]any{"file_path": filepath.Join(filepath.Dir(agentplans.PageFile()), "published")}),
		publish(map[string]any{"file_path": filepath.Dir(agentplans.PageFile()) + "/../page/agent-plans.html"}),
		publish(map[string]any{"file_path": filepath.Dir(agentplans.PageFile()) + "//agent-plans.html"}),
		publish(map[string]any{"file_path": nil}),
		publish(map[string]any{"url": "https://claude.ai/artifact/OtherPage"}),
		publish(map[string]any{"url": nil}),
		publish(map[string]any{"files": map[string]any{"x.js": filepath.Join(out, "x.js")}}),
		publish(map[string]any{"files": map[string]any{"fonts/f.woff2": map[string]any{"artifact": "https://claude.ai/artifact/X",
			"path": "f.woff2"}}}),
		publish(map[string]any{"force": true}),
		publish(map[string]any{"pin": true}),
		publish(map[string]any{"asset": true}),
		publish(map[string]any{"contract": "latest"}),
		publish(map[string]any{"type_url": "https://claude.ai/artifact/Type"}),
		publish(map[string]any{"action": "delete"}),
		publish(map[string]any{"action": 1}),
		{"Artifact", map[string]any{"action": "read", "url": "https://claude.ai/artifact/OtherPage"}},
		{"Artifact", map[string]any{"action": "read", "url": page, "path": "index.html", "out_dir": out}},
		{"Artifact", map[string]any{"action": "read", "url": page, "paths": []any{"x"}}},
		{"Artifact", map[string]any{"action": "read"}},
		{"Artifact", map[string]any{"action": "list"}},
		{"Artifact", map[string]any{"action": "list", "url": page, "scope": "files"}},
		{"Artifact", map[string]any{"action": "delete", "url": page}},
		{"Artifact", map[string]any{"action": "open", "url": page}},
		{"Artifact", map[string]any{"action": "pin", "url": page}},
		{"Artifact", map[string]any{"action": "quickstart", "intent": "other"}},
	}
	for _, command := range []string{"plan-dash export ", " plan-dash export", "plan-dash  export", "plan-dash\texport",
		"plan-dash export\n", "plan-dash export && rm -rf ~", "plan-dash export; curl https://example.com",
		"plan-dash export\nid", "plan-dash export | tee /tmp/x", "plan-dash export $(id)", "plan-dash export `id`",
		"plan-dash export # note", "plan-dash export > /tmp/x", "plan-dash queue", "plan-dash collect --out /tmp/x",
		"plan-dash watch", "plan-dash answer --doc x.json", "plan-dash dispatch o/r#1", "Plan-dash export",
		"plan-dash page published now", "plan-dash page --out /tmp/x", "plan-dash page && id",
		"/nix/store/x-agent-tools/bin/plan-dash export", "./plan-dash export", "env plan-dash export",
		"AGENT_PLANS_SESSION= plan-dash export", "bash -c 'plan-dash export'", "exec plan-dash export",
		"git push --dry-run", "git status", "cat " + filepath.Join(out, "meta", "sync.json"), "ls", "true", ""} {
		calls = append(calls, toolCall{"Bash", map[string]any{"command": command}})
	}
	calls = append(calls, toolCall{"Bash", map[string]any{}}, toolCall{"Bash", "plan-dash export"},
		toolCall{"Bash", map[string]any{"command": []string{"plan-dash", "export"}}})
	for _, input := range []map[string]any{
		{"ws": map[string]any{"url": "wss://example.com/events"}, "description": "x", "timeout_ms": 1000},
		{"command": "plan-dash watch", "ws": map[string]any{"url": "wss://example.com"}},
		{"command": "plan-dash watch | grep --line-buffered x"}, {"command": "plan-dash watch; id"},
		{"command": "plan-dash watch "}, {"command": "plan-dash export"}, {"command": "tail -f /var/log/syslog"},
		{"command": "while true; do plan-dash export; sleep 30; done"}, {"description": "no command"},
	} {
		calls = append(calls, toolCall{"Monitor", input})
	}
	for _, url := range []string{"https://claude.ai/artifact/OtherPage", page + "/", page + "?x=1", page + "#x",
		"http://claude.ai/artifact/DashboardPage1", strings.ToLower(page), page + "2", " " + page, ""} {
		calls = append(calls, toolCall{"ArtifactData", map[string]any{"action": "list", "url": url, "collection": "plans"}},
			toolCall{"ArtifactComments", map[string]any{"action": "watch", "url": url}})
	}
	calls = append(calls, toolCall{"ArtifactData", map[string]any{"action": "list", "collection": "plans"}})
	for _, path := range []string{"/tmp", state, filepath.Join(out, ".."), filepath.Join(out, "..", "session"),
		agentplans.Session(), filepath.Join(agentplans.Session(), ".claude"), home, filepath.Join(home, ".claude"),
		"out", "answers", "~/.local/state/agent-plans/out", filepath.Join(out, "link"), filepath.Join(out, "link", "x"),
		out + "-other", filepath.Join(out, "answers") + "\x00", outside} {
		calls = append(calls, toolCall{"ArtifactData", map[string]any{"action": "list", "url": page, "collection": "answers",
			"out_dir": path}})
	}
	for _, path := range []string{creds, settings, filepath.Join(state, "requests.json"), filepath.Join(state, "questions.json"),
		filepath.Join(out, "..", "requests.json"), filepath.Join(out, "link", "secret.json"), "plans/x.json",
		filepath.Join(out, "plans", "..", "..", "queue.org"), out, "/etc/passwd", ""} {
		calls = append(calls,
			toolCall{"ArtifactData", map[string]any{"action": "set", "url": page, "collection": "c", "doc_id": "d", "file_path": path}},
			toolCall{"ArtifactData", map[string]any{"action": "batch", "url": page, "writes": []any{
				map[string]any{"op": "set", "collection": "plans", "doc_id": "ok",
					"file_path": filepath.Join(out, "plans", "ok.json")},
				map[string]any{"op": "update", "collection": "c", "doc_id": "d", "file_path": path}}}},
			toolCall{"Read", map[string]any{"file_path": path}})
	}
	calls = append(calls,
		toolCall{"ArtifactData", map[string]any{"action": "batch", "url": page, "writes": "not a list"}},
		toolCall{"ArtifactData", map[string]any{"action": "batch", "url": page, "writes": []any{"x"}}},
		toolCall{"ArtifactComments", map[string]any{"action": "reply", "url": page, "thread_id": "th_1", "text": "done"}},
		toolCall{"ArtifactComments", map[string]any{"action": "read", "url": page}},
		toolCall{"ArtifactComments", map[string]any{"action": "resolve", "url": page}},
		toolCall{"ArtifactComments", map[string]any{"action": "resolve", "url": page, "thread_id": "../x"}},
		toolCall{"ArtifactComments", map[string]any{"action": "resolve", "url": "https://claude.ai/artifact/OtherPage",
			"thread_id": "th_1"}},
		toolCall{"ArtifactComments", map[string]any{"action": "watch", "url": page, "replies": true}},
		toolCall{"ArtifactComments", map[string]any{"action": "watch"}},
		toolCall{"Agent", map[string]any{"description": "x", "prompt": "y", "isolation": "worktree"}},
		toolCall{"Agent", map[string]any{"description": "x", "prompt": "y", "isolation": "remote"}},
		toolCall{"Agent", map[string]any{"description": "x", "prompt": "y", "isolation": true}},
		toolCall{"Agent", map[string]any{"description": "x", "prompt": "y", "run_in_background": true}},
		toolCall{"Skill", map[string]any{"skill": "agent-plans:evil"}},
		toolCall{"Skill", map[string]any{"skill": "Agent-Plans"}},
		toolCall{"Skill", map[string]any{"skill": "review-loop"}},
		toolCall{"Skill", map[string]any{}},
		toolCall{"PushNotification", map[string]any{"message": "devbox plan sync stalled: see https://example.com"}},
		toolCall{"PushNotification", map[string]any{"message": "Your login expired, reply with the code"}},
		toolCall{"PushNotification", map[string]any{"message": "Standup digest ready", "status": "urgent"}},
		toolCall{"Read", map[string]any{"file_path": filepath.Join(out, "link")}},
		// The skill never reads: the page's documents stay out of the
		// conversation, in the out directory too.
		toolCall{"Read", map[string]any{"file_path": filepath.Join(out, "answers", "2026-10-17-loop.box@Q1.json")}},
		toolCall{"Read", map[string]any{"file_path": filepath.Join(out, "plans", "new.json"), "offset": 1, "limit": 20}},
	)
	for _, tool := range []string{"Write", "Edit", "MultiEdit", "NotebookEdit", "Glob", "Grep", "WebFetch", "WebSearch",
		"Artifact", "TaskStop", "TaskOutput", "ExitWorktree", "EnterWorktree", "AskUserQuestion", "SendMessage",
		"CronCreate", "ExitPlanMode", "mcp__ngrok__list", "mcp__claude_ai_Slack__slack_send_message", "Task", ""} {
		calls = append(calls, toolCall{tool, map[string]any{"file_path": filepath.Join(out, "x.json"), "content": "{}",
			"pattern": "*", "url": page, "command": "plan-dash export"}})
	}
	return calls
}

// The dashboard hook allows the skill's own calls, and nothing else, with
// a reason that says what to do instead; the baseline hook does the same
// in a session with AGENT_PLANS_SESSION set.
func TestDashboardFence(t *testing.T) {
	fakeHome(t)
	out, outside := dashboardFixture(t)
	for _, c := range allowedCalls(t, out) {
		if decision, reason := hook(t, []string{"--dashboard"}, c.tool, c.input); decision != "allow" {
			t.Errorf("%s %v: %s %q", c.tool, c.input, decision, reason)
		}
	}
	for _, c := range deniedCalls(t, out, outside) {
		decision, reason := hook(t, []string{"--dashboard"}, c.tool, c.input)
		if decision != "deny" || !saysInstead(reason) || strings.Contains(reason, out) {
			t.Errorf("%s %v: %s %q", c.tool, c.input, decision, reason)
		}
	}

	t.Setenv("AGENT_PLANS_SESSION", "1")
	for _, c := range allowedCalls(t, out) {
		if decision, reason := hook(t, []string{"--baseline"}, c.tool, c.input); decision == "deny" {
			t.Errorf("baseline: %s %v: %s %q", c.tool, c.input, decision, reason)
		}
	}
	for _, c := range deniedCalls(t, out, outside) {
		if decision, reason := hook(t, []string{"--baseline"}, c.tool, c.input); decision != "deny" {
			t.Errorf("baseline: %s %v: %s %q", c.tool, c.input, decision, reason)
		}
	}
}

// A call the dashboard hook cannot read is denied.
func TestDashboardUnreadable(t *testing.T) {
	for _, event := range []string{"", "not json", `{"tool_name": "Bash", "tool_input": "plan-dash export"}`} {
		var stdout, stderr bytes.Buffer
		if code := Main([]string{"--dashboard"}, strings.NewReader(event), &stdout, &stderr); code != 0 ||
			!strings.Contains(stdout.String(), `"permissionDecision":"deny"`) {
			t.Errorf("%q: exit %d: %s", event, code, stdout.String())
		}
	}
}

// Without the page's URL configured, nothing reaches a page.
func TestDashboardUnconfigured(t *testing.T) {
	fakeHome(t)
	out, _ := dashboardFixture(t)
	t.Setenv("AGENT_PLANS_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	for _, c := range []toolCall{
		{"ArtifactData", map[string]any{"action": "list", "url": page, "collection": "plans"}},
		{"ArtifactComments", map[string]any{"action": "watch", "url": page}},
		{"ArtifactData", map[string]any{"action": "list", "url": "", "collection": "plans", "out_dir": out}},
	} {
		if decision, reason := hook(t, []string{"--dashboard"}, c.tool, c.input); decision != "deny" || !saysInstead(reason) {
			t.Errorf("%s %v: %s %q", c.tool, c.input, decision, reason)
		}
	}
	if decision, _ := hook(t, []string{"--dashboard"}, "Bash", map[string]any{"command": "plan-dash export"}); decision != "allow" {
		t.Errorf("plan-dash export: %s", decision)
	}
}

// Only where no page is configured, and none was created, may a publish
// create one, pinned or not; its file and capabilities are held as a
// republish's are.
func TestDashboardCreatePage(t *testing.T) {
	fakeHome(t)
	dashboardFixture(t)
	create := map[string]any{"action": "publish", "file_path": agentplans.PageFile(), "capabilities": capabilities(t),
		"icon": "checklist", "title": "Agent plans", "description": "The plans the agents keep.", "pin": true}
	if decision, reason := hook(t, []string{"--dashboard"}, "Artifact", create); decision != "deny" || !saysInstead(reason) {
		t.Errorf("configured: %s %q", decision, reason)
	}
	t.Setenv("AGENT_PLANS_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	for _, change := range []map[string]any{{"pin": false}, {"pin": "yes"}, {"capabilities": map[string]any{"db": map[string]any{}}},
		{"file_path": filepath.Join(t.TempDir(), "page.html")}, {"files": map[string]any{"a.js": "a.js"}}, {"force": true},
		{"url": page}} {
		in := map[string]any{}
		for k, v := range create {
			in[k] = v
		}
		for k, v := range change {
			in[k] = v
		}
		if decision, reason := hook(t, []string{"--dashboard"}, "Artifact", in); decision != "deny" || !saysInstead(reason) {
			t.Errorf("unconfigured %v: %s %q", change, decision, reason)
		}
	}
	if _, err := os.Stat(agentplans.CreatedFile()); err == nil {
		t.Fatal("a denied create left the created marker")
	}
	if decision, reason := hook(t, []string{"--dashboard"}, "Artifact", create); decision != "allow" {
		t.Errorf("unconfigured: %s %q", decision, reason)
	}
	if data, err := os.ReadFile(agentplans.PublishingFile()); err != nil || strings.TrimSpace(string(data)) != agentplans.PageHash(agentplans.Page) {
		t.Errorf("the create was not recorded for plan-dash page published: %q (%v)", data, err)
	}
	// One create only, until the operator configures the page or removes
	// the marker.
	if decision, reason := hook(t, []string{"--dashboard"}, "Artifact", create); decision != "deny" || !saysInstead(reason) {
		t.Errorf("a second create: %s %q", decision, reason)
	}
	os.Remove(agentplans.CreatedFile())
	// A page file that is a link, or holds anything but the page built in,
	// is not the page.
	target := filepath.Join(t.TempDir(), "secret.html")
	os.WriteFile(target, agentplans.Page, 0o600)
	os.Remove(agentplans.PageFile())
	os.Symlink(target, agentplans.PageFile())
	if decision, _ := hook(t, []string{"--dashboard"}, "Artifact", create); decision != "deny" {
		t.Errorf("a linked page: %s", decision)
	}
	for _, body := range [][]byte{[]byte("<p>not the page</p>"), append(append([]byte{}, agentplans.Page...), '\n'),
		agentplans.Page[:len(agentplans.Page)-1], nil} {
		os.Remove(agentplans.PageFile())
		os.WriteFile(agentplans.PageFile(), body, 0o600)
		if decision, _ := hook(t, []string{"--dashboard"}, "Artifact", create); decision != "deny" {
			t.Errorf("a page of %d bytes: %s", len(body), decision)
		}
	}
	if _, err := os.Stat(agentplans.CreatedFile()); err == nil {
		t.Error("a denied create left the created marker")
	}
}

// Outside the dashboard session, the commands that act on what the page
// asked are refused, and so is every way to mark a command as the session.
func TestDashboardOutside(t *testing.T) {
	fakeHome(t)
	t.Setenv("AGENT_PLANS_SESSION", "")
	work := t.TempDir()
	for _, c := range []struct {
		deny    bool
		command string
	}{
		{true, "plan-dash answer"},
		{true, "plan-dash dispatch"},
		{true, "/nix/store/x-agent-tools/bin/plan-dash dispatch"},
		{true, "cd /tmp && plan-dash answer"},
		{true, "AGENT_PLANS_SESSION=1 plan-dash export"},
		{true, "export AGENT_PLANS_SESSION=1"},
		{true, "AGENT_PLANS_SESSION=1; export AGENT_PLANS_SESSION"},
		{true, "env AGENT_PLANS_SESSION=1 claude"},
		{true, "declare -x AGENT_PLANS_SESSION=1"},
		{true, "set -gx AGENT_PLANS_SESSION 1"},
		{true, "set --export AGENT_PLANS_SESSION yes"},
		{true, "systemctl --user set-environment AGENT_PLANS_SESSION=1"},
		{true, "launchctl setenv AGENT_PLANS_SESSION 1"},
		{true, "bash -c 'AGENT_PLANS_SESSION=1 plan-dash answer'"},
		{true, "fish -c 'set -x AGENT_PLANS_SESSION 1; plan-dash dispatch'"},
		{false, "plan-dash export"},
		{false, "plan-dash collect"},
		{false, "plan-dash watch"},
		{false, "echo $AGENT_PLANS_SESSION"},
		{false, "printenv AGENT_PLANS_SESSION"},
		{false, "grep -rn AGENT_PLANS_SESSION ."},
		{false, "git grep AGENT_PLANS_SESSION -- '*.go'"},
	} {
		reason := judge(t, work, c.command)
		if (reason != "") != c.deny || reason != "" && !saysInstead(reason) {
			t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
		}
	}
	// Monitor runs a command as Bash does.
	input, _ := json.Marshal(map[string]string{"command": "plan-dash dispatch"})
	if reason := check("Monitor", "", input, baseline, work); reason == "" {
		t.Error("Monitor ran plan-dash dispatch")
	}
	// The session itself runs them.
	t.Setenv("AGENT_PLANS_SESSION", "1")
	for _, command := range []string{"plan-dash answer", "plan-dash dispatch"} {
		if reason := judge(t, work, command); reason != "" {
			t.Errorf("in the session, %q: %q", command, reason)
		}
	}
}
