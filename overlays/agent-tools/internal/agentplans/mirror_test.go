package agentplans

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func TestMain(m *testing.M) { gitenv.Isolate(); os.Exit(m.Run()) }

func mirrorHome(t *testing.T, harness, id string) {
	t.Helper()
	dir := t.TempDir()
	hostpaths.Bake(t, filepath.Join(dir, "config"), filepath.Join(dir, "state"))
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	if harness == "codex" {
		t.Setenv("CODEX_THREAD_ID", id)
	} else {
		t.Setenv("CLAUDE_CODE_SESSION_ID", id)
	}
	config := filepath.Join(dir, "config", "agent-tools", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"agentPlans":{"claudePage":"https://claude.ai/artifact/aggregate"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_TOOLS_CONFIG", config)
}

func TestMirrorPreparation(t *testing.T) {
	mirrorHome(t, "codex", "one")
	m, err := PrepareMirror("codex:one", "plan", "Current plan", "first", []byte("first"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Backend != "space" || m.Page != "" || m.Pending != nil {
		t.Fatalf("new mirror: %+v", m)
	}
	n, err := PrepareMirror("codex:one", "plan", "Current plan", "second", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if m.File != n.File || m.CreateKey != n.CreateKey || m.Hash == n.Hash {
		t.Fatalf("unstable preparation: %+v -> %+v", m, n)
	}
	if _, err := PrepareMirror("codex:other", "plan", "Current plan", "other", []byte("other")); err == nil {
		t.Fatal("another session prepared")
	}
	if _, err := PrepareMirror("codex:one", "plan", "other", "other", []byte("other")); err == nil {
		t.Fatal("title rebound")
	}
	t.Setenv("CODEX_THREAD_ID", "two")
	other, err := PrepareMirror("codex:two", "plan", "Current plan", "third", []byte("third"))
	if err != nil {
		t.Fatal(err)
	}
	if other.File == m.File || other.CreateKey == m.CreateKey {
		t.Fatal("sessions share state or creation key")
	}
	t.Setenv("CODEX_THREAD_ID", "one")
	b, err := os.ReadFile(n.File)
	if err != nil || string(b) != "second" {
		t.Fatalf("first session changed: %s %v", b, err)
	}
}

func TestAggregateOwner(t *testing.T) {
	mirrorHome(t, "claude", "one")
	current, err := PrepareMirror("claude:one", "plan", "Current plan", "current", []byte("current"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareAggregate("claude:one", "all", []byte("all")); err == nil {
		t.Fatal("unowned aggregate prepared")
	}
	for range 2 {
		if status, err := ClaimAggregate("claude:one"); err != nil || status != "claimed" {
			t.Fatalf("claim: %s %v", status, err)
		}
	}
	agg, err := PrepareAggregate("claude:one", "all", []byte("all"))
	if err != nil {
		t.Fatal(err)
	}
	if agg.File == current.File || agg.Page != "https://claude.ai/artifact/aggregate" || agg.Title != "All open plan items" {
		t.Fatalf("aggregate: %+v", agg)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "two")
	if status, err := ClaimAggregate("claude:two"); err != nil || status != "busy" {
		t.Fatalf("other claim: %s %v", status, err)
	}
	if _, err := PrepareAggregate("claude:two", "other", []byte("other")); err == nil {
		t.Fatal("other owner prepared")
	}
	if err := ReleaseAggregate("claude:two"); err == nil {
		t.Fatal("other owner released")
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "one")
	in, _ := json.Marshal(map[string]any{"command": "publish", "url": agg.Page, "file_path": agg.File, "capabilities": map[string]any{}})
	if err := BeginMirror("claude:one", "Artifact", "publish", in); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseAggregate("claude:one"); err == nil {
		t.Fatal("pending aggregate released")
	}
	if _, err := PrepareAggregate("claude:one", "replacement", []byte("replacement")); err == nil {
		t.Fatal("pending aggregate overwritten")
	}
	event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "one", "tool_name": "Artifact", "tool_use_id": "publish", "tool_input": json.RawMessage(in), "tool_response": map[string]any{"status": "published", "url": "https://claude.ai/artifact/aggregate"}})
	if err := RecordMirror(event); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseAggregate("claude:one"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "two")
	if status, err := ClaimAggregate("claude:two"); err != nil || status != "claimed" {
		t.Fatalf("claim after release: %s %v", status, err)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "one")
	loaded, err := LoadMirror("claude:one")
	if err != nil || loaded.Pending != nil || loaded.File != current.File {
		t.Fatalf("aggregate altered current: %+v %v", loaded, err)
	}
}

func TestMirrorArtifact(t *testing.T) {
	mirrorHome(t, "claude", "one")
	m, err := PrepareMirror("claude:one", "plan", "Current plan", "snapshot", []byte("snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string]map[string]any{
		"arbitrary target":    {"command": "publish", "url": "https://claude.ai/artifact/other", "file_path": m.File},
		"arbitrary source":    {"command": "publish", "file_path": "/tmp/unprepared.html"},
		"database capability": {"command": "publish", "file_path": m.File, "capabilities": map[string]any{"db": true}},
	} {
		t.Run(name, func(t *testing.T) {
			in, _ := json.Marshal(args)
			if err := BeginMirror("claude:one", "Artifact", "call", in); err == nil {
				t.Fatal("unprepared Artifact write allowed")
			}
		})
	}
	in, _ := json.Marshal(map[string]any{"command": "publish", "file_path": m.File, "capabilities": map[string]any{}})
	if err := BeginMirror("claude:one", "Artifact", "call", in); err != nil {
		t.Fatal(err)
	}
	event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "one", "tool_name": "Artifact", "tool_use_id": "call", "tool_input": json.RawMessage(in), "tool_response": map[string]any{"unknown": "success"}})
	if err := RecordMirror(event); err == nil {
		t.Fatal("unknown Artifact receipt accepted")
	}
	loaded, err := LoadMirror("claude:one")
	if err != nil || loaded.Pending == nil {
		t.Fatalf("unknown outcome lost pending state: %+v %v", loaded, err)
	}
	event, _ = json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "one", "tool_name": "Artifact", "tool_use_id": "call", "tool_input": json.RawMessage(in), "tool_response": map[string]any{"status": "published", "url": "https://claude.ai/artifact/current"}})
	if err := RecordMirror(event); err != nil {
		t.Fatal(err)
	}
	loaded, err = LoadMirror("claude:one")
	if err != nil || loaded.Pending != nil || loaded.Page != "https://claude.ai/artifact/current" {
		t.Fatalf("published Artifact not bound: %+v %v", loaded, err)
	}
}

func TestMirrorRevision(t *testing.T) {
	for _, scope := range []string{"current", "aggregate"} {
		t.Run(scope, func(t *testing.T) {
			mirrorHome(t, "claude", "one")
			if scope == "aggregate" {
				if _, err := ClaimAggregate("claude:one"); err != nil {
					t.Fatal(err)
				}
			}
			prepare := func(revision, html string) Mirror {
				t.Helper()
				var m Mirror
				var err error
				if scope == "aggregate" {
					m, err = PrepareAggregate("claude:one", revision, []byte(html))
				} else {
					m, err = PrepareMirror("claude:one", "plan", "Current plan", revision, []byte(html))
				}
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			first := prepare("revision-one", "content one; refreshed at 10:00")
			args := map[string]any{"command": "publish", "file_path": first.File, "capabilities": map[string]any{}}
			url := first.Page
			if url != "" {
				args["url"] = url
			} else {
				url = "https://claude.ai/artifact/current"
			}
			input, _ := json.Marshal(args)
			if err := BeginMirror("claude:one", "Artifact", "publish", input); err != nil {
				t.Fatal(err)
			}
			var pendingErr error
			if scope == "aggregate" {
				_, pendingErr = PrepareAggregate("claude:one", "revision-one", []byte("new timestamp"))
			} else {
				_, pendingErr = PrepareMirror("claude:one", "plan", "Current plan", "revision-one", []byte("new timestamp"))
			}
			if pendingErr == nil {
				t.Fatal("same revision bypassed pending publication")
			}
			event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "one", "tool_name": "Artifact", "tool_use_id": "publish", "tool_input": json.RawMessage(input), "tool_response": map[string]any{"status": "published", "url": url}})
			if err := RecordMirror(event); err != nil {
				t.Fatal(err)
			}
			repeated := prepare("revision-one", "content one; refreshed at 10:01")
			b, err := os.ReadFile(repeated.File)
			if err != nil || string(b) != "content one; refreshed at 10:00" || repeated.Hash != first.Hash || repeated.Revision != "revision-one" || repeated.PublishedHash != first.Hash || repeated.Page != url {
				t.Fatalf("same revision replaced timestamp: %q %+v %v", b, repeated, err)
			}
			changed := prepare("revision-two", "content two; refreshed at 10:02")
			b, err = os.ReadFile(changed.File)
			if err != nil || string(b) != "content two; refreshed at 10:02" || changed.Hash == first.Hash || changed.Revision != "revision-two" {
				t.Fatalf("changed revision reused snapshot: %q %+v %v", b, changed, err)
			}
			if err := os.WriteFile(changed.File, []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
			repaired := prepare("revision-two", "content two; refreshed at 10:03")
			b, err = os.ReadFile(repaired.File)
			if err != nil || string(b) != "content two; refreshed at 10:03" || repaired.Hash == changed.Hash {
				t.Fatalf("corrupted snapshot reused: %q %+v %v", b, repaired, err)
			}
			if scope == "aggregate" {
				if err := ReleaseAggregate("claude:one"); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CLAUDE_CODE_SESSION_ID", "two")
				if _, err := ClaimAggregate("claude:two"); err != nil {
					t.Fatal(err)
				}
				transferred, err := PrepareAggregate("claude:two", "revision-two", []byte("content two; refreshed at 10:04"))
				if err != nil || transferred.Session != "claude:two" || transferred.Hash != repaired.Hash {
					t.Fatalf("owner transition failed: %+v %v", transferred, err)
				}
				b, err = os.ReadFile(transferred.File)
				if err != nil || string(b) != "content two; refreshed at 10:03" {
					t.Fatalf("owner transition replaced bytes: %q %v", b, err)
				}
			}
		})
	}
}

func TestMirrorPlanTransition(t *testing.T) {
	mirrorHome(t, "claude", "one")
	first, err := PrepareMirror("claude:one", "plan-A", "Current plan", "A", []byte("plan A"))
	if err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(map[string]any{"command": "publish", "file_path": first.File, "capabilities": map[string]any{}})
	if err := BeginMirror("claude:one", "Artifact", "publish-A", input); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareMirror("claude:one", "plan-B", "Current plan", "B", []byte("plan B")); err == nil {
		t.Fatal("pending publication allowed plan transition")
	}
	event, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": "one", "tool_name": "Artifact", "tool_use_id": "publish-A", "tool_input": json.RawMessage(input), "tool_response": map[string]any{"status": "published", "url": "https://claude.ai/artifact/current"}})
	if err := RecordMirror(event); err != nil {
		t.Fatal(err)
	}
	next, err := PrepareMirror("claude:one", "plan-B", "Current plan", "B", []byte("plan B"))
	if err != nil {
		t.Fatalf("same session cannot advance to next plan: %v", err)
	}
	if next.Plan != "plan-B" || next.Page != "https://claude.ai/artifact/current" || next.File != first.File || next.CreateKey != first.CreateKey || next.PublishedHash != first.Hash || next.Hash == first.Hash {
		t.Fatalf("plan transition changed destination or lost publication state: %+v", next)
	}
	if _, err := PrepareMirror("claude:one", "plan-C", "Changed title", "C", []byte("plan C")); err == nil {
		t.Fatal("plan transition changed fixed title")
	}
}
