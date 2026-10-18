package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/usage/runs"
)

const hookSession = "46a9d218-3ef8-4e96-978e-9ea630a586fc"

// ledger is every run the ledger under state holds.
func ledger(t *testing.T, state string) map[string]runs.Record {
	t.Helper()
	got, unreadable, err := runs.Ledger{Dir: filepath.Join(state, "agent-tools", "usage")}.Read(time.Now().Add(-time.Hour))
	if err != nil || unreadable > 0 {
		t.Fatalf("ledger: %v, %d unreadable", err, unreadable)
	}
	return got
}

func hookRun(t *testing.T, input string) (int, string) {
	t.Helper()
	old := stdin
	stdin = strings.NewReader(input)
	t.Cleanup(func() { stdin = old })
	var stdout, stderr bytes.Buffer
	code := run([]string{"usage", "hook"}, &stdout, &stderr)
	if stdout.Len() > 0 {
		t.Errorf("the hook printed %q, which would become the session's context", stdout.String())
	}
	return code, stderr.String()
}

// usage hook records a tagged session as it starts, from Claude Code's
// SessionStart input and AGENT_RUN_TAG, and nothing for an untagged one.
func TestUsageHook(t *testing.T) {
	state := setUp(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "/srv/claude")
	input := `{"session_id":"` + hookSession + `","cwd":"/x","hook_event_name":"SessionStart","source":"startup","model":"claude-opus-5-5"}`

	t.Setenv(runs.Env, "")
	if code, stderr := hookRun(t, input); code != 0 || len(ledger(t, state)) != 0 {
		t.Errorf("untagged: exit %d (%s), ledger %+v", code, stderr, ledger(t, state))
	}

	t.Setenv(runs.Env, "category=chat")
	if code, stderr := hookRun(t, input); code != 1 || !strings.Contains(stderr, "category") || len(ledger(t, state)) != 0 {
		t.Errorf("bad tag: exit %d (%s), ledger %+v", code, stderr, ledger(t, state))
	}

	t.Setenv(runs.Env, "category=triage;repo=acme/widget;pr=12")
	if code, stderr := hookRun(t, input); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	r, ok := ledger(t, state)["claude:"+hookSession]
	if !ok || r.Category != runs.Triage || r.Labels != (runs.Labels{Repo: "acme/widget", PR: 12}) ||
		r.ConfigDir != "/srv/claude" || r.Model != "claude-opus-5-5" || r.Started.IsZero() {
		t.Errorf("record %+v", r)
	}
}

// The help names every category and kind a tag may hold.
func TestUsageHookHelp(t *testing.T) {
	_, hook, _ := strings.Cut(usage, "  usage hook\n")
	hook, _, _ = strings.Cut(hook, "  usage run")
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(hook, func(r rune) bool { return r != '-' && (r < 'a' || r > 'z') }) {
		words[w] = true
	}
	for _, word := range append(append([]string{}, runs.Categories...), runs.Kinds...) {
		if !words[word] {
			t.Errorf("usage hook's help does not name %q", word)
		}
	}
}

// fakeClaude prints a print session's stream with a result, and exits 3.
const fakeClaude = `printf '%s\n' '{"type":"system","subtype":"init","session_id":"` + hookSession + `"}' ` +
	`'{"session_id":"` + hookSession + `","modelUsage":{"claude-opus-5-5":{"inputTokens":5,"outputTokens":7,` +
	`"cacheReadInputTokens":11,"cacheCreationInputTokens":13}},"type":"result","result":"synthetic reply text"}'; exit 3`

// usage run passes the command's output through whole, exits with its
// status, and records the tokens its last result reports.
func TestUsageRun(t *testing.T) {
	state := setUp(t)
	t.Setenv(runs.Env, "category=loop;repo=acme/widget")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"usage", "run", "--", "sh", "-c", fakeClaude}, &stdout, &stderr); code != 3 {
		t.Errorf("exit %d, want the command's 3: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"subtype":"init"`) || !strings.HasSuffix(stdout.String(), `"result":"synthetic reply text"}`+"\n") {
		t.Errorf("output %q", stdout.String())
	}
	r, ok := ledger(t, state)["claude:"+hookSession]
	if !ok || r.Category != runs.Loop || r.Labels.Repo != "acme/widget" || r.Ended.IsZero() || r.Model != "claude-opus-5-5" ||
		r.Tokens == nil || *r.Tokens != (runs.Tokens{Input: 5, Output: 7, CacheRead: 11, CacheWrite: 13}) {
		t.Errorf("record %+v", r)
	}
	if data, _ := os.ReadFile(filepath.Join(state, "agent-tools", "usage", "runs-"+time.Now().UTC().Format("2006-01")+".jsonl")); bytes.Contains(data, []byte("synthetic")) {
		t.Errorf("the ledger holds the session's text: %s", data)
	}

	// Untagged, or with a tag it cannot read, the command still runs.
	state = setUp(t)
	for _, tag := range []string{"", "category=chat"} {
		t.Setenv(runs.Env, tag)
		stdout.Reset()
		if code := run([]string{"usage", "run", "--", "sh", "-c", fakeClaude}, &stdout, &stderr); code != 3 || stdout.Len() == 0 {
			t.Errorf("tag %q: exit %d, output %q", tag, code, stdout.String())
		}
		if got := ledger(t, state); len(got) != 0 {
			t.Errorf("tag %q recorded %+v", tag, got)
		}
	}

	for _, args := range [][]string{{"usage", "run"}, {"usage", "run", "sh"}, {"usage", "hook", "x"}} {
		stderr.Reset()
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: exit %d: %s", args, code, stderr.String())
		}
	}
	if code := run([]string{"usage", "run", "--", "/nonexistent/claude"}, &stdout, &stderr); code != 127 {
		t.Errorf("a missing command: exit %d", code)
	}
}
