// Package guard is a PreToolUse hook for Claude Code, Codex and ECA. With
// `--baseline`, in every session, it denies what is the operator's to do:
// publishing, merging, GitHub's settings and credentials. In a review-loop
// session it also denies the ways an agent could write to GitHub or Linear
// other than through pr-loop, which enforces the loop's policy, and keeps
// the loop's pr-owner and reviewer agents to their roles.
//
// It inspects commands as the agent typed them, so it catches a forgotten rule
// but not a command built at run time. In a sandbox without credentials the
// sandbox is the boundary; without one, this is a reminder that cannot be
// forgotten, not a wall.
package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var (
	ghReadOnly = map[string]bool{"pr view": true, "pr diff": true, "pr list": true, "pr checks": true, "pr status": true,
		"issue view": true, "issue list": true, "repo view": true, "run view": true, "run list": true, "auth status": true}
	gitValueFlags = map[string]bool{"-c": true, "-C": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
		"--config-env": true}
	// ghAPIValueFlags are the `gh api` flags whose value is the next word,
	// which is therefore not the endpoint.
	ghAPIValueFlags = map[string]bool{"-X": true, "--method": true, "-H": true, "--header": true,
		"-f": true, "--raw-field": true, "-F": true, "--field": true, "--input": true, "-q": true, "--jq": true,
		"-t": true, "--template": true, "--hostname": true, "-p": true, "--preview": true, "--cache": true}
)

type input struct {
	HookType  string          `json:"hook_type"` // set by ECA
	ToolName  string          `json:"tool_name"`
	Server    string          `json:"server"` // ECA's MCP server name
	ToolInput json.RawMessage `json:"tool_input"`
	// Claude Code names the subagent making the call, prefixed with its
	// plugin's name if a plugin defines it ("pr-loop:pr-owner"); ECA names
	// the agent in agent. Codex names none.
	AgentType string `json:"agent_type"`
	Agent     string `json:"agent"`
	// The directory the command runs in, which bounds the files the guard
	// reads. Without it, the guard's own working directory stands in.
	Cwd string `json:"cwd"`
	// Set by Codex alone, whose hooks cannot ask the user.
	TurnID    string `json:"turn_id"`
	SessionID string `json:"session_id"`
	// Set by Claude Code and Codex for a sub-agent's call alone.
	AgentID string `json:"agent_id"`
}

// role is who makes a call. Each role is denied everything the one before it
// is, and more.
type role int

const (
	baseline    role = iota // every session: denied what is the operator's to do
	coordinator             // a review loop's coordinator: also kept to pr-loop
	owner                   // pr-owner: also denied the steps the coordinator alone takes
	reviewer                // reviewer: also denied every change to the repository or GitHub
)

// roleNames are the agents with a role of their own, by agent name.
var roleNames = map[string]role{"pr-coordinator": coordinator, "pr-owner": owner, "reviewer": reviewer}

// roleOf is the role of the agent named agentType, or fallback for an agent
// without a role of its own.
func roleOf(agentType string, fallback role) role {
	for name, r := range roleNames {
		if agentType == name || strings.HasSuffix(agentType, ":"+name) {
			return r
		}
	}
	return fallback
}

// Main reads one hook event and writes a denial in the caller's format, or
// nothing to let the call through. Without args, an agent without a role of
// its own is a loop's coordinator; with `--baseline`, for the hook every
// session runs, it is an ordinary session.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fallback := coordinator
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--baseline":
		fallback = baseline
	case len(args) == 1 && args[0] == "--post":
		return post(stdin, stdout)
	default:
		fmt.Fprintf(stderr, "pr-loop guard: takes no arguments, --baseline or --post; got %q\n", args)
		return 1
	}
	var in input
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0
	}
	if in.Cwd == "" {
		in.Cwd, _ = os.Getwd()
	}
	who := roleOf(in.AgentType+in.Agent, fallback)
	reason := checkSubagentStash(in)
	if reason == "" {
		reason = check(in.ToolName, in.Server, in.ToolInput, who, in.Cwd)
	}
	if reason == "" && in.TurnID != "" && (in.ToolName == "Bash" || in.ToolName == "exec_command") {
		// Codex alone keeps a command's terminal for write_stdin.
		var args struct {
			Command string `json:"command"`
		}
		json.Unmarshal(in.ToolInput, &args)
		if terminalReader(args.Command) {
			reason = readsTerminal
		}
	}
	if reason == "" {
		reason = checkOwner(in)
	}
	if reason == "" {
		edit := in.ToolName == "Edit" || in.ToolName == "Write" || in.ToolName == "MultiEdit"
		if fallback == baseline && edit && in.HookType == "" {
			if context := branchHint(in.SessionID, in.ToolInput, in.Cwd); context != "" {
				json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
					"hookEventName": "PreToolUse", "additionalContext": context}})
			}
		}
		return 0
	}
	if reason == sleeping && in.HookType == "" && in.TurnID == "" {
		reason = sleepingClaude
	}
	if reason == keyTouch && in.HookType != "" {
		reason = keyTouchPlain
	}
	var out any
	decision := "deny"
	if asks[reason] && who == baseline {
		// Claude Code and ECA ask the operator; Codex cannot.
		if in.TurnID != "" {
			reason = cannotAsk(reason)
		} else {
			decision = "ask"
		}
	}
	if in.HookType != "" {
		out = map[string]string{"approval": decision, "additionalContext": reason}
	} else {
		out = map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "PreToolUse", "permissionDecision": decision, "permissionDecisionReason": reason}}
	}
	json.NewEncoder(stdout).Encode(out)
	return 0
}

