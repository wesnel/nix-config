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
		"--commit", "Add the loop"); code != 0 {
		t.Fatalf("log: %d %s", code, errs)
	}
	code, out, _ := agentPlan(t, "", "status", plan)
	if code != 0 || !strings.Contains(out, "| S1 | Add the loop | yes | no | Add the loop |") {
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
	code, _, errs := agentPlan(t, "", "goal", plan)
	if code != 2 || !refusal.SaysInstead(strings.TrimPrefix(strings.TrimSpace(errs), "agent-plan: refused: ")) {
		t.Errorf("an empty goal: %d %s", code, errs)
	}
	code, _, errs = agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "done", "--summary", "x\n* forged")
	if code != 2 {
		t.Errorf("a summary with a newline: %d %s", code, errs)
	}
	data, _ = os.ReadFile(plan)
	os.WriteFile(plan, []byte(strings.Replace(string(data), "** Open questions\n",
		"** Open questions\n*** Rename it?\n:PROPERTIES:\n:ID: Q1\n:RECOMMENDATION: no\n:END:\n- yes\n"+
			"*** Rename its flag too?\n:PROPERTIES:\n:ID: Q2\n:RECOMMENDATION: no\n:AFTER: Q1\n:END:\n", 1)), 0o644)
	for args, want := range map[string]string{
		"questions PLAN":                   "- Q1 :: Rename it? Recommended: no. Or: yes.\n",
		"questions PLAN --all":             "- Q1 :: Rename it? Recommended: no. Or: yes.\n",
		"questions PLAN --count":           "1\n",
		"questions --json PLAN":            `"recommendation": "no"`,
		"questions PLAN --format":          "",
		"questions PLAN --waiting":         "- Q2 :: Rename its flag too? Recommended: no. Waits for: Q1.\n",
		"questions PLAN --waiting --count": "2\n",
	} {
		words := strings.Fields(strings.Replace(args, "PLAN", plan, 1))
		code, out, errs = agentPlan(t, "", words...)
		if (want == "") != (code != 0) || !strings.Contains(out, want) {
			t.Errorf("%s: %d %q %s", args, code, out, errs)
		}
		if !strings.Contains(args, "--waiting") && strings.Contains(out, "Q2") {
			t.Errorf("%s lists Q2, which waits for Q1: %q", args, out)
		}
	}
	code, out, _ = agentPlan(t, `{"session_id":"s-1","cwd":"/nowhere"}`, "statusline")
	if code != 0 || out != "1 open question · /questions\n" {
		t.Errorf("statusline: %d %q", code, out)
	}
	code, out, errs = agentPlan(t, "", "decide", plan, "Q1", "--answer", "yes")
	if code != 0 || !strings.Contains(out, `"decision": "D1"`) {
		t.Errorf("decide: %d %s %s", code, out, errs)
	}
	code, out, _ = agentPlan(t, "", "questions", plan)
	if code != 0 || !strings.HasPrefix(out, "- Q2 ::") {
		t.Errorf("questions once Q1 is decided: %d %q", code, out)
	}
	if code, _, errs = agentPlan(t, "", "decide", plan, "Q2", "--answer", "no"); code != 0 {
		t.Errorf("decide Q2: %d %s", code, errs)
	}
	for _, stdin := range []string{`{"session_id":"s-1"}`, `{`, ``} {
		code, out, errs = agentPlan(t, stdin, "statusline")
		if code != 0 || out != "" || errs != "" {
			t.Errorf("statusline with %q: %d %q %q", stdin, code, out, errs)
		}
	}
	code, out, errs = agentPlan(t, "", "ask", plan, "--question", "Keep the alias?", "--recommendation", "yes",
		"--alternative", "drop it", "--alternative", "rename it", "--after", "Q1", "--context", "Two scripts call it.")
	if code != 0 || !strings.Contains(out, `"id": "Q3"`) {
		t.Errorf("ask: %d %s %s", code, out, errs)
	}
	code, out, _ = agentPlan(t, "", "questions", plan)
	if code != 0 || out != "- Q3 :: Keep the alias? Recommended: yes. Or: drop it; rename it.\n" {
		t.Errorf("questions after ask: %d %q", code, out)
	}
	if code, _, errs = agentPlan(t, "", "ask", plan, "--question", "Why?"); code != 2 {
		t.Errorf("ask without a recommendation: %d %s", code, errs)
	}
	code, out, errs = agentPlan(t, "", "sync", "--settle", "0s")
	if code != 0 || !strings.Contains(out, `"skipped"`) {
		t.Errorf("sync outside a repository: %d %s %s", code, out, errs)
	}
	code, _, errs = agentPlan(t, "", "reseal", plan)
	if code != 2 || !strings.Contains(errs, "nothing to reseal") {
		t.Errorf("reseal of an intact log: %d %s", code, errs)
	}
	log := strings.TrimSuffix(plan, ".org") + ".log.org"
	data, _ = os.ReadFile(log)
	os.WriteFile(log, append(data, "by hand\n"...), 0o644)
	code, out, errs = agentPlan(t, "", "reseal", plan)
	if code != 0 || !strings.Contains(out, `"kind": "resealed"`) {
		t.Errorf("reseal: %d %s %s", code, out, errs)
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
		{"find", "--hook"},
		{"sync"},
		{"sync", "--pull"},
		{"reseal", plan},
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
		{"questions", plan},
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
	for args, want := range map[string]bool{"find --hook": true, "statusline": true, "remind": true, "find --session x": false,
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

// Claude Code's SessionStart hook: the session's plans are named when it
// resumes.
func TestClaudeHooks(t *testing.T) {
	setup(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-9")
	plan := newPlan(t)
	code, out, _ := agentPlan(t, `{"session_id":"s-9","source":"compact","cwd":"/nowhere"}`, "find", "--hook")
	if code != 0 || !strings.Contains(out, plan) || !strings.Contains(out, `"hookEventName":"SessionStart"`) {
		t.Errorf("find --hook: %d %s", code, out)
	}
	code, out, _ = agentPlan(t, `{"session_id":"other","source":"resume","cwd":"/nowhere"}`, "find", "--hook")
	if code != 0 || out != "" {
		t.Errorf("find --hook for a session without a plan: %d %q", code, out)
	}
	code, out, errs := agentPlan(t, `{"session_id":"s-9","hook_event_name":"PreCompact","trigger":"auto"}`, "remind")
	if code != 0 || !strings.Contains(out, plan) || errs != "" {
		t.Errorf("remind before a compaction: %d %q %q", code, out, errs)
	}
	t.Setenv("SANDBOX_RELAY_URL", "http://127.0.0.1:1")
	for _, stdin := range []string{`{"session_id":"other","hook_event_name":"PreCompact"}`, `{`, ``} {
		if code, out, errs = agentPlan(t, stdin, "remind"); code != 0 || out != "" || errs != "" {
			t.Errorf("remind with %q: %d %q %q", stdin, code, out, errs)
		}
	}
}
