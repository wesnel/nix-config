package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func TestMirrorBoundary(t *testing.T) {
	root := fakeHome(t)
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	t.Run("reviewed native reads", func(t *testing.T) {
		for _, name := range []string{"find_pages", "read_page"} {
			if reason := CheckBaseline("mcp__codex_apps__chatgpt_space__"+name, json.RawMessage(`{}`)); reason != "" {
				t.Errorf("reviewed read denied: %s", reason)
			}
		}
	})
	t.Run("protected mirror state", func(t *testing.T) {
		file := filepath.Join(agentplans.Dir(), "mirrors", "session.json")
		in, _ := json.Marshal(map[string]string{"file_path": file, "content": "{}"})
		if CheckBaseline("Write", in) == "" {
			t.Error("mirror binding is writable")
		}
		if judge(t, root, "echo '{}' > "+file) == "" {
			t.Error("shell can overwrite binding")
		}
	})
	t.Run("receipt cannot be forged by shell", func(t *testing.T) {
		for _, cmd := range []string{"plan-dash record", "env plan-dash record", "sh -c 'plan-dash record'"} {
			if judge(t, root, cmd) == "" {
				t.Errorf("shell can forge receipt: %s", cmd)
			}
		}
	})
	t.Run("session identity cannot be replaced", func(t *testing.T) {
		for _, cmd := range []string{"CODEX_THREAD_ID=other plan-dash snapshot", "env CLAUDE_CODE_SESSION_ID=other plan-dash snapshot", "export CODEX_THREAD_ID=other; plan-dash snapshot", "unset CODEX_THREAD_ID; plan-dash snapshot"} {
			if judge(t, root, cmd) == "" {
				t.Errorf("forged session allowed: %s", cmd)
			}
		}
	})
	t.Run("subagent cannot prepare or claim", func(t *testing.T) {
		for _, cmd := range []string{"plan-dash snapshot", "plan-dash claim", "plan-dash release", "sh -c 'plan-dash snapshot --open'"} {
			args, _ := json.Marshal(map[string]string{"command": cmd})
			if reason, _ := judgeInput(input{ToolName: "Bash", ToolInput: args, AgentID: "child", SessionID: "main", Cwd: root}, baseline); reason == "" {
				t.Errorf("subagent mutation allowed: %s", cmd)
			}
		}
		for tool, args := range map[string]string{"local_shell": `{"command":["plan-dash","snapshot"]}`, "Monitor": `{"command":"plan-dash snapshot"}`, "write_stdin": `{"chars":"plan-dash snapshot\n"}`} {
			if reason, _ := judgeInput(input{ToolName: tool, ToolInput: json.RawMessage(args), AgentID: "child", SessionID: "main", Cwd: root}, baseline); reason == "" {
				t.Errorf("subagent mutation allowed through %s", tool)
			}
		}
	})
}

