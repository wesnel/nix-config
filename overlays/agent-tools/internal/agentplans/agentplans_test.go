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
	if Out() != filepath.Join(baked, "state", "agent-plans", "out") {
		t.Errorf("baked: out %s", Out())
	}
}

// The targets are agentPlans in agent-tools' config.json, which the llm
// module writes from the machine's options, null where one is unset.
func TestLoadTargets(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AGENT_TOOLS_CONFIG", file)
	if _, err := LoadTargets(); err == nil || !strings.Contains(err.Error(), file) {
		t.Errorf("without the file: %v", err)
	}
	for _, c := range []struct{ body, claude, space string }{
		{`{"agentPlans": {"claudePage": "https://claude.ai/artifact/XrwFbuUrFW9ctBGqveAFqq", "spacePage": "page_0123abcd"}}`,
			"https://claude.ai/artifact/XrwFbuUrFW9ctBGqveAFqq", "page_0123abcd"},
		{`{"agentPlans": {"claudePage": "https://claude.ai/code/artifact/0b7f5c1e-1d2a-4c3b-9e8f-123456789abc", "spacePage": null}}`,
			"https://claude.ai/code/artifact/0b7f5c1e-1d2a-4c3b-9e8f-123456789abc", ""},
		{`{"agentPlans": {"claudePage": null, "spacePage": "page_x"}}`, "", "page_x"},
		{`{"plans": {}}`, "", ""},
	} {
		os.WriteFile(file, []byte(c.body), 0o600)
		if got, err := LoadTargets(); got.ClaudePage != c.claude || got.SpacePage != c.space || err != nil {
			t.Errorf("%s: %+v %v", c.body, got, err)
		}
	}
	for _, body := range []string{`{"agentPlans": {"claudePage": "https://claude.ai/artifact/X/../../other"}}`,
		`{"agentPlans": {"claudePage": "https://claude.ai/artifact/X?y"}}`, `{"agentPlans": {"claudePage": "http://claude.ai/artifact/X"}}`,
		`{"agentPlans": {"claudePage": "https://claude.ai.example.com/artifact/X"}}`, `{"agentPlans": {"claudePage": "https://claude.ai/artifact/"}}`,
		`{"agentPlans": {"claudePage": "https://claude.ai/artifact/X "}}`, `{"agentPlans": {"claudePage": ""}}`,
		`{"agentPlans": {"spacePage": "page/../x"}}`, `{"agentPlans": {"spacePage": ""}}`,
		`not json`, `{"agentPlans": {"claudePage": ["x"]}}`} {
		os.WriteFile(file, []byte(body), 0o600)
		if got, err := LoadTargets(); got != (Targets{}) || err == nil {
			t.Errorf("%s: %+v %v", body, got, err)
		}
	}
}
