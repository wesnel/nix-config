package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/cli"
	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/guard"
	"github.com/wesnel/agent-tools/internal/review"
)

// The mcp subcommand serves the GitHub and review operations a Codex
// session cannot run in its sandbox, which denies the gh login and the
// reviewers' credentials to its commands, as tools of a stdio MCP server
// Codex starts as a process of its own, outside that sandbox. Each tool
// runs one pr-loop or git delegate command, its argv built from typed
// arguments, in this process: no tool takes a shell line, gh arguments or
// argv. The commands read the gh login themselves, as they do on the
// command line, and print what they print there. Codex asks the operator
// before each call (the codex module sets the server's approval mode).

// mcpParam is one argument of a tool.
type mcpParam struct {
	name     string
	kind     paramKind
	required bool
	enum     []string
	desc     string
}

type paramKind int

const (
	text   paramKind = iota // a string
	number                  // an integer
	toggle                  // a boolean
	texts                   // an array of strings
	file                    // a path to a file in the worktree, absolute or relative to it
)

// mcpTool is one tool: its argv from its checked arguments, and what runs
// it. A tool without noWorktree also takes worktree, where it runs.
type mcpTool struct {
	name, desc string
	params     []mcpParam
	noWorktree bool
	argv       func(a mcpArgs) []string
	run        func(ctx context.Context, argv []string, stdout, stderr io.Writer) int
}

// mcpArgs are a call's arguments, checked against its tool's params, with
// each file resolved to its absolute path.
type mcpArgs map[string]any

func (a mcpArgs) str(name string) string { s, _ := a[name].(string); return s }
func (a mcpArgs) strs(name string) []string {
	s, _ := a[name].([]string)
	return s
}
func (a mcpArgs) on(name string) bool { b, _ := a[name].(bool); return b }

// opt appends flag and the argument's value when the call gives it.
func (a mcpArgs) opt(argv []string, flag, name string) []string {
	switch v := a[name].(type) {
	case string:
		return append(argv, flag, v)
	case int:
		return append(argv, flag, strconv.Itoa(v))
	case bool:
		if v {
			return append(argv, flag)
		}
	case []string:
		for _, s := range v {
			argv = append(argv, flag, s)
		}
	}
	return argv
}

func prLoopTool(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	return run(argv[0], argv[1:], strings.NewReader(""), stdout, stderr)
}

func delegateTool(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	return review.Main(ctx, argv, strings.NewReader(""), stdout, stderr)
}

// The arguments several tools share.
var (
	prsParam   = mcpParam{name: "prs", kind: texts, desc: "PRs by number or branch; default: the worktree's branch's"}
	dryRun     = mcpParam{name: "dry_run", kind: toggle, desc: "check and print what would be done, doing nothing"}
	sourceParm = mcpParam{name: "source", kind: text, required: true,
		desc: "BASE..TIP, a range of the worktree's repository; or a pull request, OWNER/NAME#N or N for origin's"}
)