// CheckBaseline is Check for an ordinary session.
func CheckBaseline(tool, server string, toolInput json.RawMessage) string {
	dir, _ := os.Getwd()
	return check(tool, server, toolInput, baseline, dir)
}

// Check returns why a tool call by the coordinator, run in the current
// directory, is denied, or "" to allow it.
func Check(tool, server string, toolInput json.RawMessage) string {
	dir, _ := os.Getwd()
	return check(tool, server, toolInput, coordinator, dir)
}

// CheckOwner is Check for a pr-owner, which is also denied the steps the
// coordinator alone takes.
func CheckOwner(tool, server string, toolInput json.RawMessage) string {
	dir, _ := os.Getwd()
	return check(tool, server, toolInput, owner, dir)
}

// CheckReviewer is CheckOwner for a reviewer, which only reads: it is also
// denied git commands that change the repository and every pr-loop command
// but the reading ones, and is told so whichever rule a call breaks.
func CheckReviewer(tool, server string, toolInput json.RawMessage) string {
	dir, _ := os.Getwd()
	return check(tool, server, toolInput, reviewer, dir)
}

// check returns a denial's reason, which reaches the agent verbatim: a first
// sentence saying why, then what to do instead, naming the command in
// backticks or handing the step to the operator. A reason that only says no
// sends the agent looking for another way through. TestCheck and TestPolicy
// hold every reason to this.
func check(tool, server string, toolInput json.RawMessage, who role, dir string) string {
	if service, name, mcp := mcpTool(tool, server); mcp {
		return checkMCP(service, name, who)
	}
	switch {
	case tool == "Bash" || tool == "exec_command" || tool == "shell_command" || tool == "git":
		// ECA's git tool takes a whole git or gh command line, like the shell.
		// Claude Code's Bash runs the command in the background with
		// run_in_background, and ECA's shell_command with a background name.
		var args struct {
			Command         string `json:"command"`
			RunInBackground bool   `json:"run_in_background"`
			Background      string `json:"background"`
		}
		json.Unmarshal(toolInput, &args)
		return command(args.Command, 0, who, dir, args.RunInBackground || args.Background != "")
	case tool == "Monitor":
		// Claude Code's Monitor runs a command in the background as Bash
		// does, or opens a WebSocket.
		var args struct {
			Command string `json:"command"`
			WS      *struct {
				URL string `json:"url"`
			} `json:"ws"`
		}
		json.Unmarshal(toolInput, &args)
		if args.WS != nil {
			return checkWebSocket(args.WS.URL)
		}
		return command(args.Command, 0, who, dir, true)
	case tool == "local_shell":
		// Codex's local_shell runs an argv, which the guard reads as the
		// command line that would run it.
		var args struct {
			Command          json.RawMessage `json:"command"`
			WorkingDirectory string          `json:"working_directory"`
		}
		json.Unmarshal(toolInput, &args)
		if filepath.IsAbs(args.WorkingDirectory) {
			dir = args.WorkingDirectory
		}
		var argv []string
		if json.Unmarshal(args.Command, &argv) != nil {
			var line string
			json.Unmarshal(args.Command, &line)
			return command(line, 0, who, dir, false)
		}
		quoted := make([]string, len(argv))
		for i, a := range argv {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		return command(strings.Join(quoted, " "), 0, who, dir, false)
	case tool == "write_stdin":
		// Codex's write_stdin types chars into a session exec_command
		// started, which may be a shell; the session runs on its own, so a
		// sleep there holds nothing.
		var args struct {
			Chars string `json:"chars"`
		}
		json.Unmarshal(toolInput, &args)
		if reason := command(args.Chars, 0, who, dir, true); reason != "" {
			return typed + reason
		}
		return ""
	case tool == "ExitWorktree":
		return checkExitWorktree(toolInput, dir)
	case tool == "Edit" || tool == "Write" || tool == "MultiEdit" || tool == "NotebookEdit" || tool == "apply_patch",
		server == "eca" && (tool == "write_file" || tool == "edit_file" || tool == "move_file"):
		return checkFileTool(tool, toolInput, dir)
	}
	return ""
}

// typed prefixes the reason a command line typed into a running session is
// denied for.
const typed = "This text goes to a running session, which may be a shell, so the guard reads it as a command line. "

// maxDepth is how many shells, evals and watches deep the guard reads a
// command; it denies a deeper one.
const maxDepth = 3

const tooDeep = "The guard reads commands nested at most three shells deep, and this one is deeper. Run the innermost command directly."

// unknownProgram is why a command whose program the shell picks at run time
// is denied when its words include a guarded verb.
const unknownProgram = "The shell picks this command's program at run time, so the guard cannot check it. Run the program by name, as `git ...` or `gh ...`."

// guardedVerbs are the words that make a command whose program the guard
// cannot see one it denies.
var guardedVerbs = set("push", "merge", "close", "reopen", "ready", "comment", "review", "edit", "create",
	"delete", "token", "api", "commit", "config", "sign-pass", "publish", "switch", "activate")

// command judges a command line run in dir, in the background when
// background is set.
func command(text string, depth int, who role, dir string, background bool) string {
	if depth > maxDepth {
		return tooDeep
	}
	var calls []call
	var backgrounds []bool
	texts, inBackground := backgroundSegments(text)
	for i, segment := range texts {
		if words, expands := split(segment); len(words) > 0 {
			calls = append(calls, call{words: words, expands: expands})
			backgrounds = append(backgrounds, background || inBackground[i])
		}
	}
	// A file a gh call reads is what the guard sees only when nothing else
	// on the command line can have written it first.
	files := ""
	if depth == 0 && len(calls) == 1 {
		files = dir
	}
	// Where each command runs, as cd moves it; "" once the guard cannot
	// tell.
	cwd := dir
	// Whether the call being judged runs in the background.
	bg := false
	v := visitor{
		script: func(text string) string { return command(text, depth+1, who, cwd, bg) },
		call: func(c call) string {
			if len(c.words) > 0 && !c.expands[0] && (c.words[0] == "cd" || c.words[0] == "pushd") {
				cwd = cdTarget(c, cwd)
				return ""
			}
			if !bg && longSleep(c) {
				return sleeping
			}
			return program(c, who, files, cwd)
		},
	}
	for i, c := range calls {
		bg = backgrounds[i]
		if reason := unwrap(c, v); reason != "" {
			return reason
		}
	}
	return ""
}

// cdTarget is where cd moves from cwd: "" when the shell decides, or cwd
// is unknown and the target relative.
func cdTarget(c call, cwd string) string {
	args, expands := c.words[1:], c.expands[1:]
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-" {
		args, expands = args[1:], expands[1:]
	}
	switch {
	case len(args) == 0:
		return home
	case expands[0] || args[0] == "-":
		return ""
	}
	path := expandPath(args[0], cwd)
	if !filepath.IsAbs(path) {
		return ""
	}
	return path
}

