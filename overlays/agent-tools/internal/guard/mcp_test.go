package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/hostpaths"
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
		{"Pages native write", []string{"--baseline"}, claude("mcp__codex_apps__chatgpt_space__create_page_visualization"), deny, "plan-dash space publish"},
		{"Pages native write from Codex", []string{"--baseline"}, codex("mcp__codex_apps__chatgpt_space_create_page_visualization"), deny, "plan-dash space publish"},
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

func TestSpaceTools(t *testing.T) {
	root := t.TempDir()
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	config := filepath.Join(root, "config", "agent-tools", "agent-plans-space.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, []byte(`{"page_id":"page_owned","block_id":"block_owned","thread_id":"thread_owned"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	html := strings.Replace(string(agentplans.Page), "<!-- snapshot -->", `<script type="application/json" id="snapshot">{"plans":[],"github":{},"meta":{},"today":{}}</script>`, 1)
	write := map[string]any{"page_id": "page_owned", "replace_block": map[string]string{"block_id": "block_owned", "expected_hash": "observed"}, "title": "Agent plans", "html": html}
	for _, tc := range []struct {
		name, tool string
		args       any
		allow      bool
	}{
		{"raw read", "chatgpt_space__read_page", map[string]string{"page_id": "page_owned"}, true},
		{"normalized read", "chatgpt_space_read_page", map[string]string{"page_id": "page_owned"}, true},
		{"owned snapshot", "chatgpt_space__create_page_visualization", write, false},
		{"normalized owned snapshot", "chatgpt_space_create_page_visualization", write, false},
		{"unrelated app", "sites_get_site", map[string]string{}, false},
		{"invented name", "chatgpt_space___read_page", map[string]string{}, false},
		{"new page", "chatgpt_space_create_page", map[string]string{}, false},
		{"page edit", "chatgpt_space_edit_page", map[string]string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, _ := json.Marshal(tc.args)
			reason := CheckBaseline("mcp__codex_apps__"+tc.tool, input)
			if (reason == "") != tc.allow {
				t.Fatalf("allow=%v, reason=%s", tc.allow, reason)
			}
		})
	}
	for i, change := range []map[string]any{
		{"page_id": "page_other"}, {"replace_block": map[string]string{"block_id": "block_other", "expected_hash": "observed"}},
		{"replace_block": nil}, {"after_block_id": "block_other"}, {"html": "<script>fetch('https://example.com')</script>"},
		{"html": strings.Replace(html, `"plans":[]`, `"plans":["</script><script>bad()</script>"]`, 1)},
		{"html": strings.Replace(html, `"plans":[]`, `"Plans":[]`, 1)},
		{"title": "Other"}, {"base_sequence": -1}, {"approval": true},
	} {
		args := map[string]any{}
		for k, v := range write {
			args[k] = v
		}
		for k, v := range change {
			args[k] = v
		}
		input, _ := json.Marshal(args)
		if err := agentplans.AuthorizeSpaceSnapshot(input); err == nil {
			t.Errorf("allowed changed snapshot arguments at case %d", i)
		}
	}
	for _, tc := range []struct {
		name, plans string
		allow       bool
	}{
		{"valid ID", `[{"id":"plan-one"}]`, true},
		{"null", `[null]`, false},
		{"scalar", `[7]`, false},
		{"array", `[[]]`, false},
		{"missing ID", `[{}]`, false},
		{"wrong ID type", `[{"id":7}]`, false},
		{"empty ID", `[{"id":""}]`, false},
		{"wrong ID case", `[{"ID":"plan-one"}]`, false},
		{"duplicate ID", `[{"id":"plan-one"},{"id":"plan-one"}]`, false},
	} {
		t.Run("plan entry "+tc.name, func(t *testing.T) {
			args := map[string]any{}
			for k, v := range write {
				args[k] = v
			}
			args["html"] = strings.Replace(html, `"plans":[]`, `"plans":`+tc.plans, 1)
			input, _ := json.Marshal(args)
			err := agentplans.AuthorizeSpaceSnapshot(input)
			if (err == nil) != tc.allow {
				t.Fatalf("allow=%v, error=%v", tc.allow, err)
			}
		})
	}
	input, _ := json.Marshal(write)
	for _, invalid := range []string{
		strings.Replace(string(input), `"page_id":"page_owned"`, `"page_id":"page_other","PAGE_ID":"page_owned"`, 1),
		strings.Replace(string(input), `"block_id":"block_owned"`, `"block_id":"block_other","BLOCK_ID":"block_owned"`, 1),
	} {
		if err := agentplans.AuthorizeSpaceSnapshot([]byte(invalid)); err == nil {
			t.Error("case-insensitive keys overrode the exact target")
		}
	}
	if reason := CheckBaseline("mcp__CODEX_APPS__chatgpt_space__read_page", []byte(`{"page_id":"page_owned"}`)); reason == "" {
		t.Error("unreviewed server spelling was accepted")
	}
	if reason := CheckReviewer("mcp__codex_apps__chatgpt_space__create_page_visualization", input); reason == "" {
		t.Error("reviewer published")
	}
	if err := os.Remove(config); err != nil {
		t.Fatal(err)
	}
	if err := agentplans.AuthorizeSpaceSnapshot(input); err == nil {
		t.Error("unconfigured publication allowed")
	}
}

