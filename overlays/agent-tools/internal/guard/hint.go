package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// sessionID matches a session ID that is safe as a file name.
var sessionID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// branchHint is the context for a file edit, by Claude Code's Edit, Write
// or MultiEdit, that lands in another worktree or on another branch than
// the session's last edit, which it records under
// $XDG_STATE_HOME/agent-tools/branch/<session>; "" otherwise. It never
// denies.
func branchHint(session string, toolInput json.RawMessage, cwd string) string {
	var in struct {
		FilePath string `json:"file_path"`
	}
	json.Unmarshal(toolInput, &in)
	if !sessionID.MatchString(session) || in.FilePath == "" {
		return ""
	}
	dir := filepath.Dir(expandPath(in.FilePath, cwd))
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	g := gitState{dir: dir}
	top, ok := g.run("rev-parse", "--show-toplevel")
	if !ok {
		return ""
	}
	branch, _ := g.run("branch", "--show-current")
	if branch == "" {
		head, _ := g.run("rev-parse", "--short", "HEAD")
		branch = "a detached HEAD at " + head
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	file := filepath.Join(state, "agent-tools", "branch", session)
	now := branch + "\t" + top
	last, _ := os.ReadFile(file)
	if string(last) != now {
		os.MkdirAll(filepath.Dir(file), 0o700)
		os.WriteFile(file, []byte(now), 0o600)
	}
	lastBranch, lastTop, ok := strings.Cut(string(last), "\t")
	if !ok || string(last) == now {
		return ""
	}
	return fmt.Sprintf("This edit is on %s in %s; the last one was on %s in %s.", branch, top, lastBranch, lastTop)
}
