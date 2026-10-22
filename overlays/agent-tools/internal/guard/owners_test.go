package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// TestOwners covers worktrees a plan gives an owner: the file tools and
// shell edits of any other agent there are denied, the owner's pass.
func TestOwners(t *testing.T) {
	// owned makes a repository, main, with a linked worktree that a plan in
	// a plans directory of the test's own gives to agent a1.
	owned := func(t *testing.T) (main, linked string) {
		t.Helper()
		root, _ := filepath.EvalSymlinks(t.TempDir())
		c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
		config := filepath.Join(root, "config.json")
		data, _ := json.Marshal(map[string]any{"plans": c})
		os.WriteFile(config, data, 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)

		main, linked = filepath.Join(root, "main"), filepath.Join(root, "linked")
		os.Mkdir(main, 0o755)
		gitIn(t, main, "init", "-q", "-b", "main")
		gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
		gitIn(t, main, "worktree", "add", "-q", "-b", "feature", linked)
		made, err := c.New(plans.NewOptions{Title: "X", Slug: "x"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		p, _ := c.Open(made.Plan)
		if _, err := p.SetOwner(linked, "a1", time.Now()); err != nil {
			t.Fatal(err)
		}
		return main, linked
	}

	// MAIN and LINKED in an event stand for the worktrees owned makes.
	for _, e := range []struct {
		name string
		deny bool
		json string
	}{
		{"owner's Edit", false, `{"tool_name":"Edit","agent_id":"a1","tool_input":{"file_path":"LINKED/a.go"}}`},
		{"another agent's Edit", true, `{"tool_name":"Edit","agent_id":"a2","tool_input":{"file_path":"LINKED/a.go"}}`},
		{"main thread's Write", true, `{"tool_name":"Write","tool_input":{"file_path":"LINKED/sub/new.go"}}`},
		{"Edit in an unowned worktree", false, `{"tool_name":"Edit","agent_id":"a2","tool_input":{"file_path":"MAIN/a.go"}}`},
		{"another agent's shell write", true, `{"tool_name":"Bash","agent_id":"a2","cwd":"LINKED","tool_input":{"command":"echo x > new.txt"}}`},
		{"another agent's shell edit by cd", true, `{"tool_name":"Bash","agent_id":"a2","cwd":"MAIN","tool_input":{"command":"cd ../linked && sed -i '' s/a/b/ a.go"}}`},
		{"another agent's shell read", false, `{"tool_name":"Bash","agent_id":"a2","cwd":"LINKED","tool_input":{"command":"cat a.go > /dev/null"}}`},
		{"owner's shell write", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"echo x > new.txt"}}`},
		{"another agent's patch", true, `{"tool_name":"apply_patch","turn_id":"t","agent_id":"a2","cwd":"LINKED","tool_input":{"command":"*** Begin Patch\n*** Add File: b.go\n+x\n*** End Patch\n"}}`},
		{"ECA's write", false, `{"hook_type":"preToolCall","server":"eca","agent":"code","tool_name":"write_file","tool_input":{"path":"LINKED/a.go"}}`},
	} {
		t.Run(caseName(e.deny, e.name), func(t *testing.T) {
			main, linked := owned(t)
			event := strings.NewReplacer("MAIN", main, "LINKED", linked).Replace(e.json)
			var out bytes.Buffer
			Main([]string{"--baseline"}, strings.NewReader(event), &out, io.Discard)
			denied := strings.Contains(out.String(), `"deny"`)
			if denied != e.deny {
				t.Errorf("%s: deny=%v, output %s", event, denied, out.String())
			}
			if denied && (!strings.Contains(out.String(), "a1") || !strings.Contains(out.String(), "agent-plan own")) {
				t.Errorf("%s: the reason does not name the owner and the hand-over: %s", event, out.String())
			}
		})
	}
}

// stashRepos is a repository with a linked worktree, under the returned
// root. TestSubagentStash's cases share it, built once, as the guard only
// reads it.
var stashRepos = sync.OnceValues(func() (string, error) {
	if err := os.MkdirAll(fixtures, 0o755); err != nil {
		return "", err
	}
	root, err := os.MkdirTemp(fixtures, "stash")
	if err != nil {
		return "", err
	}
	root, _ = filepath.EvalSymlinks(root)
	main, linked := filepath.Join(root, "main"), filepath.Join(root, "linked")
	os.Mkdir(main, 0o755)
	return root, fixtureGits(
		[]string{main, "init", "-q", "-b", "main"},
		[]string{main, "commit", "-q", "--allow-empty", "-m", "base"},
		[]string{main, "worktree", "add", "-q", "-b", "feature", linked},
	)
})

// TestSubagentStash covers a sub-agent's stash commands: every one that
// writes or restores an entry is denied, where the main thread is held to
// tagged entries alone.
func TestSubagentStash(t *testing.T) {
	linked := filepath.Join(fixture(t, stashRepos), "linked")

	claude := func(command string) string {
		data, _ := json.Marshal(map[string]any{"tool_name": "Bash", "agent_id": "a1", "cwd": linked,
			"tool_input": map[string]string{"command": command}})
		return string(data)
	}
	codex := func(command string) string {
		data, _ := json.Marshal(map[string]any{"tool_name": "exec_command", "turn_id": "t", "agent_id": "a1", "cwd": linked,
			"tool_input": map[string]string{"command": command}})
		return string(data)
	}
	mainThread := func(command string) string {
		data, _ := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": linked,
			"tool_input": map[string]string{"command": command}})
		return string(data)
	}
	eca := func(command string) string {
		data, _ := json.Marshal(map[string]any{"hook_type": "preToolCall", "server": "eca", "agent": "code",
			"tool_name": "shell_command", "cwd": linked, "tool_input": map[string]string{"command": command}})
		return string(data)
	}
	for _, e := range []struct {
		name string
		deny bool
		json string
	}{
		{"git stash", true, claude("git stash")},
		{"git stash -m agent-x-1", true, claude("git stash -m agent-x-1")},
		{"git stash push -u -m agent-x-1", true, claude("git stash push -u -m agent-x-1")},
		{"git stash save agent-x-1", true, claude("git stash save agent-x-1")},
		{"git stash pop 3f2a9c1", true, claude("git stash pop 3f2a9c1")},
		{"git stash apply 3f2a9c1", true, claude("git stash apply 3f2a9c1")},
		{"git stash branch b 3f2a9c1", true, claude("git stash branch b 3f2a9c1")},
		{"git stash drop stash@{1}", true, claude("git stash drop stash@{1}")},
		{"git stash clear", true, claude("git stash clear")},
		{"git stash create", true, claude("git stash create")},
		{"git stash store 3f2a9c1", true, claude("git stash store 3f2a9c1")},
		{"git -C ../main stash push -m agent-x-1", true, claude("git -C ../main stash push -m agent-x-1")},
		{"stash in bash -c", true, claude("git status && bash -c 'git stash push -m agent-x-1'")},
		{"Codex's git stash push -m agent-x-1", true, codex("git stash push -m agent-x-1")},
		{"git stash list", false, claude("git stash list")},
		{"git stash show -p 3f2a9c1", false, claude("git stash show -p 3f2a9c1")},
		{"git status", false, claude("git status")},
		{"main thread's git stash push -u -m agent-x-1", false, mainThread("git stash push -u -m agent-x-1")},
		{"main thread's git stash apply 3f2a9c1", false, mainThread("git stash apply 3f2a9c1")},
		{"ECA's git stash push -u -m agent-x-1", false, eca("git stash push -u -m agent-x-1")},
	} {
		t.Run(caseName(e.deny, e.name), func(t *testing.T) {
			var out bytes.Buffer
			Main([]string{"--baseline"}, strings.NewReader(e.json), &out, io.Discard)
			denied := strings.Contains(out.String(), `"deny"`)
			if denied != e.deny {
				t.Errorf("%s: deny=%v, output %s", e.json, denied, out.String())
			}
			if denied && !strings.Contains(out.String(), "WIP commit") {
				t.Errorf("%s: the reason does not say to commit the work as a WIP commit: %s", e.json, out.String())
			}
		})
	}
}
