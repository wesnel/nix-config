package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// gitIn runs git in dir.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// digest standup, on a weekday within its slot's hours, commits the
// digest to the notes repository and pushes it, and writes it as the
// Today document; outside them it writes nothing.
func TestStandup(t *testing.T) {
	state := setUp(t)
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "Operator", "GIT_AUTHOR_EMAIL": "operator@example.com",
		"GIT_COMMITTER_NAME": "Operator", "GIT_COMMITTER_EMAIL": "operator@example.com"} {
		t.Setenv(k, v)
	}
	root, _ := filepath.EvalSymlinks(t.TempDir())
	origin, notes := filepath.Join(root, "origin.git"), filepath.Join(root, "notes")
	gitIn(t, root, "init", "-q", "--bare", "-b", "main", origin)
	gitIn(t, root, "clone", "-q", origin, notes)
	gitIn(t, notes, "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "Start")
	gitIn(t, notes, "push", "-q", "origin", "main")
	os.MkdirAll(filepath.Join(notes, "agents"), 0o755)
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"plans": {"dir": "`+filepath.Join(notes, "agents")+`", "host": "box"}}`), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	la, _ := time.LoadLocation("America/Los_Angeles")
	args := []string{"digest", "standup", "--at", "07:30", "--zone", "America/Los_Angeles"}
	today := filepath.Join(state, "agent-plans", "out", "digests", "today.json")
	t.Cleanup(func() { clock = time.Now })

	// Saturday, and Monday's boot catching up on Friday: nothing.
	for _, at := range []time.Time{time.Date(2026, 10, 17, 7, 30, 0, 0, la), time.Date(2026, 10, 12, 6, 0, 0, 0, la)} {
		clock = func() time.Time { return at }
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"skipped": "not due: `) {
			t.Errorf("%v: exit %d: %s%s", at, code, stdout.String(), stderr.String())
		}
	}
	if _, err := os.Stat(today); err == nil {
		t.Error("a skipped run wrote the Today document")
	}

	clock = func() time.Time { return time.Date(2026, 10, 14, 7, 31, 0, 0, la) }
	var stdout, stderr bytes.Buffer
	if code := run(args, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	var got struct {
		File   string `json:"file"`
		Wrote  bool   `json:"wrote"`
		Pushed bool   `json:"pushed"`
		Today  string `json:"today"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.File != "digests/2026/10-14-standup.box.org" ||
		!got.Wrote || !got.Pushed || got.Today != today {
		t.Errorf("printed %s (%v)", stdout.String(), err)
	}
	pushed := gitIn(t, origin, "show", "main:digests/2026/10-14-standup.box.org")
	if !strings.HasPrefix(pushed, "#+title: Standup 2026-10-14 Wed\n") || !strings.Contains(pushed, "[[https://github.com/o/r/pull/7][o/r#7]] :: draft") {
		t.Errorf("the pushed digest:\n%s", pushed)
	}
	if log := gitIn(t, origin, "log", "-1", "--format=%G? %s", "main"); log != "N Add the standup digest for 2026-10-14\n" {
		t.Errorf("origin's last commit: %q", log)
	}
	var doc struct {
		Kind, Date, File, Text string
	}
	data, _ := os.ReadFile(today)
	if err := json.Unmarshal(data, &doc); err != nil || doc.Kind != "standup" || doc.Date != "2026-10-14" ||
		doc.File != got.File || doc.Text != pushed {
		t.Errorf("today.json: %s (%v)", data, err)
	}

	// The remote out of reach, as at boot: the commit stays for the next
	// sync, the page still gets the digest, and the run is no failure.
	gitIn(t, notes, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	clock = func() time.Time { return time.Date(2026, 10, 15, 7, 31, 0, 0, la) }
	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"unpushed": "`) ||
		!strings.Contains(stdout.String(), `"today": "`) {
		t.Errorf("unreachable: exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	if log := gitIn(t, notes, "log", "-1", "--format=%s"); log != "Add the standup digest for 2026-10-15\n" {
		t.Errorf("the clone's last commit: %q", log)
	}
	data, _ = os.ReadFile(today)
	if err := json.Unmarshal(data, &doc); err != nil || doc.Date != "2026-10-15" {
		t.Errorf("today.json: %s (%v)", data, err)
	}

	// Committed but stuck for a reason no later sync fixes, here a
	// detached HEAD: a failure, not "unpushed".
	gitIn(t, notes, "checkout", "-q", "--detach")
	clock = func() time.Time { return time.Date(2026, 10, 19, 7, 31, 0, 0, la) }
	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != 1 || strings.Contains(stdout.String(), "unpushed") {
		t.Errorf("detached: exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	gitIn(t, notes, "checkout", "-q", "main")

	// A publish that fails before its commit leaves the Today document as
	// it was, and fails.
	os.MkdirAll(filepath.Join(notes, "digests", "2026"), 0o755)
	os.Symlink(t.TempDir(), filepath.Join(notes, "digests", "2026", "10-16-standup.box.org"))
	clock = func() time.Time { return time.Date(2026, 10, 16, 7, 31, 0, 0, la) }
	stdout.Reset()
	stderr.Reset()
	if code := run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "is a link") {
		t.Errorf("a failed publish: exit %d: %s%s", code, stdout.String(), stderr.String())
	}
	data, _ = os.ReadFile(today)
	if err := json.Unmarshal(data, &doc); err != nil || doc.Date != "2026-10-15" {
		t.Errorf("today.json after a failed publish: %s (%v)", data, err)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"serve"}, {"collect", "--bogus"}, {"export", "now"}, {"export", "--out", "x"},
		{"digest"}, {"digest", "standup"}, {"digest", "standup", "--at", "07:30"},
		{"digest", "standup", "--at", "07:30", "--zone", "America/Los_Angeles", "now"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "collect") {
			t.Errorf("%q: exit %d: %s", args, code, stderr.String())
		}
	}
	for _, args := range [][]string{{"digest", "standup", "--at", "7am", "--zone", "America/Los_Angeles"},
		{"digest", "standup", "--at", "07:30", "--zone", "Local"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "plan-dash: ") {
			t.Errorf("%q: exit %d: %s", args, code, stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "collect") {
		t.Errorf("--help: exit %d: %s", code, stdout.String())
	}
}
