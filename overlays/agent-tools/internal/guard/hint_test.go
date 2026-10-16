package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// TestBranchHint covers the context the baseline hook adds when a file
// edit lands in another worktree or branch than the session's last one,
// which the session's plan logs as branch-changed. Each case starts from a
// fresh repository and plan, which names the sessions s1 and s2, and makes
// the edits that come before the one it checks.
func TestBranchHint(t *testing.T) {
	// hinting makes a repository, main, with a linked worktree on feature,
	// under root, and the plan, and returns a function that runs the
	// baseline hook on a file edit and returns the context it adds and its
	// decision.
	hinting := func(t *testing.T) (root, main, linked string, edit func(session, tool, path string) (string, string)) {
		t.Helper()
		root, _ = filepath.EvalSymlinks(t.TempDir())
		main, linked = filepath.Join(root, "main"), filepath.Join(root, "linked")
		os.Mkdir(main, 0o755)
		gitIn(t, main, "init", "-q", "-b", "main")
		gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
		gitIn(t, main, "worktree", "add", "-q", "-b", "feature", linked)
		c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
		config := filepath.Join(root, "config.json")
		data, _ := json.Marshal(map[string]any{"plans": c})
		os.WriteFile(config, data, 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		if _, err := c.New(plans.NewOptions{Title: "Add the loop", Slug: "loop", Sessions: []string{"claude:s1", "claude:s2"}},
			time.Now()); err != nil {
			t.Fatal(err)
		}

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

	t.Run("a session without a plan", func(t *testing.T) {
		_, main, linked, edit := hinting(t)
		edit("s9", "Edit", filepath.Join(main, "a.go"))
		if c, _ := edit("s9", "Edit", filepath.Join(linked, "b.go")); c != "" {
			t.Errorf("a session without a plan: %q", c)
		}
	})

	// The plan's log holds each change of branch, and only those.
	t.Run("the plan's log", func(t *testing.T) {
		_, main, linked, edit := hinting(t)
		edit("s1", "Edit", filepath.Join(main, "a.go"))
		edit("s1", "Edit", filepath.Join(main, "b.go"))
		edit("s1", "Edit", filepath.Join(linked, "b.go"))
		gitIn(t, linked, "switch", "-q", "--detach")
		edit("s1", "Edit", filepath.Join(linked, "b.go"))
		edit("s2", "Edit", filepath.Join(linked, "b.go"))
		c, _ := plans.Load()
		p, _, _ := c.Locate("s1", "")
		events, _ := p.Events()
		var got []string
		for _, e := range events {
			got = append(got, e.Kind+" "+e.Session+" "+e.Branch+" "+e.Worktree)
		}
		want := []string{"branch-changed s1 main " + main, "branch-changed s1 feature " + linked,
			"branch-changed s1  " + linked, "branch-changed s2  " + linked}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("logged:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})
}
