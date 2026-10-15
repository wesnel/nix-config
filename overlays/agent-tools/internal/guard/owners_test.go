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

// TestOwners covers worktrees a plan gives an owner. A sub-agent edits,
// by a file tool or the shell, only the worktrees it owns, scratch and the
// plans directory; one that owns none edits only scratch and plans. The
// main thread is kept out of worktrees other agents own alone.
func TestOwners(t *testing.T) {
	// owned makes a repository, main, with linked worktrees: linked, which a
	// plan in a plans directory of the test's own gives to agent a1, and
	// sibling, which it gives to a3. Scratch is a directory beside them that
	// stands for the session's temporary directories.
	owned := func(t *testing.T) *strings.Replacer {
		t.Helper()
		root, _ := filepath.EvalSymlinks(t.TempDir())
		c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
		config := filepath.Join(root, "config.json")
		data, _ := json.Marshal(map[string]any{"plans": c})
		os.WriteFile(config, data, 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		scratch := filepath.Join(root, "scratch")
		os.Mkdir(scratch, 0o755)
		old := scratchRoots
		scratchRoots = func() []string { return []string{scratch} }
		t.Cleanup(func() { scratchRoots = old })

		main, linked, sibling := filepath.Join(root, "main"), filepath.Join(root, "linked"), filepath.Join(root, "sibling")
		os.Mkdir(main, 0o755)
		os.Mkdir(filepath.Join(root, "loose"), 0o755)
		gitIn(t, main, "init", "-q", "-b", "main")
		gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
		gitIn(t, main, "worktree", "add", "-q", "-b", "feature", linked)
		gitIn(t, main, "worktree", "add", "-q", "-b", "other", sibling)
		// parked is a worktree in scratch that a plan gives to a3, and
		// throwaway a repository in scratch that no plan names.
		parked, throwaway := filepath.Join(scratch, "parked"), filepath.Join(scratch, "throwaway")
		gitIn(t, main, "worktree", "add", "-q", "-b", "parked", parked)
		os.Mkdir(throwaway, 0o755)
		gitIn(t, throwaway, "init", "-q", "-b", "main")
		// Links in scratch into the sibling's worktree and the main checkout.
		os.Symlink(sibling, filepath.Join(scratch, "to-sibling"))
		os.Symlink(main, filepath.Join(scratch, "to-main"))
		made, err := c.New(plans.NewOptions{Title: "X", Slug: "x"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		p, _ := c.Open(made.Plan)
		for wt, agent := range map[string]string{linked: "a1", sibling: "a3", parked: "a3"} {
			if _, err := p.SetOwner(wt, agent, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		return strings.NewReplacer("MAIN", main, "LINKED", linked, "SIBLING", sibling, "PARKED", parked,
			"THROWAWAY", throwaway, "SCRATCH", scratch, "PLANS", c.Dir, "LOOSE", filepath.Join(root, "loose"))
	}

	// MAIN, LINKED, SIBLING, SCRATCH, PLANS and LOOSE (a directory in no
	// repository) in an event or a reason stand for the paths owned makes.
	for _, e := range []struct {
		name string
		deny bool
		json string
		says []string // what a denial's reason names
	}{
		{"owner's Edit", false, `{"tool_name":"Edit","agent_id":"a1","tool_input":{"file_path":"LINKED/a.go"}}`, nil},
		{"owner's shell write", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"echo x > new.txt"}}`, nil},
		{"owner's Edit in a sibling's worktree", true, `{"tool_name":"Edit","agent_id":"a1","tool_input":{"file_path":"SIBLING/a.go"}}`, []string{"LINKED"}},
		{"owner's Edit in the main checkout", true, `{"tool_name":"Edit","agent_id":"a1","tool_input":{"file_path":"MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's Write outside any repository", true, `{"tool_name":"Write","agent_id":"a1","tool_input":{"file_path":"LOOSE/a.txt"}}`, []string{"LINKED"}},
		{"owner's shell edit by cd into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cd ../main && sed -i '' s/a/b/ a.go"}}`, []string{"LINKED"}},
		{"owner's Write in scratch", false, `{"tool_name":"Write","agent_id":"a1","tool_input":{"file_path":"SCRATCH/notes.md"}}`, nil},
		{"owner's shell write in scratch", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"git log > SCRATCH/log.txt"}}`, nil},
		{"owner's Write in the plans directory", false, `{"tool_name":"Write","agent_id":"a1","tool_input":{"file_path":"PLANS/2026-10-15-x.box.P1.org"}}`, nil},
		{"owner's shell read in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"cat a.go > /dev/null"}}`, nil},
		{"unowned agent's Edit in the main checkout", true, `{"tool_name":"Edit","agent_id":"a2","tool_input":{"file_path":"MAIN/a.go"}}`, []string{"a2", "agent-plan own"}},
		{"unowned agent's Edit in an owned worktree", true, `{"tool_name":"Edit","agent_id":"a2","tool_input":{"file_path":"LINKED/a.go"}}`, []string{"a2", "agent-plan own"}},
		{"unowned agent's shell write", true, `{"tool_name":"Bash","agent_id":"a2","cwd":"MAIN","tool_input":{"command":"echo x > new.txt"}}`, []string{"a2", "agent-plan own"}},
		{"unowned agent's patch", true, `{"tool_name":"apply_patch","turn_id":"t","agent_id":"a2","cwd":"LINKED","tool_input":{"command":"*** Begin Patch\n*** Add File: b.go\n+x\n*** End Patch\n"}}`, []string{"a2", "agent-plan own"}},
		{"unowned agent's shell read", false, `{"tool_name":"Bash","agent_id":"a2","cwd":"LINKED","tool_input":{"command":"cat a.go > /dev/null"}}`, nil},
		{"unowned agent's Write in scratch", false, `{"tool_name":"Write","agent_id":"a2","tool_input":{"file_path":"SCRATCH/notes.md"}}`, nil},
		{"main thread's Edit in the main checkout", false, `{"tool_name":"Edit","tool_input":{"file_path":"MAIN/a.go"}}`, nil},
		{"main thread's shell write in the main checkout", false, `{"tool_name":"Bash","cwd":"MAIN","tool_input":{"command":"echo x > new.txt"}}`, nil},
		{"main thread's Write in an owned worktree", true, `{"tool_name":"Write","tool_input":{"file_path":"LINKED/sub/new.go"}}`, []string{"a1", "agent-plan own"}},
		{"ECA's write in an owned worktree", false, `{"hook_type":"preToolCall","server":"eca","agent":"code","tool_name":"write_file","tool_input":{"path":"LINKED/a.go"}}`, nil},
		// A worktree a plan names is judged by its owner wherever it is, and
		// a path by where its links lead.
		{"owner's Edit in another's worktree in scratch", true, `{"tool_name":"Edit","agent_id":"a1","tool_input":{"file_path":"PARKED/a.go"}}`, []string{"LINKED"}},
		{"owner's shell write in another's worktree in scratch", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"PARKED","tool_input":{"command":"echo x > new.txt"}}`, []string{"LINKED"}},
		{"owner's Write in a repository in scratch no plan names", false, `{"tool_name":"Write","agent_id":"a1","tool_input":{"file_path":"THROWAWAY/a.go"}}`, nil},
		{"owner's Write through a link into another's worktree", true, `{"tool_name":"Write","agent_id":"a1","tool_input":{"file_path":"SCRATCH/to-sibling/a.go"}}`, []string{"LINKED"}},
		{"owner's shell write through a link into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"echo x > SCRATCH/to-main/a.go"}}`, []string{"LINKED"}},
		{"unowned agent's Write through a link into the main checkout", true, `{"tool_name":"Write","agent_id":"a2","tool_input":{"file_path":"SCRATCH/to-main/a.go"}}`, []string{"a2"}},
		// Every program that writes, moves or removes a file is read for its
		// targets, as is a git command that writes where it runs. Another
		// rule may refuse mv, trash and rm first (trash on PATH, or a path
		// under /nix, where the build sandbox keeps its temporary files), so
		// their reasons are not checked.
		{"owner's cp into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp f MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's cp out of the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp MAIN/a.go SCRATCH/a.go"}}`, nil},
		{"owner's mv into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"mv a.go MAIN/"}}`, nil},
		{"owner's install into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"install -m 644 a.go MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's rsync into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"rsync -a ./ MAIN/"}}`, []string{"LINKED"}},
		{"owner's touch in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"touch MAIN/new.txt"}}`, []string{"LINKED"}},
		{"owner's trash in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"trash MAIN/a.go"}}`, nil},
		{"owner's rm in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"rm -f MAIN/a.go"}}`, nil},
		{"owner's dd into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"dd if=/dev/zero of=MAIN/a.go count=1"}}`, []string{"LINKED"}},
		{"owner's tee into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"echo x | tee MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's awk in place in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gawk -i inplace '{print}' MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's git -C commit in another's worktree", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git -C SIBLING commit -m x"}}`, []string{"LINKED"}},
		{"owner's relative git -C commit in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git -C ../main add a.go"}}`, []string{"LINKED"}},
		{"owner's git commit after cd into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cd MAIN && git commit -m x"}}`, []string{"LINKED"}},
		{"owner's git commit run in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"git commit -m x"}}`, []string{"LINKED"}},
		{"owner's git commit in its worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"git -C LINKED commit -m x"}}`, nil},
		{"owner's git -C log in another's worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git -C SIBLING log -1"}}`, nil},
	} {
		t.Run(caseName(e.deny, e.name), func(t *testing.T) {
			paths := owned(t)
			event := paths.Replace(e.json)
			var out bytes.Buffer
			Main([]string{"--baseline"}, strings.NewReader(event), &out, io.Discard)
			denied := strings.Contains(out.String(), `"deny"`)
			if denied != e.deny {
				t.Fatalf("%s: deny=%v, output %s", event, denied, out.String())
			}
			if !denied {
				return
			}
			var printed struct {
				Hook struct {
					Reason string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			json.Unmarshal(out.Bytes(), &printed)
			reason := printed.Hook.Reason
			for _, s := range e.says {
				if !strings.Contains(reason, paths.Replace(s)) {
					t.Errorf("%s: the reason does not name %s: %s", event, paths.Replace(s), reason)
				}
			}
			if !saysInstead(reason) {
				t.Errorf("%s: the reason does not say what to do instead: %s", event, reason)
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