// program judges a call once unwrap has found the program it runs, in dir.
func program(c call, who role, files, dir string) string {
	if reason := checkEnv(c.env); reason != "" {
		return reason
	}
	if reason := checkNgrokEnv(c); reason != "" {
		return reason
	}
	if len(c.words) == 0 {
		return ""
	}
	if c.expands[0] {
		for _, w := range c.words[1:] {
			if guardedVerbs[w] {
				return unknownProgram
			}
		}
		return ""
	}
	if reason := checkSession(c, dir); reason != "" {
		return reason
	}
	words := c.words
	switch name := base(words[0]); {
	case name == "git":
		return checkGit(words, c.env, who, dir)
	case name == "gh":
		if reason := checkGh(words[1:], c.expands[1:], files, who); reason != "" && who == reviewer {
			return reviewerReport
		} else {
			return reason
		}
	case name == "pr-loop":
		return checkPrLoop(words[1:], who)
	case name == "git-fresh-review" && who == reviewer:
		return nestedReview
	case name == "agent-plan" && len(words) > 1 && words[1] == "sync":
		return planSync
	case name == "agent-plan" && len(words) > 1 && words[1] == "reseal":
		return planReseal
	case name == "agent-plan" && who == reviewer && (len(words) < 2 || !planReads[words[1]]):
		return reviewerReport
	case name == "git-sign-pass" && who == reviewer:
		return reviewerReport
	case name == "git-sign-pass" && who >= coordinator:
		return signing
	case name == "git-sign-pass":
		return keyTouch
	case name == "curl" || name == "wget" || name == "http" || name == "https" || name == "xh" || name == "xhs":
		if githubAPIWrite(name, words[1:]) {
			return githubAPI
		}
	}
	return ""
}

const githubAPI = "Writes to GitHub's API go through gh or pr-loop, which the guard can check. Use `pr-loop` or a `gh` command, or put the call in your report for the operator."

