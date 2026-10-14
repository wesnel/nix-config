package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// With AGENT_PLAN_TEST_MAIN set, the test binary is agent-plan itself, for
// tests that run it as separate processes.
func TestMain(m *testing.M) {
	gitenv.Isolate()
	if os.Getenv("AGENT_PLAN_TEST_MAIN") == "1" {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// setup points agent-plan at a plans directory of the test's own.
func setup(t *testing.T) plans.Config {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	config := filepath.Join(dir, "config.json")
	c := plans.Config{Dir: filepath.Join(dir, "agents"), Host: "box"}
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("SANDBOX_RELAY_URL", "")
	t.Setenv("SANDBOX_RELAYED", "")
	return c
}

func agentPlan(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(args, strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// newPlan makes a plan with steps S1 and S2.
func newPlan(t *testing.T) string {
	t.Helper()
	code, out, errs := agentPlan(t, "", "new", "--title", "Add the loop", "--slug", "add-loop", "--branch", "main..loop")
	if code != 0 {
		t.Fatalf("new: %d %s", code, errs)
	}
	var made plans.Created
	json.Unmarshal([]byte(out), &made)
	data, _ := os.ReadFile(made.File)
	text := strings.Replace(string(data), "** Steps\n",
		"** Steps\n*** TODO Add the loop\n:PROPERTIES:\n:ID: S1\n:END:\n*** TODO Test it\n:PROPERTIES:\n:ID: S2\n:END:\n", 1)
	os.WriteFile(made.File, []byte(text), 0o644)
	return made.Plan
}

func TestCommands(t *testing.T) {
	setup(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-1")
	plan := newPlan(t)
	if filepath.Base(plan) != time.Now().Format("2006-01-02")+"-add-loop.box.org" {
		t.Errorf("new printed the plan %s", plan)
	}
	data, _ := os.ReadFile(plan)
	if !strings.Contains(string(data), ":SESSIONS: claude:s-1\n") || !strings.Contains(string(data), ":HARNESS: claude\n") {
		t.Errorf("the session is not recorded:\n%s", data)
	}
	if code, _, errs := agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "done", "--summary", "Add the loop",
		"--commit", "Add the loop", "--tokens", "1500", "--model", "opus", "--effort", "low"); code != 0 {
		t.Fatalf("log: %d %s", code, errs)
	}
	code, out, _ := agentPlan(t, "", "status", plan)
	if code != 0 || !strings.Contains(out, "| S1 | Add the loop | yes | no | Add the loop |") || !strings.Contains(out, "- Tokens: 1500") {
		t.Errorf("status: %d\n%s", code, out)
	}
	code, out, _ = agentPlan(t, "", "status", plan, "--format", "json")
	var s plans.Status
	if code != 0 || json.Unmarshal([]byte(out), &s) != nil || s.Implemented != "0/2" {
		t.Errorf("status as JSON: %d\n%s", code, out)
	}
	code, out, _ = agentPlan(t, "", "find", "--session", "s-1")
	if code != 0 || !strings.Contains(out, plan) {
		t.Errorf("find: %d\n%s", code, out)
	}
	code, out, _ = agentPlan(t, "", "init")
	if code != 0 || !strings.Contains(out, `"dir"`) {
		t.Errorf("init: %d\n%s", code, out)
	}
	code, out, _ = agentPlan(t, "", "tokens")
	if code != 0 || !strings.Contains(out, "| done | opus | low | 1 | 1500 |") {
		t.Errorf("tokens: %d\n%s", code, out)
	}
	code, _, errs := agentPlan(t, "", "goal", plan)
	if code != 2 || !refusal.SaysInstead(strings.TrimPrefix(strings.TrimSpace(errs), "agent-plan: refused: ")) {
		t.Errorf("an empty goal: %d %s", code, errs)
	}
	code, _, errs = agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "done", "--summary", "x\n* forged")
	if code != 2 {
		t.Errorf("a summary with a newline: %d %s", code, errs)
	}
}

// Under the relay, a sandbox may log to and read plans, and nothing else.
func TestRelayed(t *testing.T) {
	setup(t)
	plan := newPlan(t)
	t.Setenv("SANDBOX_RELAYED", "1")
	for _, args := range [][]string{
		{"new", "--title", "T", "--slug", "t"},
		{"init"},
		{"import-claude"},
		{"tokens"},
		{"find", "--hook"},
		{"bogus"},
	} {
		code, _, errs := agentPlan(t, "{}", args...)
		reason := strings.TrimPrefix(strings.TrimSpace(errs), "agent-plan: refused: ")
		if code != 2 || !refusal.SaysInstead(reason) {
			t.Errorf("relayed %v: %d %q", args, code, errs)
		}
	}
	for _, args := range [][]string{
		{"log", plan, "--item", "S1", "--kind", "started"},
		{"status", plan},
		{"find", "--branch", "loop"},
	} {
		if code, _, errs := agentPlan(t, "", args...); code != 0 {
			t.Errorf("relayed %v: %d %s", args, code, errs)
		}
	}
	code, _, errs := agentPlan(t, "", "log", t.TempDir(), "--item", "S1", "--kind", "started")
	if code != 2 {
		t.Errorf("a plan outside the plans directory: %d %s", code, errs)
	}
}

// Hooks run on the host, so a session's relay never takes them.
func TestClientSide(t *testing.T) {
	for args, want := range map[string]bool{"import-claude": true, "find --hook": true, "find --session x": false,
		"log P --item S1": false, "new --title T": false} {
		if got := clientSide(strings.Fields(args)); got != want {
			t.Errorf("clientSide(%s) = %v", args, got)
		}
	}
}

// Writers in separate processes never interleave or lose an event.
func TestConcurrentProcesses(t *testing.T) {
	setup(t)
	plan := newPlan(t)
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				cmd := exec.Command(os.Args[0], "log", plan, "--item", "S2", "--kind", "started",
					"--summary", fmt.Sprintf("writer %d event %d %s", w, i, strings.Repeat("y", 4000)))
				cmd.Env = append(os.Environ(), "AGENT_PLAN_TEST_MAIN=1")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("writer %d: %v\n%s", w, err, out)
				}
			}
		}(w)
	}
	wg.Wait()
	c, _ := plans.Load()
	p, _ := c.Open(plan)
	events, err := p.Events()
	if err != nil || len(events) != 30 {
		t.Fatalf("%d events, %v", len(events), err)
	}
	seen := map[string]bool{}
	for _, e := range events {
		if !strings.HasPrefix(e.Summary, "writer ") || !strings.HasSuffix(e.Summary, strings.Repeat("y", 4000)) {
			t.Errorf("garbled: %.60q", e.Summary)
		}
		seen[e.Summary[:20]] = true
	}
	if len(seen) != 30 {
		t.Errorf("%d distinct events", len(seen))
	}
}

