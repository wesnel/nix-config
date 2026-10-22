package agentplans

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

func TestPaths(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if Out() != filepath.Join(state, "agent-plans", "out") || Session() != filepath.Join(state, "agent-plans", "session") {
		t.Errorf("out %s, session %s", Out(), Session())
	}
	// A build that bakes the directories in takes neither from the
	// environment.
	baked := t.TempDir()
	hostpaths.Bake(t, filepath.Join(baked, "config"), filepath.Join(baked, "state"))
	if Out() != filepath.Join(baked, "state", "agent-plans", "out") ||
		ConfigFile() != filepath.Join(baked, "config", "agent-tools", "agent-plans.json") {
		t.Errorf("baked: out %s, config %s", Out(), ConfigFile())
	}
}

func TestURL(t *testing.T) {
	file := filepath.Join(t.TempDir(), "agent-plans.json")
	t.Setenv("AGENT_PLANS_CONFIG", file)
	if _, err := URL(); err == nil || !strings.Contains(err.Error(), file) {
		t.Errorf("without the file: %v", err)
	}
	for _, url := range []string{"https://claude.ai/artifact/XrwFbuUrFW9ctBGqveAFqq",
		"https://claude.ai/code/artifact/0b7f5c1e-1d2a-4c3b-9e8f-123456789abc"} {
		os.WriteFile(file, []byte(`{"url": "`+url+`"}`), 0o600)
		if got, err := URL(); got != url || err != nil {
			t.Errorf("%s: %q %v", url, got, err)
		}
	}
	for _, body := range []string{`{"url": "https://claude.ai/artifact/X/../../other"}`,
		`{"url": "https://claude.ai/artifact/X?y"}`, `{"url": "http://claude.ai/artifact/X"}`,
		`{"url": "https://claude.ai.example.com/artifact/X"}`, `{"url": "https://claude.ai/artifact/"}`,
		`{"url": "https://claude.ai/artifact/X "}`, `{"url": ""}`, `{}`, `not json`, `{"url": ["x"]}`} {
		os.WriteFile(file, []byte(body), 0o600)
		if got, err := URL(); got != "" || err == nil {
			t.Errorf("%s: %q %v", body, got, err)
		}
	}
}
