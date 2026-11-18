package guard

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDebugPermissions(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "unconfigured"
		config := `{"mcpProxy":{"endpoints":{}}}`
		if configured {
			name = "configured"
			config = `{"mcpProxy":{"endpoints":{"debug":"/debug/mcp"}}}`
		}
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(file, []byte(config), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENT_TOOLS_CONFIG", file)
			for _, harness := range []string{"claude", "codex"} {
				for _, role := range []string{"--baseline", "--reviewer"} {
					for _, tool := range []string{"lima_dev_start", "lima_dev_stop", "lima_dev_exec_start", "lima_dev_import", "lima_dev_job_cancel"} {
						t.Run(harness+"/"+role+"/"+tool, func(t *testing.T) {
							event := `{"tool_name":"mcp__debug__` + tool + `","tool_input":{"script":"sudo -n id"}`
							if harness == "codex" {
								event += `,"turn_id":"test"`
							}
							var out bytes.Buffer
							Main([]string{role}, strings.NewReader(event+`}`), &out, io.Discard)
							if configured && role == "--baseline" {
								if out.Len() != 0 {
									t.Fatalf("preapproved debug call refused: %s", out.String())
								}
							} else if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
								t.Fatalf("unconfigured or reviewer mutation allowed: %s", out.String())
							}
						})
					}
				}
			}
		})
	}
	for _, tool := range []string{"mcp__debug__lima_stop", "mcp__debug__lima_list", "mcp__debug__lima_dev_future", "mcp__debug_direct__lima_dev_exec_start", "mcp__DEBUG__lima_dev_exec_start"} {
		t.Run(tool, func(t *testing.T) {
			var out bytes.Buffer
			Main([]string{"--baseline"}, strings.NewReader(`{"tool_name":"`+tool+`","turn_id":"test","tool_input":{}}`), &out, io.Discard)
			if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
				t.Fatalf("unlisted endpoint or tool allowed: %s", out.String())
			}
		})
	}
}
