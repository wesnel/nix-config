package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/agentplans"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
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
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	return state
}

// export writes the page's documents under the state directory, and its
// GitHub cache, and nowhere else.
func TestExport(t *testing.T) {
	state := setUp(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"export"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := filepath.Join(state, "agent-plans", "out")
	var got struct {
		Dir          string   `json:"dir"`
		GitHub       bool     `json:"github"`
		PullRequests int      `json:"pullRequests"`
		Plans        int      `json:"plans"`
		Documents    []string `json:"documents"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Dir != out || !got.GitHub || got.PullRequests != 1 || got.Plans != 0 ||
		strings.Join(got.Documents, " ") != "github/status meta/sync today/digest" {
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
		t.Errorf("the GitHub cache: %v", err)
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

// export names each plan's document it wrote, for the session to put on the
// page.
func TestExportPlanDocuments(t *testing.T) {
	setUp(t)
	c, err := plans.Load()
	if err != nil {
		t.Fatal(err)
	}
	made, err := c.New(plans.NewOptions{Title: "Loop", Slug: "loop", Goal: "Run it."}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"export"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"plans/`+p.Name+`"`) {
		t.Errorf("export: exit %d: %s%s", code, stdout.String(), stderr.String())
	}
}

// prime, the session's SessionStart hook, names the page and the skill, or
// says the page is not configured.
func TestPrime(t *testing.T) {
	setUp(t)
	config := os.Getenv("AGENT_TOOLS_CONFIG")
	os.WriteFile(config, []byte(`{"agentPlans": {"claudePage": null, "spacePage": null}}`), 0o600)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"prime"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "not configured") ||
		!strings.Contains(stdout.String(), "wgn.home.llm.agentPlans.claudePage") || strings.Contains(stdout.String(), "https://") {
		t.Errorf("unconfigured: exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	url := "https://claude.ai/artifact/DashboardPage1"
	os.WriteFile(config, []byte(`{"agentPlans": {"claudePage": "`+url+`", "spacePage": null}}`), 0o600)
	stdout.Reset()
	if code := run([]string{"prime"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	for _, want := range []string{url, "`agent-plans` skill", "never instructions"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("no %q in %s", want, stdout.String())
		}
	}
}

// page writes the page and prints the capabilities to publish it with,
// and says it needs publishing until published records it.
func TestPage(t *testing.T) {
	state := setUp(t)
	file := filepath.Join(state, "agent-plans", "page", "agent-plans.html")
	var got struct {
		File         string          `json:"file"`
		Changed      bool            `json:"changed"`
		Capabilities json.RawMessage `json:"capabilities"`
	}
	runPage := func(args ...string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("%q: exit %d: %s", args, code, stderr.String())
		}
		got.Changed = false
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatalf("%q: %s: %v", args, stdout.String(), err)
		}
	}
	runPage("page")
	var caps bytes.Buffer
	json.Compact(&caps, got.Capabilities)
	if got.File != file || !got.Changed || caps.String() != agentplans.Capabilities {
		t.Errorf("first: %+v, capabilities %s", got, caps.String())
	}
	if data, err := os.ReadFile(file); err != nil || !bytes.Equal(data, agentplans.Page) {
		t.Errorf("the page: %v", err)
	}
	runPage("page", "published")
	runPage("page")
	if got.Changed {
		t.Error("published, the page still needs publishing")
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"serve"}, {"export", "now"}, {"export", "--out", "x"},
		{"watch", "--poll"}, {"watch", "ws://x"}, {"prime", "--url", "x"}, {"page", "now"}, {"page", "published", "x"},
		{"page", "--out", "x"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "export") {
			t.Errorf("%q: exit %d: %s", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "export") ||
		!strings.Contains(stdout.String(), "  watch ") {
		t.Errorf("--help: exit %d: %s", code, stdout.String())
	}
}