// githubAPIWrite reports whether an HTTP client's arguments send something
// to GitHub's API or upload host.
func githubAPIWrite(name string, args []string) bool {
	github := false
	for _, a := range args {
		github = github || strings.Contains(a, "api.github.com") || strings.Contains(a, "uploads.github.com")
	}
	if !github {
		return false
	}
	read := func(method string) bool {
		method = strings.ToUpper(method)
		return method == "GET" || method == "HEAD" || method == "OPTIONS"
	}
	switch name {
	case "curl":
		for i, a := range args {
			switch {
			case a == "-X" || a == "--request":
				if i+1 < len(args) && !read(args[i+1]) {
					return true
				}
			case strings.HasPrefix(a, "--request="):
				if !read(strings.TrimPrefix(a, "--request=")) {
					return true
				}
			case strings.HasPrefix(a, "-X"):
				if !read(a[2:]) {
					return true
				}
			case strings.HasPrefix(a, "--data"), strings.HasPrefix(a, "--form"), strings.HasPrefix(a, "--json"),
				strings.HasPrefix(a, "--upload-file"), strings.HasPrefix(a, "-d"), strings.HasPrefix(a, "-F"),
				strings.HasPrefix(a, "-T"):
				return true
			}
		}
	case "wget":
		for i, a := range args {
			switch {
			case strings.HasPrefix(a, "--post-"), strings.HasPrefix(a, "--body-"):
				return true
			case a == "--method":
				if i+1 < len(args) && !read(args[i+1]) {
					return true
				}
			case strings.HasPrefix(a, "--method="):
				if !read(strings.TrimPrefix(a, "--method=")) {
					return true
				}
			}
		}
	default: // httpie and xh: [METHOD] URL [ITEM...]
		positional := 0
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			positional++
			if positional == 1 && httpMethod.MatchString(a) {
				if !read(a) {
					return true
				}
				continue
			}
			// A data or file item; `==` is a query parameter.
			if dataItem.MatchString(a) {
				return true
			}
		}
	}
	return false
}

var (
	httpMethod = regexp.MustCompile(`^[A-Za-z]+$`)
	dataItem   = regexp.MustCompile(`^[^=:@/]+(:=@?|=@?|@)([^=]|$)`)
)

// configKey matches each key in GIT_CONFIG_PARAMETERS, whose entries are
// 'key=value' or 'key'='value'.
var configKey = regexp.MustCompile(`'([^'=]+)'?=`)

// checkEnv checks the assignments made for a command, which reach every git
// it runs, however deep.
func checkEnv(env []string) string {
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case name == "GIT_CONFIG_PARAMETERS":
			for _, m := range configKey.FindAllStringSubmatch(value, -1) {
				if reason := checkConfigKey(m[1]); reason != "" {
					return reason
				}
			}
		case strings.HasPrefix(name, "GIT_CONFIG_KEY_"):
			if reason := checkConfigKey(value); reason != "" {
				return reason
			}
		case name == "GIT_SSH" || name == "GIT_SSH_COMMAND" || name == "GIT_ASKPASS":
			return transport
		}
	}
	return ""
}

// transportKeys are the git config keys that choose credentials or where
// and how git connects.
var transportKeys = regexp.MustCompile(`^(credential(\..*)?|http\..*extraheader|url\..*\.(push)?insteadof|core\.sshcommand)$`)

const transport = "Credentials and the transport git uses are the operator's configuration. Leave them as they are, and put the change you need in your report for the operator."

// checkConfigKey checks a key set for one git command, by `-c`,
// `--config-env` or the environment, or written with `git config`.
func checkConfigKey(key string) string {
	key = strings.ToLower(key)
	switch {
	case strings.HasPrefix(key, "alias."):
		return alias
	case strings.HasPrefix(key, "push-guard."):
		return pushGuard
	case transportKeys.MatchString(key):
		return transport
	}
	return ""
}

const signing = "Signing needs the operator's hardware key. Leave the commits unsigned and end the report with the command for the operator to run: `pr-loop sign` for the loop's stack."

const pushGuard = "Publishing private history in a public repository is the operator's decision. Leave push-guard.* alone, and name the repository and the commits in the report for the operator."

const prLoopWrites = "Use `pr-loop push`, `reply`, `resolve`, `request` or `link`; any other write goes in the report for the operator."

const alias = "A git alias can stand for any command, which the guard cannot check. Run the command itself, as `git <command>`."

// coordinatorOnly are the pr-loop steps an owner leaves to the coordinator,
// which keeps the stack consistent across PRs.
var coordinatorOnly = map[string]bool{"open": true, "link": true, "begin": true, "push": true, "reply": true, "resolve": true,
	"request": true, "worktrees": true, "sign": true, "end": true}

// reviewerReads are the pr-loop commands a reviewer may run, besides
// `wait ci`.
var reviewerReads = map[string]bool{"stack": true, "threads": true, "status": true, "ci": true, "logs": true,
	"setup-command": true}

// planSync is why no agent syncs plans: the sync pushes with a deploy key,
// which is the watcher's, outside every session.
const planSync = "Plans are synced by a watcher outside every session, which alone pushes them. Leave it to the watcher; if another host's plans look stale, say so in your report for the operator."

