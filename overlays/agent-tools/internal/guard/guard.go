// Package guard is a PreToolUse hook for a review-loop session in Claude Code
// or ECA. It denies the ways an agent could write to GitHub or Linear other
// than through pr-loop, which enforces the loop's policy.
//
// It inspects commands as the agent typed them, so it catches a forgotten rule
// but not a command built at run time. In a sandbox without credentials the
// sandbox is the boundary; without one, this is a reminder that cannot be
// forgotten, not a wall.
package guard

import (
	"encoding/json"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

var (
	readOnlyTool = regexp.MustCompile(`(?i)(^|__)(get|list|search|fetch|read|query)[_a-z]*$`)
	assignment   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	wrappers     = map[string]bool{"env": true, "command": true, "nohup": true, "time": true, "timeout": true, "nice": true, "exec": true, "xargs": true}
	shells       = map[string]bool{"bash": true, "sh": true, "zsh": true, "fish": true, "dash": true}
	ghReadOnly   = map[string]bool{"pr view": true, "pr diff": true, "pr list": true, "pr checks": true, "pr status": true,
		"issue view": true, "issue list": true, "repo view": true, "run view": true, "run list": true, "auth status": true}
	gitValueFlags = map[string]bool{"-c": true, "-C": true, "--git-dir": true, "--work-tree": true, "--namespace": true}
)

type input struct {
	HookType  string          `json:"hook_type"` // set by ECA
	ToolName  string          `json:"tool_name"`
	Server    string          `json:"server"` // ECA's MCP server name
	ToolInput json.RawMessage `json:"tool_input"`
	// Claude Code names the subagent making the call, prefixed with its
	// plugin's name ("pr-loop:pr-owner"). ECA's owner carries the owner-only
	// rules as deny patterns instead.
	AgentType string `json:"agent_type"`
}

// Main reads one hook event and writes a denial in the caller's format, or
// nothing to let the call through.
func Main(stdin io.Reader, stdout io.Writer) int {
	var in input
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0
	}
	owner := in.AgentType == "pr-owner" || strings.HasSuffix(in.AgentType, ":pr-owner")
	reason := check(in.ToolName, in.Server, in.ToolInput, owner)
	if reason == "" {
		return 0
	}
	var out any
	if in.HookType != "" {
		out = map[string]string{"approval": "deny", "additionalContext": reason}
	} else {
		out = map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}}
	}
	json.NewEncoder(stdout).Encode(out)
	return 0
}

// Check returns why a tool call by the coordinator is denied, or "" to allow
// it.
func Check(tool, server string, toolInput json.RawMessage) string {
	return check(tool, server, toolInput, false)
}

// CheckOwner is Check for a pr-owner, which is also denied the steps the
// coordinator alone takes.
func CheckOwner(tool, server string, toolInput json.RawMessage) string {
	return check(tool, server, toolInput, true)
}

func check(tool, server string, toolInput json.RawMessage, owner bool) string {
	full := tool
	if server != "" {
		full = server + "__" + tool
	}
	switch {
	case tool == "Bash" || tool == "shell_command" || tool == "git":
		// ECA's git tool takes a whole git or gh command line, like the shell.
		var args struct {
			Command string `json:"command"`
		}
		json.Unmarshal(toolInput, &args)
		return command(args.Command, 0, owner)
	case strings.Contains(strings.ToLower(full), "linear") && !readOnlyTool.MatchString(full):
		return "The review loop does not change Linear. Put the proposed change in the final report for Wesley to approve."
	case strings.Contains(strings.ToLower(full), "github") && !readOnlyTool.MatchString(full):
		return "GitHub writes in the review loop go through pr-loop."
	}
	return ""
}

func command(text string, depth int, owner bool) string {
	for _, segment := range segments(text) {
		words := strip(tokens(segment))
		if len(words) == 0 {
			continue
		}
		name := words[0][strings.LastIndex(words[0], "/")+1:]
		var reason string
		switch {
		case shells[name] && depth < 3:
			for i, w := range words {
				if w == "-c" && i+1 < len(words) {
					reason = command(words[i+1], depth+1, owner)
					break
				}
			}
		case name == "eval" && depth < 3:
			reason = command(strings.Join(words[1:], " "), depth+1, owner)
		case name == "git":
			reason = checkGit(words)
		case name == "gh":
			reason = checkGh(words[1:])
		case name == "pr-loop":
			reason = checkPrLoop(words[1:], owner)
		case name == "git-sign-pass":
			reason = signing
		}
		if reason != "" {
			return reason
		}
	}
	return ""
}

const signing = "Signing needs Wesley's hardware key. Leave the commits unsigned and hand over with the command to run."

const pushGuard = "Publishing private history in a public repository is Wesley's decision. Stop and ask instead of changing push-guard.* settings."

// coordinatorOnly are the pr-loop steps an owner leaves to the coordinator,
// which keeps the stack consistent across PRs.
var coordinatorOnly = map[string]bool{"open": true, "begin": true, "push": true, "reply": true, "resolve": true,
	"request": true, "worktrees": true, "sign": true, "end": true}

// checkPrLoop keeps the decisions that are Wesley's: waiving a CI failure,
// and discarding the unsquashed history.
func checkPrLoop(args []string, owner bool) string {
	if len(args) == 0 {
		return ""
	}
	if owner && coordinatorOnly[args[0]] {
		return "The coordinator alone runs `pr-loop " + args[0] + "`. Report what you need it to do instead."
	}
	for _, a := range args[1:] {
		switch {
		case args[0] == "request" && (a == "--accept-failure" || strings.HasPrefix(a, "--accept-failure=")):
			return "Whether a CI failure is not this PR's doing is Wesley's call. Stop and put the evidence in the report."
		case args[0] == "end" && a == "--prune":
			return "Pruning deletes the unsquashed history and round snapshots; leave that to Wesley."
		}
	}
	return ""
}

