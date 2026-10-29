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
		{"owner's Write in the plans directory", false, `{"tool_name":"Write","agent_id":"a1","tool_input":{"file_path":"PLANS/notes.org"}}`, nil},
		{"owner's Write of a plan file", true, `{"tool_name":"Write","agent_id":"a1","tool_input":{"file_path":"PLANS/2026-10-15-x.box.P1.org"}}`, []string{"agent-plan step"}},
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
		{"owner's trash-scratch in main", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"trash-scratch new"}}`, nil},
		{"owner's trash-scratch sibling", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"trash-scratch SIBLING/new"}}`, nil},
		{"owner's trash-scratch own file", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"trash-scratch new"}}`, nil},
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
		// Programs that write into a directory they are given, or the one
		// they run in.
		{"owner's cp -t into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp -t MAIN a.go b.go"}}`, []string{"LINKED"}},
		{"owner's cp --target-directory into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp --target-directory=MAIN a.go SCRATCH/b.go"}}`, []string{"LINKED"}},
		{"owner's install -t into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"install -t MAIN a.go"}}`, []string{"LINKED"}},
		{"owner's tar -x -C into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"tar -x -C MAIN -f SCRATCH/a.tar"}}`, []string{"LINKED"}},
		{"owner's tar xf run in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"tar xf SCRATCH/a.tar"}}`, []string{"LINKED"}},
		{"owner's tar -x into scratch", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"git archive HEAD | tar -x -C SCRATCH"}}`, nil},
		{"owner's tar listing in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"tar -tf SCRATCH/a.tar"}}`, nil},
		{"owner's unzip in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"unzip -o SCRATCH/a.zip"}}`, []string{"LINKED"}},
		{"owner's patch in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"patch -d MAIN -p1 < SCRATCH/a.diff"}}`, []string{"LINKED"}},
		{"owner's gofmt -w in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gofmt -w MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's gofmt -l in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gofmt -l MAIN"}}`, nil},
		// Refused first under /nix, as mv and rm are.
		{"owner's chmod in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"chmod +x MAIN/a.sh"}}`, nil},
		{"owner's mkdir in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"mkdir -p MAIN/new"}}`, []string{"LINKED"}},
		{"owner's go generate in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"go generate ./..."}}`, []string{"LINKED"}},
		{"owner's go test profile in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"go test -coverprofile=MAIN/c.out ./..."}}`, []string{"LINKED"}},
		{"owner's go test in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"go test -count=1 ./..."}}`, nil},
		{"owner's nix build in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"nix build .#x"}}`, []string{"LINKED"}},
		{"owner's nix build --no-link in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"nix build --no-link .#x"}}`, nil},
		{"owner's make in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"make -C MAIN"}}`, []string{"LINKED"}},
		{"owner's make in its worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"make test"}}`, nil},
		{"unowned agent's mutant copy in scratch", false, `{"tool_name":"Bash","agent_id":"a2","cwd":"SCRATCH","tool_input":{"command":"git -C MAIN archive HEAD | tar -x -C SCRATCH/copy"}}`, nil},
		{"unowned agent's mutant copy over the main checkout", true, `{"tool_name":"Bash","agent_id":"a2","cwd":"SCRATCH","tool_input":{"command":"git -C MAIN archive HEAD | tar -x -C MAIN"}}`, []string{"a2"}},
		{"owner's cp -rt into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp -rt MAIN dir"}}`, []string{"LINKED"}},
		{"owner's ln -s run in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"ln -s SCRATCH/x"}}`, []string{"LINKED"}},
		{"owner's go build run in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"go build ./cmd/x"}}`, []string{"LINKED"}},
		{"owner's go build -o into scratch", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"go build -o SCRATCH/x ./..."}}`, nil},
		{"owner's go work init in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"go work init ."}}`, []string{"LINKED"}},
		{"owner's go install", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"go install ./cmd/x"}}`, []string{"LINKED"}},
		{"owner's go clean -cache", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"go clean -cache"}}`, []string{"LINKED"}},
		{"owner's nix fmt in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"nix fmt"}}`, []string{"LINKED"}},
		{"owner's nix run .#fmt in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"nix run .#fmt"}}`, []string{"LINKED"}},
		{"owner's coreutils fmt in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"fmt README.md"}}`, nil},
		{"owner's treefmt in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"treefmt"}}`, []string{"LINKED"}},
		{"owner's alejandra on the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"alejandra MAIN"}}`, []string{"LINKED"}},
		{"owner's alejandra --check on the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"alejandra --check MAIN"}}`, nil},
		{"owner's prettier -w in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"prettier -w MAIN/a.js"}}`, []string{"LINKED"}},
		{"owner's prettier without -w", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"prettier MAIN/a.js"}}`, nil},
		{"owner's black in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"black ."}}`, []string{"LINKED"}},
		{"owner's shfmt -w in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"shfmt -w MAIN/a.sh"}}`, []string{"LINKED"}},
		{"owner's ruff format in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"ruff format"}}`, []string{"LINKED"}},
		{"owner's ruff check", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"ruff check ."}}`, nil},
		{"owner's rustfmt in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"rustfmt MAIN/a.rs"}}`, []string{"LINKED"}},
		{"owner's sops -i in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"sops -i MAIN/s.yaml"}}`, []string{"LINKED"}},
		{"owner's make -n in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"make -n -C MAIN"}}`, nil},
		// Git's reading forms of its writing commands, anywhere.
		{"owner's tag -l in another's worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git -C SIBLING tag -l"}}`, nil},
		{"owner's bare tag in another's worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git -C SIBLING tag"}}`, nil},
		{"owner's notes list in another's worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git -C SIBLING notes list"}}`, nil},
		{"owner's apply --check in another's worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git -C SIBLING apply --check SCRATCH/a.diff"}}`, nil},
		{"owner's apply in another's worktree", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git -C SIBLING apply SCRATCH/a.diff"}}`, []string{"LINKED"}},
		{"main thread's tag --list in an owned worktree", false, `{"tool_name":"Bash","cwd":"MAIN","tool_input":{"command":"git -C LINKED tag --list"}}`, nil},
		{"main thread's stash show in an owned worktree", false, `{"tool_name":"Bash","cwd":"MAIN","tool_input":{"command":"git -C LINKED stash show"}}`, nil},
		// The commands git runs for itself are read as the shell's.
		{"owner's write in rebase --exec in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"git rebase --exec 'touch MAIN/x' HEAD~1"}}`, []string{"LINKED"}},
		{"owner's write in bisect run in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"git bisect run sh -c 'echo x > a.go'"}}`, []string{"LINKED"}},
		{"owner's touch in submodule foreach from the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"git submodule foreach 'touch a.go'"}}`, []string{"LINKED"}},
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
