package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBranchHint covers the context the baseline hook adds when a file
// edit lands in another worktree or branch than the session's last one.
func TestBranchHint(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root, _ := filepath.EvalSymlinks(t.TempDir())
	main, linked := filepath.Join(root, "main"), filepath.Join(root, "linked")
	os.Mkdir(main, 0o755)
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, main, "worktree", "add", "-q", "-b", "feature", linked)

	edit := func(session, tool, path string) (context, decision string) {
		t.Helper()
		event, _ := json.Marshal(map[string]any{"session_id": session, "tool_name": tool, "cwd": main,
			"tool_input": map[string]string{"file_path": path, "old_string": "a", "new_string": "b", "content": "c"}})
		var out bytes.Buffer
		Main([]string{"--baseline"}, bytes.NewReader(event), &out, io.Discard)
		if out.Len() == 0 {
			return "", ""
		}
		var got struct {
			Specific struct {
				Event    string `json:"hookEventName"`
				Decision string `json:"permissionDecision"`
				Context  string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Specific.Event != "PreToolUse" {
			t.Fatalf("output %s: %v", out.String(), err)
		}
		return got.Specific.Context, got.Specific.Decision
	}

	if c, _ := edit("s1", "Edit", filepath.Join(main, "a.go")); c != "" {
		t.Errorf("first edit: %q", c)
	}
	if c, _ := edit("s1", "Write", filepath.Join(main, "sub", "new.go")); c != "" {
		t.Errorf("same branch: %q", c)
	}
	c, d := edit("s1", "MultiEdit", filepath.Join(linked, "b.go"))
	if d != "" || !strings.Contains(c, "feature in "+linked) || !strings.Contains(c, "main in "+main) {
		t.Errorf("another worktree: %q %q", c, d)
	}
	if c, _ := edit("s2", "Edit", filepath.Join(main, "a.go")); c != "" {
		t.Errorf("another session's first edit: %q", c)
	}
	gitIn(t, linked, "switch", "-q", "-c", "other")
	if c, _ := edit("s1", "Edit", filepath.Join(linked, "b.go")); !strings.Contains(c, "other in "+linked) {
		t.Errorf("another branch in the same worktree: %q", c)
	}
	if c, _ := edit("", "Edit", filepath.Join(main, "a.go")); c != "" {
		t.Errorf("no session: %q", c)
	}
	if c, _ := edit("s1", "Edit", filepath.Join(root, "outside.txt")); c != "" {
		t.Errorf("outside any repository: %q", c)
	}
	if _, d := edit("s1", "Edit", filepath.Join(main, ".git", "config")); d != "deny" {
		t.Errorf("a denial: %q", d)
	}
	if c, _ := edit("../x", "Edit", filepath.Join(linked, "b.go")); c != "" {
		t.Errorf("a session ID that is a path: %q", c)
	}
}
