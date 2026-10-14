// Package guard is a PreToolUse hook for a review-loop session in Claude Code
// or ECA. It denies the ways an agent could write to GitHub or Linear other
// than through pr-loop, which enforces the loop's policy. Run with
// `--only reviewer` in every Claude Code session, it keeps the reviewer agent
// to reading there too.
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
	"strings"
)

var (
	readOnlyTool = regexp.MustCompile(`(?i)(^|__)(get|list|search|fetch|read|query)[_a-z]*$`)
	ghReadOnly   = map[string]bool{"pr view": true, "pr diff": true, "pr list": true, "pr checks": true, "pr status": true,
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
	// plugin's name if a plugin defines it ("pr-loop:pr-owner"). ECA's
	// sub-agents carry their role's rules as deny patterns instead.
	AgentType string `json:"agent_type"`
	// The directory the command runs in, which bounds the files the guard
	// reads. Without it, the guard's own working directory stands in.
	Cwd string `json:"cwd"`
}

// role is who makes a call. Each role is denied everything the one before it
// is, and more.
type role int

const (
	coordinator role = iota
	owner            // pr-owner: also denied the steps the coordinator alone takes
	reviewer         // reviewer: also denied every change to the repository or GitHub
)

// roleNames are the agents with a role of their own, by agent name.
var roleNames = map[string]role{"pr-owner": owner, "reviewer": reviewer}

func roleOf(agentType string) role {
	for name, r := range roleNames {
		if agentType == name || strings.HasSuffix(agentType, ":"+name) {
			return r
		}
	}
	return coordinator
}

// Main reads one hook event and writes a denial in the caller's format, or
// nothing to let the call through. With args `--only ROLE` it judges only
// calls by that role's agent and lets every other call through, for a hook
// that every session runs.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	only, restricted := role(0), false
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "--only" && roleNames[args[1]] != coordinator:
		only, restricted = roleNames[args[1]], true
	default:
		fmt.Fprintf(stderr, "pr-loop guard: takes no arguments, or --only reviewer or --only pr-owner; got %q\n", args)
		return 1
	}
	var in input
	if err := json.NewDecoder(stdin).Decode(&in); err != nil {
		return 0
	}
	if in.Cwd == "" {
		in.Cwd, _ = os.Getwd()
	}
	who := roleOf(in.AgentType)
	if restricted && who != only {
		return 0
	}
	reason := check(in.ToolName, in.Server, in.ToolInput, who, in.Cwd)
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
// backticks or handing the step to Wesley. A reason that only says no sends
// the agent looking for another way through. TestCheck and TestPolicy hold
// every reason to this.
func check(tool, server string, toolInput json.RawMessage, who role, dir string) string {
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
		return command(args.Command, 0, who, dir)
	case (strings.Contains(strings.ToLower(full), "linear") || strings.Contains(strings.ToLower(full), "github")) &&
		!readOnlyTool.MatchString(full) && who == reviewer:
		return reviewerReport
	case strings.Contains(strings.ToLower(full), "linear") && !readOnlyTool.MatchString(full):
		return "The review loop does not change Linear. Put the proposed change in the final report for Wesley to approve."
	case strings.Contains(strings.ToLower(full), "github") && !readOnlyTool.MatchString(full):
		return "GitHub writes in the review loop go through pr-loop. " + prLoopWrites
	}
	return ""
}

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
	"delete", "token", "api", "commit", "config", "sign-pass")

func command(text string, depth int, who role, dir string) string {
	if depth > maxDepth {
		return tooDeep
	}
	var calls []call
	for _, segment := range segments(text) {
		if words, expands := split(segment); len(words) > 0 {
			calls = append(calls, call{words: words, expands: expands})
		}
	}
	// A file a gh call reads is what the guard sees only when nothing else
	// on the command line can have written it first.
	files := ""
	if depth == 0 && len(calls) == 1 {
		files = dir
	}
	v := visitor{
		script: func(text string) string { return command(text, depth+1, who, dir) },
		call:   func(c call) string { return program(c, who, files) },
	}
	for _, c := range calls {
		if reason := unwrap(c, v); reason != "" {
			return reason
		}
	}
	return ""
}

// program judges a call once unwrap has found the program it runs.
func program(c call, who role, files string) string {
	if reason := checkEnv(c.env); reason != "" {
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
	words := c.words
	switch name := base(words[0]); {
	case name == "git":
		return checkGit(words, who)
	case name == "gh":
		if reason := checkGh(words[1:], c.expands[1:], files); reason != "" && who == reviewer {
			return reviewerReport
		} else {
			return reason
		}
	case name == "pr-loop":
		return checkPrLoop(words[1:], who)
	case name == "git-fresh-review" && who == reviewer:
		return nestedReview
	case name == "git-sign-pass" && who == reviewer:
		return reviewerReport
	case name == "git-sign-pass":
		return signing
	}
	return ""
}

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
		}
	}
	return ""
}

// checkConfigKey checks a key set for one git command, by `-c`,
// `--config-env` or the environment.
func checkConfigKey(key string) string {
	key = strings.ToLower(key)
	switch {
	case strings.HasPrefix(key, "alias."):
		return alias
	case strings.HasPrefix(key, "push-guard."):
		return pushGuard
	}
	return ""
}

const signing = "Signing needs Wesley's hardware key. Leave the commits unsigned and end the report with the command for Wesley to run: `pr-loop sign` for the loop's stack."

