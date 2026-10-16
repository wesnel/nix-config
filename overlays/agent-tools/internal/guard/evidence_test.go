package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/evidence"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// Each shell call is recorded in its session's evidence log, with the exit
// status each harness reports: Claude Code's failures arrive on
// PostToolUseFailure, with the status in error's text; ECA's in the
// response's text, with error set on a failure; Codex gives none, and no
// hook sees a backgrounded command end. A failure adds no context.
func TestPostEvidence(t *testing.T) {
	hostpaths.Bake(t, t.TempDir(), t.TempDir())
	repo := t.TempDir()
	if err := fixtureGits([]string{repo, "init", "-q", "-b", "main"},
		[]string{repo, "commit", "-q", "--allow-empty", "-m", "one"}); err != nil {
		t.Fatal(err)
	}
	event := func(fields map[string]any) string {
		fields["cwd"] = repo
		if fields["tool_name"] == nil {
			fields["tool_name"] = "Bash"
		}
		data, _ := json.Marshal(fields)
		return string(data)
	}
	token := "ghp_" + strings.Repeat("b", 36)
	unknown := -1
	for _, c := range []struct {
		name, session, event string
		exit                 int
		background           bool
		output               string
	}{
		{"Claude Code success", "cc-ok", event(map[string]any{"hook_event_name": "PostToolUse", "session_id": "cc-ok",
			"tool_input":    map[string]any{"command": "echo out-ok"},
			"tool_response": map[string]any{"stdout": "out-ok", "stderr": "warn " + token, "interrupted": false}}),
			0, false, "out-ok\nwarn [REDACTED GitHub token]"},
		{"Claude Code failure", "cc-fail", event(map[string]any{"hook_event_name": "PostToolUseFailure", "session_id": "cc-fail",
			"tool_input": map[string]any{"command": "echo out-fail; exit 3"}, "tool_use_id": "u",
			"error": "Exit code 3\nout-fail\nerr-fail", "is_interrupt": false}), 3, false, "out-fail\nerr-fail"},
		{"Claude Code interrupted", "cc-int", event(map[string]any{"hook_event_name": "PostToolUseFailure", "session_id": "cc-int",
			"tool_input": map[string]any{"command": "sleep 99"}, "error": "Interrupted", "is_interrupt": true}), unknown, false, "Interrupted"},
		{"Claude Code background", "cc-bg", event(map[string]any{"hook_event_name": "PostToolUse", "session_id": "cc-bg",
			"tool_input":    map[string]any{"command": "sleep 3; exit 5", "run_in_background": true},
			"tool_response": map[string]any{"stdout": "", "stderr": "", "backgroundTaskId": "b1"}}), unknown, true, ""},
		{"Codex", "cx", event(map[string]any{"hook_event_name": "PostToolUse", "session_id": "cx", "turn_id": "t",
			"tool_input": map[string]any{"command": "exit 3"}, "tool_response": "Process exited with code 3\nout"}), unknown, false, "out"},
		{"ECA failure", "eca-fail", event(map[string]any{"hook_type": "postToolCall", "chat_id": "eca-fail", "server": "eca",
			"tool_name": "shell_command", "tool_input": map[string]any{"command": "false"}, "error": true,
			"tool_response": "Exit code: 2\nStderr:\nbad\nStdout:\n"}), 2, false, "bad"},
		{"ECA success", "eca-ok", event(map[string]any{"hook_type": "postToolCall", "chat_id": "eca-ok", "server": "eca",
			"tool_name": "shell_command", "tool_input": map[string]any{"command": "true"}, "error": false,
			"tool_response": "Exit code: 0\nStderr:\n\nStdout:\nfine"}), 0, false, "fine"},
		{"ECA background", "eca-bg", event(map[string]any{"hook_type": "postToolCall", "chat_id": "eca-bg", "server": "eca",
			"tool_name": "shell_command", "tool_input": map[string]any{"command": "make", "background": "build"}, "error": false,
			"tool_response": "Background job job-1 started"}), unknown, true, "started"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			if code := Main([]string{"--post"}, strings.NewReader(c.event), &out, io.Discard); code != 0 {
				t.Errorf("exit %d", code)
			}
			if c.exit > 0 && out.Len() != 0 {
				t.Errorf("a failure added context: %s", out.String())
			}
			got, err := evidence.Read([]string{c.session}, time.Now())
			if err != nil || len(got) != 1 {
				t.Fatalf("%v, %d records", err, len(got))
			}
			r := got[0]
			switch {
			case c.exit == unknown && r.Exit != nil:
				t.Errorf("exit %d, want unknown", *r.Exit)
			case c.exit != unknown && (r.Exit == nil || *r.Exit != c.exit):
				t.Errorf("exit %v, want %d", r.Exit, c.exit)
			}
			if r.Background != c.background || r.Session != c.session || r.Cwd != repo || r.Head == "" || r.Tree == "" {
				t.Errorf("record %+v", r)
			}
			if !strings.Contains(r.OutputTail, c.output) || strings.Contains(r.OutputTail, token) {
				t.Errorf("output %q, want it to hold %q", r.OutputTail, c.output)
			}
		})
	}
	t.Run("no session, no command", func(t *testing.T) {
		for _, e := range []string{
			event(map[string]any{"hook_event_name": "PostToolUse", "tool_input": map[string]any{"command": "true"}}),
			event(map[string]any{"hook_event_name": "PostToolUse", "session_id": "edit", "tool_name": "Edit",
				"tool_input": map[string]any{"file_path": "/x"}}),
		} {
			Main([]string{"--post"}, strings.NewReader(e), io.Discard, io.Discard)
		}
		entries, _ := os.ReadDir(evidence.Dir())
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "edit.") || !strings.HasSuffix(e.Name(), ".jsonl") {
				t.Errorf("recorded %s", filepath.Join(evidence.Dir(), e.Name()))
			}
		}
	})
}

