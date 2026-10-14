package guard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	// Whether trash is on PATH differs between hosts and the Nix build;
	// TestTrash sets it.
	trashOnPath = func() bool { return false }
	// Plans, and the worktree owners they record, come from a directory of
	// the tests' own unless a test names another.
	dir, err := os.MkdirTemp("", "guard-plans")
	if err != nil {
		panic(err)
	}
	config := filepath.Join(dir, "config.json")
	os.WriteFile(config, []byte(`{"plans": {"dir": "`+filepath.Join(dir, "agents")+`", "host": "box"}}`), 0o600)
	os.Setenv("AGENT_TOOLS_CONFIG", config)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