func checkGit(words []string) string {
	i := 1
	for i < len(words) && strings.HasPrefix(words[i], "-") {
		if gitValueFlags[words[i]] {
			// An alias given on the command line can stand for any command.
			if words[i] == "-c" && i+1 < len(words) && strings.HasPrefix(strings.ToLower(words[i+1]), "alias.") {
				return "Git aliases are not used in the review loop; run the command itself."
			}
			if words[i] == "-c" && i+1 < len(words) && strings.HasPrefix(strings.ToLower(words[i+1]), "push-guard.") {
				return pushGuard
			}
			i++
		}
		i++
	}
	if i >= len(words) {
		return ""
	}
	sub, rest := words[i], words[i+1:]
	has := func(flags ...string) bool {
		for _, w := range rest {
			for _, f := range flags {
				if w == f || strings.HasPrefix(w, f+"=") {
					return true
				}
			}
		}
		return false
	}
	switch sub {
	case "sign-pass":
		return signing
	case "push":
		return "A review-loop session pushes only with `pr-loop push`, or `pr-loop open` for a new branch, which check the branch policy and need no key touch. The signed push at the end is `pr-loop sign`."
	case "commit":
		switch {
		case has("--no-verify", "-n"):
			return "Repository hooks stay on in the review loop; fix what they report."
		case has("--amend"):
			return "The review loop never rewrites the PR's commits. Commit the change with `git commit --fixup=<commit>`; the signing pass squashes it in."
		case !has("--fixup"):
			return "Every commit in the review loop is `git commit --fixup=<commit>` against the PR commit it belongs to, with the review details as --trailer lines, so the signing pass can squash it in."
		}
	case "config":
		reading := has("--get", "--get-all", "--get-regexp", "-l", "--list")
		for _, w := range rest {
			key := strings.ToLower(w)
			switch {
			case reading:
			case strings.HasPrefix(key, "pr-loop."):
				return "pr-loop.* settings are Wesley's to change. Stop and ask for a decision instead."
			case strings.HasPrefix(key, "push-guard."):
				return pushGuard
			case strings.HasPrefix(key, "alias."):
				return "Git aliases are not used in the review loop; run the command itself."
			}
		}
	}
	// An existing alias runs whatever it names, which this check cannot see.
	if aliases, _ := exec.Command("git", "config", "--get-regexp", `^alias\.`).Output(); len(aliases) > 0 {
		for _, line := range strings.Split(string(aliases), "\n") {
			name, _, _ := strings.Cut(line, " ")
			if strings.TrimPrefix(name, "alias.") == sub {
				return "`git " + sub + "` is an alias; run the command it stands for, so it can be checked."
			}
		}
	}
	return ""
}

func checkGh(args []string) string {
	if len(args) >= 2 && ghReadOnly[args[0]+" "+args[1]] {
		return ""
	}
	if len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
		return "The review loop does not read GitHub credentials; pr-loop holds the login."
	}
	if len(args) >= 1 && args[0] == "api" {
		rest := args[1:]
		endpoint := ""
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				endpoint = a
				break
			}
		}
		if endpoint == "graphql" {
			if strings.Contains(strings.Join(rest, " "), "mutation") {
				return "GraphQL mutations go through pr-loop (reply, resolve, push)."
			}
			return ""
		}
		method, explicit, fields := "GET", false, false
		for i, a := range rest {
			switch a {
			case "-X", "--method":
				if i+1 < len(rest) {
					method, explicit = strings.ToUpper(rest[i+1]), true
				}
			case "-f", "-F", "--field", "--raw-field", "--input":
				fields = true
			}
		}
		if method != "GET" || (fields && !explicit) {
			return "Only read-only `gh api` calls are allowed in the review loop; writes go through pr-loop."
		}
		return ""
	}
	what := strings.Join(args[:min(2, len(args))], " ")
	return "`gh " + what + "` can change GitHub. In the review loop use pr-loop (status, ci, logs, threads, reply, resolve, request). " +
		"Top-level PR comments, reviews, merges, PR edits and marking a PR ready are left to Wesley."
}

// strip drops leading environment assignments and wrapper commands.
func strip(words []string) []string {
	for len(words) > 0 && (assignment.MatchString(words[0]) || wrappers[words[0]]) {
		words = words[1:]
	}
	return words
}

// segments splits a command line at ;, &&, ||, |, &, newlines and command
// substitutions, outside quotes.
func segments(text string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	flush := func() {
		out = append(out, cur.String())
		cur.Reset()
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(text) {
				cur.WriteByte(c)
				i++
				c = text[i]
			}
			cur.WriteByte(c)
		case c == '\'' || c == '"':
			quote = c
			cur.WriteByte(c)
		case c == '\\' && i+1 < len(text):
			cur.WriteByte(c)
			i++
			cur.WriteByte(text[i])
		case c == ';' || c == '\n' || c == '|' || c == '&' || c == '`' || c == ')' || c == '(':
			flush()
		case c == '$' && i+1 < len(text) && text[i+1] == '(':
			flush()
			i++
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	return out
}

// tokens splits one segment into words the way a shell would, removing quotes.
func tokens(segment string) []string {
	var out []string
	var cur strings.Builder
	in, quote := false, byte(0)
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(segment) {
				i++
				cur.WriteByte(segment[i])
			} else {
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == '\\' && i+1 < len(segment):
			i++
			cur.WriteByte(segment[i])
			in = true
		case c == ' ' || c == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		case c == '#' && !in:
			return out
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}
