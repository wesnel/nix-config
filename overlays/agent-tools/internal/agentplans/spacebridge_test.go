package agentplans

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func TestMain(m *testing.M) { gitenv.Isolate(); os.Exit(m.Run()) }

func spaceFixture(t *testing.T) (SpaceTarget, []byte) {
	t.Helper()
	root := t.TempDir()
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	if err := os.MkdirAll(filepath.Dir(SpaceConfigFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(SpaceConfigFile(), []byte(`{"page_id":"page_owned","block_id":"block_owned","thread_id":"thread_owned"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	target, err := LoadSpaceTarget()
	if err != nil {
		t.Fatal(err)
	}
	html := bytes.Replace(Page, []byte("<!-- snapshot -->"), []byte(`<script type="application/json" id="snapshot">{"plans":[],"github":{},"meta":{},"today":{}}</script>`), 1)
	return target, html
}

// Pages is an external authenticated service. These responses use its observed
// read_page/create_page_visualization schemas and the app-server JSON-RPC
// envelopes; validateBridgeRequests checks every request against those shapes.
func fakeSpaceRPC(t *testing.T, mode string, saved bool) (*spaceRPC, *bytes.Buffer) {
	t.Helper()
	responses := []string{`{"jsonrpc":"2.0","id":1,"result":{}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thread_owned"}}}`,
		`{"jsonrpc":"2.0","id":3,"result":{"data":[{"name":"codex_apps","authStatus":"oAuth","runtimeStatus":"connected","resourceTemplates":[],"resources":[],"tools":{"read":{"name":"chatgpt_space__read_page","inputSchema":{"type":"object","properties":{"page_id":{"type":"string"},"block_ids":{"type":"array"}},"required":["page_id"]}},"write":{"name":"chatgpt_space__create_page_visualization","inputSchema":{"type":"object","properties":{"page_id":{"type":"string"},"replace_block":{"type":"object"},"base_sequence":{"type":"integer"},"title":{"type":"string"},"html":{"type":"string"}},"required":["page_id","html","title"]}}}}],"nextCursor":null}}`}
	read := func(id int, saved bool) string {
		t.Helper()
		hash, reference := "old_hash", "old_file"
		if saved {
			hash, reference = "new_hash", "new_file"
		}
		body := map[string]any{"content": map[string]any{"page_id": "page_owned", "schema_version": 2, "title": "Dashboard", "title_hash": "title_hash", "through_sequence": 9, "blocks": []any{map[string]any{"id": "block_owned", "hash": hash, "kind": "markdown", "markdown": "![Agent plans](visualize:" + reference + ")"}}}, "metadata": map[string]any{"page_id": "page_owned", "stream_kind": "content", "url": "https://chatgpt.com/"}, "selection": map[string]any{"selection_complete": true}}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"content": []any{}, "structuredContent": body}})
		return string(b)
	}
	responses = append(responses, read(4, saved))
	write := `{"jsonrpc":"2.0","id":5,"result":{"content":[],"structuredContent":{"page_id":"page_owned","title":"Agent plans","file_reference":"new_file","markdown":"![Agent plans](visualize:new_file)","edit":{"new_sequence":10,"operation_results":[{"operation_index":0,"status":"applied","ignored_reason":null,"error_code":null,"affected_blocks":[{"block_id":"block_owned","hash":"new_hash"}],"created_block_ids":[],"continuation_position":null,"preceding_block_id":null}]}}}}`
	switch mode {
	case "approval":
		write = `{"jsonrpc":"2.0","id":100,"method":"item/commandExecution/requestApproval","params":{}}`
	case "unknown":
		write = `{"jsonrpc":"2.0","id":5,"error":{"code":-32000,"message":"connection lost"}}`
	case "conflict":
		write = strings.Replace(write, `"new_sequence":10,"operation_results":[{"operation_index":0,"status":"applied","ignored_reason":null,"error_code":null,"affected_blocks":[{"block_id":"block_owned","hash":"new_hash"}],"created_block_ids":[],"continuation_position":null,"preceding_block_id":null}]`, `"error":{"code":"edit_conflict","commit_status":"not_committed","message":"hash changed"}`, 1)
	case "saved_unknown_top":
		write = strings.Replace(write, `"new_sequence":10,"operation_results":[{"operation_index":0,"status":"applied","ignored_reason":null,"error_code":null,"affected_blocks":[{"block_id":"block_owned","hash":"new_hash"}],"created_block_ids":[],"continuation_position":null,"preceding_block_id":null}]`, `"error":{"code":"edit_conflict","commit_status":"unknown","message":"save uncertain"}`, 1)
	case "saved_unknown", "error_bad_reference", "error_wrong_page":
		write = `{"jsonrpc":"2.0","id":5,"result":{"isError":true,"content":[],"structuredContent":{"error":{"code":"save_failed","commit_status":"unknown","message":"save uncertain","visualization":{"file_reference":"new_file","markdown":"![Agent plans](visualize:new_file)"}}}}}`
		if mode == "error_bad_reference" {
			write = strings.Replace(write, `"file_reference":"new_file"`, `"file_reference":"other_file"`, 1)
		}
		if mode == "error_wrong_page" {
			write = strings.Replace(write, `"structuredContent":{`, `"structuredContent":{"page_id":"page_elsewhere",`, 1)
		}
	case "ignored":
		write = strings.Replace(write, `"ignored_reason":null`, `"ignored_reason":"block_deleted"`, 1)
	case "wrong_block":
		write = strings.Replace(write, `"block_id":"block_owned"`, `"block_id":"block_elsewhere"`, 1)
	case "bad_reference":
		write = strings.Replace(write, `"file_reference":"new_file"`, `"file_reference":"other_file"`, 1)
	}
	responses = append(responses, write, read(6, mode != "conflict" && mode != "ignored"))
	var sent bytes.Buffer
	return newSpaceRPC(strings.NewReader(strings.Join(responses, "\n")+"\n"), &sent), &sent
}

func validateBridgeRequests(t *testing.T, sent *bytes.Buffer) (reads, writes int) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(sent.Bytes()))
	for d.More() {
		var req struct {
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := d.Decode(&req); err != nil {
			t.Fatal(err)
		}
		switch req.Method {
		case "initialize":
			if len(req.Params) != 2 {
				t.Fatal("initialization added an undocumented field")
			}
			var client map[string]string
			var capabilities map[string]bool
			if json.Unmarshal(req.Params["clientInfo"], &client) != nil || len(client) != 2 || client["name"] != "plan-dash" || client["version"] != "1" {
				t.Fatal("invalid client identity")
			}
			if json.Unmarshal(req.Params["capabilities"], &capabilities) != nil || len(capabilities) != 2 || !capabilities["experimentalApi"] || !capabilities["explicitGatewayOauth"] {
				t.Fatal("bridge changed its authentication or experimental capability contract")
			}
		case "initialized":
			if len(req.Params) != 0 {
				t.Fatal("initialized notification supplied parameters")
			}
		case "thread/resume":
			if len(req.Params) != 2 || string(req.Params["threadId"]) != `"thread_owned"` || string(req.Params["excludeTurns"]) != "true" {
				t.Fatal("resume changed thread, permissions or history contract")
			}
		case "mcpServerStatus/list":
			if len(req.Params) != 3 || string(req.Params["threadId"]) != `"thread_owned"` || string(req.Params["serverName"]) != `"codex_apps"` || string(req.Params["detail"]) != `"toolsAndAuthOnly"` {
				t.Fatal("discovery escaped the configured thread and server")
			}
		case "mcpServer/tool/call":
			if len(req.Params) != 4 || string(req.Params["server"]) != `"codex_apps"` || string(req.Params["threadId"]) != `"thread_owned"` {
				t.Fatalf("invalid bridge envelope: %+v", req.Params)
			}
			var args map[string]json.RawMessage
			if err := json.Unmarshal(req.Params["arguments"], &args); err != nil {
				t.Fatal(err)
			}
			if string(args["page_id"]) != `"page_owned"` {
				t.Fatal("tool escaped configured Page")
			}
			switch string(req.Params["tool"]) {
			case `"chatgpt_space__read_page"`:
				reads++
				if len(args) != 2 || string(args["block_ids"]) != `["block_owned"]` {
					t.Fatalf("read shape: %+v", args)
				}
			case `"chatgpt_space__create_page_visualization"`:
				writes++
				if len(args) != 5 || string(args["title"]) != `"Agent plans"` || string(args["base_sequence"]) != "9" {
					t.Fatalf("write shape: %+v", args)
				}
				var replace map[string]string
				if err := json.Unmarshal(args["replace_block"], &replace); err != nil || len(replace) != 2 || replace["block_id"] != "block_owned" || replace["expected_hash"] != "old_hash" {
					t.Fatalf("replace guard: %+v %v", replace, err)
				}
			default:
				t.Fatalf("unreviewed tool %s", req.Params["tool"])
			}
		default:
			t.Fatalf("unexpected method, including any model call: %s", req.Method)
		}
	}
	return reads, writes
}

