package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

// fakeGh answers `gh api graphql` with one pull request of the operator's,
// or fails where $FAKE_GH_FAIL is set.
const fakeGh = `#!/bin/sh
if [ -n "$FAKE_GH_FAIL" ]; then echo "fake gh: down" >&2; exit 1; fi
echo '{"data": {"viewer": {"login": "alice"},
  "mine": {"issueCount": 1, "nodes": [{"number": 7, "isDraft": true, "repository": {"nameWithOwner": "o/r"},
    "commits": {"nodes": []}, "reviewThreads": {"nodes": []}}]},
  "requested": {"issueCount": 0, "nodes": []}}}'
`

func setUp(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GH_FAIL", "")
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"plans": {"dir": "`+filepath.Join(t.TempDir(), "agents")+`", "host": "box"}}`), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	t.Setenv("PR_LOOP_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	return state
}

func TestCollect(t *testing.T) {
	state := setUp(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"collect"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	file := filepath.Join(state, "plan-dash", "status.org")
	var got struct {
		File         string `json:"file"`
		Stale        bool   `json:"stale"`
		PullRequests int    `json:"pullRequests"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.File != file || got.Stale || got.PullRequests != 1 {
		t.Errorf("printed %s (%v)", stdout.String(), err)
	}
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), "[[https://github.com/o/r/pull/7][o/r#7]] | draft") {
		t.Errorf("status.org: %s (%v)", data, err)
	}

	// Where the fetch fails with nothing cached beside the file, the file is
	// still written, without GitHub's part, and the command fails.
	out := filepath.Join(t.TempDir(), "status.org")
	t.Setenv("FAKE_GH_FAIL", "1")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"collect", "--out", out}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "fake gh: down") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
	if data, err := os.ReadFile(out); err != nil || !strings.Contains(string(data), "GitHub: no data, the fetch failed.") {
		t.Errorf("status.org: %s (%v)", data, err)
	}
}

// export writes the page's documents under the state directory, sharing
// collect's GitHub cache, and nowhere else.
func TestExport(t *testing.T) {
	state := setUp(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"export"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := filepath.Join(state, "agent-plans", "out")
	var got struct {
		Dir          string `json:"dir"`
		GitHub       bool   `json:"github"`
		PullRequests int    `json:"pullRequests"`
		Plans        int    `json:"plans"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Dir != out || !got.GitHub || got.PullRequests != 1 || got.Plans != 0 {
		t.Errorf("printed %s (%v)", stdout.String(), err)
	}
	data, err := os.ReadFile(filepath.Join(out, "github", "status.json"))
	if err != nil || !strings.Contains(string(data), `"repo": "o/r"`) {
		t.Errorf("github/status.json: %s (%v)", data, err)
	}
	if _, err := os.Stat(filepath.Join(out, "meta", "sync.json")); err != nil {
		t.Error(err)
	}
	entries, _ := os.ReadDir(state)
	for _, e := range entries {
		if e.Name() != "agent-plans" && e.Name() != "plan-dash" {
			t.Errorf("wrote %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(state, "plan-dash", "github.json")); err != nil {
		t.Errorf("the cache collect reads: %v", err)
	}

	// Where the fetch fails with nothing cached, the rest is still written,
	// and the command fails.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("FAKE_GH_FAIL", "1")
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"export"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "fake gh: down") {
		t.Errorf("exit %d: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_STATE_HOME"), "agent-plans", "out", "meta", "sync.json")); err != nil {
		t.Error(err)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"serve"}, {"collect", "--bogus"}, {"export", "now"}, {"export", "--out", "x"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "collect") {
			t.Errorf("%q: exit %d: %s", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "collect") {
		t.Errorf("--help: exit %d: %s", code, stdout.String())
	}
}