// mcpTools are the server's tools, each named for the command it runs:
// pr-loop X is pr_loop_X and git delegate X is git_delegate_X, dashes as
// underscores, and each flag is the argument of its name.
var mcpTools = []mcpTool{
	{name: "pr_loop_open", desc: "pr-loop open: push a branch GitHub does not have through the API and open it as a draft PR.",
		params: []mcpParam{{name: "title", kind: text, required: true, desc: "the PR's title"},
			{name: "body_file", kind: file, required: true, desc: "the description, a Markdown file in the worktree"},
			{name: "base", kind: text, desc: "the base branch; default: the default branch"},
			{name: "branch", kind: text, desc: "the branch; default: the worktree's"}, dryRun},
		argv: func(a mcpArgs) []string {
			argv := a.opt([]string{"open", "--title", a.str("title"), "--body-file", a.str("body_file")}, "--base", "base")
			argv = a.opt(argv, "--dry-run", "dry_run")
			if b := a.str("branch"); b != "" {
				argv = append(argv, b)
			}
			return argv
		}, run: prLoopTool},
	{name: "pr_loop_stack", desc: "pr-loop stack: the stack of open PRs containing a PR, and GitHub's stack of it.",
		params: []mcpParam{{name: "target", kind: text, desc: "a PR by number or branch; default: the worktree's branch's"}},
		argv: func(a mcpArgs) []string {
			if t := a.str("target"); t != "" {
				return []string{"stack", t}
			}
			return []string{"stack"}
		}, run: prLoopTool},
	{name: "pr_loop_link", desc: "pr-loop link: put PRs, bottom first, each based on the one before, into one GitHub stack.",
		params: []mcpParam{{name: "prs", kind: texts, required: true, desc: "PRs by number or branch, bottom first"}},
		argv:   func(a mcpArgs) []string { return append([]string{"link"}, a.strs("prs")...) }, run: prLoopTool},
	{name: "pr_loop_push", desc: "pr-loop push: push branches through the GitHub API, bottom up, in a bots-mode review loop.",
		params: []mcpParam{{name: "branches", kind: texts, desc: "branches, bottom first; default: the worktree's"},
			{name: "lease", kind: texts, desc: "BRANCH=SHA, the head GitHub must have for a rebased branch"}, dryRun},
		argv: func(a mcpArgs) []string {
			argv := a.opt(a.opt([]string{"push"}, "--lease", "lease"), "--dry-run", "dry_run")
			return append(argv, a.strs("branches")...)
		}, run: prLoopTool},
	{name: "pr_loop_threads", desc: "pr-loop threads: a PR's unresolved review threads, every comment read; writes its review record.",
		params: []mcpParam{{name: "pr", kind: number, required: true, desc: "the PR's number"},
			{name: "all", kind: toggle, desc: "resolved threads too"}},
		argv: func(a mcpArgs) []string {
			return append(a.opt([]string{"threads"}, "--all", "all"), strconv.Itoa(a["pr"].(int)))
		}, run: prLoopTool},
	{name: "pr_loop_reply", desc: "pr-loop reply --from: post the replies in a reply file, resolving the threads it marks.",
		params: []mcpParam{{name: "replies_file", kind: file, required: true,
			desc: "the reply file in the worktree: per thread a line `=== thread THREAD` or `=== thread THREAD resolve`, then the reply"}},
		argv: func(a mcpArgs) []string { return []string{"reply", "--from", a.str("replies_file")} }, run: prLoopTool},
	{name: "pr_loop_draft", desc: "pr-loop draft: write the answer to a thread into the PR's review record, for the operator to post.",
		params: []mcpParam{{name: "thread", kind: text, required: true, desc: "the thread's ID"},
			{name: "body_file", kind: file, required: true, desc: "the answer, a Markdown file in the worktree"},
			{name: "outcome", kind: text, required: true, desc: "the review-change skill's Triage outcome"}},
		argv: func(a mcpArgs) []string {
			return []string{"draft", a.str("thread"), "--body-file", a.str("body_file"), "--outcome", a.str("outcome")}
		}, run: prLoopTool},
	{name: "pr_loop_request", desc: "pr-loop request: ask each configured bot that has not reviewed the head to review, once CI allows.",
		params: []mcpParam{{name: "bot", kind: texts, desc: "only these bots"}, prsParam},
		argv: func(a mcpArgs) []string {
			return append(a.opt([]string{"request"}, "--bot", "bot"), a.strs("prs")...)
		}, run: prLoopTool},
	{name: "pr_loop_status", desc: "pr-loop status: CI, each bot's review and the open threads of PRs.",
		params: []mcpParam{prsParam},
		argv:   func(a mcpArgs) []string { return append([]string{"status"}, a.strs("prs")...) }, run: prLoopTool},
	{name: "pr_loop_ci", desc: "pr-loop ci: the checks on each PR's head, compared with its base.",
		params: []mcpParam{prsParam},
		argv:   func(a mcpArgs) []string { return append([]string{"ci"}, a.strs("prs")...) }, run: prLoopTool},
	{name: "pr_loop_logs", desc: "pr-loop logs: the end of a failed GitHub Actions job's log.",
		params: []mcpParam{{name: "pr", kind: text, required: true, desc: "the PR by number or branch"},
			{name: "check", kind: text, required: true, desc: "the check's name"},
			{name: "lines", kind: number, desc: "lines from the end; default 200"}},
		argv: func(a mcpArgs) []string {
			return append(a.opt([]string{"logs"}, "--lines", "lines"), a.str("pr"), a.str("check"))
		}, run: prLoopTool},
	{name: "pr_loop_wait", desc: "pr-loop wait: block until CI on each head finishes (exit 3 on timeout, 4 when a head has no checks), " +
		"or until requested reviews land (exit 3 on timeout).",
		params: []mcpParam{{name: "for", kind: text, required: true, enum: []string{"ci", "reviews"}, desc: "what to wait for"},
			{name: "sha", kind: text, desc: "ci only: the commit the PR's head must become"},
			{name: "grace", kind: number, desc: "ci only: seconds a head may have no checks; default 180"},
			{name: "timeout", kind: number, desc: "seconds; default 240"}, prsParam},
		argv: func(a mcpArgs) []string {
			argv := a.opt(a.opt(a.opt([]string{"wait", a.str("for")}, "--sha", "sha"), "--grace", "grace"), "--timeout", "timeout")
			return append(argv, a.strs("prs")...)
		}, run: prLoopTool},
	{name: "pr_loop_sign_check", desc: "pr-loop sign-check: squash the fixups of the worktree's stack unsigned in a throwaway " +
		"worktree and check every branch keeps its own commits and files.",
		argv: func(mcpArgs) []string { return []string{"sign-check"} }, run: prLoopTool},
	{name: "pr_loop_end", desc: "pr-loop end: delete your review-request trigger comments.",
		params: []mcpParam{prsParam},
		argv:   func(a mcpArgs) []string { return append([]string{"end"}, a.strs("prs")...) }, run: prLoopTool},

	{name: "git_delegate", desc: "git delegate: review a range or a pull request with reviewers that did not write it, and keep " +
		"their findings in its review record. Exit 3: open findings; 4: a reviewer could not run here.",
		params: []mcpParam{sourceParm,
			{name: "reviewer", kind: texts, desc: "reviewer types to run (git delegate -h); default: the general review and the routed ones"},
			{name: "plan", kind: text, desc: "the plan, its file or name, whose Goal is the goal statement; required for a range"},
			{name: "part", kind: text, desc: "the plan's part whose Goal to read, as P1"},
			{name: "items", kind: file, desc: "the items the change answers, a file in the worktree"},
			{name: "red_check", kind: file, desc: "git red-check BASE..TIP's JSON, a file in the worktree"},
			{name: "description", kind: file, desc: "the PR description, a file in the worktree"},
			{name: "timeout", kind: number, desc: "seconds before a reviewer is stopped; default 1800"}},
		argv: func(a mcpArgs) []string {
			argv := a.opt(nil, "--reviewer", "reviewer")
			for _, f := range []string{"plan", "part", "items", "red_check", "description", "timeout"} {
				argv = a.opt(argv, "--"+strings.ReplaceAll(f, "_", "-"), f)
			}
			return append(argv, a.str("source"))
		}, run: delegateTool},
	{name: "git_delegate_ask", desc: "git delegate ask: a second opinion on a design question from another model family, " +
		"judged from the named files of the worktree's repository alone.",
		params: []mcpParam{{name: "question", kind: file, required: true, desc: "the question, a file in the worktree"},
			{name: "paths", kind: texts, required: true, desc: "the repository's files the answer rests on"},
			{name: "reviewer", kind: text, desc: "the reviewer type; default: the other model family"}},
		argv: func(a mcpArgs) []string {
			argv := a.opt([]string{"ask"}, "--reviewer", "reviewer")
			return append(append(argv, "--question", a.str("question")), a.strs("paths")...)
		}, run: delegateTool},
	{name: "git_delegate_settle", desc: "git delegate settle: record how a finding of a review was settled, in its record.",
		params: []mcpParam{sourceParm, {name: "finding", kind: text, required: true, desc: "the finding's record ID, as F3"},
			{name: "outcome", kind: text, required: true, desc: "the review-change skill's Triage outcome, or open"},
			{name: "target", kind: text, desc: "for duplicate-of and belongs-in: the finding or branch"},
			{name: "reason_file", kind: file, desc: "the reason, a file in the worktree"}},
		argv: func(a mcpArgs) []string {
			argv := []string{"settle", a.str("source"), a.str("finding"), a.str("outcome")}
			return a.opt(a.opt(argv, "--target", "target"), "--reason-file", "reason_file")
		}, run: delegateTool},
	{name: "git_delegate_show", desc: "git delegate show: print a range's or a pull request's review record.",
		params: []mcpParam{sourceParm},
		argv:   func(a mcpArgs) []string { return []string{"show", a.str("source")} }, run: delegateTool},

	{name: "github_profile", desc: "The name and pronouns a GitHub user's profile lists, null where it lists none.",
		params:     []mcpParam{{name: "login", kind: text, required: true, desc: "the user's login"}},
		noWorktree: true, argv: func(a mcpArgs) []string { return []string{a.str("login")} }, run: githubProfile},
}

