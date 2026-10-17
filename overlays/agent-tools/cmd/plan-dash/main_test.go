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
  "requested": {"issueCount": 1, "nodes": [{"number": 9, "isDraft": false, "repository": {"nameWithOwner": "o/s"},
    "headRefOid": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "commits": {"nodes": []}, "reviewRequests": {"nodes": []},
    "timelineItems": {"nodes": []}}]}}}'
`

// fakeSystemctl logs its arguments, and whether AGENT_PLANS_SESSION
// reached it.
const fakeSystemctl = `#!/bin/sh
echo "$* session=${AGENT_PLANS_SESSION:-}" >> "$FAKE_LOG"
`

func setUp(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte(fakeSystemctl), 0o755)
	t.Setenv("FAKE_LOG", filepath.Join(t.TempDir(), "log"))
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

// dispatch queues only what export listed, from the page's documents under
// the state directory, and starts the queue's unit without the dashboard
// session's mark.
func TestDispatch(t *testing.T) {
	state := setUp(t)
	t.Setenv("AGENT_PLANS_SESSION", "1")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"dispatch"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "plan-dash export") {
		t.Errorf("before an export: exit %d: %s", code, stderr.String())
	}
	if code := run([]string{"export"}, &stdout, &stderr); code != 0 {
		t.Fatalf("export: exit %d: %s", code, stderr.String())
	}
	dir := filepath.Join(state, "agent-plans", "out", "dispatch")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "o~s~9.json"), []byte(`{"state": "requested"}`), 0o600)
	os.WriteFile(filepath.Join(dir, "o~r~7.json"), []byte(`{"state": "requested"}`), 0o600)
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"dispatch"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var got struct {
		Queued  []string `json:"queued"`
		Refused []string `json:"refused"`
		Started bool     `json:"started"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || len(got.Queued) != 1 || got.Queued[0] != "o/s#9" ||
		len(got.Refused) != 1 || got.Refused[0] != "o~r~7" || !got.Started {
		t.Errorf("printed %s (%v)", stdout.String(), err)
	}
	if log, _ := os.ReadFile(os.Getenv("FAKE_LOG")); string(log) != "--user start --no-block agent-plans-queue.service session=\n" {
		t.Errorf("systemctl: %q", log)
	}
	if data, err := os.ReadFile(filepath.Join(state, "agent-plans", "queue.org")); err != nil || !strings.Contains(string(data), "* QUEUED o/s#9") {
		t.Errorf("queue.org: %s (%v)", data, err)
	}
}

// answer records what the page answered to a question export listed, by
// the option's key, as the phone's decision, and writes its state back.
func TestAnswer(t *testing.T) {
	state := setUp(t)
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
	if _, err := p.Ask(plans.AskOptions{Question: "Keep the flag?", Recommendation: "drop it",
		Alternatives: []string{"keep it :: costs a flag"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"answer"}, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "plan-dash export") {
		t.Errorf("before an export: exit %d: %s", code, stderr.String())
	}
	if code := run([]string{"export"}, &stdout, &stderr); code != 0 {
		t.Fatalf("export: exit %d: %s", code, stderr.String())
	}
	dir := filepath.Join(state, "agent-plans", "out", "answers")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, p.Name+"@Q1.json"), []byte(`{"choice": "B", "at": "2026-10-17T09:00:00Z"}`), 0o600)
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"answer"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var got struct {
		Recorded []string `json:"recorded"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || len(got.Recorded) != 1 || got.Recorded[0] != p.Name+"~Q1" {
		t.Errorf("printed %s (%v)", stdout.String(), err)
	}
	stdout.Reset()
	if code := run([]string{"export"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"plans/`+p.Name+`"`) {
		t.Errorf("export: exit %d: %s", code, stdout.String())
	}
	if ds, err := p.Decisions(true); err != nil || len(ds) != 1 || ds[0].Answer != "keep it" {
		t.Errorf("decisions %+v (%v)", ds, err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, p.Name+"@Q1.json")); err != nil || !strings.Contains(string(data), `"state": "recorded"`) {
		t.Errorf("the document: %s (%v)", data, err)
	}
}