// Claude Code's hooks: an approved plan is recorded, and the session's
// plans are named when it resumes.
func TestClaudeHooks(t *testing.T) {
	c := setup(t)
	hook := `{"session_id":"s-9","cwd":"/nowhere","tool_name":"ExitPlanMode","tool_input":{"plan":"# Fix it\n\n- step"}}`
	code, out, errs := agentPlan(t, hook, "import-claude")
	if code != 0 || !strings.Contains(out, `"hookEventName":"PostToolUse"`) {
		t.Fatalf("import-claude: %d %s %s", code, out, errs)
	}
	matches, _ := filepath.Glob(filepath.Join(c.Dir, "*-fix-it.box.org"))
	if len(matches) != 1 {
		t.Fatalf("no plan made: %v", matches)
	}
	code, out, _ = agentPlan(t, `{"session_id":"s-9","source":"compact","cwd":"/nowhere"}`, "find", "--hook")
	if code != 0 || !strings.Contains(out, matches[0]) || !strings.Contains(out, `"hookEventName":"SessionStart"`) {
		t.Errorf("find --hook: %d %s", code, out)
	}
	code, out, _ = agentPlan(t, `{"session_id":"other","source":"resume","cwd":"/nowhere"}`, "find", "--hook")
	if code != 0 || out != "" {
		t.Errorf("find --hook for a session without a plan: %d %q", code, out)
	}
}