const pushGuard = "Publishing private history in a public repository is Wesley's decision. Leave push-guard.* alone, and name the repository and the commits in the report for Wesley."

const prLoopWrites = "Use `pr-loop push`, `reply`, `resolve` or `request`; any other write goes in the report for Wesley."

const alias = "A git alias can stand for any command, which the guard cannot check. Run the command itself, as `git <command>`."

// coordinatorOnly are the pr-loop steps an owner leaves to the coordinator,
// which keeps the stack consistent across PRs.
var coordinatorOnly = map[string]bool{"open": true, "begin": true, "push": true, "reply": true, "resolve": true,
	"request": true, "worktrees": true, "sign": true, "end": true}

// reviewerReads are the pr-loop commands a reviewer may run, besides
// `wait ci`.
var reviewerReads = map[string]bool{"stack": true, "threads": true, "status": true, "ci": true, "logs": true,
	"setup-command": true}

// reviewerReport is why a reviewer may not make a change.
const reviewerReport = "A reviewer only reads. Report the finding with its evidence; whoever started the review acts on it."

// nestedReview is why a reviewer may not start a review.
const nestedReview = "A reviewer does not start a review of its own. Report your findings; whoever started you decides on another round."

// checkPrLoop keeps the decisions that are Wesley's: waiving a CI failure,
// and discarding the unsquashed history.
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
			return "Whether a CI failure is not this PR's doing is Wesley's call. Put the evidence in the report, with `pr-loop request --accept-failure CHECK PR` for Wesley to run if he agrees."
		case args[0] == "end" && a == "--prune":
			return "Pruning deletes the unsquashed history and round snapshots, which is Wesley's call. Leave `pr-loop end --prune` to him."
		}
	}
	return ""
}

// gitWrites are the git commands that change a worktree, the index, refs,
// stashes or a remote, which a reviewer may not run.
var gitWrites = map[string]bool{"commit": true, "rebase": true, "reset": true, "checkout": true, "switch": true,
	"restore": true, "merge": true, "cherry-pick": true, "revert": true, "am": true, "apply": true, "stash": true,
	"add": true, "rm": true, "mv": true, "clean": true, "tag": true, "update-ref": true, "notes": true, "worktree": true,
	"push": true, "sign-pass": true}

func checkGit(words []string, who role) string {
	i := 1
	for i < len(words) && strings.HasPrefix(words[i], "-") {
		// Configuration given on the command line, such as an alias, which
		// can stand for any command.
		key := ""
		switch w := words[i]; {
		case (w == "-c" || w == "--config-env") && i+1 < len(words):
			key, _, _ = strings.Cut(words[i+1], "=")
		case strings.HasPrefix(w, "--config-env="):
			key, _, _ = strings.Cut(strings.TrimPrefix(w, "--config-env="), "=")
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
			return "Repository hooks stay on in the review loop. Fix what they report and commit again with `git commit --fixup=<commit>`."
		case has("--amend"):
			return "The review loop never rewrites the PR's commits. Commit the change with `git commit --fixup=<commit>`; the signing pass squashes it in."
		case !has("--fixup"):
			return "Every commit in the review loop is a fixup the signing pass can squash in. Commit with `git commit --fixup=<commit>` against the PR commit it belongs to, with the review details as --trailer lines."
		}
	case "config":
		reading := has("--get", "--get-all", "--get-regexp", "-l", "--list")
		for _, w := range rest {
			key := strings.ToLower(w)
			switch {
			case reading:
			case strings.HasPrefix(key, "pr-loop."):
				return "pr-loop.* settings are Wesley's to change. Read them with `git config --get`, and put the `git config` command you propose in the report for Wesley."
			case strings.HasPrefix(key, "push-guard."):
				return pushGuard
			case strings.HasPrefix(key, "alias."):
				return alias
			}
		}
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

// checkGh checks gh's arguments, with which of them the shell expands. It
// reads files a GraphQL call names only inside workspace, and none when
// workspace is "".
func checkGh(args []string, expanded []bool, workspace string) string {
	if len(args) >= 2 && ghReadOnly[args[0]+" "+args[1]] {
		return ""
	}
	if len(args) >= 2 && args[0] == "auth" && args[1] == "token" {
		return "The review loop does not read GitHub credentials, which pr-loop holds. Read GitHub with `pr-loop status`, `ci`, `logs` or `threads`."
	}
	if len(args) >= 1 && args[0] == "api" {
		rest := args[1:]
		endpoint := ""
		for i := 0; i < len(rest); i++ {
			if ghAPIValueFlags[rest[i]] {
				i++
			} else if !strings.HasPrefix(rest[i], "-") {
				endpoint = rest[i]
				break
			}
		}
		if endpoint == "graphql" {
			return checkGraphQL(rest, expanded[1:], workspace)
		}
		method, explicit, fields := apiMethod(rest)
		if method != "GET" || (fields && !explicit) {
			return "Only read-only `gh api` calls are allowed in the review loop. " + prLoopWrites
		}
		return ""
	}
	what := strings.Join(args[:min(2, len(args))], " ")
	return "`gh " + what + "` can change GitHub. In the review loop use pr-loop (status, ci, logs, threads, reply, resolve, request). " +
		"Top-level PR comments, reviews, merges, PR edits and marking a PR ready are left to Wesley."
}

const graphQLMutation = "GraphQL mutations in the review loop go through pr-loop. " + prLoopWrites

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
