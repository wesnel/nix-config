package guard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// post is `pr-loop guard --post`, after a shell call: the PostToolUse of
// Claude Code or Codex, or ECA's postToolCall. It records the call's facts
// in the plan of its session or worktree (recordFacts), and after a push to
// GitHub adds one line of context naming the `pr-loop wait ci` to start,
// read from the command's JSON output; it prints nothing when the output
// shows no push.
func post(stdin io.Reader, stdout io.Writer) int {
	var in struct {
		HookType     string          `json:"hook_type"`
		ToolInput    json.RawMessage `json:"tool_input"`
		ToolResponse json.RawMessage `json:"tool_response"`
		Cwd          string          `json:"cwd"`
		SessionID    string          `json:"session_id"`
		ChatID       string          `json:"chat_id"` // set by ECA
	}
	if json.NewDecoder(stdin).Decode(&in) != nil {
		return 0
	}
	var args struct {
		Command string `json:"command"`
	}
	json.Unmarshal(in.ToolInput, &args)
	if args.Command == "" {
		return 0
	}
	if in.Cwd == "" {
		in.Cwd, _ = os.Getwd()
	}
	texts := responseTexts(in.ToolResponse)
	lines := recordFacts(session(in.ChatID, in.SessionID), in.Cwd, args.Command, texts, time.Now())
	if targets := pushedTo(texts); len(targets) > 0 && pushes(args.Command, 0) {
		lines = append(lines, fmt.Sprintf("You just pushed to GitHub. Start `pr-loop wait ci %s` in the background now, "+
			"and act on its result as the signing-pass skill's \"After a push\" says.", strings.Join(targets, " ")))
	}
	if len(lines) == 0 {
		return 0
	}
	context := strings.Join(lines, "\n")
	var out any = map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": "PostToolUse", "additionalContext": context}}
	if in.HookType != "" {
		out = map[string]string{"additionalContext": context}
	}
	json.NewEncoder(stdout).Encode(out)
	return 0
}

// pushes reports whether a command line runs a command that pushes to
// GitHub: `pr-loop open` or `push` without --dry-run, or `git sign-pass`
// with --push or --set-upstream.
func pushes(text string, depth int) bool {
	if depth > maxDepth {
		return false
	}
	found := false
	v := visitor{
		script: func(text string) string {
			found = found || pushes(text, depth+1)
			return ""
		},
		call: func(c call) string {
			w := c.words
			if len(w) == 0 || c.expands[0] {
				return ""
			}
			flag := func(names ...string) bool {
				for _, a := range w[1:] {
					for _, n := range names {
						if a == n || strings.HasPrefix(a, n+"=") {
							return true
						}
					}
				}
				return false
			}
			switch name := base(w[0]); {
			case name == "pr-loop" && len(w) > 1 && (w[1] == "open" || w[1] == "push"):
				found = found || !flag("--dry-run", "-dry-run")
			case name == "git-sign-pass", name == "git" && contains(w[1:], "sign-pass"):
				found = found || flag("--push", "--set-upstream")
			}
			return ""
		},
	}
	for _, segment := range segments(text) {
		if words, expands := split(segment); len(words) > 0 {
			unwrap(call{words: words, expands: expands}, v)
		}
	}
	return found
}

// pushedTo reads a tool's output texts (responseTexts) for what pr-loop or
// git sign-pass reports it pushed: the PR `open` opened, or the branches
// under "pushed".
func pushedTo(texts []string) []string {
	for _, result := range jsonObjects(texts) {
		if n, ok := result["pr"].(float64); ok && result["url"] != nil {
			return []string{strconv.Itoa(int(n))}
		}
		if pushed, ok := result["pushed"].(map[string]any); ok {
			var branches []string
			for b := range pushed {
				branches = append(branches, b)
			}
			sort.Strings(branches)
			return branches
		}
	}
	return nil
}