// planReseal is why no agent reseals a log: it accepts the edits made to
// the log outside agent-plan.
const planReseal = "Resealing a plan's log accepts edits made to it outside agent-plan, which is the operator's call. Hand the operator the command, `agent-plan reseal PLAN`, in your report."

// planReads are the agent-plan commands a reviewer may run.
var planReads = set("status", "goal", "find", "tokens", "questions", "-h", "--help", "help")

// reviewerReport is why a reviewer may not make a change.
const reviewerReport = "A reviewer only reads. Report the finding with its evidence; whoever started the review acts on it."

// nestedReview is why a reviewer may not start a review.
const nestedReview = "A reviewer does not start a review of its own. Report your findings; whoever started you decides on another round."

// checkPrLoop keeps the decisions that are the operator's: waiving a CI
// failure, and discarding the unsquashed history.
func checkPrLoop(args []string, who role) string {
	if len(args) == 0 {
		return ""
	}
	waitCI := args[0] == "wait" && len(args) > 1 && args[1] == "ci"
	if who == reviewer && !reviewerReads[args[0]] && !waitCI {
		return reviewerReport
	}
	if who >= owner && coordinatorOnly[args[0]] {
		return "The coordinator alone runs `pr-loop " + args[0] + "`. Report what you need it to do instead."
	}
	for _, a := range args[1:] {
		switch {
		case args[0] == "request" && (a == "--accept-failure" || strings.HasPrefix(a, "--accept-failure=")):
			return "Whether a CI failure is not this PR's doing is the operator's call. Put the evidence in the report, with `pr-loop request --accept-failure CHECK PR` for the operator to run if they agree."
		case args[0] == "end" && a == "--prune":
			return "Pruning deletes the unsquashed history and round snapshots, which is the operator's call. Leave `pr-loop end --prune` to them."
		}
	}
	return ""
}

// gitWrites are the git commands that change a worktree, the index, refs,
// stashes or a remote, which a reviewer may not run, and which a sub-agent
// runs only where it may edit (checkOwner).
var gitWrites = map[string]bool{"commit": true, "rebase": true, "reset": true, "checkout": true, "switch": true,
	"restore": true, "merge": true, "cherry-pick": true, "revert": true, "am": true, "apply": true, "stash": true,
	"add": true, "rm": true, "mv": true, "clean": true, "tag": true, "update-ref": true, "notes": true, "worktree": true,
	"push": true, "sign-pass": true, "pull": true}

// checkGit checks a git command run in dir, "" when the guard cannot tell
// where, with the assignments env made for it.
func checkGit(words, env []string, who role, dir string) string {
	i := 1
	var config []string // the -c and --config-env options, for gitState.signs
	for i < len(words) && strings.HasPrefix(words[i], "-") {
		// Configuration given on the command line, such as an alias, which
		// can stand for any command.
		key := ""
		switch w := words[i]; {
		case (w == "-c" || w == "--config-env") && i+1 < len(words):
			key, _, _ = strings.Cut(words[i+1], "=")
			config = append(config, w, words[i+1])
		case strings.HasPrefix(w, "--config-env="):
			key, _, _ = strings.Cut(strings.TrimPrefix(w, "--config-env="), "=")
			config = append(config, w)
		case w == "-C" && i+1 < len(words):
			if dir = expandPath(words[i+1], dir); !filepath.IsAbs(dir) {
				dir = ""
			}
		case w == "--git-dir" || w == "--work-tree" || strings.HasPrefix(w, "--git-dir=") || strings.HasPrefix(w, "--work-tree="):
			dir = ""
		}
		if reason := checkConfigKey(key); reason != "" {
			return reason
		}
		if gitValueFlags[words[i]] {
			i++
		}
		i++
	}
	if i >= len(words) {
		return ""
	}
	sub, rest := words[i], words[i+1:]
	if who == reviewer && sub == "fresh-review" {
		return nestedReview
	}
	if who == reviewer && gitWrites[sub] && !(sub == "worktree" && len(rest) > 0 && rest[0] == "list") {
		return reviewerReport
	}
	if reason := checkGitSession(sub, rest, gitState{dir: dir}); reason != "" {
		return reason
	}
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
	switch {
	case sub == "credential" && len(rest) > 0 && rest[0] == "fill",
		sub == "credential-osxkeychain" && len(rest) > 0 && rest[0] == "get":
		return secretRead
	case sub == "sign-pass" && who >= coordinator:
		return signing
	case sub == "sign-pass":
		return keyTouch
	case sub == "push" && who >= coordinator:
		return "A review-loop session pushes only with `pr-loop push`, or `pr-loop open` for a new branch, which check the branch policy and need no key touch. The signed push at the end is `pr-loop sign`."
	case sub == "push":
		return "Publishing a branch is the operator's step, signed with their key. Open a new branch as a draft PR with `pr-loop open`, or end your report with `git sign-pass --push` for the operator to run."
	case overSSH(sub, rest, gitState{dir: dir, config: config, env: env}):
		return keyTouch
	case sub == "remote" && len(rest) > 0 && (rest[0] == "add" || rest[0] == "set-url"):
		return remotes
	case sub == "commit" && who >= coordinator:
		switch {
		case has("--no-verify", "-n"):
			return "Repository hooks stay on in the review loop. Fix what they report and commit again with `git commit --fixup=<commit>`."
		case has("--amend"):
			return "The review loop never rewrites the PR's commits. Commit the change with `git commit --fixup=<commit>`; the signing pass squashes it in."
		case !has("--fixup"):
			return "Every commit in the review loop is a fixup the signing pass can squash in. Commit with `git commit --fixup=<commit>` against the PR commit it belongs to, with the review details as --trailer lines."
		}
	case has("--no-verify") || sub == "commit" && has("-n"):
		return "Repository hooks check every change, and stay on. Fix what they report and run the command again without `--no-verify`; if a hook itself is broken, say so in your report for the operator."
	case sub == "tag" && tagWrites(rest):
		return "Tags are published history, which is the operator's to make or remove. List them with `git tag -l`, and put the `git tag` command in your report for the operator."
	case sub == "config" && !configRead(rest):
		for _, w := range rest {
			key := strings.ToLower(w)
			switch {
			case strings.HasPrefix(key, "pr-loop."):
				return "pr-loop.* settings are the operator's to change. Read them with `git config --get`, and put the `git config` command you propose in the report for the operator."
			case remoteURL.MatchString(key):
				return remotes
			}
			if reason := checkConfigKey(key); reason != "" {
				return reason
			}
			if !has("--worktree") && sharedKeys.MatchString(key) {
				return sharedConfig
			}
		}
	}
	if reason := checkSigning(sub, rest, gitState{dir: dir, config: config, env: env}); reason != "" {
		return reason
	}
	// An existing alias runs whatever it names, which this check cannot see.
	if aliases, _ := exec.Command("git", "config", "--get-regexp", `^alias\.`).Output(); len(aliases) > 0 {
		for _, line := range strings.Split(string(aliases), "\n") {
			name, _, _ := strings.Cut(line, " ")
			if strings.TrimPrefix(name, "alias.") == sub {
				return "`git " + sub + "` is an alias, so the guard cannot see what it runs. Run the command it stands for, which `git config --get alias." + sub + "` shows."
			}
		}
	}
	return ""
}

