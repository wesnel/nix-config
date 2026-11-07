package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

func TestMCPPlan(t *testing.T) {
	bin := t.TempDir()
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin+string(filepath.Separator), ".", "../agent-plan")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real MCP and plan commands: %v\n%s", err, out)
	}
	f := newMCPFixture(t)
	profile := t.TempDir()
	if err := os.Symlink(filepath.Join(bin, "pr-loop"), filepath.Join(profile, "pr-loop")); err != nil {
		t.Fatal(err)
	}
	// A different sibling models a profile whose packages come from different installations.
	if err := os.WriteFile(filepath.Join(profile, "agent-plan"), []byte("#!/bin/sh\necho wrong-sibling\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), filepath.Join(profile, "pr-loop"), "mcp")
	cmd.Env, cmd.Dir, cmd.Stderr = f.env, f.repo, os.Stderr
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
	t.Cleanup(func() { in.Close(); cmd.Wait() })
	c := &mcpClient{in: in, out: bufio.NewReader(out)}
	call := func(t *testing.T, name string, args map[string]any) map[string]any {
		t.Helper()
		args["worktree"], args["plan"] = f.repo, f.plan
		r := c.tool(t, "agent_plan_"+name, args)
		if r.isError || !strings.Contains(r.text, "exit 0") {
			t.Fatalf("%s: %s", name, r.text)
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(r.out), &result); err != nil {
			t.Fatalf("%s output: %s", name, r.text)
		}
		return result
	}
	readPlan := func(t *testing.T, command string, args ...string) string {
		t.Helper()
		read := exec.CommandContext(t.Context(), filepath.Join(bin, "agent-plan"), append([]string{command, f.plan}, args...)...)
		read.Env, read.Dir = f.env, f.repo
		got, err := read.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %s: %v", command, got, err)
		}
		return strings.TrimSpace(string(got))
	}
	t.Run("section round trip", func(t *testing.T) {
		goal := "- Keep literal prose.\nA second line with `code` and $(literal).\nBuilds on: the section tool.\nLikely next changes: none."
		r := c.tool(t, "agent_plan_section", map[string]any{"worktree": f.repo, "plan": f.plan, "section": "Goal", "text": goal})
		if r.isError || !strings.Contains(r.text, "exit 0") {
			t.Fatalf("section: %s", r.text)
		}
		read := exec.CommandContext(t.Context(), filepath.Join(bin, "agent-plan"), "goal", f.plan)
		read.Env, read.Dir = f.env, f.repo
		got, err := read.CombinedOutput()
		if err != nil || strings.TrimSpace(string(got)) != goal {
			t.Fatalf("goal = %q, err %v", got, err)
		}
	})
	t.Run("visual round trip", func(t *testing.T) {
		const url = "https://github.com/user-attachments/assets/1a2b"
		v := call(t, "visual", map[string]any{"branch": "loop", "url": url, "caption": "The plan page"})
		call(t, "visual", map[string]any{"entry": v["id"], "url": url + "c"})
		data, err := os.ReadFile(f.plan)
		if err != nil || !strings.Contains(string(data), "*** The plan page\n:PROPERTIES:\n:CUSTOM_ID: "+v["id"].(string)+
			"\n:BRANCH: loop\n:URL: "+url+"c\n:END:\n") {
			t.Fatalf("visual: %v\n%s", err, data)
		}
	})
	t.Run("digest and brag", func(t *testing.T) {
		notes := filepath.Dir(filepath.Dir(f.plan))
		digest := "* Grow the code\n:PROPERTIES:\n:PLAN: id:abc-1\n:PR: o/r#7\n:END:\nRun `go test` and $(see).\n"
		r := c.tool(t, "agent_plan_digest", map[string]any{"worktree": f.repo, "date": "2026-11-06", "text": digest})
		if r.isError || !strings.Contains(r.text, "exit 0") {
			t.Fatalf("digest: %s", r.text)
		}
		if data, err := os.ReadFile(filepath.Join(notes, "digests", "2026", "11-06-demo.org")); err != nil || string(data) != digest {
			t.Errorf("digest: %v\n%s", err, data)
		}
		call(t, "brag", map[string]any{"pr": "o/r#7", "date": "2026-11-05", "title": "Grew the code", "text": "It grew."})
		if data, err := os.ReadFile(filepath.Join(notes, "brag", "2026.org")); err != nil ||
			!strings.Contains(string(data), "* Grew the code\n:PROPERTIES:\n:PR: https://github.com/o/r/pull/7\n") {
			t.Errorf("brag: %v\n%s", err, data)
		}
	})
	t.Run("empty section", func(t *testing.T) {
		call(t, "section", map[string]any{"section": "Verification", "text": "Temporary verification text."})
		before, err := os.ReadFile(f.plan)
		if err != nil || !strings.Contains(string(before), "Temporary verification text.") {
			t.Fatalf("verification was not set: %v", err)
		}
		call(t, "section", map[string]any{"section": "Verification", "text": ""})
		data, err := os.ReadFile(f.plan)
		if err != nil || !strings.Contains(string(data), "** Verification\n") || strings.Contains(string(data), "Temporary verification text.") {
			t.Fatalf("empty verification: %v\n%s", err, data)
		}
	})
	t.Run("workflow", func(t *testing.T) {
		step := call(t, "step", map[string]any{"title": "Verify behavior", "text": "Read the state back."})
		id := step["id"].(string)
		call(t, "edit", map[string]any{"item": id, "title": "Verify the round trip"})
		call(t, "log", map[string]any{"item": id, "kind": "started"})
		call(t, "log", map[string]any{"item": id, "kind": "done", "revision": "HEAD", "evidence": "Observed state."})
		if got := readPlan(t, "commits"); !strings.Contains(got, "Grow the code") || !strings.Contains(got, `"revision"`) {
			t.Fatalf("commit association: %s", got)
		}
		marker := filepath.Join(f.repo, "must-not-exist")
		call(t, "log", map[string]any{"item": id, "kind": "handed-back", "command": "touch " + marker, "dir": f.repo})
		if got := readPlan(t, "waiting", "--json"); !strings.Contains(got, "touch "+marker) {
			t.Fatalf("stored hand-back: %s", got)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("hand-back command executed: %v", err)
		}
		call(t, "set", map[string]any{"property": "STATUS", "values": []string{"working"}})
		q := call(t, "ask", map[string]any{"question": "Keep the behavior?", "recommendation": "yes", "context": "The CLI validates question context."})
		call(t, "decide", map[string]any{"question": q["id"], "answer": "yes"})
		if got := readPlan(t, "decisions", "--json"); !strings.Contains(got, "Keep the behavior?") || !strings.Contains(got, "yes") {
			t.Fatalf("decisions: %s", got)
		}
		status := readPlan(t, "status", "--format=json")
		if !strings.Contains(status, "Verify the round trip") || !strings.Contains(status, "working") {
			t.Fatalf("status: %s", status)
		}
		call(t, "after", map[string]any{"item": id, "clear": true})
		call(t, "postpone", map[string]any{"item": id, "until": "2099-01-01 12:00"})
		call(t, "postpone", map[string]any{"item": id, "clear": true})
		call(t, "reference", map[string]any{"url": "https://example.test/spec", "note": "The contract."})
		call(t, "followup", map[string]any{"title": "Later work", "found_in": id, "context": "Separate scope."})
		call(t, "set", map[string]any{"property": "PULL_REQUESTS", "values": []string{"https://github.com/o/r/pull/1"}})
		call(t, "unfile", map[string]any{"property": "PULL_REQUESTS", "value": "https://github.com/o/r/pull/1", "reason": "Belongs to another plan."})
		call(t, "part", map[string]any{"title": "A part", "target_worktree": f.repo})
	})
	t.Run("refusals leave plan unchanged", func(t *testing.T) {
		before, err := os.ReadFile(f.plan)
		if err != nil {
			t.Fatal(err)
		}
		logFile := strings.TrimSuffix(f.plan, ".org") + ".log.org"
		logBefore, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			args map[string]any
			says string
		}{
			{"sync", nil, "no tool"}, {"reseal", nil, "no tool"}, {"init", nil, "no tool"},
			{"own", map[string]any{"target_worktree": f.repo, "agent": "worker-1"}, "no tool"},
			{"set", map[string]any{"property": "OWNERS", "values": []string{f.repo + "=worker-1"}}, "takes one of"},
			{"unfile", map[string]any{"property": "OWNERS", "value": f.repo + "=main", "reason": "Reassign"}, "takes one of"},
			{"section", map[string]any{"section": "Goal", "text": "x", "text_file": f.plan}, "text_file"},
			{"section", map[string]any{"section": "Goal", "text": "x", "argv": []string{"sync"}}, "argv"},
			{"section", map[string]any{"section": "Goal", "text": "x", "plan": "--help"}, "starts with"},
			{"section", map[string]any{"section": "Questions", "text": "x"}, "takes one of"},
			{"section", map[string]any{"section": "Goal", "text": "* Injected"}, "heading"},
			{"set", map[string]any{"property": "SESSIONS", "values": []string{"--help"}}, "starts with"},
			{"log", map[string]any{"item": "S1", "kind": "committed"}, "a hook"},
			{"log", map[string]any{"item": "S1", "kind": "property", "summary": "OWNERS changed"}, "agent-plan logs"},
			{"find", map[string]any{"hook": true}, "hook"},
		} {
			t.Run(tc.name+"/"+tc.says, func(t *testing.T) {
				args := map[string]any{"worktree": f.repo, "plan": f.plan}
				if tc.name == "find" {
					delete(args, "plan")
				}
				for k, v := range tc.args {
					args[k] = v
				}
				r := c.tool(t, "agent_plan_"+tc.name, args)
				if !r.isError || !strings.Contains(r.text, tc.says) {
					t.Fatalf("refusal: %s", r.text)
				}
				got, err := os.ReadFile(f.plan)
				if err != nil || !bytes.Equal(before, got) {
					t.Fatalf("refused operation changed plan: %v", err)
				}
				logAfter, err := os.ReadFile(logFile)
				if err != nil || !bytes.Equal(logBefore, logAfter) {
					t.Fatalf("refused operation changed log: %v", err)
				}
			})
		}
	})
	t.Run("plan boundary", func(t *testing.T) {
		foreign, err := (plans.Config{Dir: filepath.Dir(f.plan), Host: "elsewhere"}).New(plans.NewOptions{Title: "Foreign", Slug: "foreign", Goal: "Owned elsewhere."}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.org")
		if err := os.WriteFile(outside, []byte("private fixture content"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(filepath.Dir(f.plan), "outside-link.org")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct{ path, says string }{{foreign.Plan, "only agent-plan on elsewhere"}, {outside, "not a plan under"}, {link, "not a plan under"}} {
			before, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			r := c.tool(t, "agent_plan_section", map[string]any{"worktree": f.repo, "plan": tc.path, "section": "Goal", "text": "Overwrite"})
			if !r.isError || !strings.Contains(r.text, tc.says) || strings.Contains(r.text, "private fixture content") {
				t.Fatalf("boundary: %s", r.text)
			}
			after, err := os.ReadFile(tc.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("target changed: %v", err)
			}
		}
	})
	t.Run("new plan inherits thread", func(t *testing.T) {
		r := c.tool(t, "agent_plan_new", map[string]any{"worktree": f.repo, "title": "New work", "slug": "new-work"})
		var made struct{ Plan string }
		if err := json.Unmarshal([]byte(r.out), &made); err != nil || r.isError {
			t.Fatalf("new: %s", r.text)
		}
		data, err := os.ReadFile(made.Plan)
		if err != nil || !strings.Contains(string(data), "codex:thread-1") || !strings.Contains(string(data), f.repo) {
			t.Fatalf("new plan: %s: %v", data, err)
		}
		call(t, "link", map[string]any{"other": made.Plan, "why": "Related work."})
		for _, name := range []string{"find", "workstream_list"} {
			r := c.tool(t, "agent_plan_"+name, map[string]any{"worktree": f.repo})
			if r.isError || !strings.Contains(r.text, "exit 0") {
				t.Fatalf("%s: %s", name, r.text)
			}
		}
		r = c.tool(t, "agent_plan_workstream_new", map[string]any{"worktree": f.repo, "title": "Tools", "summary": "Agent tools."})
		if r.isError || !strings.Contains(r.text, "workstream-tools") {
			t.Fatalf("workstream: %s", r.text)
		}
		r = c.tool(t, "agent_plan_sign_id", map[string]any{"worktree": f.repo, "plan": f.plan, "branch": []string{"main"}})
		if r.isError || !strings.Contains(r.text, "exit 0") {
			t.Fatalf("sign-id: %s", r.text)
		}
	})
	t.Run("repository program settings are refused", func(t *testing.T) {
		config := func(args ...string) {
			t.Helper()
			cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", f.repo, "config", "--local"}, args...)...)
			cmd.Env = f.env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("fixture config: %v\n%s", err, out)
			}
		}
		config("diff.external", "fixture-program")
		t.Cleanup(func() {
			cmd := exec.Command("git", "-C", f.repo, "config", "--local", "--unset", "diff.external")
			cmd.Env = f.env
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("fixture cleanup: %v\n%s", err, out)
			}
		})
		before, err := os.ReadFile(f.plan)
		if err != nil {
			t.Fatal(err)
		}
		r := c.tool(t, "agent_plan_section", map[string]any{"worktree": f.repo, "plan": f.plan, "section": "Goal", "text": "Must be refused before changing the plan."})
		if !r.isError || !strings.Contains(r.text, "local diff.external") {
			t.Fatalf("unchecked repository: %s", r.text)
		}
		after, err := os.ReadFile(f.plan)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("unchecked repository changed the plan: %v", err)
		}
	})
	t.Run("missing sibling is an error", func(t *testing.T) {
		if err := os.Rename(filepath.Join(bin, "agent-plan"), filepath.Join(bin, "saved-agent-plan")); err != nil {
			t.Fatal(err)
		}
		r := c.tool(t, "agent_plan_status", map[string]any{"worktree": f.repo, "plan": f.plan})
		if !r.isError || !strings.Contains(r.text, "exit 1") || strings.Contains(r.text, "wrong-sibling") {
			t.Fatalf("missing sibling: %s", r.text)
		}
	})
}
