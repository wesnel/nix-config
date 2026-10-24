package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/repos"
)

// claudeMaxTurns bounds a headless reviewer's turns, so that a reviewer
// that never replies ends.
const claudeMaxTurns = 60

// delegateConfig is what the Claude Code module writes into agent-tools'
// config for the headless reviewers: the store paths of review-agents.json,
// their --agents file, and reviewer-settings.json, their only settings.
type delegateConfig struct {
	Agents   string `json:"agents"`
	Settings string `json:"settings"`
}

func loadDelegateConfig() (delegateConfig, error) {
	var config struct {
		Delegate delegateConfig `json:"delegate"`
	}
	data, err := os.ReadFile(repos.Path())
	if errors.Is(err, fs.ErrNotExist) {
		return config.Delegate, nil
	}
	if err != nil {
		return config.Delegate, err
	}
	err = json.Unmarshal(data, &config)
	return config.Delegate, err
}

// claudeMissing says why no Claude reviewer can run, or "".
func claudeMissing() string {
	if _, err := exec.LookPath("claude"); err != nil {
		return "claude is not on PATH"
	}
	c, err := loadDelegateConfig()
	switch {
	case err != nil:
		return fmt.Sprintf("agent-tools' config %s cannot be read: %v", repos.Path(), err)
	case c.Agents == "" || c.Settings == "":
		return fmt.Sprintf("agent-tools' config %s names no delegate.agents and delegate.settings, which the Claude Code "+
			"module writes", repos.Path())
	}
	return ""
}

// Claude is one headless Claude Code reviewer's run.
type Claude struct {
	// Checkout is the worktree of TIP the reviewer reads, its working
	// directory.
	Checkout string
	// Agent is its agent in review-agents.json.
	Agent   string
	Prompt  string
	Schema  []byte
	Timeout time.Duration
	// Log receives Claude Code's stderr.
	Log io.Writer
}

// ClaudeRun runs c's agent headless and returns its reply's JSON, from the
// first of: the structured output --json-schema asks for, the last fenced
// json block of its result, and the last JSON object in that result.
//
// Nothing of the user's or the reviewed project's reaches it: no settings
// file (--setting-sources "", with reviewer-settings.json as --settings, so
// its sandbox holds and no project can loosen it), no MCP server, no
// CLAUDE.md, and its agent from --agents, which outranks a project's own.
// The agent's frontmatter hook keeps it to reading.
func ClaudeRun(ctx context.Context, c Claude) (*CodexReply, error) {
	config, err := loadDelegateConfig()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	args := []string{"-p", "--setting-sources", "",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--agents", config.Agents, "--agent", c.Agent,
		"--settings", config.Settings,
		"--permission-mode", "dontAsk",
		"--output-format", "json", "--json-schema", string(c.Schema),
		"--no-session-persistence", "--max-turns", strconv.Itoa(claudeMaxTurns),
		c.Prompt}
	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = c.Checkout
	cmd.Env = claudeEnv(os.Environ())
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, c.Log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		stopGroup(cmd.Process.Pid, 5*time.Second)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("claude stopped: %w", ctx.Err())
	}
	var result struct {
		Result     string                     `json:"result"`
		Structured json.RawMessage            `json:"structured_output"`
		IsError    bool                       `json:"is_error"`
		Subtype    string                     `json:"subtype"`
		ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		if runErr != nil {
			return nil, fmt.Errorf("claude: %w", runErr)
		}
		return nil, fmt.Errorf("claude printed no JSON result: %v", err)
	}
	reply := &CodexReply{}
	if models := slices.Sorted(maps.Keys(result.ModelUsage)); len(models) > 0 {
		reply.Model = strings.Join(models, ", ")
	}
	if result.IsError || runErr != nil {
		return nil, badReply(fmt.Sprintf("the reviewer ended with %q (%v): %s", result.Subtype, runErr,
			strings.TrimSpace(result.Result)))
	}
	reply.Text, err = replyJSON(result.Structured, result.Result)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// claudeEnv is the reviewer's environment: the caller's, without what
// points git at a repository or gives it configuration, with git reading
// neither the user's configuration nor the system's (the user's includes
// a file the reviewer's sandbox denies, which stops every git command),
// without the mark of a Claude Code session, which a session started from
// one would take as nested, and with every CLAUDE.md kept out of context,
// the reviewed project's and the operator's alike.
func claudeEnv(env []string) []string {
	var out []string
	for _, kv := range gitenv.WithoutConfig(env) {
		name, _, _ := strings.Cut(kv, "=")
		if name == "CLAUDECODE" || name == "CLAUDE_CODE_DISABLE_CLAUDE_MDS" {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1")
}

// badReply is a reply that holds nothing delegate can use, which it asks
// for again once.
type badReply string

func (e badReply) Error() string { return string(e) }

var fencedJSON = regexp.MustCompile("(?s)```json[ \t]*\n(.*?)\n[ \t]*```")

// replyJSON is a reply's JSON: the structured output where there is one,
// else the last fenced json block in the result, else its last top-level
// JSON object.
func replyJSON(structured json.RawMessage, result string) (string, error) {
	if s := bytes.TrimSpace(structured); len(s) > 0 && string(s) != "null" {
		return string(s), nil
	}
	if blocks := fencedJSON.FindAllStringSubmatch(result, -1); len(blocks) > 0 {
		return blocks[len(blocks)-1][1], nil
	}
	if obj := lastObject(result); obj != "" {
		return obj, nil
	}
	return "", badReply("the reply holds no structured output, no fenced json block and no JSON object")
}

// lastObject is the last top-level JSON object in text that parses, or "".
func lastObject(text string) string {
	var found string
	depth, start, inString, escaped := 0, -1, false, false
	for i, c := range text {
		switch {
		case inString:
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"' && depth > 0:
			inString = true
		case c == '{':
			if depth == 0 {
				start = i
			}
			depth++
		case c == '}' && depth > 0:
			depth--
			if depth == 0 && json.Valid([]byte(text[start:i+1])) {
				found = text[start : i+1]
			}
		}
	}
	return found
}