func TestArtifactMirrorScope(t *testing.T) {
	root := fakeHome(t)
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	config := filepath.Join(root, "config", "agent-tools", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"agentPlans":{"claudePage":"https://claude.ai/artifact/aggregate"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "owner")
	judgeArtifact := func(raw string, session string) string {
		t.Helper()
		reason, _ := judgeInput(input{ToolName: "Artifact", ToolInput: json.RawMessage(raw), SessionID: session, ToolUseID: "call", Cwd: root}, baseline)
		return reason
	}
	ordinary := `{"command":"publish","url":"https://claude.ai/artifact/unrelated","file_path":"/tmp/ordinary.html"}`
	ordinaryEvent, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "owner", "tool_name": "Artifact", "tool_use_id": "ordinary", "tool_input": json.RawMessage(ordinary), "tool_response": map[string]any{"status": "published", "url": "https://claude.ai/artifact/unrelated"}})
	if err := RecordMirror(ordinaryEvent); err != nil {
		t.Errorf("ordinary post-tool receipt produced hook error: %v", err)
	}
	for _, raw := range []string{ordinary, `{"command":"list"}`, `{"command":"read"}`} {
		if reason := judgeArtifact(raw, "owner"); reason != "" {
			t.Errorf("ordinary native Artifact refused: %s", reason)
		}
		if reason := CheckBaseline("Artifact", json.RawMessage(raw)); reason != "" {
			t.Errorf("ordinary Artifact refused without hook identity: %s", reason)
		}
	}
	m, err := agentplans.PrepareMirror("claude:owner", "plan", "Current plan", "revision", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(map[string]any{"command": "publish", "file_path": m.File, "capabilities": map[string]any{}})
	if reason := CheckBaseline("Artifact", in); reason == "" {
		t.Fatal("protected snapshot allowed without native identity")
	}
	if reason := judgeArtifact(string(in), "owner"); reason != "" {
		t.Fatal(reason)
	}
	event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "owner", "tool_name": "Artifact", "tool_use_id": "call", "tool_input": json.RawMessage(in), "tool_response": map[string]any{"status": "published", "url": "https://claude.ai/artifact/mirror"}})
	if err := agentplans.RecordMirror(event); err != nil {
		t.Fatal(err)
	}
	if reason := judgeArtifact(ordinary, "owner"); reason != "" {
		t.Errorf("owning a mirror blocks unrelated publish: %s", reason)
	}
	if err := RecordMirror(ordinaryEvent); err != nil {
		t.Errorf("owning a mirror blocks unrelated post-tool receipt: %v", err)
	}
	update, _ := json.Marshal(map[string]any{"command": "publish", "url": "https://claude.ai/artifact/mirror", "file_path": m.File, "capabilities": map[string]any{}})
	if reason := judgeArtifact(string(update), "owner"); reason != "" {
		t.Fatalf("exact owned Artifact update refused: %s", reason)
	}
	if err := RecordMirror(ordinaryEvent); err != nil {
		t.Errorf("pending mirror blocks unrelated receipt: %v", err)
	}
	pending, err := agentplans.LoadMirror("claude:owner")
	if err != nil || pending.Pending == nil {
		t.Fatalf("unrelated receipt altered pending mirror: %+v %v", pending, err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "other")
	for _, url := range []string{"https://claude.ai/artifact/mirror", "https://claude.ai/code/artifact/mirror", "https://CLAUDE.ai/artifact/%6dirror/?x=y#z", "https://claude.ai/artifact/aggregate"} {
		raw, _ := json.Marshal(map[string]any{"command": "publish", "url": url, "file_path": "/tmp/ordinary.html"})
		if reason := judgeArtifact(string(raw), "other"); reason == "" {
			t.Errorf("cross-session mirror target allowed: %s", url)
		}
		if reason := CheckBaseline("Artifact", raw); reason == "" {
			t.Errorf("mirror target allowed without hook identity: %s", url)
		}
	}
	for _, raw := range []string{`{"command":"publish","url":42}`, `{"command":"publish","file_path":[]}`, `{"command":"publish","url":"://"}`, `{"command":"publish","url":"https://claude.ai/artifact/mirror","URL":"https://claude.ai/artifact/other"}`} {
		if reason := judgeArtifact(raw, "other"); reason == "" {
			t.Errorf("malformed target allowed: %s", raw)
		}
	}
	for _, cwd := range []string{"", "relative"} {
		event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Artifact", "cwd": cwd, "tool_input": map[string]any{"command": "publish", "file_path": "snapshot.html"}})
		if err := RecordMirror(event); err == nil {
			t.Errorf("relative post-tool source accepted cwd %q", cwd)
		}
	}
	protectedEvent, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "other", "tool_name": "Artifact", "tool_use_id": "wrong", "tool_input": map[string]any{"command": "publish", "url": "https://claude.ai/artifact/mirror", "file_path": "/tmp/other.html"}})
	if err := RecordMirror(protectedEvent); err == nil {
		t.Error("protected receipt mismatch ignored")
	}
	for _, linkKind := range []string{"symlink", "hardlink"} {
		alias := filepath.Join(t.TempDir(), "outside.html")
		var err error
		if linkKind == "symlink" {
			err = os.Symlink(m.File, alias)
		} else {
			err = os.Link(m.File, alias)
		}
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(map[string]any{"command": "publish", "file_path": alias})
		if reason := CheckBaseline("Artifact", raw); reason == "" {
			t.Errorf("protected %s snapshot allowed", linkKind)
		}
	}
	state := filepath.Join(agentplans.MirrorDir(), agentplans.PageHash([]byte("claude:owner"))+".json")
	if err := os.WriteFile(state, []byte(`{"broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	if reason := judgeArtifact(ordinary, "other"); reason == "" {
		t.Error("unreadable registry treated as no protected target")
	}
	if reason := judgeArtifact(`{"command":"list"}`, "other"); reason != "" {
		t.Errorf("targetless call unnecessarily reads registry: %s", reason)
	}
}

func TestMirrorNative(t *testing.T) {
	root := fakeHome(t)
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("CODEX_THREAD_ID", "session")
	m, err := agentplans.PrepareMirror("codex:session", "plan", "Current plan", "<p>plan</p>", []byte("<p>plan</p>"))
	if err != nil {
		t.Fatal(err)
	}
	create := map[string]any{"title": "Current plan", "idempotency_key": m.CreateKey}
	invoke := func(tool, call, agent string, args map[string]any) bool {
		t.Helper()
		e, _ := json.Marshal(map[string]any{"tool_name": tool, "tool_use_id": call, "session_id": "session", "turn_id": "turn", "agent_id": agent, "tool_input": args})
		var out bytes.Buffer
		if code := Main([]string{"--baseline"}, bytes.NewReader(e), &out, io.Discard); code != 0 {
			t.Fatalf("hook status %d", code)
		}
		return !bytes.Contains(out.Bytes(), []byte(`"permissionDecision":"deny"`))
	}
	const createTool = "mcp__codex_apps__chatgpt_space__create_page"
	const visualTool = "mcp__codex_apps__chatgpt_space__create_page_visualization"
	for _, key := range []string{"parent_page_id", "space_id", "initial_blocks", "coordinator_dot_id", "namespace", "position", "site_project_id"} {
		bad := map[string]any{"title": "Current plan", "idempotency_key": m.CreateKey, key: "other"}
		if invoke(createTool, "bad", "", bad) {
			t.Errorf("creation accepted %s", key)
		}
	}
	if invoke(createTool, "subagent", "child", create) {
		t.Fatal("subagent created page")
	}
	if invoke(createTool, "", "", create) {
		t.Fatal("call without identity created page")
	}
	if !invoke(createTool, "create-1", "", create) {
		t.Fatal("exact prepared creation refused")
	}
	if invoke(createTool, "create-2", "", create) {
		t.Fatal("pending creation repeated")
	}
	record := func(tool, call string, args map[string]any, response string) error {
		var r any
		if err := json.Unmarshal([]byte(response), &r); err != nil {
			t.Fatal(err)
		}
		e, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "session", "turn_id": "turn", "tool_name": tool, "tool_use_id": call, "tool_input": args, "tool_response": r})
		return agentplans.RecordMirror(e)
	}
	if err := record(createTool, "wrong", create, `{"structuredContent":{"metadata":{"page_id":"page-own"}}}`); err == nil {
		t.Fatal("wrong call bound page")
	}
	if err := record(createTool, "create-1", create, `{"isError":true,"structuredContent":{"metadata":{"page_id":"page-own"}}}`); err == nil {
		t.Fatal("error bound page")
	}
	if _, err := agentplans.PrepareMirror("codex:session", "plan", "Current plan", "replacement", []byte("replacement")); err == nil {
		t.Fatal("preparation cleared pending creation")
	}
	if err := record(createTool, "create-1", create, `{"structuredContent":{"metadata":{"page_id":"page-own"}}}`); err != nil {
		t.Fatal(err)
	}
	visual := map[string]any{"page_id": "page-own", "title": "Current plan", "html": "<p>plan</p>"}
	for key, value := range map[string]string{"page_id": "page-other", "title": "Other", "html": "arbitrary"} {
		bad := map[string]any{"page_id": "page-own", "title": "Current plan", "html": "<p>plan</p>"}
		bad[key] = value
		if invoke(visualTool, "bad", "", bad) {
			t.Errorf("visualization accepted changed %s", key)
		}
	}
	if !invoke(visualTool, "visual-1", "", visual) {
		t.Fatal("exact prepared visualization refused")
	}
	receipt := func(block, hash string, created string) string {
		return fmt.Sprintf(`{"structuredContent":{"page_id":"page-own","edit":{"operation_results":[{"status":"applied","affected_blocks":[{"block_id":%q,"hash":%q}],"created_block_ids":%s}]}}}`, block, hash, created)
	}
	if err := record(visualTool, "visual-1", visual, receipt("owned-block", "hash-1", `["owned-block"]`)); err != nil {
		t.Fatal(err)
	}
	m, err = agentplans.LoadMirror("codex:session")
	if err != nil || m.Page != "page-own" || m.Block != "owned-block" || m.BlockHash != "hash-1" || m.Pending != nil {
		t.Fatalf("receipt binding: %+v %v", m, err)
	}
	if invoke(visualTool, "append", "", visual) {
		t.Fatal("bound page allowed another append")
	}
	visual["replace_block"] = map[string]any{"block_id": "other", "expected_hash": "hash-1"}
	if invoke(visualTool, "other", "", visual) {
		t.Fatal("unowned block accepted")
	}
	visual["replace_block"] = map[string]any{"block_id": "owned-block", "expected_hash": "stale"}
	if invoke(visualTool, "stale", "", visual) {
		t.Fatal("stale block hash accepted")
	}
	visual["replace_block"] = map[string]any{"block_id": "owned-block", "expected_hash": "hash-1"}
	if !invoke(visualTool, "replace", "", visual) {
		t.Fatal("owned replacement refused")
	}
	if err := record(visualTool, "replace", visual, receipt("other", "hash-2", `[]`)); err == nil {
		t.Fatal("receipt replaced another block")
	}
	if err := record(visualTool, "replace", visual, receipt("owned-block", "hash-2", `[]`)); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"trash_page", "update_page_sharing", "edit_page", "move_page"} {
		if invoke("mcp__codex_apps__chatgpt_space__"+tool, "write", "", map[string]any{"page_id": "page-own"}) {
			t.Errorf("unreviewed mutation %s accepted", tool)
		}
	}
}