// githubProfile prints the profile of the login argv names.
func githubProfile(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	var data struct {
		User *struct {
			Name     *string `json:"name"`
			Pronouns *string `json:"pronouns"`
		} `json:"user"`
	}
	err := gh.GraphQL(`query($login: String!) { user(login: $login) { name pronouns } }`, map[string]any{"login": argv[0]}, &data)
	if err == nil && data.User == nil {
		err = fmt.Errorf("GitHub has no user %s", argv[0])
	}
	var result any
	if err == nil {
		result = map[string]any{"login": argv[0], "name": data.User.Name, "pronouns": data.User.Pronouns}
	}
	return cli.Report("pr-loop mcp", result, err, 0, stdout, stderr)
}

// schema is t's input schema, which takes nothing it does not name.
func (t mcpTool) schema() map[string]any {
	properties := map[string]any{}
	required := []string{}
	params := t.params
	if !t.noWorktree {
		params = append([]mcpParam{{name: "worktree", kind: text, required: true,
			desc: "the absolute path of the git worktree to run in"}}, params...)
	}
	for _, p := range params {
		var s map[string]any
		switch p.kind {
		case text, file:
			s = map[string]any{"type": "string"}
		case number:
			s = map[string]any{"type": "integer"}
		case toggle:
			s = map[string]any{"type": "boolean"}
		case texts:
			s = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
		}
		if p.enum != nil {
			s["enum"] = p.enum
		}
		s["description"] = p.desc
		properties[p.name] = s
		if p.required {
			required = append(required, p.name)
		}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

// check returns the call's arguments as its argv takes them, the worktree
// resolved, or why the tool cannot take them. A string may not start with
// -, which a command would read as a flag; a file must lie in the worktree
// once its links are followed.
func (t mcpTool) check(raw map[string]any) (mcpArgs, string, error) {
	params := map[string]mcpParam{}
	for _, p := range t.params {
		params[p.name] = p
	}
	worktree := ""
	if !t.noWorktree {
		w, ok := raw["worktree"].(string)
		if !ok || !filepath.IsAbs(w) {
			return nil, "", fmt.Errorf("worktree must be the absolute path of a git worktree")
		}
		real, err := filepath.EvalSymlinks(w)
		if info, statErr := os.Stat(real); err != nil || statErr != nil || !info.IsDir() {
			return nil, "", fmt.Errorf("worktree %s is not a directory", w)
		}
		worktree = real
	}
	args := mcpArgs{}
	for name, v := range raw {
		if name == "worktree" && !t.noWorktree {
			continue
		}
		p, ok := params[name]
		if !ok {
			return nil, "", fmt.Errorf("%s takes no argument %s; it takes %s", t.name, name, strings.Join(t.names(), ", "))
		}
		value, err := p.value(v, worktree)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", name, err)
		}
		args[name] = value
	}
	for _, p := range t.params {
		if _, ok := args[p.name]; p.required && !ok {
			return nil, "", fmt.Errorf("%s needs %s", t.name, p.name)
		}
	}
	return args, worktree, nil
}

// names are the arguments t takes.
func (t mcpTool) names() []string {
	var names []string
	if !t.noWorktree {
		names = append(names, "worktree")
	}
	for _, p := range t.params {
		names = append(names, p.name)
	}
	return names
}

// value is v as p takes it.
func (p mcpParam) value(v any, worktree string) (any, error) {
	plain := func(v any) (string, error) {
		s, ok := v.(string)
		switch {
		case !ok:
			return "", fmt.Errorf("takes a string, not %v", v)
		case strings.HasPrefix(s, "-"):
			return "", fmt.Errorf("%q starts with -, which the command would read as a flag", s)
		case p.enum != nil && !slices.Contains(p.enum, s):
			return "", fmt.Errorf("takes one of %s, not %q", strings.Join(p.enum, ", "), s)
		}
		return s, nil
	}
	switch p.kind {
	case number:
		n, ok := v.(float64)
		if !ok || n != math.Trunc(n) || math.Abs(n) > 1<<31 {
			return nil, fmt.Errorf("takes an integer, not %v", v)
		}
		return int(n), nil
	case toggle:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("takes true or false, not %v", v)
		}
		return b, nil
	case texts:
		list, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("takes an array of strings, not %v", v)
		}
		out := []string{}
		for _, item := range list {
			s, err := plain(item)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	case file:
		s, err := plain(v)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(s) {
			s = filepath.Join(worktree, s)
		}
		real, err := filepath.EvalSymlinks(s)
		if err != nil {
			return nil, fmt.Errorf("no file %s in the worktree", s)
		}
		if rel, err := filepath.Rel(worktree, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("%s is outside the worktree %s; write the file there", s, worktree)
		}
		return real, nil
	}
	return plain(v)
}

