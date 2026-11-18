package loop

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// plansConfig is the tests' plans config, in a directory of their own.
var plansConfig string

func TestMain(m *testing.M) {
	gitenv.Isolate()
	// pr-loop logs its actions in the plan of the session running it: the
	// tests' plans are in a directory of their own, and no harness's
	// session is theirs.
	dir, err := os.MkdirTemp("", "loop-plans")
	if err != nil {
		panic(err)
	}
	plansConfig = filepath.Join(dir, "config.json")
	os.WriteFile(plansConfig, []byte(`{"plans": {"dir": "`+filepath.Join(dir, "agents")+`", "host": "box"}}`), 0o600)
	os.Setenv("AGENT_TOOLS_CONFIG", plansConfig)
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID"} {
		os.Unsetenv(v)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// bake bakes configHome and state in, as hostpaths.Bake does, and puts the
// tests' plans config in configHome. A baked config home makes repos.Path
// ignore AGENT_TOOLS_CONFIG, and without a config there plans.Load falls
// back to the home directory's plans, which pr-loop would then record in.
func bake(t *testing.T, configHome, state string) {
	t.Helper()
	hostpaths.Bake(t, configHome, state)
	data, err := os.ReadFile(plansConfig)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configHome, "agent-tools", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
