package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMCPTools covers MCP tools as each harness's hook sends their calls:
// on a server the guard lists, reads pass, confirmed writes ask where the
// harness can ask, and every other tool is refused; on any other server,
// every tool asks in a main session that can ask and is refused elsewhere.
func TestMCPTools(t *testing.T) {
	const (
		ask  = `"permissionDecision":"ask"`
		deny = `"permissionDecision":"deny"`
	)
	claude := func(tool string) string { return `{"tool_name":"` + tool + `","tool_input":{}}` }
	codex := func(tool string) string { return `{"tool_name":"` + tool + `","turn_id":"t","tool_input":{}}` }
	for _, c := range []struct {
		name  string
		args  []string // nil for a review loop's hook
		event string
		want  string // "" to let the call through
		says  string // what a denial's reason must say
	}{
		{"Linear read", []string{"--baseline"}, claude("mcp__linear__get_issue"), "", ""},
		{"Linear write asks", []string{"--baseline"}, claude("mcp__linear__save_issue"), ask, "Linear"},
		{"Linear tool not reviewed", []string{"--baseline"}, claude("mcp__linear__delete_workspace"), deny, "delete_workspace"},
		{"Linear connector write asks", []string{"--baseline"}, claude("mcp__claude_ai_Linear__save_comment"), ask, ""},
		{"Linear document write asks", []string{"--baseline"}, claude("mcp__linear__save_document"), ask, "Linear"},
		{"Linear comment deletion", []string{"--baseline"}, claude("mcp__claude_ai_Linear__delete_comment"), deny, "delete_comment"},
		{"Linear diff merge", []string{"--baseline"}, claude("mcp__linear__merge_diff"), deny, "merge_diff"},
		{"Linear write from a reviewer", []string{"--reviewer"}, claude("mcp__linear__save_issue"), deny, "reviewer only reads"},
		{"Linear read from a reviewer", []string{"--reviewer"}, claude("mcp__linear__list_issues"), "", ""},
		{"Linear write from Codex", []string{"--baseline"}, codex("mcp__linear__save_issue"), deny, "Claude Code"},
		{"Linear read from Codex", []string{"--baseline"}, codex("mcp__linear__get_issue"), "", ""},
		{"Notion read", []string{"--baseline"}, claude("mcp__notion__notion-fetch"), "", ""},
		{"Notion write asks", []string{"--baseline"}, claude("mcp__notion__notion-create-pages"), ask, "Notion"},
		{"Notion connector write asks", []string{"--baseline"}, claude("mcp__claude_ai_Notion__notion-update-page"), ask, ""},
		{"Notion page deletion", []string{"--baseline"}, claude("mcp__notion__notion-delete-pages"), deny, "notion-delete-pages"},
		{"Notion database deletion", []string{"--baseline"}, claude("mcp__claude_ai_Notion__notion-delete-databases"), deny, "notion-delete-databases"},
		{"Notion comment deletion", []string{"--baseline"}, claude("mcp__notion__notion-delete-comment"), deny, "notion-delete-comment"},
		{"Hex read", []string{"--baseline"}, claude("mcp__hex__get_cell_output"), "", ""},
		{"Hex run asks", []string{"--baseline"}, claude("mcp__hex__run_cell"), ask, "Hex"},
		{"Hex run from Codex", []string{"--baseline"}, codex("mcp__hex__run_cell"), deny, "Claude Code"},
		{"Hex cell deletion", []string{"--baseline"}, claude("mcp__hex__delete_cell"), deny, "delete_cell"},
		{"Datadog read", []string{"--baseline"}, claude("mcp__datadog__search_datadog_logs"), "", ""},
		{"Datadog notebook write", []string{"--baseline"}, claude("mcp__datadog__create_datadog_notebook"), deny, "create_datadog_notebook"},
		{"Datadog connector read", []string{"--baseline"}, claude("mcp__claude_ai_Datadog_2__get_datadog_trace"), "", ""},
		{"Buildkite read", []string{"--baseline"}, claude("mcp__buildkite__get_build"), "", ""},
		{"Buildkite write", []string{"--baseline"}, claude("mcp__buildkite__create_build"), deny, "create_build"},
		{"Buildkite token", []string{"--baseline"}, claude("mcp__buildkite__access_token"), deny, "access_token"},
		{"Buildkite cluster secret", []string{"--baseline"}, claude("mcp__buildkite__get_cluster_secret"), deny, "get_cluster_secret"},
		{"Buildkite cluster secrets from the connector", []string{"--baseline"}, claude("mcp__claude_ai_mcp_buildkite_com__list_cluster_secrets"), deny, "list_cluster_secrets"},
		{"Slack read", []string{"--baseline"}, claude("mcp__claude_ai_Slack__slack_read_thread"), "", ""},
		{"Slack message", []string{"--baseline"}, claude("mcp__claude_ai_Slack__slack_send_message"), deny, "slack_send_message"},
		{"Slack draft", []string{"--baseline"}, claude("mcp__claude_ai_Slack__slack_send_message_draft"), deny, ""},
		{"ngrok read", []string{"--baseline"}, claude("mcp__ngrok__list_endpoints"), "", ""},
		{"ngrok read with a writing verb", []string{"--baseline"}, claude("mcp__ngrok__get_and_delete_endpoint"), deny, "get_and_delete_endpoint"},
		{"ngrok write", []string{"--baseline"}, claude("mcp__ngrok__create_endpoint"), deny, ""},
		{"ngrok connector write", []string{"--baseline"}, claude("mcp__claude_ai_mcp_ngrok_com__update_domain"), deny, ""},
		// A server mcp.json does not list has had none of its tools reviewed.
		{"Gmail read asks", []string{"--baseline"}, claude("mcp__claude_ai_Gmail__search_threads"), ask, "mcp.json"},
		{"Gmail read from Codex", []string{"--baseline"}, codex("mcp__claude_ai_Gmail__search_threads"), deny, "mcp.json"},
		{"GitHub connector read asks", []string{"--baseline"}, claude("mcp__claude_ai_Github__get_pull_request"), ask, "mcp.json"},
		{"GitHub connector read from Codex", []string{"--baseline"}, codex("mcp__claude_ai_Github__get_pull_request"), deny, "mcp.json"},
		{"GitHub connector read from a reviewer", []string{"--reviewer"}, claude("mcp__claude_ai_Github__get_pull_request"), deny, "mcp.json"},
		{"unlisted server asks", []string{"--baseline"}, claude("mcp__somewhere__get_thing"), ask, "mcp.json"},
		{"unlisted plugin server asks", []string{"--baseline"}, claude("mcp__plugin_x_files__write_file"), ask, "mcp.json"},
		{"unlisted server from Codex", []string{"--baseline"}, codex("mcp__somewhere__get_thing"), deny, "mcp.json"},
		{"unlisted server from a reviewer", []string{"--reviewer"}, claude("mcp__somewhere__get_thing"), deny, "mcp.json"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			Main(c.args, strings.NewReader(c.event), &out, io.Discard)
			if c.want == "" {
				if out.Len() != 0 {
					t.Errorf("%s: printed %s, want nothing", c.event, out.String())
				}
				return
			}
			if !strings.Contains(out.String(), c.want) {
				t.Fatalf("%s: printed %s, want %s", c.event, out.String(), c.want)
			}
			var printed struct {
				Hook struct {
					Reason string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			json.Unmarshal(out.Bytes(), &printed)
			reason := printed.Hook.Reason
			if !strings.Contains(reason, c.says) || !saysInstead(reason) {
				t.Errorf("%s: reason %q does not say %q and what to do instead", c.event, reason, c.says)
			}
		})
	}
}

// TestMCPListsMatchServer holds each service with a recorded tools/list in
// testdata/tools (SERVICE.json, by its key in mcp.json) to that list: every
// tool the server offers is a read, a confirmed write or a refused tool,
// and every tool mcp.json names is one the server offers. A tool the server
// adds fails here until it is reviewed into one of the three lists.
func TestMCPListsMatchServer(t *testing.T) {
	lists := mcpLists(t)
	files, err := filepath.Glob(filepath.Join("testdata", "tools", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no recorded tools/list under testdata/tools: %v", err)
	}
	for _, file := range files {
		id := strings.TrimSuffix(filepath.Base(file), ".json")
		t.Run(id, func(t *testing.T) {
			l, ok := lists[id]
			if !ok {
				t.Fatalf("%s: mcp.json has no service %s", file, id)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var recorded struct {
				Tools []struct{ Name string }
			}
			if err := json.Unmarshal(data, &recorded); err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			offered := map[string]bool{}
			for _, tool := range recorded.Tools {
				offered[tool.Name] = true
			}
			listed := map[string]bool{}
			for _, names := range [][]string{l.Reads, l.Confirms, l.Refuses} {
				for _, name := range names {
					listed[name] = true
					if !offered[name] {
						t.Errorf("%s names %s, which the server does not offer", id, name)
					}
				}
			}
			for _, tool := range recorded.Tools {
				if !listed[tool.Name] {
					t.Errorf("the %s server offers %s, which is in none of reads, confirms and refuses", id, tool.Name)
				}
			}
		})
	}
}

// mcpLists is each service's three lists as mcp.json writes them, refuses
// included, which the guard does not read: a refused tool is refused as
// any tool in neither of the others is.
func mcpLists(t *testing.T) map[string]struct{ Reads, Confirms, Refuses []string } {
	t.Helper()
	var lists map[string]struct{ Reads, Confirms, Refuses []string }
	if err := json.Unmarshal(mcpJSON, &lists); err != nil {
		t.Fatal(err)
	}
	return lists
}

// TestLinearRefuses holds Linear's destructive tools, every delete_* and
// merge_diff it offers, to refuses alone, and each refused tool to a denial
// on both of Linear's servers.
func TestLinearRefuses(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "tools", "linear.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		Tools []struct{ Name string }
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	l := mcpLists(t)["linear"]
	refused, elsewhere := set(l.Refuses...), set(append(l.Reads, l.Confirms...)...)
	destructive := 0
	for _, tool := range recorded.Tools {
		if strings.HasPrefix(tool.Name, "delete_") || tool.Name == "merge_diff" {
			destructive++
			if !refused[tool.Name] || elsewhere[tool.Name] {
				t.Errorf("%s: in refuses %v, in reads or confirms %v; want refuses alone", tool.Name, refused[tool.Name], elsewhere[tool.Name])
			}
		}
	}
	if destructive == 0 {
		t.Fatal("the recording offers no delete_* or merge_diff tool")
	}
	for _, name := range l.Refuses {
		for _, server := range []string{"linear", "claude_ai_Linear"} {
			var out bytes.Buffer
			Main([]string{"--baseline"}, strings.NewReader(`{"tool_name":"mcp__`+server+`__`+name+`","tool_input":{}}`), &out, io.Discard)
			if !strings.Contains(out.String(), `"permissionDecision":"deny"`) || !strings.Contains(out.String(), name) {
				t.Errorf("mcp__%s__%s: printed %s, want a denial naming it", server, name, out.String())
			}
		}
	}
}

// TestMCPListsDisjoint holds every listed server to tools that are in at
// most one of reads, confirms and refuses, and to lists mcp-proxy can use:
// an empty allow list there exposes every tool.
func TestMCPListsDisjoint(t *testing.T) {
	for id, l := range mcpLists(t) {
		in := map[string]string{}
		for list, names := range map[string][]string{"reads": l.Reads, "confirms": l.Confirms, "refuses": l.Refuses} {
			for _, name := range names {
				if other, ok := in[name]; ok {
					t.Errorf("%s: %s is in both %s and %s", id, name, other, list)
				}
				in[name] = list
			}
		}
	}
	for id, s := range mcpServices {
		if !s.readsByName && len(s.reads) == 0 {
			t.Errorf("%s: lists no reads", id)
		}
	}
}
