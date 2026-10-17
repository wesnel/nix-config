package guard

import (
	"os"
	"path/filepath"
)

// linkedAgentToolsState reports whether path leads into agent-tools' state
// directory under XDG_STATE_HOME through a link: agentToolsState matches a
// path as written, and a session can make a link wherever it may write.
func linkedAgentToolsState(path string) bool {
	base := os.Getenv("XDG_STATE_HOME")
	if !filepath.IsAbs(base) {
		base = filepath.Join(home, ".local", "state")
	}
	return within(path, filepath.Join(base, "agent-tools"))
}
