package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitgrant"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/review"
)

// serveMCPEnv makes TestMain run this test binary as `pr-loop mcp` against
// the fake GitHub at its URL, so a test talks to the server as Codex does:
// a process of its own, over its standard input and output.
const serveMCPEnv = "PR_LOOP_TEST_SERVE_MCP"

// fakeToken is the login the fake gh prints for `gh auth token`; no tool
// result may hold it.
const fakeToken = "gho_fakeloginfakeloginfakeloginfake0001"

// fakeGitHub stands in for api.github.com, which a test must not reach. It
// answers the GraphQL that reading a pull request's review threads and a
// user's profile make, for the viewer "me" in o/r, and refuses a request
// without the fake login's token.
type fakeGitHub struct {
	url   string
	calls atomic.Int32
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+fakeToken {
			http.Error(w, `{"message": "Bad credentials"}`, http.StatusUnauthorized)
			return
		}
		var req struct{ Query string }
		json.NewDecoder(r.Body).Decode(&req)
		var data string
		switch {
		case strings.Contains(req.Query, "viewer { login }"):
			data = `{"viewer": {"login": "me"}, "repository": {"id": "R", "nameWithOwner": "o/r", "defaultBranchRef": {"name": "main"}}}`
		case strings.Contains(req.Query, "reviewThreads(first: 50"):
			data = `{"repository": {"pullRequest": {"reviewThreads": {"pageInfo": {"hasNextPage": false}, "nodes": [
				{"id": "T1", "isResolved": false, "isOutdated": false, "path": "a.go", "line": 3, "comments": {
					"totalCount": 1, "pageInfo": {"hasNextPage": false, "endCursor": "1"}, "nodes": [
						{"author": {"login": "copilot-pull-request-reviewer", "__typename": "Bot"}, "body": "Rename this.",
						 "url": "https://github.com/o/r/pull/1#discussion_r1", "createdAt": "2026-10-20T10:00:00Z"}]}}]}}}}`
		case strings.Contains(req.Query, "user(login:"):
			data = `{"user": {"name": "Ada Lovelace", "pronouns": "she/her"}}`
		default:
			http.Error(w, "unexpected query: "+req.Query, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"data": `+data+`}`)
	}))
	t.Cleanup(server.Close)
	f.url = server.URL
	return f
}

// fakeGh stands in for gh, whose login the server reads: it prints the fake
// login for `gh auth token` and fails anything else.
const fakeGh = `#!/bin/sh
if [ "$1 $2" = "auth token" ]; then echo "` + fakeToken + `"; exit 0; fi
echo "fake gh: unexpected $*" >&2; exit 1
`

// fakeClaude stands in for claude, a reviewer that costs money and reaches
// outside the machine: as the agent --agent names, it prints
// $FAKE_CLAUDE_LOG/AGENT-out and writes its environment to AGENT-env there.
const fakeClaude = `#!/bin/sh
agent= prev=
for a in "$@"; do
  if [ "$prev" = --agent ]; then agent=$a; fi
  prev=$a
done
env > "$FAKE_CLAUDE_LOG/$agent-env"
cat "$FAKE_CLAUDE_LOG/$agent-out"
`

// mcpFixture is a checkout of o/r with two commits, and the environment a
// server started for it gets.
type mcpFixture struct {
	// plan is a plan in the fixture's plans directory, whose goal git
	// delegate reads.
	repo, base, log, plan string
	github                *fakeGitHub
	env                   []string
}

// newMCPFixture makes the checkout, a PATH whose gh and claude are the
// fakes, review bots, review records and agent-tools' config of its own,
// and an environment of only what Codex hands a stdio server (HOME, PATH,
// TMPDIR and the like) and those.
func newMCPFixture(t *testing.T) *mcpFixture {
	t.Helper()
	f := &mcpFixture{github: newFakeGitHub(t), log: t.TempDir()}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.repo = repo
	bin, home, tmp := t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	os.WriteFile(filepath.Join(bin, "claude"), []byte(fakeClaude), 0o755)
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"bots": {"copilot": {"login": "^copilot", "request": {"reviewer": "@copilot"}}}}`), 0o644)
	planConfig := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	created, err := planConfig.New(plans.NewOptions{Title: "Grow the code", Slug: "grow",
		Goal: "Grow the code to three lines."}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.plan = created.Plan
	agentTools := filepath.Join(t.TempDir(), "agent-tools.json")
	data, _ := json.Marshal(map[string]any{"plans": planConfig, "delegate": map[string]any{
		"agents": "/store/review-agents.json", "settings": "/store/reviewer-settings.json"}})
	os.WriteFile(agentTools, data, 0o644)
	// The fake claude runs env and cat, which a Linux build sandbox has
	// only where coreutils is, not in /usr/bin or /bin.
	path := []string{bin}
	for _, tool := range []string{"git", "env", "cat"} {
		found, err := exec.LookPath(tool)
		if err != nil {
			t.Fatal(err)
		}
		path = append(path, filepath.Dir(found))
	}
	f.env = []string{
		"HOME=" + home, "TMPDIR=" + tmp, "LANG=C",
		"PATH=" + strings.Join(append(path, "/usr/bin", "/bin"), string(os.PathListSeparator)),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@e", "GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@e",
		"XDG_STATE_HOME=" + t.TempDir(), "XDG_CACHE_HOME=" + t.TempDir(),
		"PR_LOOP_CONFIG=" + config, "AGENT_TOOLS_CONFIG=" + agentTools, "FAKE_CLAUDE_LOG=" + f.log,
		serveMCPEnv + "=" + f.github.url,
	}
	inRepo := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = f.repo, f.env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	inRepo("init", "-q", "-b", "main")
	inRepo("remote", "add", "origin", "https://github.com/o/r.git")
	os.WriteFile(filepath.Join(f.repo, "code.txt"), []byte("a\n"), 0o644)
	inRepo("add", "code.txt")
	inRepo("commit", "-q", "-m", "Start")
	f.base = inRepo("rev-parse", "HEAD")
	os.WriteFile(filepath.Join(f.repo, "code.txt"), []byte("a\nb\nc\n"), 0o644)
	inRepo("commit", "-q", "-am", "Grow the code")
	return f
}

