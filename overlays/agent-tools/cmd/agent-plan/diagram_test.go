package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

const statesGo = "package queue\n\nconst (\n\tQueued = \"queued\"\n\tReady  = \"ready\"\n)\n"

// diagramRepo makes a repository holding statesGo, the process's
// directory, and returns it and its commit.
func diagramRepo(t *testing.T) (string, string) {
	t.Helper()
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "T",
		"GIT_COMMITTER_EMAIL": "t@example.com"} {
		t.Setenv(k, v)
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	gitRun(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "queue.go"), []byte(statesGo), 0o644)
	gitRun(t, dir, "add", "queue.go")
	gitRun(t, dir, "commit", "-q", "-m", "Add the states")
	t.Chdir(dir)
	return dir, gitRun(t, dir, "rev-parse", "HEAD")
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// diagramFile writes a diagram of statesGo at commit at, its second node
// citing line, and returns its path.
func diagramFile(t *testing.T, at string, line int) string {
	t.Helper()
	data, _ := json.Marshal(map[string]any{"kind": "state", "title": "Entry states", "at": at,
		"nodes": []any{
			map[string]any{"id": "N1", "label": "Queued", "cite": map[string]any{"path": "queue.go", "line": 4, "endLine": nil, "symbol": "Queued"}, "planned": nil},
			map[string]any{"id": "N2", "label": "Ready", "cite": map[string]any{"path": "queue.go", "line": line, "endLine": nil, "symbol": "Ready"}, "planned": nil},
		},
		"edges": []any{}})
	path := filepath.Join(t.TempDir(), "diagram.json")
	os.WriteFile(path, data, 0o644)
	return path
}

func TestDiagramCommands(t *testing.T) {
	c := setup(t)
	dir, at := diagramRepo(t)
	plan := newPlan(t)

	code, out, errs := agentPlan(t, "", "diagram", "schema")
	var schema map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &schema) != nil || schema["properties"] == nil {
		t.Fatalf("schema: %d %s %s", code, out, errs)
	}

	code, out, errs = agentPlan(t, "", "diagram", "check", plan, diagramFile(t, at, 4))
	if code != 3 || !strings.Contains(out, `"element": "N2"`) {
		t.Errorf("a citation to the wrong span: %d %s %s", code, out, errs)
	}
	file := diagramFile(t, at, 5)
	// Who drew it is the harness its environment marks; with none marked,
	// the check refuses to record one.
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SANDBOX", "")
	if code, _, errs := agentPlan(t, "", "diagram", "check", plan, file); code != 2 {
		t.Errorf("a check with no harness marked: %d %s", code, errs)
	}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-9")
	code, out, errs = agentPlan(t, "", "diagram", "check", plan, file)
	var checked struct {
		Hash    string `json:"hash"`
		Diagram struct {
			Nodes []struct {
				Cite struct {
					Hash string `json:"hash"`
				} `json:"cite"`
			} `json:"nodes"`
		} `json:"diagram"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &checked) != nil || len(checked.Hash) != 64 ||
		len(checked.Diagram.Nodes[1].Cite.Hash) != 64 {
		t.Fatalf("check: %d %s %s", code, out, errs)
	}

	code, _, errs = agentPlan(t, "", "diagram", "keep", plan, file)
	if code != 2 || !strings.Contains(errs, "git fresh-review diagram") {
		t.Errorf("kept without a critique: %d %s", code, errs)
	}
	p, err := c.Open(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Record("", plans.Event{Kind: "diagram-critiqued", Diagram: checked.Hash, Critic: "codex"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	code, out, errs = agentPlan(t, "", "diagram", "keep", plan, file)
	if code != 0 || !strings.Contains(out, `"id": "G1"`) {
		t.Fatalf("keep: %d %s %s", code, out, errs)
	}
	var drafted []plans.Event
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "diagram-drafted" {
			drafted = append(drafted, e)
		}
	}
	if len(drafted) != 1 || drafted[0].Drafter != "claude" || drafted[0].Diagram != checked.Hash || drafted[0].Session != "s-9" {
		t.Errorf("drafted %+v", drafted)
	}
	if data, _ := os.ReadFile(p.File()); !strings.Contains(string(data), ":REPO: "+gitRun(t, dir, "rev-list", "--max-parents=0", "HEAD")) {
		t.Errorf("no REPO:\n%s", data)
	}

	code, out, errs = agentPlan(t, "", "diagram", "check", plan)
	if code != 0 || !strings.Contains(out, `"status": "checked"`) {
		t.Errorf("check the plan: %d %s %s", code, out, errs)
	}
	os.WriteFile(filepath.Join(dir, "queue.go"), []byte(strings.Replace(statesGo, `"ready"`, `"done"`, 1)), 0o644)
	gitRun(t, dir, "commit", "-q", "-am", "Rename a state")
	code, out, errs = agentPlan(t, "", "diagram", "check", plan)
	if code != 3 || !strings.Contains(out, `"status": "stale"`) || !strings.Contains(out, `"N2"`) {
		t.Errorf("check the plan after a change: %d %s %s", code, out, errs)
	}
	if code, _, _ := agentPlan(t, "", "diagram", "keep", plan); code != 2 {
		t.Errorf("keep without a file: %d", code)
	}
}
