package guard

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

const mirrorDenied = "Native publication must match this main session's prepared plan mirror. Run plan-dash snapshot first; put an uncertain or conflicting publication in your report for the operator."

func checkMirrorEnvironment(c call) string {
	for _, word := range append(append([]string{}, c.env...), c.words...) {
		name, _, assigned := strings.Cut(word, "=")
		if name != "CODEX_THREAD_ID" && name != "CLAUDE_CODE_SESSION_ID" {
			continue
		}
		if assigned || len(c.words) > 0 && (c.words[0] == "unset" || c.words[0] == "export" || c.words[0] == "set" || c.words[0] == "env") {
			return "The harness session identity binds plan mirror ownership. Run plan-dash in the current session without overriding its session variables."
		}
	}
	return ""
}

func checkMirror(in input, who role) (bool, string) {
	if in.AgentID != "" {
		if mirrorShellMutation(mirrorCommand(in), 0) {
			return true, mirrorDenied
		}
	}
	mutation := in.ToolName == "mcp__codex_apps__chatgpt_space__create_page" || in.ToolName == "mcp__codex_apps__chatgpt_space__create_page_visualization"
	if !mutation && in.ToolName != "Artifact" {
		return false, ""
	}
	if in.ToolName == "Artifact" {
		protected, err := artifactMirrorScope(in.ToolInput, in.Cwd)
		if err != nil {
			return true, unreadable
		}
		if !protected {
			return false, ""
		}
	}
	if who != baseline || in.AgentID != "" || in.SessionID == "" {
		return true, mirrorDenied
	}
	h := "claude"
	if in.TurnID != "" {
		h = "codex"
	}
	call := in.ToolUseID
	if in.ToolCallID != "" {
		if call != "" && call != in.ToolCallID {
			return true, mirrorDenied
		}
		call = in.ToolCallID
	}
	if err := agentplans.BeginMirror(h+":"+in.SessionID, in.ToolName, call, in.ToolInput); err != nil {
		return true, mirrorDenied + " " + err.Error()
	}
	return true, ""
}

func artifactMirrorScope(input json.RawMessage, dir string) (bool, error) {
	a, err := artifactScopeFields(input)
	if err != nil {
		return false, err
	}
	if a["command"] == "read" {
		return false, nil
	}
	if file := a["file_path"]; file != "" {
		path := physical(rawPath(file, dir))
		if !filepath.IsAbs(path) {
			return false, errors.New("relative Artifact source requires its native working directory")
		}
		root := physical(agentplans.MirrorDir())
		if fold(path) == fold(root) || strings.HasPrefix(fold(path), fold(root)+string(filepath.Separator)) || Reaches(path, root, true) {
			return true, nil
		}
	}
	if a["url"] == "" {
		return false, nil
	}
	u, err := url.Parse(a["url"])
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false, errors.New("unreadable Artifact destination")
	}
	key := artifactURLKey(u)
	if key == "" {
		return false, nil
	}
	targets, err := agentplans.ArtifactMirrorTargets()
	if err != nil {
		return false, err
	}
	for _, target := range targets {
		u, err := url.Parse(target)
		if err != nil {
			return false, err
		}
		if artifactURLKey(u) == key {
			return true, nil
		}
	}
	return false, nil
}

// RecordMirror ignores unrelated Artifact receipts and sends mirror receipts
// to the journal. Native cwd resolves relative source paths as at pre-tool time.
func RecordMirror(event []byte) error {
	var e struct {
		Event string          `json:"hook_event_name"`
		Tool  string          `json:"tool_name"`
		Input json.RawMessage `json:"tool_input"`
		Cwd   string          `json:"cwd"`
	}
	if err := json.Unmarshal(event, &e); err != nil {
		return err
	}
	if e.Tool == "Artifact" && e.Event == "PostToolUse" {
		protected, err := artifactMirrorScope(e.Input, e.Cwd)
		if err != nil {
			return err
		}
		if !protected {
			return nil
		}
	}
	return agentplans.RecordMirror(event)
}

func artifactURLKey(u *url.URL) string {
	if !strings.EqualFold(u.Hostname(), "claude.ai") {
		return ""
	}
	path := strings.TrimPrefix(u.Path, "/code")
	if !strings.HasPrefix(path, "/artifact/") {
		return ""
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(path, "/artifact/"), "/")
	return id
}

func artifactScopeFields(input json.RawMessage) (map[string]string, error) {
	bad := errors.New("unreadable Artifact fields")
	if !json.Valid(input) {
		return nil, bad
	}
	d := json.NewDecoder(bytes.NewReader(input))
	opening, err := d.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, bad
	}
	fields := map[string]string{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, bad
		}
		key, ok := token.(string)
		if !ok {
			return nil, bad
		}
		var raw json.RawMessage
		if d.Decode(&raw) != nil {
			return nil, bad
		}
		key = strings.ToLower(key)
		if key != "command" && key != "url" && key != "file_path" {
			continue
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, bad
		}
		var value string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
			return nil, bad
		}
		fields[key] = value
	}
	return fields, nil
}

func mirrorCommand(in input) string {
	var args struct {
		Command json.RawMessage `json:"command"`
		Chars   string          `json:"chars"`
	}
	if json.Unmarshal(in.ToolInput, &args) != nil {
		return ""
	}
	switch in.ToolName {
	case "Bash", "exec_command", "Monitor", "local_shell":
		var command string
		if json.Unmarshal(args.Command, &command) == nil {
			return command
		}
		var words []string
		if in.ToolName == "local_shell" && json.Unmarshal(args.Command, &words) == nil {
			for i, word := range words {
				words[i] = "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
			}
			return strings.Join(words, " ")
		}
	case "write_stdin":
		return args.Chars
	}
	return ""
}

func mirrorShellMutation(command string, depth int) bool {
	if depth > maxDepth {
		return true
	}
	found := false
	v := visitor{
		script: func(s string) string { found = found || mirrorShellMutation(s, depth+1); return "" },
		call: func(c call) string {
			w := c.named().words
			if len(w) > 1 && base(w[0]) == "plan-dash" {
				switch w[1] {
				case "snapshot", "claim", "release", "record":
					found = true
				}
			}
			return ""
		},
	}
	for _, segment := range segments(command) {
		if w, x := split(segment); len(w) > 0 {
			unwrap(call{words: w, expands: x}, v)
		}
	}
	return found
}
