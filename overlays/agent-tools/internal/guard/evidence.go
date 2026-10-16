package guard

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/evidence"
)

// exitCode is the exit status a harness reports in text: the first line of
// Claude Code's PostToolUseFailure error, "Exit code 3", or of ECA's
// response, "Exit code: 3".
var exitCode = regexp.MustCompile(`^Exit code:? (\d+)(?:\n|$)`)

// recordEvidence writes the shell call in to its session's evidence log, and
// reports whether the call failed. The exit status is each harness's own:
// Claude Code fires PostToolUse only after a success, which carries no
// status, and PostToolUseFailure, whose error starts with the status, after
// a failure; ECA's response starts with it, and its error flag marks a
// failure; Codex's hook carries none. A backgrounded command's end reaches
// no hook, so its status stays unknown. A call no session names is not
// recorded.
func recordEvidence(in postInput, args shellArgs, now time.Time) (failed bool) {
	var errText string
	var errFlag bool
	if json.Unmarshal(in.Error, &errText) != nil {
		json.Unmarshal(in.Error, &errFlag) //nolint:gosec // neither text nor a flag: no failure reported
	}
	status := func(text string) *int {
		m := exitCode.FindStringSubmatch(text)
		if m == nil {
			return nil
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			return nil
		}
		return &n
	}
	zero := 0
	r := evidence.Record{Time: now, Session: session(in.ChatID, in.SessionID), AgentID: in.AgentID, Cwd: in.Cwd,
		Command: args.Command}
	output := outputText(in.ToolResponse)
	switch {
	case in.HookType != "":
		failed = errFlag
		r.Background = args.Background != ""
		if r.Exit = status(output); r.Exit == nil && !failed && !r.Background {
			r.Exit = &zero
		}
	case in.TurnID != "":
	case in.HookEvent == "PostToolUseFailure":
		failed = true
		output = errText
		if r.Exit = status(errText); r.Exit != nil {
			_, output, _ = strings.Cut(errText, "\n")
		}
	default:
		r.Background = args.RunInBackground || backgroundTask(in.ToolResponse)
		if !r.Background {
			r.Exit = &zero
		}
	}
	if r.Session == "" {
		return failed
	}
	// The hook's cwd is the session's directory, not where a cd in the
	// command took the shell, and a sub-agent's is its parent's.
	if dir := runDir(args.Command, in.Cwd, 0); dir != "" {
		r.Cwd = dir
		r.Head, r.Tree, r.Skipped = evidence.Worktree(dir)
	}
	r.OutputTail = output
	evidence.Append(r) //nolint:gosec // the call ran either way; unrecorded, an audit finds no evidence of it
	return failed
}

// runDir is the one directory every command of text runs in, started in
// dir: moved by cd or pushd before any other command, and by git's -C. It
// is "" where the command line does not say: a cd the shell decides or
// after another command, one where a subshell or substitution may hold it,
// git told another repository, or commands that run in different places.
func runDir(text, dir string, depth int) string {
	if depth > maxDepth {
		return ""
	}
	cwd, known, others := dir, true, false
	var dirs []string
	v := visitor{
		script: func(text string) string {
			others = true
			dirs = append(dirs, runDir(text, cwd, depth+1))
			return ""
		},
		call: func(c call) string {
			w := c.words
			switch {
			case len(w) > 0 && !c.expands[0] && (w[0] == "cd" || w[0] == "pushd"):
				// segments puts a subshell's or substitution's commands in
				// line with the rest, so a cd in one would seem to last.
				known = known && !others && !strings.ContainsAny(text, "(`")
				cwd = cdTarget(c, cwd)
				known = known && cwd != ""
				return ""
			case len(w) > 0 && !c.expands[0] && base(w[0]) == "git":
				dirs = append(dirs, gitDir(w[1:], cwd))
			default:
				dirs = append(dirs, cwd)
			}
			others = true
			return ""
		},
	}
	for _, segment := range segments(text) {
		if words, expands := split(segment); len(words) > 0 {
			unwrap(call{words: words, expands: expands}, v)
		}
	}
	if !known {
		return ""
	}
	for _, d := range dirs {
		if d != dirs[0] {
			return ""
		}
	}
	if len(dirs) > 0 {
		return dirs[0]
	}
	return cwd
}

// gitDir is the directory git's options, args, have it work in, started in
// cwd, or "" where they name a repository elsewhere.
func gitDir(args []string, cwd string) string {
	for i := 0; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		switch a := args[i]; {
		case a == "-C" && i+1 < len(args):
			if cwd = expandPath(args[i+1], cwd); !filepath.IsAbs(cwd) {
				return ""
			}
		case a == "--git-dir" || a == "--work-tree" || strings.HasPrefix(a, "--git-dir=") || strings.HasPrefix(a, "--work-tree="):
			return ""
		}
		if gitValueFlags[args[i]] {
			i++
		}
	}
	return cwd
}

// outputText is a shell call's output as its response holds it: Claude
// Code's stdout and then stderr, or the text Codex and ECA give.
func outputText(response json.RawMessage) string {
	var v any
	if json.Unmarshal(response, &v) != nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		return v
	case map[string]any:
		if stdout, ok := v["stdout"].(string); ok {
			stderr, _ := v["stderr"].(string)
			if stderr == "" {
				return stdout
			}
			return strings.TrimSuffix(stdout, "\n") + "\n" + stderr
		}
	}
	return strings.Join(responseTexts(response), "\n")
}

// backgroundTask reports whether Claude Code's response names the
// background task a command was left running as.
func backgroundTask(response json.RawMessage) bool {
	var r struct {
		BackgroundTaskID string `json:"backgroundTaskId"`
	}
	json.Unmarshal(response, &r) //nolint:gosec // a response that is no object names no task
	return r.BackgroundTaskID != ""
}
