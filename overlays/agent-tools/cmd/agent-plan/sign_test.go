package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// A sign-pass handed over in a session is minted an ID there; its result
// is read once through sign-result or the next prompt's remind, whichever
// comes first, even with prompts in separate processes at once.
func TestSign(t *testing.T) {
	sessionPlan(t)
	code, out, errs := agentPlan(t, "", "sign-id", "--branch", "loop", "--branch", "flag")
	id := strings.TrimSpace(out)
	if code != 0 || id == "" || strings.ContainsAny(id, " \n") {
		t.Fatalf("sign-id: %d %q %q", code, out, errs)
	}
	if code, out, _ := agentPlan(t, "", "sign-result", id, "--check"); code != 1 || out != "" {
		t.Errorf("--check before the pass: %d %q", code, out)
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
	for _, out := range outs {
		if out == "" {
			continue
		}
		delivered++
		var got struct {
			Specific struct {
				Event   string `json:"hookEventName"`
				Context string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if json.Unmarshal([]byte(out), &got) != nil || got.Specific.Event != "UserPromptSubmit" || got.Specific.Context != want {
			t.Errorf("remind printed %q", out)
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
	if code, out, _ := agentPlan(t, `{"session_id":"s-1","hook_event_name":"UserPromptSubmit","prompt":"go"}`, "remind"); code != 0 || out != "" {
		t.Errorf("remind after sign-result: %d %q", code, out)
	}
}

// Without a plan, or a session, there is nowhere to deliver the result.
func TestSignIDRefusals(t *testing.T) {
	setup(t)
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("ECA_CHAT_ID", "")
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