func TestSpacePublication(t *testing.T) {
	for _, mode := range []string{"success", "approval", "unknown", "conflict", "saved_unknown", "saved_unknown_top", "error_bad_reference", "error_wrong_page", "ignored", "wrong_block", "bad_reference"} {
		t.Run(mode, func(t *testing.T) {
			target, html := spaceFixture(t)
			now := func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }
			rpc, sent := fakeSpaceRPC(t, mode, false)
			receipt, err := publishSpace(target, html, rpc, SpaceStateFile(), now)
			_, writes := validateBridgeRequests(t, sent)
			if writes != 1 {
				t.Fatalf("uploads=%d, want one", writes)
			}
			if (err == nil) != (mode == "success") {
				t.Fatalf("mode %s: receipt %+v error %v", mode, receipt, err)
			}
			var state spaceState
			data, readErr := os.ReadFile(SpaceStateFile())
			if readErr != nil || json.Unmarshal(data, &state) != nil {
				t.Fatalf("journal: %s %v", data, readErr)
			}
			if mode == "success" {
				if state.Pending != nil || state.Published == nil || receipt.PageID != "page_owned" || receipt.BlockID != "block_owned" || receipt.BlockHash != "new_hash" || !receipt.VerifiedAt.Equal(now()) {
					t.Fatalf("bad verified receipt: %s", data)
				}
			} else if state.Pending == nil || state.Published != nil {
				t.Fatalf("failure discarded pending state or claimed publication: %s", data)
			}
			recovers := mode == "success" || mode == "saved_unknown" || mode == "saved_unknown_top"
			if recovers || mode == "unknown" || mode == "conflict" || strings.HasPrefix(mode, "error_") {
				rpc, sent = fakeSpaceRPC(t, "success", recovers || strings.HasPrefix(mode, "error_"))
				_, err = publishSpace(target, html, rpc, SpaceStateFile(), now)
				reads, writes := validateBridgeRequests(t, sent)
				if reads != 1 || writes != 0 {
					t.Fatalf("retry reads=%d uploads=%d; must read before deciding without another upload", reads, writes)
				}
				if (err == nil) != recovers {
					t.Fatalf("recovery: %v", err)
				}
			}
		})
	}
}

