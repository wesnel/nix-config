package loop

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	// pr-loop logs its actions in the plan of the session running it: the
	// tests' plans are in a directory of their own, and no harness's
	// session is theirs.
	dir, err := os.MkdirTemp("", "loop-plans")
	if err != nil {
		panic(err)
	}
	config := filepath.Join(dir, "config.json")
	os.WriteFile(config, []byte(`{"plans": {"dir": "`+filepath.Join(dir, "agents")+`", "host": "box"}}`), 0o600)
	os.Setenv("AGENT_TOOLS_CONFIG", config)
	for _, v := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID", "ECA_CHAT_ID"} {
		os.Unsetenv(v)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