// prime, the session's SessionStart hook, names the page and the skill, or
// says the page is not configured.
func TestPrime(t *testing.T) {
	setUp(t)
	config := filepath.Join(t.TempDir(), "agent-plans.json")
	t.Setenv("AGENT_PLANS_CONFIG", config)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"prime"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "not configured") ||
		strings.Contains(stdout.String(), "https://") {
		t.Errorf("unconfigured: exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	url := "https://claude.ai/artifact/DashboardPage1"
	os.WriteFile(config, []byte(`{"url": "`+url+`"}`), 0o600)
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
	// Recorded only after the guard let a publish of it through.
	var stdout, stderr bytes.Buffer
	if code := run([]string{"page", "published"}, &stdout, &stderr); code == 0 {
		t.Error("recorded as published with no publish let through")
	}
	os.WriteFile(agentplans.PublishingFile(), []byte(agentplans.PageHash(agentplans.Page)+"\n"), 0o600)
	runPage("page", "published")
	runPage("page")
	if got.Changed {
		t.Error("published, the page still needs publishing")
	}
}

// queue stops at the kill switch, and is refused to the dashboard session.
func TestQueue(t *testing.T) {
	state := setUp(t)
	dir := filepath.Join(state, "agent-plans")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "queue.org"), []byte("* QUEUED o/s#9\n:PROPERTIES:\n:TRIAGE: t\n:END:\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "off"), nil, 0o600)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"queue"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"off": true`) {
		t.Errorf("exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	t.Setenv("AGENT_PLANS_SESSION", "1")
	stdout.Reset()
	if code := run([]string{"queue"}, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "agent-plans-queue.service") {
		t.Errorf("in the session: exit %d: %s", code, stderr.String())
	}
}

// usage reads the operator's transcripts and the dashboard session's, and
// prints numbers and labels, never what a transcript says.
func TestUsageReport(t *testing.T) {
	state := setUp(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	const secret = "ghp_SyntheticSecret0123456789abcdefABCDEF"
	at := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	line := func(session, req string) string {
		return `{"type":"assistant","sessionId":"` + session + `","requestId":"` + req + `","timestamp":"` + at +
			`","cwd":"/nowhere","entrypoint":"cli","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"` +
			secret + `"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"cache_creation_input_tokens":0,` +
			`"cache_read_input_tokens":100,"output_tokens":10}}}` + "\n"
	}
	for dir, l := range map[string]string{
		filepath.Join(home, ".claude", "projects", "-p", "a.jsonl"):                      line("a", "req_a"),
		filepath.Join(state, "agent-plans", "claude", "projects", "-session", "b.jsonl"): line("b", "req_b"),
	} {
		os.MkdirAll(filepath.Dir(dir), 0o755)
		os.WriteFile(dir, []byte(l), 0o644)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"usage", "--since", "7d"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"by category", "interactive", "dashboard"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, secret) || strings.Contains(out, "/nowhere") {
		t.Errorf("prints transcript text:\n%s", out)
	}

	stdout.Reset()
	if code := run([]string{"usage", "--by", "role", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("--json: exit %d: %s", code, stderr.String())
	}
	var got struct {
		By   string `json:"by"`
		Rows []struct {
			Label string `json:"label"`
			Total int64  `json:"total"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.By != "role" || len(got.Rows) != 1 ||
		got.Rows[0].Label != "main" || got.Rows[0].Total != 222 {
		t.Errorf("--json: %s (%v)", stdout.String(), err)
	}
	if strings.Contains(stdout.String(), secret) {
		t.Errorf("--json prints transcript text:\n%s", stdout.String())
	}

	for _, args := range [][]string{{"usage", "--by", "model"}, {"usage", "--since", "soon"}, {"usage", "--since", "-1d"},
		{"usage", "now"}} {
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: exit %d: %s", args, code, stderr.String())
		}
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"serve"}, {"collect", "--bogus"}, {"export", "now"}, {"export", "--out", "x"},
		{"dispatch", "o/s#9"}, {"dispatch", "--out", "x"}, {"queue", "run"}, {"answer", "2026-10-17-loop.box"},
		{"answer", "--doc", "x.json"}, {"watch", "--poll"}, {"watch", "ws://x"}, {"prime", "--url", "x"}, {"page", "now"}, {"page", "published", "x"},
		{"page", "--out", "x"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "collect") {
			t.Errorf("%q: exit %d: %s", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "collect") ||
		!strings.Contains(stdout.String(), "  watch ") || !strings.Contains(stdout.String(), "  usage ") {
		t.Errorf("--help: exit %d: %s", code, stdout.String())
	}
}
