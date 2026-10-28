package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

func TestLifecycle(t *testing.T) {
	for _, source := range []string{"startup", "resume", "clear", "fork"} {
		t.Run("Codex "+source, func(t *testing.T) {
			c := setup(t)
			worktree, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if out, err := exec.CommandContext(t.Context(), "git", "init", "--quiet", worktree).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v %s", err, out)
			}
			other, err := c.New(plans.NewOptions{Title: "Claude work", Slug: "claude-work",
				Sessions: []string{"claude:same-id"}, Harness: "claude", Worktrees: []string{worktree}}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			made, err := c.New(plans.NewOptions{Title: "Codex work", Slug: "codex-work",
				Goal:     "Keep this Codex goal. " + strings.Repeat("bounded context ", 2000),
				Sessions: []string{"codex:same-id"}, Harness: "codex", Worktrees: []string{worktree}}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(made.File)
			if err != nil {
				t.Fatal(err)
			}
			logBefore, err := os.ReadFile(made.Log)
			if err != nil {
				t.Fatal(err)
			}
			input := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"same-id","source":%q,"cwd":%q}`, source, worktree)
			var previous string
			for i := 0; i < 2; i++ {
				code, out, errs := agentPlan(t, input, "find", "--hook", "--harness", "codex")
				var payload struct {
					Specific struct {
						Event   string `json:"hookEventName"`
						Context string `json:"additionalContext"`
					} `json:"hookSpecificOutput"`
				}
				if code != 0 || json.Unmarshal([]byte(out), &payload) != nil || payload.Specific.Event != "SessionStart" {
					t.Fatalf("find --hook: code %d, output %q, stderr %s", code, out, errs)
				}
				context := payload.Specific.Context
				if !strings.Contains(context, made.Plan) || !strings.Contains(context, "Keep this Codex goal.") || strings.Contains(context, other.Plan) {
					t.Errorf("Codex context selected the wrong plan: %s", context)
				}
				if len(context) > 10000 {
					t.Errorf("context exceeds its byte budget: %d", len(context))
				}
				if i > 0 && out != previous {
					t.Error("repeated lifecycle event changed its output")
				}
				previous = out
			}
			after, err := os.ReadFile(made.File)
			if err != nil || string(after) != string(before) {
				t.Errorf("lifecycle lookup changed the plan: %v", err)
			}
			logAfter, err := os.ReadFile(made.Log)
			if err != nil || string(logAfter) != string(logBefore) {
				t.Errorf("lifecycle lookup changed the log: %v", err)
			}
		})
	}

	t.Run("Codex clear leaves Claude handover for Claude", func(t *testing.T) {
		c := setup(t)
		made, err := c.New(plans.NewOptions{Title: "Claude work", Slug: "claude-work",
			Sessions: []string{"claude:before"}, Harness: "claude"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.New(plans.NewOptions{Title: "Codex work", Slug: "codex-work",
			Sessions: []string{"codex:before"}, Harness: "codex"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		end := `{"hook_event_name":"SessionEnd","session_id":"before","reason":"clear","cwd":"/nowhere"}`
		if code, _, errs := agentPlan(t, end, "find", "--hook"); code != 0 {
			t.Fatalf("Claude session end: %d %s", code, errs)
		}
		clear := `{"hook_event_name":"SessionStart","session_id":"after","source":"clear","cwd":"/nowhere"}`
		if code, out, errs := agentPlan(t, clear, "find", "--hook", "--harness", "codex"); code != 0 || out != "" {
			t.Errorf("Codex consumed Claude handover: %d %s %s", code, out, errs)
		}
		if code, out, errs := agentPlan(t, clear, "find", "--hook"); code != 0 || !strings.Contains(out, made.Plan) {
			t.Errorf("Claude handover was lost: %d %s %s", code, out, errs)
		}
		data, err := os.ReadFile(made.File)
		if err != nil || !strings.Contains(string(data), ":SESSIONS: claude:before claude:after\n") {
			t.Errorf("Claude session handover: %s, %v", data, err)
		}
	})

	t.Run("ambiguous Codex workspace does not choose a plan", func(t *testing.T) {
		c := setup(t)
		worktree, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if out, err := exec.CommandContext(t.Context(), "git", "init", "--quiet", worktree).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		var files []string
		before := map[string]string{}
		for _, slug := range []string{"first", "second"} {
			made, err := c.New(plans.NewOptions{Title: slug, Slug: slug, Worktrees: []string{worktree},
				Goal: "Do not select this goal arbitrarily."}, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, made.File)
			for _, path := range []string{made.File, made.Log} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = string(data)
			}
		}
		input := fmt.Sprintf(`{"hook_event_name":"SessionStart","session_id":"new","source":"clear","cwd":%q}`, worktree)
		code, out, errs := agentPlan(t, input, "find", "--hook", "--harness", "codex")
		if code != 0 || !strings.Contains(out, files[0]) || !strings.Contains(out, files[1]) || strings.Contains(out, "Do not select this goal") {
			t.Errorf("ambiguous lookup must list candidates without selecting one: %d %s %s", code, out, errs)
		}
		for path, want := range before {
			data, err := os.ReadFile(path)
			if err != nil || string(data) != want {
				t.Errorf("ambiguous lookup changed %s: %v", path, err)
			}
		}
	})

	for _, input := range []string{`null`, `{}`, `{"hook_event_name":"Stop","session_id":"x","source":"resume"}`, `{"hook_event_name":"SessionEnd","session_id":"x","reason":"clear"}`, `{"hook_event_name":"SessionStart","session_id":"x","source":"unknown"}`} {
		t.Run("malformed "+input, func(t *testing.T) {
			setup(t)
			if code, out, errs := agentPlan(t, input, "find", "--hook", "--harness", "codex"); code == 0 || out != "" || errs == "" {
				t.Errorf("malformed event accepted: %d %q %q", code, out, errs)
			}
		})
	}

	t.Run("invalid hook harness", func(t *testing.T) {
		setup(t)
		input := `{"hook_event_name":"SessionStart","session_id":"x","source":"resume"}`
		if code, out, errs := agentPlan(t, input, "find", "--hook", "--harness", "other"); code == 0 || out != "" || errs == "" {
			t.Errorf("unsupported harness accepted: %d %q %q", code, out, errs)
		}
	})
}
