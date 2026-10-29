package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// Run as codex by fakeCodexOnPath.
	if os.Getenv("AGENT_PLAN_FAKE_CODEX") == "1" {
		os.Exit(fakeCodexAppServer())
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
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
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
		"** Steps\n*** TODO Add the loop\n:PROPERTIES:\n:CUSTOM_ID: S1\n:END:\n*** TODO Test it\n:PROPERTIES:\n:CUSTOM_ID: S2\n:END:\n", 1)
	os.WriteFile(made.File, []byte(text), 0o644)
	return made.Plan
}

// sessionPlan makes a plan, as newPlan does, in session s-1 of Claude Code.
func sessionPlan(t *testing.T) string {
	t.Helper()
	setup(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-1")
	return newPlan(t)
}

// withQuestions makes a plan, as sessionPlan does, with two open questions:
// Q1, and Q2, which waits for Q1.
func withQuestions(t *testing.T) string {
	t.Helper()
	plan := sessionPlan(t)
	data, _ := os.ReadFile(plan)
	os.WriteFile(plan, []byte(strings.Replace(string(data), "** Questions\n",
		"** Questions\n*** ASK Rename it?\n:PROPERTIES:\n:CUSTOM_ID: Q1\n:RECOMMENDATION: no\n:END:\n- yes\n"+
			"*** ASK Rename its flag too?\n:PROPERTIES:\n:CUSTOM_ID: Q2\n:RECOMMENDATION: no\n:AFTER: Q1\n:END:\n", 1)), 0o644)
	return plan
}

func TestCommands(t *testing.T) {
	t.Run("new", func(t *testing.T) {
		plan := sessionPlan(t)
		if !regexp.MustCompile(`^` + time.Now().Format("20060102") + `[0-9]{6}-add-loop\.box\.org$`).MatchString(filepath.Base(plan)) {
			t.Errorf("new printed the plan %s", plan)
		}
		data, _ := os.ReadFile(plan)
		if !strings.Contains(string(data), ":SESSIONS: claude:s-1\n") || !strings.Contains(string(data), ":HARNESS: claude\n") {
			t.Errorf("the session is not recorded:\n%s", data)
		}
	})

	// Without the notes repository's deploy key, new still writes the
	// plan, and warns on stderr that it is not synced.
	t.Run("new without the deploy key", func(t *testing.T) {
		dir, _ := filepath.EvalSymlinks(t.TempDir())
		key := filepath.Join(dir, "missing-key")
		c := plans.Config{Dir: filepath.Join(dir, "notes", "agents"), Host: "box", Remote: filepath.Join(dir, "origin"), Key: key}
		data, _ := json.Marshal(map[string]any{"plans": c})
		config := filepath.Join(dir, "config.json")
		os.WriteFile(config, data, 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
		t.Setenv("CLAUDE_CODE_SESSION_ID", "")
		code, out, errs := agentPlan(t, "", "new", "--title", "Check", "--slug", "check")
		if code != 0 {
			t.Fatalf("new without the key: %d %s", code, errs)
		}
		var made plans.Created
		if err := json.Unmarshal([]byte(out), &made); err != nil || filepath.Dir(made.Plan) != c.Dir {
			t.Errorf("new printed %s, %v", out, err)
		}
		if _, err := os.Stat(made.Plan); err != nil {
			t.Errorf("plan not written: %v", err)
		}
		if !strings.Contains(errs, "agent-plan: ") || !strings.Contains(errs, key) {
			t.Errorf("no warning naming the key on stderr: %q", errs)
		}
	})

	t.Run("log and report", func(t *testing.T) {
		plan := sessionPlan(t)
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
	})

	t.Run("empty goal", func(t *testing.T) {
		plan := sessionPlan(t)
		code, _, errs := agentPlan(t, "", "goal", plan)
		if code != 2 || !refusal.SaysInstead(strings.TrimPrefix(strings.TrimSpace(errs), "agent-plan: refused: ")) {
			t.Errorf("an empty goal: %d %s", code, errs)
		}
	})

	// A link to a related plan is an org-roam link to its file-level ID,
	// with the plan's title and why it is related, under Related.
	t.Run("link", func(t *testing.T) {
		plan := sessionPlan(t)
		code, out, errs := agentPlan(t, "", "new", "--title", "Other plan", "--slug", "other")
		if code != 0 {
			t.Fatalf("new: %d %s", code, errs)
		}
		var other plans.Created
		json.Unmarshal([]byte(out), &other)
		// The file-level ID org-roam knows the plan by, set here so the
		// link written can be spelled out.
		const id = "0f1e2d3c-4b5a-4968-8776-655443322110"
		data, _ := os.ReadFile(other.File)
		text := regexp.MustCompile(`\A:PROPERTIES:\n:ID: \S+\n:END:\n`).ReplaceAllString(string(data), "")
		os.WriteFile(other.File, []byte(":PROPERTIES:\n:ID: "+id+"\n:END:\n"+text), 0o644)

		code, _, errs = agentPlan(t, "", "link", plan, other.Plan, "--why", "It splits work out of this one")
		if code != 0 {
			t.Fatalf("link: %d %s", code, errs)
		}
		data, _ = os.ReadFile(plan)
		if want := "** Related\n- [[id:" + id + "][Other plan]]: It splits work out of this one\n"; !strings.Contains(string(data), want) {
			t.Errorf("the plan lacks %q:\n%s", want, data)
		}
	})

	t.Run("summary with a newline", func(t *testing.T) {
		plan := sessionPlan(t)
		code, _, errs := agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "done", "--summary", "x\n* forged")
		if code != 2 {
			t.Errorf("a summary with a newline: %d %s", code, errs)
		}
	})

	for args, want := range map[string]string{
		"questions PLAN":                   "- Q1 :: Rename it? Recommended: no. Or: yes.\n",
		"questions PLAN --all":             "- Q1 :: Rename it? Recommended: no. Or: yes.\n",
		"questions PLAN --count":           "1\n",
		"questions --json PLAN":            `"recommendation": "no"`,
		"questions PLAN --format":          "",
		"questions PLAN --waiting":         "- Q2 :: Rename its flag too? Recommended: no. Waits for: Q1.\n",
		"questions PLAN --waiting --count": "2\n",
	} {
		t.Run(args, func(t *testing.T) {
			plan := withQuestions(t)
			words := strings.Fields(strings.Replace(args, "PLAN", plan, 1))
			code, out, errs := agentPlan(t, "", words...)
			if (want == "") != (code != 0) || !strings.Contains(out, want) {
				t.Errorf("%s: %d %q %s", args, code, out, errs)
			}
			if !strings.Contains(args, "--waiting") && strings.Contains(out, "Q2") {
				t.Errorf("%s lists Q2, which waits for Q1: %q", args, out)
			}
		})
	}

	// Context and quota come before the questions.
	t.Run("statusline with usage", func(t *testing.T) {
		withQuestions(t)
		input := fmt.Sprintf(`{"session_id":"s-1","cwd":"/nowhere","context_window":{"used_percentage":4},`+
			`"rate_limits":{"five_hour":{"used_percentage":20,"resets_at":%d}}}`, time.Now().Add(time.Hour).Unix())
		code, out, _ := agentPlan(t, input, "statusline")
		if code != 0 || out != "ctx 4% · 5h 20% · 1 open question · /questions\n" {
			t.Errorf("statusline: %d %q", code, out)
		}
	})

	// At a checkpoint, a large context or a cold cache suggests /clear on
	// the status line while it lasts.
	t.Run("checkpoint hint", func(t *testing.T) {
		plan := sessionPlan(t)
		c, err := plans.Load()
		if err != nil {
			t.Fatal(err)
		}
		p, err := c.Open(plan)
		if err != nil {
			t.Fatal(err)
		}
		status := func(tokens int, expires time.Time) string {
			return fmt.Sprintf(`{"session_id":"s-1","cwd":"/nowhere","context_window":{"used_percentage":25,`+
				`"context_window_size":1000000,"current_usage":{"input_tokens":1,"cache_creation_input_tokens":0,`+
				`"cache_read_input_tokens":%d}},"prompt_cache":{"warm":true,"expires_at":%d}}`, tokens, expires.Unix())
		}
		large, small := status(250000, time.Now().Add(time.Hour)), status(30000, time.Now().Add(time.Hour))
		if _, out, _ := agentPlan(t, large, "statusline"); strings.Contains(out, "/clear") {
			t.Errorf("a hint before any checkpoint: %q", out)
		}
		if _, err := p.Log(plans.Event{Kind: "handed-back", Item: p.Name}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, out, _ := agentPlan(t, small, "statusline"); strings.Contains(out, "/clear") {
			t.Errorf("a hint for a small warm context: %q", out)
		}
		if _, out, _ := agentPlan(t, status(30000, time.Now().Add(-time.Minute)), "statusline"); !strings.HasSuffix(out, "checkpoint · /clear\n") {
			t.Errorf("no hint for a cold cache: %q", out)
		}
		if _, out, _ := agentPlan(t, large, "statusline"); !strings.HasSuffix(out, "checkpoint · /clear\n") {
			t.Errorf("no hint for a large context: %q", out)
		}
	})

	t.Run("decide and ask", func(t *testing.T) {
		plan := withQuestions(t)
		code, out, _ := agentPlan(t, `{"session_id":"s-1","cwd":"/nowhere"}`, "statusline")
		if code != 0 || out != "1 open question · /questions\n" {
			t.Errorf("statusline: %d %q", code, out)
		}
		code, out, errs := agentPlan(t, "", "decide", plan, "Q1", "--answer", "yes")
		if code != 0 || !strings.Contains(out, `"state": "DECIDED"`) {
			t.Errorf("decide: %d %s %s", code, out, errs)
		}
		code, out, _ = agentPlan(t, "", "questions", plan)
		if code != 0 || !strings.HasPrefix(out, "- Q2 ::") {
			t.Errorf("questions once Q1 is decided: %d %q", code, out)
		}
		if code, out, errs = agentPlan(t, "", "decide", plan, "Q2", "--drop", "--answer", "moot"); code != 0 ||
			!strings.Contains(out, `"state": "DROPPED"`) {
			t.Errorf("drop Q2: %d %s %s", code, out, errs)
		}
		code, out, _ = agentPlan(t, "", "decisions", plan)
		if code != 0 || out != "- Q1 :: Rename it? yes\n" {
			t.Errorf("decisions: %d %q", code, out)
		}
		for _, stdin := range []string{`{"session_id":"s-1"}`, `{`, ``} {
			code, out, errs = agentPlan(t, stdin, "statusline")
			if code != 0 || out != "" || errs != "" {
				t.Errorf("statusline with %q: %d %q %q", stdin, code, out, errs)
			}
		}
		code, out, errs = agentPlan(t, "", "ask", plan, "--question", "Keep the alias?", "--recommendation", "yes",
			"--alternative", "drop it", "--alternative", "rename it", "--after", "Q1", "--context", "Two scripts call it.",
			"--priority", "A")
		if code != 0 || !strings.Contains(out, `"id": "Q3"`) {
			t.Errorf("ask: %d %s %s", code, out, errs)
		}
		code, out, _ = agentPlan(t, "", "questions", plan)
		if code != 0 || out != "- Q3 [#A] :: Keep the alias? Recommended: yes. Or: drop it; rename it.\n" {
			t.Errorf("questions after ask: %d %q", code, out)
		}
		if code, _, errs = agentPlan(t, "", "ask", plan, "--question", "Why?"); code != 2 {
			t.Errorf("ask without a recommendation: %d %s", code, errs)
		}
		code, out, errs = agentPlan(t, "", "ask", plan, "--question", "Keep tests offline?", "--recommendation", "yes",
			"--answer", "yes: CI has no network", "--role", "single")
		if code != 0 || !strings.Contains(out, `"id": "Q4"`) {
			t.Errorf("ask --answer: %d %s %s", code, out, errs)
		}
		code, out, _ = agentPlan(t, "", "decisions", plan, "--json")
		if code != 0 || !strings.Contains(out, `"decidedBy": "agent"`) || !strings.Contains(out, `"id": "Q4"`) {
			t.Errorf("decisions after ask --answer: %d %s", code, out)
		}
		if code, out, errs = agentPlan(t, "", "decide", plan, "Q5", "--answer", "x", "--supersedes", "Q4"); code != 2 {
			t.Errorf("decide an unknown question: %d %s %s", code, out, errs)
		}
		// A retro proposal is asked with its kind, which questions --json
		// shows, and an unknown kind is refused.
		code, out, errs = agentPlan(t, "", "ask", plan, "--question", "Deny git add -A?", "--recommendation", "a guard rule",
			"--kind", "retro")
		if code != 0 || !strings.Contains(out, `"id": "Q5"`) {
			t.Errorf("ask --kind retro: %d %s %s", code, out, errs)
		}
		code, out, _ = agentPlan(t, "", "questions", plan, "--json")
		if code != 0 || strings.Count(out, `"kind": "retro"`) != 1 || !strings.Contains(out, `"id": "Q5"`) {
			t.Errorf("questions --json after ask --kind: %d %s", code, out)
		}
		if code, out, errs = agentPlan(t, "", "ask", plan, "--question", "Why?", "--recommendation", "yes",
			"--kind", "bug"); code != 2 {
			t.Errorf("ask --kind bug: %d %s %s", code, out, errs)
		}
	})

	t.Run("followup", func(t *testing.T) {
		plan := sessionPlan(t)
		code, out, errs := agentPlan(t, "", "followup", plan, "--title", "Guard accepts --draft=false", "--found-in", "S1",
			"--context", "Found while adding the loop.")
		if code != 0 || !strings.Contains(out, `"id": "F1"`) || !strings.Contains(out, `"kind": "discovered"`) {
			t.Errorf("followup: %d %s %s", code, out, errs)
		}
		code, out, _ = agentPlan(t, "", "status", plan)
		if code != 0 || !strings.Contains(out, "- Follow-ups open: F1\n") {
			t.Errorf("status: %d\n%s", code, out)
		}
		code, out, _ = agentPlan(t, "", "brief", plan)
		if code != 0 || !strings.HasPrefix(out, "Brief of plan "+plan) || !strings.Contains(out, "Follow-ups open: 1\n") {
			t.Errorf("brief: %d\n%s", code, out)
		}
		code, out, _ = agentPlan(t, "", "brief", plan, "--resume")
		if code != 0 || !strings.HasPrefix(out, "Resume brief of plan "+plan) || !strings.Contains(out, "Follow-ups open: 1\n") {
			t.Errorf("brief --resume: %d\n%s", code, out)
		}
	})

	t.Run("reference", func(t *testing.T) {
		plan := sessionPlan(t)
		code, out, errs := agentPlan(t, "", "reference", plan, "https://example.com/guide", "--note", "Used for the review; fed Q1.")
		if code != 0 || !strings.Contains(out, `"kind": "referenced"`) {
			t.Errorf("reference: %d %s %s", code, out, errs)
		}
		data, _ := os.ReadFile(plan)
		if !strings.Contains(string(data), ":URL: https://example.com/guide\n:END:\nUsed for the review; fed Q1.\n") {
			t.Errorf("plan:\n%s", data)
		}
		if code, _, _ := agentPlan(t, "", "reference", plan); code != 2 {
			t.Errorf("reference without a URL: %d", code)
		}
	})

	t.Run("new from a missing template", func(t *testing.T) {
		setup(t)
		code, _, errs := agentPlan(t, "", "new", "--title", "T", "--slug", "t", "--template", "nowhere")
		if code != 2 || !strings.Contains(errs, "templates") {
			t.Errorf("new --template nowhere: %d %s", code, errs)
		}
	})

	t.Run("postpone", func(t *testing.T) {
		plan := sessionPlan(t)
		readySteps := func() []string {
			t.Helper()
			code, out, _ := agentPlan(t, "", "status", plan, "--format", "json")
			var s plans.Status
			if code != 0 || json.Unmarshal([]byte(out), &s) != nil {
				t.Fatalf("status: %d %s", code, out)
			}
			return s.ReadySteps
		}
		if code, out, errs := agentPlan(t, "", "postpone", plan, "S1", "--until", "2099-01-01 09:00"); code != 0 ||
			!strings.Contains(out, `"kind": "postponed"`) {
			t.Errorf("postpone: %d %s %s", code, out, errs)
		}
		if got := strings.Join(readySteps(), " "); got != "S2" {
			t.Errorf("ready steps once S1 is postponed: %q", got)
		}
		if code, _, errs := agentPlan(t, "", "postpone", plan, "S1", "--clear"); code != 0 {
			t.Errorf("postpone --clear: %d %s", code, errs)
		}
		if got := strings.Join(readySteps(), " "); got != "S1 S2" {
			t.Errorf("ready steps once cleared: %q", got)
		}
		for _, args := range [][]string{{"S1"}, {"S1", "--until", "2099-01-01", "--clear"}} {
			if code, _, _ := agentPlan(t, "", append([]string{"postpone", plan}, args...)...); code != 2 {
				t.Errorf("postpone %v: %d", args, code)
			}
		}
	})

	t.Run("step, part, edit, after and section", func(t *testing.T) {
		plan := sessionPlan(t)
		code, out, errs := agentPlan(t, "", "step", plan, "--title", "Tag it", "--text", "Done when the tag is pushed.", "--after", "S1")
		if code != 0 || !strings.Contains(out, `"id": "S3"`) {
			t.Fatalf("step: %d %s %s", code, out, errs)
		}
		code, out, errs = agentPlan(t, "", "part", plan, "--title", "plan-write-path", "--repo", "nix-config",
			"--branch", "main..plan-write-path", "--worktree", "/w/p")
		if code != 0 || !strings.Contains(out, `"id": "P1"`) {
			t.Fatalf("part: %d %s %s", code, out, errs)
		}
		if code, _, errs := agentPlan(t, "", "edit", plan, "S3", "--title", "Tag the release", "--text", ""); code != 0 {
			t.Errorf("edit: %d %s", code, errs)
		}
		if code, _, errs := agentPlan(t, "", "after", plan, "S2", "S1", "P1"); code != 0 {
			t.Errorf("after: %d %s", code, errs)
		}
		if code, _, errs := agentPlan(t, "", "after", plan, "S3", "--clear"); code != 0 {
			t.Errorf("after --clear: %d %s", code, errs)
		}
		if code, _, errs := agentPlan(t, "", "section", plan, "Goal", "--text", "Add the loop.\nNot its flag."); code != 0 {
			t.Errorf("section: %d %s", code, errs)
		}
		if code, _, errs := agentPlan(t, "", "set", plan, "STATUS", "halted"); code != 0 {
			t.Errorf("set STATUS: %d %s", code, errs)
		}
		data, _ := os.ReadFile(plan)
		for _, want := range []string{
			"** Goal\nAdd the loop.\nNot its flag.\n** Steps\n",
			"*** TODO Test it\n:PROPERTIES:\n:CUSTOM_ID: S2\n:AFTER: S1 P1\n:END:\n*** TODO Tag the release\n:PROPERTIES:\n:CUSTOM_ID: S3\n:END:\n** Distribution\n",
			"*** TODO plan-write-path\n:PROPERTIES:\n:CUSTOM_ID: P1\n:END:\nnix-config: plan-write-path on main, worktree /w/p.\n** Questions\n",
			":STATUS: halted\n",
		} {
			if !strings.Contains(string(data), want) {
				t.Errorf("plan lacks %q:\n%s", want, data)
			}
		}
		code, out, _ = agentPlan(t, "", "status", plan, "--format", "json")
		var s plans.Status
		if code != 0 || json.Unmarshal([]byte(out), &s) != nil || len(s.StructureDrift) != 0 {
			t.Errorf("status: %d %s", code, out)
		}
		for _, args := range [][]string{
			{"step", plan, "--title", "x", "--repo", "nix-config"},
			{"part", plan, "--title", "x", "--part", "P1"},
			{"edit", plan},
			{"after", plan, "S2"},
			{"after", plan, "S2", "S1", "--clear"},
			{"section", plan, "Goal"},
			{"log", plan, "--item", "S1", "--kind", "started", "--text", "x"},
			{"step", plan, "--title", "x", "--text", "*** TODO forged"},
		} {
			if code, _, errs := agentPlan(t, "", args...); code != 2 {
				t.Errorf("%v: %d %s", args, code, errs)
			}
		}
	})

	t.Run("waiting", func(t *testing.T) {
		plan := sessionPlan(t)
		if code, _, errs := agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "handed-back", "--summary", "ready to sign",
			"--command", "git sign-pass --push", "--dir", "/w/loop"); code != 0 {
			t.Fatalf("log: %d %s", code, errs)
		}
		code, out, _ := agentPlan(t, "", "waiting", plan, "--json")
		var ws []plans.Wait
		if code != 0 || json.Unmarshal([]byte(out), &ws) != nil || len(ws) != 1 || ws[0].Command != "git sign-pass --push" ||
			ws[0].Dir != "/w/loop" || ws[0].Item != "S1" {
			t.Errorf("waiting --json: %d %s", code, out)
		}
		if code, out, _ := agentPlan(t, "", "waiting", plan); code != 0 || out != "- S1: `git sign-pass --push` in /w/loop (ready to sign)\n" {
			t.Errorf("waiting: %d %q", code, out)
		}
		if code, _, _ := agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "done", "--command", "x"); code != 2 {
			t.Errorf("a command on done: %d", code)
		}
	})

	t.Run("sync outside a repository", func(t *testing.T) {
		sessionPlan(t)
		code, out, errs := agentPlan(t, "", "sync", "--settle", "0s")
		if code != 0 || !strings.Contains(out, `"skipped"`) {
			t.Errorf("sync outside a repository: %d %s %s", code, out, errs)
		}
	})

	t.Run("reseal", func(t *testing.T) {
		plan := sessionPlan(t)
		// An intact log with events in it, as agent-plan left it.
		if code, _, errs := agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "done", "--summary", "Add the loop"); code != 0 {
			t.Fatalf("log: %d %s", code, errs)
		}
		code, _, errs := agentPlan(t, "", "reseal", plan)
		if code != 2 || !strings.Contains(errs, "nothing to reseal") {
			t.Errorf("reseal of an intact log: %d %s", code, errs)
		}
		log := strings.TrimSuffix(plan, ".org") + ".log.org"
		data, _ := os.ReadFile(log)
		os.WriteFile(log, append(data, "by hand\n"...), 0o644)
		code, out, errs := agentPlan(t, "", "reseal", plan)
		if code != 0 || !strings.Contains(out, `"kind": "resealed"`) {
			t.Errorf("reseal: %d %s %s", code, out, errs)
		}
	})
}

