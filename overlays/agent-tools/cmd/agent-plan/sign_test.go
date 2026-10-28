package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

func TestSignPlan(t *testing.T) {
	t.Run("selected", testSignSelectedPlan)
	t.Run("refused", testSignPlanRefused)
}

func testSignSelectedPlan(t *testing.T) {
	c := setup(t)
	worktree, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", worktree, "init", "-q", "-b", "topic").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	t.Chdir(worktree)
	selected, err := c.New(plans.NewOptions{Title: "Selected", Slug: "selected", Sessions: []string{"codex:handoff"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	implicit, err := c.New(plans.NewOptions{Title: "Implicit", Slug: "implicit", Worktrees: []string{worktree}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"sign-id", "--session", "codex:handoff", "--worktree", worktree,
		"--branch", "topic", "--branch", "other", "--base", "parent-feature"}
	code, out, errs := agentPlan(t, "", args...)
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), implicit.ID+".") {
		t.Fatalf("default worktree selection: %d %q %s", code, out, errs)
	}
	before, err := os.ReadFile(implicit.Log)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errs = agentPlan(t, "", append(args, "--plan", selected.Plan)...)
	id := strings.TrimSpace(out)
	if code != 0 || !strings.HasPrefix(id, selected.ID+".") {
		t.Fatalf("explicit plan selection: %d %q %s", code, out, strings.SplitN(errs, "\n", 2)[0])
	}
	p, err := c.Open(selected.Plan)
	if err != nil {
		t.Fatal(err)
	}
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("explicit request did not reach the selected log")
	}
	last := events[len(events)-1]
	if last.Kind != "sign-requested" || last.Sign != id || last.Item != selected.ID ||
		last.Session != "codex:handoff" || last.Worktree != worktree || last.Branch != "topic,other" || last.Base != "parent-feature" {
		t.Errorf("explicit request: %+v", last)
	}
	after, err := os.ReadFile(implicit.Log)
	if err != nil || string(after) != string(before) {
		t.Errorf("explicit request changed the implicit plan log: %v", err)
	}
}

func testSignPlanRefused(t *testing.T) {
	for _, name := range []string{"empty", "foreign host", "missing", "unreadable", "other command"} {
		t.Run(name, func(t *testing.T) {
			c := setup(t)
			selected, err := c.New(plans.NewOptions{Title: "Selected", Slug: "selected", Sessions: []string{"codex:handoff"}}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(c.Dir)
			target, want, wantCode := selected.Plan, "", 2
			logs := []string{selected.Log}
			switch name {
			case "empty":
				target, want = "", "omit --plan"
			case "foreign host":
				foreign := c
				foreign.Host = "away"
				made, err := foreign.New(plans.NewOptions{Title: "Foreign", Slug: "foreign"}, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				target, want, logs = made.Plan, "only agent-plan on away", append(logs, made.Log)
			case "missing":
				target = filepath.Join(c.Dir, "missing.box.org")
				want = target
			case "unreadable":
				if os.Geteuid() == 0 {
					t.Skip("run as an unprivileged user to exercise a real file-permission refusal")
				}
				if err := os.Chmod(selected.File, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := os.Chmod(selected.File, 0o644); err != nil {
						t.Error(err)
					}
				})
				want, wantCode = selected.File, 1
			case "other command":
				want = "--plan is sign-id's"
			}
			before := map[string]string{}
			for _, file := range logs {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				before[file] = string(data)
			}
			args := []string{"sign-id", "--session", "codex:handoff", "--plan", target}
			if name == "other command" {
				args = []string{"status", selected.Plan, "--plan", target}
			}
			code, out, errs := agentPlan(t, "", args...)
			if code != wantCode || out != "" || !strings.Contains(errs, want) {
				t.Errorf("invalid selector: %d %q %q; want code %d and %q", code, out, strings.SplitN(errs, "\n", 2)[0], wantCode, want)
			}
			for file, data := range before {
				after, err := os.ReadFile(file)
				if err != nil || string(after) != data {
					t.Errorf("refusal changed %s: %v", file, err)
				}
			}
		})
	}
}

// A sign-pass handed over in a session is minted an ID there; its result
// is read once through sign-result or the next prompt's remind, whichever
// comes first, even with prompts in separate processes at once.
func TestSign(t *testing.T) {
	plan := sessionPlan(t)
	plansOf := func(t *testing.T) plans.Plan {
		t.Helper()
		c, _ := plans.Load()
		p, err := c.Open(plan)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	code, out, errs := agentPlan(t, "", "sign-id", "--branch", "loop", "--branch", "flag")
	id := strings.TrimSpace(out)
	if code != 0 || id == "" || strings.ContainsAny(id, " \n") {
		t.Fatalf("sign-id: %d %q %q", code, out, errs)
	}
	if code, out, _ := agentPlan(t, "", "sign-result", id, "--check"); code != 1 || out != "" {
		t.Errorf("--check before the pass: %d %q", code, out)
	}
	// The branch is the given worktree's, not the one sign-id runs in.
	wt, _ := filepath.EvalSymlinks(t.TempDir())
	if out, err := exec.Command("git", "-C", wt, "init", "-q", "-b", "run-flag").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if code, _, errs := agentPlan(t, "", "sign-id", "--worktree", wt, "--base", "diagrams"); code != 0 {
		t.Errorf("sign-id --worktree --base: %d %s", code, errs)
	}
	events, _ := plansOf(t).Events()
	if last := events[len(events)-1]; last.Worktree != wt || last.Branch != "run-flag" || last.Base != "diagrams" {
		t.Errorf("sign-requested %+v", last)
	}
	if code, _, _ := agentPlan(t, "", "log", t.TempDir(), "--base", "x"); code != 2 {
		t.Errorf("--base on log: %d", code)
	}
	if code, _, errs := agentPlan(t, "", "sign-result", id); code != 1 || !strings.Contains(errs, "no result") {
		t.Errorf("sign-result before the pass: %d %q", code, errs)
	}
	c, _ := plans.Load()
	if _, err := c.RecordSign(id, plans.SignResult{Exit: 0, Summary: "2 signed; pushed loop"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := agentPlan(t, "", "sign-result", id, "--check"); code != 0 || out != "" {
		t.Errorf("--check after the pass: %d %q", code, out)
	}

	// Prompts in separate processes at once: one of them delivers it.
	prompt := `{"session_id":"s-1","hook_event_name":"UserPromptSubmit","prompt":"Carry on"}`
	var wg sync.WaitGroup
	outs := make([]string, 8)
	for i := range outs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "remind")
			cmd.Env = append(os.Environ(), "AGENT_PLAN_TEST_MAIN=1")
			cmd.Stdin = strings.NewReader(prompt)
			data, err := cmd.Output()
			if err != nil {
				t.Errorf("remind: %v", err)
			}
			outs[i] = string(data)
		}()
	}
	wg.Wait()
	delivered := 0
	want := "sign-pass " + id + " signed: 2 signed; pushed loop"
	// Every prompt also carries the time, after any result.
	for _, out := range outs {
		var got struct {
			Specific struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if json.Unmarshal([]byte(out), &got) != nil || got.Specific.Event != "UserPromptSubmit" ||
			!strings.HasPrefix(got.Specific.Context, "It is ") && !strings.HasPrefix(got.Specific.Context, want+"\n\nIt is ") {
			t.Errorf("remind printed %q", out)
		}
		if strings.Contains(got.Specific.Context, "sign-pass") {
			delivered++
		}
	}
	if delivered != 1 {
		t.Errorf("delivered %d times: %q", delivered, outs)
	}

	// sign-result still prints it, saying the session has it.
	code, out, errs = agentPlan(t, "", "sign-result", id)
	if code != 0 || !strings.Contains(out, want) || !strings.Contains(out, "already delivered") {
		t.Errorf("sign-result after delivery: %d %q %q", code, out, errs)
	}
}

// sign-result --wait blocks until the pass's result is logged, and gives
// up at --timeout, so a session's waiter is one command of its own.
func TestSignResultWait(t *testing.T) {
	sessionPlan(t)
	_, out, _ := agentPlan(t, "", "sign-id")
	id := strings.TrimSpace(out)
	start := time.Now()
	code, out, errs := agentPlan(t, "", "sign-result", id, "--wait", "--timeout", "300ms")
	if took := time.Since(start); code != 1 || out != "" || !strings.Contains(errs, "no result") || took < 300*time.Millisecond {
		t.Errorf("--wait with no result: %d %q %q after %v", code, out, errs, took)
	}
	type result struct {
		code      int
		out, errs string
	}
	done := make(chan result, 1)
	go func() {
		code, out, errs := agentPlan(t, "", "sign-result", id, "--wait")
		done <- result{code, out, errs}
	}()
	c, _ := plans.Load()
	if _, err := c.RecordSign(id, plans.SignResult{Exit: 0, Summary: "1 signed"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.code != 0 || strings.TrimSpace(r.out) != "sign-pass "+id+" signed: 1 signed" {
			t.Errorf("--wait once the result is logged: %d %q %q", r.code, r.out, r.errs)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("--wait did not return once the result was logged")
	}
}

// sign-result consumes what remind would otherwise deliver.
func TestSignResultFirst(t *testing.T) {
	sessionPlan(t)
	_, out, _ := agentPlan(t, "", "sign-id")
	id := strings.TrimSpace(out)
	c, _ := plans.Load()
	c.RecordSign(id, plans.SignResult{Exit: 2, Summary: "this worktree has uncommitted changes"}, time.Now())
	code, out, errs := agentPlan(t, "", "sign-result", id)
	if code != 0 || strings.TrimSpace(out) != "sign-pass "+id+" refused: this worktree has uncommitted changes" {
		t.Errorf("sign-result: %d %q %q", code, out, errs)
	}
	if code, out, _ := agentPlan(t, `{"session_id":"s-1","hook_event_name":"UserPromptSubmit","prompt":"go"}`, "remind"); code != 0 || strings.Contains(out, "sign-pass") {
		t.Errorf("remind after sign-result: %d %q", code, out)
	}
}

// Without a plan, or a session, there is nowhere to deliver the result.
func TestSignIDRefusals(t *testing.T) {
	setup(t)
	t.Setenv("CODEX_THREAD_ID", "")
	if code, _, errs := agentPlan(t, "", "sign-id"); code != 2 || !strings.Contains(errs, "--session") {
		t.Errorf("no session: %d %q", code, errs)
	}
	t.Setenv("CODEX_THREAD_ID", "t-1")
	if code, _, errs := agentPlan(t, "", "sign-id"); code != 2 || !strings.Contains(errs, "agent-plan new") {
		t.Errorf("no plan: %d %q", code, errs)
	}
	if code, _, _ := agentPlan(t, "", "sign-result", "not an id"); code != 2 {
		t.Errorf("a malformed ID: %d", code)
	}
}