func TestSpaceConfigProtection(t *testing.T) {
	root := fakeHome(t)
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	alias := filepath.Join(t.TempDir(), "root-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{agentplans.SpaceConfigFile(), agentplans.SpaceStateFile()} {
		if _, err := os.Stat(filepath.Dir(file)); !os.IsNotExist(err) {
			t.Fatalf("fixture requires an absent parent directory: %v", err)
		}
		rel, err := filepath.Rel(root, file)
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{file, filepath.Dir(file) + "/./" + filepath.Base(file), filepath.Join(alias, rel)}
		if runtime.GOOS == "darwin" {
			paths = append(paths, filepath.Join(root, strings.ToUpper(rel)))
		}
		for _, path := range paths {
			if !spaceAuthorityFile(path) {
				t.Errorf("absent-parent Space path is unprotected: %s", path)
			}
			input, _ := json.Marshal(map[string]string{"file_path": path, "content": "{}"})
			if reason := CheckBaseline("Write", input); reason == "" {
				t.Errorf("agent could create protected file before its parent exists: %s", path)
			}
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		tmp := t.TempDir()
		link, hard := filepath.Join(tmp, "link"), filepath.Join(tmp, "hard")
		if err := os.Symlink(file, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(file, hard); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{file, link, hard} {
			input, _ := json.Marshal(map[string]string{"file_path": path, "content": "{}"})
			if reason := CheckBaseline("Write", input); reason == "" {
				t.Errorf("agent could write %s", path)
			}
		}
	}
	safe := filepath.Join(root, "safe", "publication.json")
	input, _ := json.Marshal(map[string]string{"file_path": safe, "content": "{}"})
	if spaceAuthorityFile(safe) || CheckBaseline("Write", input) != "" {
		t.Error("unrelated new file was refused")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AGENT_PLANS_CONFIG", filepath.Join(t.TempDir(), "replacement.json"))
	if agentplans.SpaceConfigFile() != filepath.Join(root, "config", "agent-tools", "agent-plans-space.json") {
		t.Fatal("request environment redirected trusted target")
	}
}

func TestSpaceRawBridge(t *testing.T) {
	for _, command := range []string{"codex app-server", "codex app-server --listen stdio://", "codex -c x=y app-server"} {
		input, _ := json.Marshal(map[string]string{"command": command})
		if reason := CheckBaseline("Bash", input); reason == "" {
			t.Errorf("raw bridge allowed: %s", command)
		}
	}
}

func TestSpaceDirectoryWrites(t *testing.T) {
	root := fakeHome(t)
	hostpaths.Bake(t, filepath.Join(root, "config"), filepath.Join(root, "state"))
	for _, file := range []string{agentplans.SpaceConfigFile(), agentplans.SpaceStateFile()} {
		t.Run(filepath.Base(file), func(t *testing.T) {
			dest, work := filepath.Dir(file), t.TempDir()
			if err := os.MkdirAll(dest, 0o700); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(work, "linked-destination")
			if err := os.Symlink(dest, link); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				name, dir, command string
				deny               bool
			}{
				{"tar destination", work, "tar -xf fixture.tar -C " + dest, true},
				{"tar ancestor", work, "gtar xzf fixture.tgz --directory=" + filepath.Dir(dest), true},
				{"tar implicit destination", dest, "bsdtar -xf fixture.tar", true},
				{"tar implicit ancestor", filepath.Dir(dest), "tar -xf fixture.tar", true},
				{"tar linked destination", work, "tar -xf fixture.tar -C " + link, true},
				{"unzip destination", work, "unzip fixture.zip -d" + dest, true},
				{"unzip implicit destination", dest, "unzip fixture.zip", true},
				{"ditto destination", work, "ditto -x -k fixture.zip " + dest, true},
				{"copy ancestor", work, "cp -R fixture " + filepath.Dir(dest), true},
				{"rsync ancestor", work, "rsync -a fixture/ " + filepath.Dir(dest) + "/", true},
				{"tar listing", work, "tar -tf fixture.tar -C " + dest, false},
				{"unzip listing", dest, "unzip -l fixture.zip", false},
				{"safe extraction", dest, "tar -xf fixture.tar -C " + work, false},
				{"safe ditto", work, "ditto fixture " + filepath.Join(work, "safe"), false},
				{"safe copy", work, "cp -R fixture " + filepath.Join(work, "safe"), false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					words := strings.Fields(tc.command)
					reason := checkSpaceAuthorityShell(words[0], words[1:], nil, tc.dir)
					if (reason != "") != tc.deny {
						t.Errorf("Space destination rule deny=%v, reason=%s", tc.deny, reason)
					}
					if reason := judge(t, tc.dir, tc.command); (reason != "") != tc.deny {
						t.Errorf("baseline deny=%v, reason=%s", tc.deny, reason)
					}
				})
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

// TestMCPLookalike covers a server mcp.json does not list whose name says
// a reviewed service, as one added directly in place of an mcp-proxy server
// that is not connected: it still asks in Claude Code and is refused in
// Codex, and its reason names the service's reviewed servers and, where
// this host's mcp-proxy serves the service, the command that connects it.
func TestMCPLookalike(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"mcpProxy": {"url": "http://127.0.0.1:6290",
		"endpoints": {"linear": "/linear/mcp", "notion": "/notion/mcp", "buildkite": "/buildkite/mcp"}}}`), 0o600)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	claude := func(tool string) string { return `{"tool_name":"` + tool + `","tool_input":{}}` }
	codex := func(tool string) string { return `{"tool_name":"` + tool + `","turn_id":"t","tool_input":{}}` }
	decide := func(t *testing.T, args []string, event string) (decision, reason string) {
		t.Helper()
		var out bytes.Buffer
		Main(args, strings.NewReader(event), &out, io.Discard)
		var printed struct {
			Hook struct {
				Decision string `json:"permissionDecision"`
				Reason   string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &printed); err != nil {
			t.Fatalf("%s: printed %q: %v", event, out.String(), err)
		}
		return printed.Hook.Decision, printed.Hook.Reason
	}
	covered := map[string]bool{}
	for _, c := range []struct {
		key, tool, reviewed string
		authorize           bool // the proxy serves it, so the reason names mcp-proxy-authorize KEY
	}{
		{"linear", "mcp__linear-server__get_issue", "mcp__linear__", true},
		{"notion", "mcp__notion_direct__notion-fetch", "mcp__notion__", true},
		{"buildkite", "mcp__my_buildkite__get_build", "mcp__buildkite__", true},
		{"hex", "mcp__hex-local__get_project", "mcp__hex__", false},
		{"datadog", "mcp__datadog.eu__search_datadog_logs", "mcp__claude_ai_Datadog_2__", false},
		{"slack", "mcp__slack__slack_read_thread", "mcp__claude_ai_Slack__", false},
		{"ngrok", "mcp__ngrok-dev__list_endpoints", "mcp__ngrok__", false},
		{"pages", "mcp__pages-direct__chatgpt_space__read_page", "mcp__codex_apps__", false},
	} {
		covered[c.key] = true
		for _, h := range []struct {
			harness, event, want string
		}{{"Claude Code", claude(c.tool), "ask"}, {"Codex", codex(c.tool), "deny"}} {
			t.Run(c.key+" from "+h.harness, func(t *testing.T) {
				decision, reason := decide(t, []string{"--baseline"}, h.event)
				if decision != h.want {
					t.Errorf("%s: decision %q, want %q", c.tool, decision, h.want)
				}
				if !strings.Contains(reason, c.reviewed) || !saysInstead(reason) {
					t.Errorf("%s: reason %q does not name %s and say what to do instead", c.tool, reason, c.reviewed)
				}
				if got := strings.Contains(reason, "`mcp-proxy-authorize "+c.key+"`"); got != c.authorize {
					t.Errorf("%s: reason %q names mcp-proxy-authorize %s: %v, want %v", c.tool, reason, c.key, got, c.authorize)
				}
			})
		}
	}
	for key := range mcpServices {
		if !covered[key] {
			t.Errorf("no case for a server whose name says %s", key)
		}
	}
	t.Run("unrelated server", func(t *testing.T) {
		decision, reason := decide(t, []string{"--baseline"}, claude("mcp__somewhere__get_thing"))
		if decision != "ask" || strings.Contains(reason, "mcp-proxy-authorize") || strings.Contains(reason, "mcp__") {
			t.Errorf("decision %q, reason %q: want ask, naming no reviewed server", decision, reason)
		}
	})
}