// A call is recorded at the worktree it ran in, which a leading cd or git
// -C names, relative to the hook's cwd, where the harness gives the
// session's directory rather than the shell's. Where the command does not
// say, no HEAD or tree is recorded.
func TestPostEvidenceDirectory(t *testing.T) {
	hostpaths.Bake(t, t.TempDir(), t.TempDir())
	parent := t.TempDir()
	a, b := filepath.Join(parent, "wtA"), filepath.Join(parent, "wtB")
	heads := map[string]string{}
	for _, dir := range []string{a, b} {
		if err := fixtureGits([]string{parent, "init", "-q", "-b", "main", dir},
			[]string{dir, "commit", "-q", "--allow-empty", "-m", filepath.Base(dir)}); err != nil {
			t.Fatal(err)
		}
		head, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
		if err != nil {
			t.Fatal(err)
		}
		heads[dir] = strings.TrimSpace(string(head))
	}
	for i, c := range []struct{ command, dir string }{
		{"go test ./...", a},
		{"cd ../wtB && go test ./...", b},
		{"cd " + b + "; go test ./... 2>&1 | tail -5", b},
		{"pushd ../wtB && go test ./...", b},
		{"git -C ../wtB status", b},
		{"cd .. && git -C wtB log", b},
		{"bash -c 'cd ../wtB && go test ./...'", b},
		{"cd ../wtB && bash -c 'go test ./...'", b},
		{`cd "$WT" && go test ./...`, ""},
		{"cd - && go test ./...", ""},
		{"go vet ./... && cd ../wtB && go test ./...", ""},
		{"git -C ../wtB status && go test ./...", ""},
		{"(cd ../wtB && go test ./...); go vet ./...", ""},
		{"echo $(cd ../wtB && pwd) && go test ./...", ""},
		{"git --git-dir ../wtB/.git status", ""},
	} {
		t.Run(c.command, func(t *testing.T) {
			session := "dir-" + strconv.Itoa(i)
			data, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "session_id": session, "cwd": a,
				"tool_name": "Bash", "tool_input": map[string]any{"command": c.command},
				"tool_response": map[string]any{"stdout": "", "stderr": ""}})
			Main([]string{"--post"}, bytes.NewReader(data), io.Discard, io.Discard)
			got, err := evidence.Read([]string{session}, time.Now())
			if err != nil || len(got) != 1 {
				t.Fatalf("%v, %d records", err, len(got))
			}
			r := got[0]
			switch {
			case c.dir == "" && (r.Head != "" || r.Tree != "" || r.Cwd != a):
				t.Errorf("recorded %+v, want the hook's cwd and no HEAD or tree", r)
			case c.dir != "" && (r.Head != heads[c.dir] || r.Tree == "" || r.Cwd != c.dir):
				t.Errorf("recorded %+v, want %s at %s", r, c.dir, heads[c.dir])
			}
		})
	}
}