// mcpClient talks to one server process as Codex does: one JSON-RPC
// message a line each way.
type mcpClient struct {
	in   io.WriteCloser
	out  *bufio.Reader
	next int
}

// start runs the server for f and initializes it.
func (f *mcpFixture) start(t *testing.T) *mcpClient {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env, cmd.Dir, cmd.Stderr = f.env, t.TempDir(), os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		cmd.Wait()
	})
	return &mcpClient{in: in, out: bufio.NewReader(out)}
}

type rpcReply struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call sends a request and returns its reply.
func (c *mcpClient) call(t *testing.T, method string, params any) rpcReply {
	t.Helper()
	c.next++
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.in.Write(append(line, '\n')); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	reply, err := c.out.ReadBytes('\n')
	if err != nil {
		t.Fatalf("%s: no reply: %v", method, err)
	}
	var r rpcReply
	if err := json.Unmarshal(reply, &r); err != nil || r.ID != c.next {
		t.Fatalf("%s: reply %q: %v", method, reply, err)
	}
	return r
}

// toolResult is a tools/call result: what the command printed on stdout,
// the first content, and every content joined.
type toolResult struct {
	out, text string
	isError   bool
}

// tool calls name with args, as Codex does, giving its thread in _meta.
func (c *mcpClient) tool(t *testing.T, name string, args map[string]any) toolResult {
	t.Helper()
	r := c.call(t, "tools/call", map[string]any{"name": name, "arguments": args, "_meta": map[string]any{"threadId": "thread-1"}})
	if r.Error != nil {
		t.Fatalf("%s: error %+v", name, r.Error)
	}
	var result struct {
		Content []struct{ Type, Text string }
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(r.Result, &result); err != nil {
		t.Fatalf("%s: result %s: %v", name, r.Result, err)
	}
	var texts []string
	for _, c := range result.Content {
		if c.Type != "text" {
			t.Fatalf("%s: content of type %q", name, c.Type)
		}
		texts = append(texts, c.Text)
	}
	out := toolResult{text: strings.Join(texts, "\n"), isError: result.IsError}
	if len(texts) > 0 {
		out.out = texts[0]
	}
	if strings.Contains(out.text, fakeToken) {
		t.Fatalf("%s: the result holds the login's token: %s", name, out.text)
	}
	return out
}

// stageFixture is an MCP fixture whose server can stage: grants in a state
// home of its own, and this binary as its codex. Codex's sandbox cannot
// nest in the one the tests may run in, so this binary stands in for it
// (TestMain), running the child unconfined; TestGitStageNative covers the
// real one.
func stageFixture(t *testing.T) *mcpFixture {
	t.Helper()
	state := realTempDir(t)
	hostpaths.Bake(t, "", state)
	f := newMCPFixture(t)
	codex := filepath.Join(realTempDir(t), "codex")
	if err := os.Symlink(os.Args[0], codex); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, hostpaths.TestStateHomeEnv+"="+state, "AGENT_GIT_TEST_SANDBOX=1", gitStageCodexEnv+"="+codex)
	return f
}