// rpcMessage is a JSON-RPC message from the client: a request, or a
// notification, which has no ID.
type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// mcpProtocols are the MCP versions this server speaks, newest first.
var mcpProtocols = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// mcpMain serves MCP on stdin and stdout until stdin ends. The commands
// the tools run write to os.Stdout and read os.Stdin in places, as on a
// terminal, so those point at stderr and nothing while it serves.
func mcpMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "pr-loop mcp takes no arguments")
		return 2
	}
	null, err := os.Open(os.DevNull)
	if err != nil {
		fmt.Fprintln(stderr, "pr-loop mcp:", err)
		return 1
	}
	defer null.Close()
	os.Stdin, os.Stdout = null, os.Stderr
	if err := serveMCP(context.Background(), stdin, stdout); err != nil {
		fmt.Fprintln(stderr, "pr-loop mcp:", err)
		return 1
	}
	return 0
}

// serveMCP answers each request on in, one at a time, on out.
func serveMCP(ctx context.Context, in io.Reader, out io.Writer) error {
	lines := bufio.NewScanner(in)
	lines.Buffer(make([]byte, 64*1024), 16<<20)
	encoder := json.NewEncoder(out)
	for lines.Scan() {
		if len(bytes.TrimSpace(lines.Bytes())) == 0 {
			continue
		}
		var m rpcMessage
		if err := json.Unmarshal(lines.Bytes(), &m); err != nil {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": nil,
				"error": map[string]any{"code": -32700, "message": "parse error: " + err.Error()}}); err != nil {
				return err
			}
			continue
		}
		if len(m.ID) == 0 {
			continue // a notification, which needs no answer
		}
		result, rpcErr := answer(ctx, m)
		reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
		if rpcErr != nil {
			reply["error"] = rpcErr
		} else {
			reply["result"] = result
		}
		if err := encoder.Encode(reply); err != nil {
			return err
		}
	}
	return lines.Err()
}

