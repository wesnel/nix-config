package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/agents"
)

// claudeAgents are the user-level Claude Code agents the operator's
// configuration defines.
var claudeAgents = set(agents.Claude()...)

const shadowedAgent = "This project has agent definitions of its own, in %s, and Claude Code runs a project's " +
	"definition in place of the operator's `%s` when one has that name, with that file's prompt and tools. " +
	"Put this in your report for the operator, who can check those files or run the step themselves."

// checkAgentShadow denies starting one of claudeAgents, by the Agent tool's
// subagent_type, where the session's project has any agent definition:
// Claude Code reads a project's agents from every .claude/agents between
// the working directory, dir, and the top of its repository, prefers them
// to the user's, and names each by its YAML frontmatter, which the guard
// does not parse. The home directory's are the user's own. A directory
// --add-dir gave the session, and one the session moved to with cd, are
// not in the hook's input, so they are not checked: a session given
// --add-dir to a repository it does not trust must not start these agents,
// as the coordinate-subagents skill says.
func checkAgentShadow(toolInput json.RawMessage, dir string) string {
	var args struct {
		SubagentType string `json:"subagent_type"`
	}
	json.Unmarshal(toolInput, &args)
	if !claudeAgents[args.SubagentType] || !filepath.IsAbs(dir) {
		return ""
	}
	for d := filepath.Clean(dir); d != home; d = filepath.Dir(d) {
		agents := filepath.Join(d, ".claude", "agents")
		if holdsMarkdown(agents, 0) {
			return fmt.Sprintf(shadowedAgent, agents, args.SubagentType)
		}
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil || filepath.Dir(d) == d {
			return ""
		}
	}
	return ""
}

// holdsMarkdown reports whether dir, following symbolic links, holds a .md
// file; a tree deeper than eight directories counts as holding one.
func holdsMarkdown(dir string, depth int) bool {
	if depth > 8 {
		return true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		switch {
		case err != nil:
		case info.IsDir():
			if holdsMarkdown(path, depth+1) {
				return true
			}
		case strings.HasSuffix(strings.ToLower(e.Name()), ".md"):
			return true
		}
	}
	return false
}
