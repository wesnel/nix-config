package gitenvtest

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// IsolateUser gives a TestMain and its subprocesses private config and state.
// It returns cleanup to run after m.Run; fixture-specific settings may override it.
func IsolateUser() func() {
	dir, err := os.MkdirTemp("", "agent-tools-test-home-")
	if err != nil {
		panic(err)
	}
	configHome := filepath.Join(dir, "config")
	config := filepath.Join(configHome, "agent-tools", "config.json")
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		panic(err)
	}
	data, err := json.Marshal(map[string]any{"plans": map[string]string{"dir": filepath.Join(dir, "plans"), "host": "box"}})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(config, data, 0o600); err != nil {
		panic(err)
	}
	for name, value := range map[string]string{
		"HOME":               dir,
		"XDG_CONFIG_HOME":    configHome,
		"XDG_STATE_HOME":     filepath.Join(dir, "state"),
		"XDG_CACHE_HOME":     filepath.Join(dir, "cache"),
		"XDG_DATA_HOME":      filepath.Join(dir, "data"),
		"AGENT_TOOLS_CONFIG": config,
	} {
		if err := os.Setenv(name, value); err != nil {
			panic(err)
		}
	}
	for _, name := range []string{"CLAUDE_CODE_SESSION_ID", "CODEX_THREAD_ID"} {
		if err := os.Unsetenv(name); err != nil {
			panic(err)
		}
	}
	return func() {
		if err := os.RemoveAll(dir); err != nil {
			panic(err)
		}
	}
}