// answer is the result of request m, or its JSON-RPC error.
func answer(ctx context.Context, m rpcMessage) (any, map[string]any) {
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(m.Params, &p)
		version := mcpProtocols[0]
		if slices.Contains(mcpProtocols, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": guard.OpsServer, "version": "1"}}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := []any{}
		for _, t := range mcpTools {
			tools = append(tools, map[string]any{"name": t.name, "description": t.desc, "inputSchema": t.schema()})
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
			Meta      struct {
				ThreadID string `json:"threadId"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, map[string]any{"code": -32602, "message": err.Error()}
		}
		return callTool(ctx, p.Name, p.Arguments, p.Meta.ThreadID), nil
	}
	return nil, map[string]any{"code": -32601, "message": "no method " + m.Method}
}

// callTool runs tool name with args, for the Codex thread threadID, in its
// worktree. Its result holds what the command printed on stdout, then its
// exit status and stderr; it is an error where the command failed or
// refused, not where its status reports a finding, a timeout or checks
// that never ran (3 and 4).
func callTool(ctx context.Context, name string, raw map[string]any, threadID string) map[string]any {
	refused := func(err error) map[string]any {
		return map[string]any{"isError": true, "content": []any{map[string]any{"type": "text",
			"text": "pr-loop mcp: refused: " + err.Error()}}}
	}
	i := slices.IndexFunc(mcpTools, func(t mcpTool) bool { return t.name == name })
	if i < 0 {
		return refused(fmt.Errorf("no tool %s", name))
	}
	t := mcpTools[i]
	args, worktree, err := t.check(raw)
	if err != nil {
		return refused(err)
	}
	if worktree != "" {
		if err := os.Chdir(worktree); err != nil {
			return refused(err)
		}
	}
	// What the call runs takes it for the Codex session's own, as the
	// commands Codex runs do.
	if threadID != "" {
		os.Setenv("CODEX_THREAD_ID", threadID)
	} else {
		os.Unsetenv("CODEX_THREAD_ID")
	}
	var stdout, stderr bytes.Buffer
	code := t.run(ctx, t.argv(args), &stdout, &stderr)
	return map[string]any{"isError": code != 0 && code != 3 && code != 4, "content": []any{
		map[string]any{"type": "text", "text": stdout.String()},
		map[string]any{"type": "text", "text": fmt.Sprintf("exit %d\n%s", code, stderr.String())},
	}}
}
