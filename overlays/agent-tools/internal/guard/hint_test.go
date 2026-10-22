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
// Each case starts from a fresh repository and session state, and makes
// the edits that come before the one it checks.
func TestBranchHint(t *testing.T) {
	// hinting makes a repository, main, with a linked worktree on feature,
	// under root, and returns a function that runs the baseline hook on a
	// file edit and returns the context it adds and its decision.
	hinting := func(t *testing.T) (root, main, linked string, edit func(session, tool, path string) (string, string)) {
		t.Helper()
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		root, _ = filepath.EvalSymlinks(t.TempDir())
		main, linked = filepath.Join(root, "main"), filepath.Join(root, "linked")
		os.Mkdir(main, 0o755)
		gitIn(t, main, "init", "-q", "-b", "main")
		gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
		gitIn(t, main, "worktree", "add", "-q", "-b", "feature", linked)

		edit = func(session, tool, path string) (context, decision string) {
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
		return root, main, linked, edit
	}

	t.Run("first edit", func(t *testing.T) {
		_, main, _, edit := hinting(t)
		if c, _ := edit("s1", "Edit", filepath.Join(main, "a.go")); c != "" {
			t.Errorf("first edit: %q", c)
		}
	})

	t.Run("same branch", func(t *testing.T) {
		_, main, _, edit := hinting(t)
		edit("s1", "Edit", filepath.Join(main, "a.go"))
		if c, _ := edit("s1", "Write", filepath.Join(main, "sub", "new.go")); c != "" {
			t.Errorf("same branch: %q", c)
		}
	})

	t.Run("another worktree", func(t *testing.T) {
		_, main, linked, edit := hinting(t)
		edit("s1", "Edit", filepath.Join(main, "a.go"))
		edit("s1", "Write", filepath.Join(main, "sub", "new.go"))
		c, d := edit("s1", "MultiEdit", filepath.Join(linked, "b.go"))
		if d != "" || !strings.Contains(c, "feature in "+linked) || !strings.Contains(c, "main in "+main) {
			t.Errorf("another worktree: %q %q", c, d)
		}
	})

	t.Run("another session's first edit", func(t *testing.T) {
		_, main, linked, edit := hinting(t)
		edit("s1", "MultiEdit", filepath.Join(linked, "b.go"))
		if c, _ := edit("s2", "Edit", filepath.Join(main, "a.go")); c != "" {
			t.Errorf("another session's first edit: %q", c)
		}
	})

	t.Run("another branch in the same worktree", func(t *testing.T) {
		_, main, linked, edit := hinting(t)
		edit("s1", "Edit", filepath.Join(main, "a.go"))
		edit("s1", "MultiEdit", filepath.Join(linked, "b.go"))
		gitIn(t, linked, "switch", "-q", "-c", "other")
		if c, _ := edit("s1", "Edit", filepath.Join(linked, "b.go")); !strings.Contains(c, "other in "+linked) {
			t.Errorf("another branch in the same worktree: %q", c)
		}
	})

	t.Run("no session", func(t *testing.T) {
		_, main, linked, edit := hinting(t)
		edit("", "Edit", filepath.Join(linked, "b.go"))
		if c, _ := edit("", "Edit", filepath.Join(main, "a.go")); c != "" {
			t.Errorf("no session: %q", c)
		}
	})

	t.Run("outside any repository", func(t *testing.T) {
		root, main, _, edit := hinting(t)
		edit("s1", "Edit", filepath.Join(main, "a.go"))
		if c, _ := edit("s1", "Edit", filepath.Join(root, "outside.txt")); c != "" {
			t.Errorf("outside any repository: %q", c)
		}
	})

	t.Run("a denial", func(t *testing.T) {
		_, main, linked, edit := hinting(t)
		edit("s1", "Edit", filepath.Join(linked, "b.go"))
		if _, d := edit("s1", "Edit", filepath.Join(main, ".git", "config")); d != "deny" {
			t.Errorf("a denial: %q", d)
		}
	})

	t.Run("a session ID that is a path", func(t *testing.T) {
		_, main, linked, edit := hinting(t)
		edit("../x", "Edit", filepath.Join(main, "a.go"))
		if c, _ := edit("../x", "Edit", filepath.Join(linked, "b.go")); c != "" {
			t.Errorf("a session ID that is a path: %q", c)
		}
	})
}