func TestLog(t *testing.T) {
	// Writers in separate processes never interleave or lose an event.
	t.Run("concurrent processes", func(t *testing.T) {
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
		all, err := p.Events()
		var events []plans.Event
		for _, e := range all {
			if e.Kind == "started" {
				events = append(events, e)
			}
		}
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
	})
}

func TestHooks(t *testing.T) {
	// planned makes a plan, as newPlan does, in session s-9 of Claude Code.
	planned := func(t *testing.T) string {
		t.Helper()
		setup(t)
		t.Setenv("CLAUDE_CODE_SESSION_ID", "s-9")
		return newPlan(t)
	}

	t.Run("Claude Code names the session's plans when it resumes", func(t *testing.T) {
		plan := planned(t)
		code, out, _ := agentPlan(t, `{"session_id":"s-9","source":"compact","cwd":"/nowhere"}`, "find", "--hook")
		if code != 0 || !strings.Contains(out, plan) || !strings.Contains(out, `"hookEventName":"SessionStart"`) {
			t.Errorf("find --hook: %d %s", code, out)
		}
	})

	t.Run("Claude Code hands the plan over across /clear", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		plan := planned(t)
		code, out, _ := agentPlan(t, `{"session_id":"s-9","hook_event_name":"SessionEnd","reason":"clear","cwd":"/nowhere"}`, "find", "--hook")
		if code != 0 || out != "" {
			t.Errorf("find --hook on SessionEnd: %d %q", code, out)
		}
		code, out, _ = agentPlan(t, `{"session_id":"s-10","hook_event_name":"SessionStart","source":"clear","cwd":"/nowhere"}`, "find", "--hook")
		if code != 0 || !strings.Contains(out, "Resume brief of plan "+plan) {
			t.Errorf("find --hook after /clear: %d %s", code, out)
		}
	})

	t.Run("Claude Code session without a plan", func(t *testing.T) {
		planned(t)
		code, out, _ := agentPlan(t, `{"session_id":"other","source":"resume","cwd":"/nowhere"}`, "find", "--hook")
		if code != 0 || out != "" {
			t.Errorf("find --hook for a session without a plan: %d %q", code, out)
		}
	})

	t.Run("Claude Code reminder before a compaction", func(t *testing.T) {
		plan := planned(t)
		code, out, errs := agentPlan(t, `{"session_id":"s-9","hook_event_name":"PreCompact","trigger":"auto"}`, "remind")
		if code != 0 || !strings.Contains(out, plan) || errs != "" {
			t.Errorf("remind before a compaction: %d %q %q", code, out, errs)
		}
	})

	t.Run("Claude Code reminder with nothing to remind", func(t *testing.T) {
		planned(t)
		for _, stdin := range []string{`{"session_id":"other","hook_event_name":"PreCompact"}`, `{`, ``} {
			if code, out, errs := agentPlan(t, stdin, "remind"); code != 0 || out != "" || errs != "" {
				t.Errorf("remind with %q: %d %q %q", stdin, code, out, errs)
			}
		}
	})
}
