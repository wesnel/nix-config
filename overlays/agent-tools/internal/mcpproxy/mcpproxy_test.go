package mcpproxy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
)

// Load reads the config the Env it is given names, whatever the process's
// environment names, and gives no proxy where that config is missing.
func TestLoad(t *testing.T) {
	t.Parallel()
	config := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(config, []byte(`{"mcpProxy": {"url": "http://127.0.0.1:6290",
		"endpoints": {"linear": "/linear/mcp"}, "localServers": ["parallels"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := Config{URL: "http://127.0.0.1:6290", Endpoints: map[string]string{"linear": "/linear/mcp"},
		LocalServers: []string{"parallels"}}
	if got := Load(env.Process().Set("AGENT_TOOLS_CONFIG=" + config)); !reflect.DeepEqual(got, want) {
		t.Errorf("Load of the Env's config: %+v, want %+v", got, want)
	}
	missing := filepath.Join(t.TempDir(), "none.json")
	if got := Load(env.Process().Set("AGENT_TOOLS_CONFIG=" + missing)); !reflect.DeepEqual(got, Config{}) {
		t.Errorf("Load of a missing config: %+v, want none", got)
	}
}