// tagLists are the `git tag` options that make it list or verify tags.
var tagLists = set("-l", "--list", "-v", "--verify", "--contains", "--no-contains", "--merged", "--no-merged", "--points-at")

// tagWrites reports whether `git tag` with these arguments creates,
// deletes or moves a tag rather than listing or verifying them.
func tagWrites(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, _, _ := strings.Cut(a, "=")
		switch {
		case tagLists[name], strings.HasPrefix(a, "-n") && !strings.HasPrefix(a, "--"):
			return false
		case a == "--sort" || a == "--format" || a == "--color" || a == "--column":
			i++
		case !strings.HasPrefix(a, "-"),
			set("-a", "-s", "-u", "-f", "-d", "-m", "-F", "-e", "--annotate", "--sign", "--local-user", "--force",
				"--delete", "--message", "--file", "--edit")[name]:
			return true
		}
	}
	return false
}

const remotes = "Where a repository's remotes point is the operator's configuration. Use the remotes as they are, and put the change you need in your report for the operator."

var remoteURL = regexp.MustCompile(`^remote\..*\.(push)?url$`)

// configRead reports whether `git config`'s arguments only read.
func configRead(args []string) bool {
	if len(args) > 0 && (args[0] == "get" || args[0] == "list") {
		return true
	}
	for _, a := range args {
		switch {
		case a == "--get" || a == "--get-all" || a == "--get-regexp" || a == "--get-urlmatch" || a == "-l" || a == "--list",
			strings.HasPrefix(a, "--get-color"):
			return true
		}
	}
	return false
}

// checkGh checks gh's arguments, with which of them the shell expands, for
// who: a loop's roles are kept to reading and pr-loop, and every session to
// what is not the operator's call. It reads files a GraphQL call names only
// inside workspace, and none when workspace is "".
func checkGh(args []string, expanded []bool, workspace string, who role) string {
	if who >= coordinator {
		if reason := checkLoopGh(args, expanded, workspace); reason != "" {
			return reason
		}
	}
	return checkBaselineGh(args, expanded, workspace)
}

