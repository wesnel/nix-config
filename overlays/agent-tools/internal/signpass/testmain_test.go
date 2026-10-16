package signpass

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	// A pass reads this host's plans for the routes a branch owes: the
	// tests' plans are in a directory of their own, empty unless a test
	// writes a config of its own.
	dir, err := os.MkdirTemp("", "signpass-plans")
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