// stageTaskWorktree is a linked worktree of f's repository on branch task,
// with its symlinks resolved, and a plan of session thread-1 naming it.
func stageTaskWorktree(t *testing.T, f *mcpFixture) (string, plans.Plan) {
	t.Helper()
	worktree := filepath.Join(realTempDir(t), "task")
	cmd := exec.Command("git", "-C", f.repo, "worktree", "add", "-q", "-b", "task", worktree)
	cmd.Env = f.env
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree: %s: %v", data, err)
	}
	config := plans.Config{Dir: filepath.Dir(f.plan), Host: "box"}
	made, err := config.New(plans.NewOptions{Title: "Task", Slug: "task", Worktrees: []string{worktree}, Sessions: []string{"codex:thread-1"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := config.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return worktree, plan
}

// The server Codex runs outside its sandbox: each operation a typed tool,
// with the operator's GitHub login read by the server and never returned.
func TestMCP(t *testing.T) {
	t.Run("git stage restores content the repository holds only in a pack", func(t *testing.T) {
		f := stageFixture(t)
		worktree, _ := stageTaskWorktree(t, f)
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
			cmd.Env = f.env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s: %v", args, out, err)
			}
		}
		for _, content := range []string{"packed\n", "a\nb\nc\n"} {
			if err := os.WriteFile(filepath.Join(worktree, "code.txt"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			git("-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-am", content)
		}
		git("gc", "-q", "--prune=now")
		if err := os.WriteFile(filepath.Join(worktree, "code.txt"), []byte("packed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"worktree": worktree, "paths": []string{"code.txt"}}
		data, _ := json.Marshal(args)
		token, err := gitgrant.Issue(gitgrant.Record{Tool: "mcp__agent_ops__git_stage", Session: "thread-1", Call: "call", Arguments: data})
		if err != nil {
			t.Fatal(err)
		}
		args["authorization"] = token
		if result := f.start(t).tool(t, "git_stage", args); result.isError {
			t.Fatalf("stage of packed content: %+v", result)
		}
		cmd := exec.Command("git", "-C", worktree, "show", ":code.txt")
		cmd.Env = f.env
		if data, err := cmd.Output(); err != nil || string(data) != "packed\n" {
			t.Fatalf("index content %q: %v", data, err)
		}
	})
	t.Run("git stage accepts an unchanged file alongside a changed one", func(t *testing.T) {
		f := stageFixture(t)
		worktree, _ := stageTaskWorktree(t, f)
		if err := os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("new\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// code.txt is as checked out and older than the index, so git add
		// finds it stat-clean, not racily clean, and hashes nothing.
		old := time.Now().Add(-time.Hour)
		if err := os.Chtimes(filepath.Join(worktree, "code.txt"), old, old); err != nil {
			t.Fatal(err)
		}
		refresh := exec.Command("git", "-C", worktree, "update-index", "--refresh")
		refresh.Env = f.env
		if out, err := refresh.CombinedOutput(); err != nil {
			t.Fatalf("refresh: %s: %v", out, err)
		}
		args := map[string]any{"worktree": worktree, "paths": []string{"code.txt", "new.txt"}}
		data, _ := json.Marshal(args)
		token, err := gitgrant.Issue(gitgrant.Record{Tool: "mcp__agent_ops__git_stage", Session: "thread-1", Call: "call", Arguments: data})
		if err != nil {
			t.Fatal(err)
		}
		args["authorization"] = token
		if result := f.start(t).tool(t, "git_stage", args); result.isError {
			t.Fatalf("stage with an unchanged file: %+v", result)
		}
		cmd := exec.Command("git", "-C", worktree, "diff", "--cached", "--name-only")
		cmd.Env = f.env
		if data, err := cmd.Output(); err != nil || string(data) != "new.txt\n" {
			t.Fatalf("staged %q: %v", data, err)
		}
	})
	t.Run("git stage stages an authorized file", func(t *testing.T) {
		f := stageFixture(t)
		worktree, _ := stageTaskWorktree(t, f)
		if err := os.WriteFile(filepath.Join(worktree, "code.txt"), []byte("through broker\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"worktree": worktree, "paths": []string{"code.txt"}}
		data, _ := json.Marshal(args)
		token, err := gitgrant.Issue(gitgrant.Record{Tool: "mcp__agent_ops__git_stage", Session: "thread-1", Call: "call", Arguments: data})
		if err != nil {
			t.Fatal(err)
		}
		args["authorization"] = token
		result := f.start(t).tool(t, "git_stage", args)
		if result.isError {
			t.Fatalf("authorized stage: %+v", result)
		}
		cmd := exec.Command("git", "-C", worktree, "show", ":code.txt")
		cmd.Env = f.env
		data, err = cmd.Output()
		if err != nil || string(data) != "through broker\n" {
			t.Fatalf("index content %q: %v", data, err)
		}
	})
	t.Run("git stage refuses when the sandbox lets its child write the repository", func(t *testing.T) {
		f := stageFixture(t)
		// The stand-in runs the probe unconfined, as a sandbox that does
		// not hold would.
		f.env = append(f.env, "AGENT_GIT_TEST_SANDBOX=leaky")
		worktree, _ := stageTaskWorktree(t, f)
		if err := os.WriteFile(filepath.Join(worktree, "code.txt"), []byte("unconfined\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"worktree": worktree, "paths": []string{"code.txt"}}
		data, _ := json.Marshal(args)
		token, err := gitgrant.Issue(gitgrant.Record{Tool: "mcp__agent_ops__git_stage", Session: "thread-1", Call: "call", Arguments: data})
		if err != nil {
			t.Fatal(err)
		}
		args["authorization"] = token
		result := f.start(t).tool(t, "git_stage", args)
		if !result.isError || !strings.Contains(result.text, "sandbox does not hold") {
			t.Fatalf("leaky sandbox: %+v", result)
		}
		cmd := exec.Command("git", "-C", worktree, "show", ":code.txt")
		cmd.Env = f.env
		if data, err := cmd.Output(); err != nil || string(data) != "a\nb\nc\n" {
			t.Fatalf("index content %q after refusal: %v", data, err)
		}
		status := exec.Command("git", "-C", worktree, "status", "--porcelain", "--ignored")
		status.Env = f.env
		if data, err := status.Output(); err != nil || string(data) != " M code.txt\n" {
			t.Fatalf("worktree status %q after refusal: %v", data, err)
		}
	})
	t.Run("git stage refuses without a configured sandbox launcher", func(t *testing.T) {
		state := realTempDir(t)
		hostpaths.Bake(t, "", state)
		f := newMCPFixture(t)
		f.env = append(f.env, hostpaths.TestStateHomeEnv+"="+state, "AGENT_GIT_TEST_SANDBOX=1", gitStageCodexEnv+"=codex")
		worktree, _ := stageTaskWorktree(t, f)
		if err := os.WriteFile(filepath.Join(worktree, "code.txt"), []byte("unconfined\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"worktree": worktree, "paths": []string{"code.txt"}}
		data, _ := json.Marshal(args)
		token, err := gitgrant.Issue(gitgrant.Record{Tool: "mcp__agent_ops__git_stage", Session: "thread-1", Call: "call", Arguments: data})
		if err != nil {
			t.Fatal(err)
		}
		args["authorization"] = token
		result := f.start(t).tool(t, "git_stage", args)
		if !result.isError || !strings.Contains(result.text, gitStageCodexEnv) {
			t.Fatalf("relative launcher: %+v", result)
		}
		cmd := exec.Command("git", "-C", worktree, "show", ":code.txt")
		cmd.Env = f.env
		if data, err := cmd.Output(); err != nil || string(data) != "a\nb\nc\n" {
			t.Fatalf("index content %q after refusal: %v", data, err)
		}
	})
	t.Run("git stage refuses a grant after ownership changes", func(t *testing.T) {
		state := realTempDir(t)
		hostpaths.Bake(t, "", state)
		f := newMCPFixture(t)
		f.env = append(f.env, hostpaths.TestStateHomeEnv+"="+state)
		worktree, plan := stageTaskWorktree(t, f)
		if _, err := plan.SetOwner(worktree, "a1", time.Now()); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"worktree": worktree, "paths": []string{"code.txt"}}
		data, _ := json.Marshal(args)
		token, err := gitgrant.Issue(gitgrant.Record{Tool: "mcp__agent_ops__git_stage", Session: "thread-1", Agent: "a1", Call: "call", Arguments: data})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plan.SetOwner(worktree, "a2", time.Now()); err != nil {
			t.Fatal(err)
		}
		args["authorization"] = token
		result := f.start(t).tool(t, "git_stage", args)
		if !result.isError || !strings.Contains(result.text, "ownership") {
			t.Fatalf("staging after transfer: %+v", result)
		}
	})
	t.Run("git stage rejects changed paths", func(t *testing.T) {
		state := realTempDir(t)
		hostpaths.Bake(t, "", state)
		f := newMCPFixture(t)
		f.env = append(f.env, hostpaths.TestStateHomeEnv+"="+state)
		data, err := json.Marshal(map[string]any{"worktree": f.repo, "paths": []string{"allowed"}})
		if err != nil {
			t.Fatal(err)
		}
		token, err := gitgrant.Issue(gitgrant.Record{Tool: "mcp__agent_ops__git_stage", Session: "thread-1", Call: "call", Arguments: data})
		if err != nil {
			t.Fatal(err)
		}
		c := f.start(t)
		result := c.tool(t, "git_stage", map[string]any{"worktree": f.repo, "paths": []string{"other"}, "authorization": token})
		if !result.isError || !strings.Contains(result.text, "authorization") {
			t.Fatalf("changed paths: %+v", result)
		}
		again := c.tool(t, "git_stage", map[string]any{"worktree": f.repo, "paths": []string{"allowed"}, "authorization": token})
		if !again.isError || !strings.Contains(again.text, "authorization") {
			t.Fatalf("replay after changed paths: %+v", again)
		}
	})
	t.Run("git stage consumes authorization before an ownership refusal", func(t *testing.T) {
		state := realTempDir(t)
		hostpaths.Bake(t, "", state)
		f := newMCPFixture(t)
		f.env = append(f.env, hostpaths.TestStateHomeEnv+"="+state)
		// A worktree a plan gives another agent is refused wherever it is,
		// scratch included, where an unnamed fixture repository would pass.
		worktree, plan := stageTaskWorktree(t, f)
		if _, err := plan.SetOwner(worktree, "a2", time.Now()); err != nil {
			t.Fatal(err)
		}
		args := map[string]any{"worktree": worktree, "paths": []string{"code.txt"}}
		data, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		token, err := gitgrant.Issue(gitgrant.Record{Tool: "mcp__agent_ops__git_stage", Session: "thread-1", Agent: "a1", Call: "call", Arguments: data})
		if err != nil {
			t.Fatal(err)
		}
		args["authorization"] = token
		c := f.start(t)
		first := c.tool(t, "git_stage", args)
		if !first.isError || !strings.Contains(first.text, "ownership") {
			t.Fatalf("ownership refusal: %+v", first)
		}
		second := c.tool(t, "git_stage", args)
		if !second.isError || !strings.Contains(second.text, "authorization") {
			t.Fatalf("replayed authorization: %+v", second)
		}
	})
	t.Run("git stage refuses without hook authorization", func(t *testing.T) {
		f := newMCPFixture(t)
		c := f.start(t)
		r := c.tool(t, "git_stage", map[string]any{"worktree": f.repo, "paths": []string{"README.md"}})
		if !r.isError || !strings.Contains(r.text, "authorization") {
			t.Fatalf("stage without authorization: %+v", r)
		}
	})
	t.Run("initialize", func(t *testing.T) {
		c := newMCPFixture(t).start(t)
		r := c.call(t, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "codex", "version": "0.165.0"}})
		var result struct {
			ProtocolVersion string                     `json:"protocolVersion"`
			Capabilities    map[string]json.RawMessage `json:"capabilities"`
			ServerInfo      struct{ Name string }      `json:"serverInfo"`
		}
		json.Unmarshal(r.Result, &result)
		if result.ProtocolVersion != "2025-06-18" || result.Capabilities["tools"] == nil || result.ServerInfo.Name != "agent_ops" {
			t.Errorf("initialize: %s %+v", r.Result, r.Error)
		}
	})

	t.Run("tools/list", func(t *testing.T) {
		c := newMCPFixture(t).start(t)
		r := c.call(t, "tools/list", map[string]any{})
		var result struct {
			Tools []struct {
				Name        string
				InputSchema struct {
					Type                 string
					Properties           map[string]json.RawMessage
					Required             []string
					AdditionalProperties *bool `json:"additionalProperties"`
				} `json:"inputSchema"`
			}
		}
		if err := json.Unmarshal(r.Result, &result); err != nil {
			t.Fatalf("tools/list: %s: %v", r.Result, err)
		}
		var names []string
		for _, tool := range result.Tools {
			names = append(names, tool.Name)
			s := tool.InputSchema
			if s.Type != "object" || s.AdditionalProperties == nil || *s.AdditionalProperties {
				t.Errorf("%s: schema takes properties it does not name: %+v", tool.Name, s)
			}
			if wantWorktree := !strings.HasPrefix(tool.Name, "github_"); slices.Contains(s.Required, "worktree") != wantWorktree {
				t.Errorf("%s: requires worktree: %v, want %v", tool.Name, !wantWorktree, wantWorktree)
			}
		}
		slices.Sort(names)
		want := []string{"git_stage", "git_delegate", "git_delegate_ask", "git_delegate_settle", "git_delegate_show", "github_file", "github_pr_diff", "github_pr_view", "github_profile",
			"pr_loop_ci", "pr_loop_draft", "pr_loop_end", "pr_loop_link", "pr_loop_logs", "pr_loop_next", "pr_loop_open", "pr_loop_push",
			"pr_loop_reply", "pr_loop_request", "pr_loop_sign_check", "pr_loop_stack", "pr_loop_status", "pr_loop_threads",
			"pr_loop_wait"}
		want = append(want, strings.Fields("agent_plan_new agent_plan_log agent_plan_set agent_plan_unfile agent_plan_ask agent_plan_decide agent_plan_followup agent_plan_step agent_plan_part agent_plan_edit agent_plan_after agent_plan_section agent_plan_reference agent_plan_link agent_plan_postpone agent_plan_sign_id agent_plan_workstream_new agent_plan_workstream_list agent_plan_find agent_plan_status")...)
		slices.Sort(want)
		if !slices.Equal(names, want) {
			t.Errorf("tools %q, want %q", names, want)
		}
	})

	t.Run("pr_loop_next returns a decision for each branch", func(t *testing.T) {
		f := newMCPFixture(t)
		c := f.start(t)
		r := c.tool(t, "pr_loop_next", map[string]any{"worktree": f.repo, "branches": []string{"absent-a", "absent-b"}})
		var steps []struct{ Kind, Branch, Why string }
		if err := json.Unmarshal([]byte(r.out), &steps); err != nil || r.isError || len(steps) != 2 {
			t.Fatalf("result %+v: %v", r, err)
		}
		for i, branch := range []string{"absent-a", "absent-b"} {
			if step := steps[i]; step.Kind != "blocked" || step.Branch != branch || !strings.Contains(step.Why, "no local branch") {
				t.Errorf("decision for %s: %+v", branch, step)
			}
		}
	})

	t.Run("pr_loop_threads reads with the server's own login", func(t *testing.T) {
		f := newMCPFixture(t)
		c := f.start(t)
		r := c.tool(t, "pr_loop_threads", map[string]any{"worktree": f.repo, "pr": 1})
		var threads []struct{ ID, Reviewer string }
		if err := json.Unmarshal([]byte(r.out), &threads); err != nil || r.isError ||
			len(threads) != 1 || threads[0].ID != "T1" || threads[0].Reviewer != "copilot" {
			t.Errorf("result %+v: %v", r, err)
		}
	})

	t.Run("github_profile", func(t *testing.T) {
		c := newMCPFixture(t).start(t)
		r := c.tool(t, "github_profile", map[string]any{"login": "ada"})
		var profile struct{ Login, Name, Pronouns string }
		if err := json.Unmarshal([]byte(r.out), &profile); err != nil || r.isError ||
			profile != (struct{ Login, Name, Pronouns string }{"ada", "Ada Lovelace", "she/her"}) {
			t.Errorf("result %+v: %v", r, err)
		}
	})

	// A call the tool cannot take as given is refused before the server
	// reaches GitHub, saying what to change.
	t.Run("refused", func(t *testing.T) {
		f := newMCPFixture(t)
		outside := filepath.Join(filepath.Dir(f.repo), "replies.md")
		os.WriteFile(outside, []byte("=== thread T1\nDone.\n"), 0o644)
		t.Cleanup(func() { os.Remove(outside) })
		os.Symlink(outside, filepath.Join(f.repo, "link.md"))
		c := f.start(t)
		for _, tc := range []struct {
			name, tool string
			args       map[string]any
			says       string
		}{
			{"an argument the tool does not take", "pr_loop_threads", map[string]any{"worktree": f.repo, "pr": 1, "repo": "x/y"}, "repo"},
			{"a missing argument", "pr_loop_threads", map[string]any{"worktree": f.repo}, "pr"},
			{"an argument of another type", "pr_loop_threads", map[string]any{"worktree": f.repo, "pr": "1"}, "pr"},
			{"a value that reads as a flag", "pr_loop_stack", map[string]any{"worktree": f.repo, "target": "--help"}, "target"},
			{"a relative worktree", "pr_loop_status", map[string]any{"worktree": "."}, "worktree"},
			{"a file outside the worktree", "pr_loop_reply", map[string]any{"worktree": f.repo, "replies_file": outside}, "worktree"},
			{"a file that leaves the worktree by ..", "pr_loop_reply", map[string]any{"worktree": f.repo, "replies_file": "../replies.md"}, "worktree"},
			{"a link out of the worktree", "pr_loop_reply", map[string]any{"worktree": f.repo, "replies_file": "link.md"}, "worktree"},
			{"a tool it does not have", "gh_api", map[string]any{"worktree": f.repo}, "gh_api"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				r := c.tool(t, tc.tool, tc.args)
				if !r.isError || !strings.Contains(r.text, tc.says) {
					t.Errorf("result %+v, want an error naming %q", r, tc.says)
				}
			})
		}
		if n := f.github.calls.Load(); n != 0 {
			t.Errorf("GitHub was called %d times", n)
		}
	})

	t.Run("git_delegate runs the reviewers it names", func(t *testing.T) {
		f := newMCPFixture(t)
		finding := map[string]any{"id": "1", "path": "code.txt", "line": 2, "endLine": nil, "kind": "vulnerability",
			"claim": "c", "evidence": "e", "about": nil, "reopens": nil}
		reply := func(structured any) string {
			data, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false,
				"result": "Done.", "modelUsage": map[string]any{"claude-fake": map[string]any{}}, "structured_output": structured})
			return string(data)
		}
		os.WriteFile(filepath.Join(f.log, "security-reviewer-out"), []byte(reply(map[string]any{
			"findings": []any{finding}, "unverified": []string{}, "checked": review.Categories})), 0o644)
		c := f.start(t)
		r := c.tool(t, "git_delegate", map[string]any{"worktree": f.repo, "source": f.base + "..HEAD",
			"reviewer": []string{"security"}, "plan": f.plan})
		var run struct {
			Reviews []struct {
				Reviewer string
				Findings []struct{ Finding, Kind string }
			}
		}
		if err := json.Unmarshal([]byte(r.out), &run); err != nil || r.isError ||
			len(run.Reviews) != 1 || run.Reviews[0].Reviewer != "security" || len(run.Reviews[0].Findings) != 1 ||
			run.Reviews[0].Findings[0].Finding != "F1" || !strings.Contains(r.text, "exit 3") {
			t.Fatalf("result %+v: %v", r, err)
		}
		// The call's Codex thread reaches what it runs, as CODEX_THREAD_ID
		// reaches a command Codex runs.
		env, _ := os.ReadFile(filepath.Join(f.log, "security-reviewer-env"))
		if !strings.Contains(string(env), "CODEX_THREAD_ID=thread-1\n") {
			t.Errorf("the reviewer's environment lacks the thread:\n%s", env)
		}
		// So Codex, which wrote the change, does not review it: Claude
		// does, and Codex, which is not installed here, is never asked.
		os.WriteFile(filepath.Join(f.log, "adversarial-reviewer-out"), []byte(reply(map[string]any{
			"findings": []any{}, "unverified": []string{}, "routes": []any{}})), 0o644)
		r = c.tool(t, "git_delegate", map[string]any{"worktree": f.repo, "source": f.base + "..HEAD", "plan": f.plan})
		var general struct{ Reviews []struct{ Reviewer string } }
		if err := json.Unmarshal([]byte(r.out), &general); err != nil || r.isError ||
			len(general.Reviews) != 1 || general.Reviews[0].Reviewer != "claude" {
			t.Errorf("without a reviewer: %+v: %v", r, err)
		}
	})
}