// checkLoopGh keeps a loop's roles to reading GitHub.
func checkLoopGh(args []string, expanded []bool, workspace string) string {
	if len(args) >= 2 && ghReadOnly[args[0]+" "+args[1]] {
		return ""
	}
	if len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
		return "The review loop does not read GitHub credentials, which pr-loop holds. Read GitHub with `pr-loop status`, `ci`, `logs` or `threads`."
	}
	if len(args) >= 1 && args[0] == "api" {
		return checkAPI(args[1:], expanded[1:], workspace, "Only read-only `gh api` calls are allowed in the review loop. "+prLoopWrites)
	}
	what := strings.Join(args[:min(2, len(args))], " ")
	return "`gh " + what + "` can change GitHub. In the review loop use pr-loop (status, ci, logs, threads, reply, resolve, request). " +
		"Top-level PR comments, reviews, merges, PR edits and marking a PR ready are left to the operator."
}

// checkAPI denies a `gh api` call that writes, with write as the reason
// for a REST call.
func checkAPI(args []string, expanded []bool, workspace, write string) string {
	endpoint := ""
	for i := 0; i < len(args); i++ {
		if ghAPIValueFlags[args[i]] {
			i++
		} else if !strings.HasPrefix(args[i], "-") {
			endpoint = args[i]
			break
		}
	}
	if endpoint == "graphql" {
		return checkGraphQL(args, expanded, workspace)
	}
	method, explicit, fields := apiMethod(args)
	if method != "GET" || (fields && !explicit) {
		return write
	}
	return ""
}

// ghReads are the gh commands every session may run, by command group;
// an empty verb stands for every command in the group, and the group alone
// for gh's help.
var ghReads = map[string]map[string]bool{
	"":           set(""),
	"pr":         set("", "view", "list", "diff", "checks", "status", "checkout"),
	"issue":      set("", "view", "list", "status"),
	"repo":       set("", "view", "list", "clone", "set-default"),
	"run":        set("", "view", "list", "watch", "download"),
	"release":    set("", "view", "list", "download"),
	"workflow":   set("", "view", "list"),
	"gist":       set("", "view", "list"),
	"label":      set("", "list"),
	"secret":     set("", "list"),
	"variable":   set("", "list", "get"),
	"cache":      set("", "list"),
	"ruleset":    set("", "list", "view", "check"),
	"extension":  set("", "list", "search", "browse"),
	"alias":      set("", "list"),
	"config":     set("", "get", "list"),
	"org":        set("", "list"),
	"auth":       set("", "status"),
	"search":     nil,
	"browse":     nil,
	"status":     nil,
	"version":    nil,
	"help":       nil,
	"completion": nil,
}

const (
	ghOperatorsCall = "Merging, closing, reopening or marking a PR ready is the operator's call. Say in your report that the PR is ready for it."
	ghComments      = "Reviews, top-level comments and issues are the operator's to write. Reply inside a review thread with `pr-loop reply`, and put anything else in your report for the operator."
	ghDraft         = "A PR opens as a draft; marking it ready is the operator's call. Open it with `pr-loop open`, or with `gh pr create --draft`."
	ghEdit          = "Only a PR's base, title and body may change here. Use `gh pr edit` with `--base`, `--title`, `--body` or `--body-file`, and leave labels, reviewers and the rest to the operator."
	ghAuth          = "GitHub credentials stay with gh and pr-loop. Use `gh` or `pr-loop` commands, which hold the login, and leave gh's login to the operator."
	ghAPIWrite      = "Only read-only `gh api` calls are allowed. Reply in a review thread with `pr-loop reply` or `resolve`, and put any other write in your report for the operator."
)

// ghEditFlags are the `gh pr edit` flags every session may use, with
// whether each takes the next word as its value.
var ghEditFlags = map[string]bool{"--base": true, "-B": true, "--title": true, "-t": true, "--body": true, "-b": true,
	"--body-file": true, "-F": true, "--repo": true, "-R": true}

// checkBaselineGh keeps every session from the GitHub changes that are
// the operator's to make.
func checkBaselineGh(args []string, expanded []bool, workspace string) string {
	var words []string
	for i := 0; i < len(args) && len(words) < 2; i++ {
		switch a := args[i]; {
		case a == "-R" || a == "--repo" || a == "--hostname":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			words = append(words, a)
		}
	}
	group, verb := "", ""
	if len(words) > 0 {
		group = words[0]
	}
	if len(words) > 1 {
		verb = words[1]
	}
	has := func(flags ...string) bool {
		for _, a := range args {
			for _, f := range flags {
				if a == f || strings.HasPrefix(a, f+"=") {
					return true
				}
			}
		}
		return false
	}
	switch {
	case group == "api":
		at := slices.Index(args, "api")
		return checkAPI(args[at+1:], expanded[at+1:], workspace, ghAPIWrite)
	case group == "auth" && verb == "status" && has("-t", "--show-token"):
		return ghAuth
	case group == "auth" && !ghReads[group][verb]:
		return ghAuth
	case group == "pr" && verb == "create":
		// The last --draft or -d decides, and a value must parse as true.
		draft := false
		for _, a := range args {
			name, value, valued := strings.Cut(a, "=")
			if name != "--draft" && name != "-d" {
				continue
			}
			draft = true
			if valued {
				draft, _ = strconv.ParseBool(value)
			}
		}
		if draft {
			return ""
		}
		return ghDraft
	case group == "pr" && verb == "edit":
		after := false
		for i := 0; i < len(args); i++ {
			a := args[i]
			if !after {
				after = a == "edit"
				continue
			}
			if !strings.HasPrefix(a, "-") {
				continue
			}
			name, _, attached := strings.Cut(a, "=")
			value, known := ghEditFlags[name]
			if !known {
				return ghEdit
			}
			if value && !attached {
				i++
			}
		}
		return ""
	case group == "pr" && verb == "ready":
		if has("--undo") {
			return ""
		}
		return ghOperatorsCall
	case group == "pr" && (verb == "merge" || verb == "close" || verb == "reopen"):
		return ghOperatorsCall
	case group == "pr" && (verb == "review" || verb == "comment"), group == "issue" && !ghReads[group][verb]:
		return ghComments
	}
	verbs, known := ghReads[group]
	switch {
	case !known:
		return "The guard does not know `gh " + group + "`, so it cannot tell a read from a write. Use a gh command it knows, such as `gh pr view` or a GET with `gh api`, or put the command in your report for the operator."
	case verbs == nil || verbs[verb]:
		return ""
	}
	return "`gh " + group + " " + verb + "` changes GitHub in a way that is the operator's call. Put the command in your report for the operator."
}