func TestSpacePendingAuthorization(t *testing.T) {
	target, html := spaceFixture(t)
	args := SpaceVisualization{PageID: target.PageID, Title: SpaceTitle, HTML: string(html)}
	args.ReplaceBlock.BlockID, args.ReplaceBlock.ExpectedHash = target.BlockID, "observed"
	input, _ := json.Marshal(args)
	if err := AuthorizeSpaceSnapshot(input); err != nil {
		t.Fatalf("valid upload without a pending publication: %v", err)
	}
	if err := saveSpaceState(SpaceStateFile(), spaceState{Pending: &SpaceReceipt{PageID: target.PageID, BlockID: target.BlockID, SnapshotHash: PageHash(html), BlockHash: "observed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSpaceState(SpaceStateFile()); err != nil {
		t.Fatalf("pending fixture must be valid: %v", err)
	}
	if err := AuthorizeSpaceSnapshot(input); err == nil {
		t.Fatal("pending publication allowed another publisher upload")
	}
}

func TestSpaceKeepsPublished(t *testing.T) {
	target, html := spaceFixture(t)
	prior := &SpaceReceipt{PageID: "page_owned", BlockID: "block_owned", SnapshotHash: "prior_snapshot", BlockHash: "old_hash", Markdown: "![Agent plans](visualize:old_file)", VerifiedAt: time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)}
	if err := saveSpaceState(SpaceStateFile(), spaceState{Published: prior}); err != nil {
		t.Fatal(err)
	}
	rpc, sent := fakeSpaceRPC(t, "unknown", false)
	if _, err := publishSpace(target, html, rpc, SpaceStateFile(), time.Now); err == nil {
		t.Fatal("unknown save succeeded")
	}
	validateBridgeRequests(t, sent)
	data, err := os.ReadFile(SpaceStateFile())
	var state spaceState
	if err != nil || json.Unmarshal(data, &state) != nil || state.Published == nil || *state.Published != *prior || state.Pending == nil {
		t.Fatalf("last-good receipt changed: %s (%v)", data, err)
	}
}

func TestSpaceCorruptJournal(t *testing.T) {
	target, html := spaceFixture(t)
	if err := saveSpaceState(SpaceStateFile(), spaceState{}); err != nil {
		t.Fatal(err)
	}
	rpc, sent := fakeSpaceRPC(t, "success", false)
	if _, err := publishSpace(target, html, rpc, SpaceStateFile(), time.Now); err == nil || sent.Len() != 0 {
		t.Fatalf("empty journal allowed bridge activity: error %v, sent %d bytes", err, sent.Len())
	}
}