const graphQLMutation = "GraphQL mutations go through pr-loop. " + prLoopWrites

const unreadableQuery = "The guard cannot read this GraphQL query, so it cannot tell a read from a mutation. " +
	"Pass the query inline with `gh api graphql -f query='...'`, or name a file in the workspace with `-F query=@FILE` in a command of its own; " +
	"a write goes through `pr-loop push`, `reply`, `resolve` or `request`."

// checkGraphQL denies a mutation, and a query or request body the guard
// cannot read: one from stdin, one the shell expands, or a file that
// readQuery refuses. Other fields are GraphQL variables, which cannot make a
// read a write.
func checkGraphQL(args []string, expanded []bool, workspace string) string {
	if strings.Contains(strings.Join(args, " "), "mutation") {
		return graphQLMutation
	}
	for i := 0; i < len(args); i++ {
		flag, value, at := args[i], "", i
		switch {
		case strings.HasPrefix(flag, "--") && strings.Contains(flag, "="):
			flag, value, _ = strings.Cut(flag, "=")
		case len(flag) > 2 && (flag[:2] == "-f" || flag[:2] == "-F"):
			flag, value = flag[:2], flag[2:]
		case ghAPIValueFlags[flag] && i+1 < len(args):
			i++
			value, at = args[i], i
		}
		var file string
		switch flag {
		case "-f", "-F", "--field", "--raw-field":
			key, v, _ := strings.Cut(value, "=")
			if expanded[at] && (key == "query" || strings.ContainsAny(key, "$`")) {
				return unreadableQuery
			}
			// Only -F and --field read the file; checking it for -f too
			// denies nothing a read needs.
			if key != "query" || !strings.HasPrefix(v, "@") {
				continue
			}
			file = v[1:]
		case "--input":
			if expanded[at] {
				return unreadableQuery
			}
			file = value
		default:
			continue
		}
		query, err := readQuery(workspace, file)
		if err != nil {
			return unreadableQuery
		}
		if strings.Contains(string(query), "mutation") {
			return graphQLMutation
		}
	}
	return ""
}

// readQuery reads a regular file of at most a mebibyte that resolves, through
// any symlinks, to a path inside workspace. A relative path is relative to
// workspace.
func readQuery(workspace, path string) ([]byte, error) {
	if workspace == "" || path == "" || path == "-" {
		return nil, errors.New("no file the guard can read")
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if rel, err := filepath.Rel(root, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("outside the workspace")
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("not a small regular file")
	}
	return os.ReadFile(real)
}

// apiMethod reads a `gh api` call's method, whether it was given, and
// whether the call sends fields, which make gh POST unless a method is
// given; each flag separate from its value or attached (-XPOST,
// --method=PUT, -fbody=x).
func apiMethod(args []string) (method string, explicit, fields bool) {
	method = "GET"
	for i, a := range args {
		switch {
		case a == "-X" || a == "--method":
			if i+1 < len(args) {
				method, explicit = strings.ToUpper(args[i+1]), true
			}
		case strings.HasPrefix(a, "--method="):
			method, explicit = strings.ToUpper(strings.TrimPrefix(a, "--method=")), true
		case strings.HasPrefix(a, "-X"):
			method, explicit = strings.ToUpper(strings.TrimPrefix(a[2:], "=")), true
		case a == "-f" || a == "-F" || a == "--field" || a == "--raw-field" || a == "--input",
			strings.HasPrefix(a, "-f") || strings.HasPrefix(a, "-F"),
			strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") || strings.HasPrefix(a, "--input="):
			fields = true
		}
	}
	return method, explicit, fields
}
