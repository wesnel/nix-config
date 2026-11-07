package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

func TestSessionEdits(t *testing.T) {
	for _, tc := range []struct {
		name, session, target, branch, command, association, file, reason string
		// rebase stops a rebase of the linked worktree onto base on a
		// conflict in a.go: "merge" or "apply" names its backend; the
		// branch field, applied first, says what it rebases.
		rebase                                       string
		noPlan, newer, sessionOnly, fixture, reclaim bool
		deny                                         bool
	}{
		{name: "own task worktree", session: "s1"},
		{name: "remove requires absolute worktree path", session: "s1", command: "git worktree remove linked", deny: true},
		{name: "suffix precedes local symlink", session: "s1", command: "git worktree remove parked", deny: true},
		{name: "remove another session worktree by basename", session: "s1", command: "git worktree remove parked", deny: true},
		{name: "move another session worktree", session: "s1", command: "git worktree move PARKED SCRATCH/moved", deny: true},
		{name: "move own worktree", session: "s1", command: "git worktree move LINKED SCRATCH/moved"},
		{name: "move requires absolute worktree path", session: "s1", command: "git worktree move linked SCRATCH/moved", deny: true},
		{name: "move into another session worktree", session: "s1", command: "git worktree move LINKED PARKED/moved", deny: true},
		{name: "unknown worktree target", session: "s1", command: "git worktree remove absent", deny: true},
		{name: "remove through symlink parent", session: "s1", command: "git worktree remove MAIN/../hop/../linked", deny: true},
		{name: "file edit through symlink parent", session: "s1", file: "MAIN/../hop/../linked/a.go", deny: true},
		{name: "move into symlink parent", session: "s1", command: "git worktree move LINKED MAIN/../hop/../linked/moved", deny: true},
		{name: "create inside another worktree", session: "s1", command: "git worktree add PARKED/nested -b nested", deny: true},
		{name: "create orphan inside another worktree", session: "s1", command: "git worktree add --orphan PARKED/nested", deny: true},
		{name: "create named orphan inside another worktree", session: "s1", command: "git worktree add --orphan -b fresh PARKED/nested", deny: true},
		{name: "metadata without worktree", session: "s1", file: "MAIN/.git/info/exclude", deny: true, reason: "outside a working tree"},
		{name: "create with PWD inside another worktree", session: "s1", command: "git worktree add $PWD/../scratch/parked/nested -b nested", deny: true},
		{name: "stage with PWD in another worktree", session: "s1", command: "git -C $PWD/../scratch/parked add a.go", deny: true},
		{name: "another session", session: "s2", deny: true},
		{name: "missing session", deny: true},
		{name: "no plan", session: "s1", noPlan: true, deny: true},
		{name: "newest worktree association wins", session: "s1", newer: true, deny: true},
		{name: "newer session-only association is ignored", session: "s1", newer: true, sessionOnly: true},
		{name: "reclaimed worktree", session: "s1", reclaim: true},
		{name: "main owner is not every session", session: "s2", reclaim: true, deny: true},
		{name: "main checkout", session: "s1", target: "MAIN", deny: true},
		{name: "default branch", session: "s1", branch: "main", deny: true},
		{name: "remote default branch", session: "s1", branch: "trunk", deny: true},
		{name: "detached head", session: "s1", branch: "detached", deny: true},
		{name: "conflicted file mid-rebase of a task branch", session: "s1", rebase: "merge"},
		{name: "conflicted file mid-apply-rebase of a task branch", session: "s1", rebase: "apply"},
		{name: "mid-rebase of main", session: "s1", branch: "main", rebase: "merge", deny: true},
		{name: "mid-rebase of a detached head", session: "s1", branch: "detached", rebase: "merge", deny: true},
		{name: "mid-rebase of a task branch in another session", session: "s2", rebase: "merge", deny: true},
		{name: "rebase abort while detached", session: "s1", branch: "detached", command: "git rebase --abort"},
		{name: "rebase abort mid-rebase of main", session: "s1", branch: "main", rebase: "merge", command: "git rebase --abort"},
		{name: "rebase continue mid-rebase of main", session: "s1", branch: "main", rebase: "merge", command: "git -c commit.gpgsign=false rebase --continue"},
		{name: "rebase skip mid-rebase of main", session: "s1", branch: "main", rebase: "merge", command: "git -c commit.gpgsign=false rebase --skip"},
		{name: "cherry-pick abort on a default branch", session: "s1", branch: "main", command: "git cherry-pick --abort"},
		{name: "rebase abort in another session worktree", session: "s2", branch: "detached", command: "git rebase --abort"},
		{name: "rebase abort in the main checkout", session: "s1", target: "MAIN", command: "git rebase --abort"},
		{name: "abort does not allow what follows it", session: "s1", branch: "detached", command: "git rebase --abort && git add a.go", deny: true},
		{name: "rebase onto while detached", session: "s1", branch: "detached", command: "git -c commit.gpgsign=false rebase base", deny: true},
		{name: "rebase abort with another argument", session: "s1", branch: "detached", command: "git rebase --abort a.go", deny: true},
		{name: "scratch linked worktree still needs a plan", session: "s1", target: "PARKED", noPlan: true, deny: true},
		{name: "scratch repository fixture", target: "FIXTURE", fixture: true, noPlan: true},
		{name: "scratch fixture with a plan is protected", session: "s2", target: "FIXTURE", fixture: true, deny: true},
		{name: "create worktree from main", target: "MAIN", command: "git worktree add --no-track SCRATCH/new -b new"},
		{name: "creation does not allow staging in main", target: "MAIN", command: "git worktree add --no-track SCRATCH/new -b new && git add a.go", deny: true},
		{name: "creation does not allow redirection into main", target: "MAIN", command: "git worktree add --no-track SCRATCH/new -b new > MAIN/out", deny: true},
		{name: "stage in own worktree", session: "s1", command: "git add a.go"},
		{name: "repository flags require explicit cwd", session: "s1", command: "git --git-dir=MAIN/.git --work-tree=MAIN checkout -- a.go", deny: true},
		{name: "repository environment requires explicit cwd", session: "s1", command: "GIT_DIR=MAIN/.git GIT_WORK_TREE=MAIN git checkout -- a.go", deny: true},
		{name: "stage in another session worktree", session: "s2", command: "git add a.go", deny: true},
		{name: "remove own worktree from main", session: "s1", target: "MAIN", association: "LINKED", command: "git worktree remove LINKED"},
		{name: "remove another session worktree from main", session: "s2", target: "MAIN", association: "LINKED", command: "git worktree remove LINKED", deny: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			main, linked, scratch := filepath.Join(root, "main"), filepath.Join(root, "linked"), filepath.Join(root, "scratch")
			os.MkdirAll(scratch, 0o755)
			os.Mkdir(main, 0o755)
			old := scratchRoots
			scratchRoots = func() []string { return []string{scratch} }
			t.Cleanup(func() { scratchRoots = old })
			gitIn(t, main, "init", "-q", "-b", "base")
			gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
			gitIn(t, main, "worktree", "add", "-q", "-b", "task", linked)
			parked, fixture := filepath.Join(scratch, "parked"), filepath.Join(scratch, "fixture")
			linkParent := strings.Contains(tc.command, "hop") || strings.Contains(tc.file, "hop")
			other := strings.Contains(tc.command, "PARKED") || strings.Contains(tc.command, "parked") || linkParent
			if linkParent {
				parked = filepath.Join(scratch, "linked")
				if err := os.Mkdir(filepath.Join(scratch, "sub"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(scratch, "sub"), filepath.Join(root, "hop")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.target == "PARKED" || other {
				gitIn(t, main, "worktree", "add", "-q", "-b", "parked", parked)
			}
			if tc.name == "suffix precedes local symlink" {
				if err := os.Symlink(linked, filepath.Join(linked, "parked")); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fixture {
				os.Mkdir(fixture, 0o755)
				gitIn(t, fixture, "init", "-q", "-b", "main")
			}
			if tc.rebase != "" {
				os.WriteFile(filepath.Join(linked, "a.go"), []byte("task\n"), 0o644)
				gitIn(t, linked, "add", "a.go")
				gitIn(t, linked, "commit", "-q", "-m", "task")
				os.WriteFile(filepath.Join(main, "a.go"), []byte("base\n"), 0o644)
				gitIn(t, main, "add", "a.go")
				gitIn(t, main, "commit", "-q", "-m", "base change")
			}
			if tc.branch == "detached" {
				gitIn(t, linked, "checkout", "--detach")
			} else if tc.branch != "" {
				gitIn(t, linked, "branch", "-m", tc.branch)
				if tc.branch == "trunk" {
					gitIn(t, main, "update-ref", "refs/remotes/upstream/trunk", "HEAD")
					gitIn(t, main, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/trunk")
				}
			}
			if tc.rebase != "" {
				cmd := exec.Command("git", "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false",
					"rebase", "--"+tc.rebase, "base")
				cmd.Dir, cmd.Env = linked, gitenv.Without(os.Environ())
				if out, err := cmd.CombinedOutput(); err == nil {
					t.Fatalf("the rebase did not stop on its conflict:\n%s", out)
				}
				if current := gitIn(t, linked, "branch", "--show-current"); current != "" {
					t.Fatalf("mid-rebase HEAD is on %q, want it detached", current)
				}
			}
			paths := strings.NewReplacer("MAIN", main, "LINKED", linked, "PARKED", parked, "FIXTURE", fixture, "SCRATCH", scratch)
			target := linked
			if tc.target != "" {
				target = paths.Replace(tc.target)
			}
			c := plans.Config{Dir: filepath.Join(root, "plans"), Host: "box"}
			config := filepath.Join(root, "config.json")
			data, _ := json.Marshal(map[string]any{"plans": c})
			os.WriteFile(config, data, 0o600)
			t.Setenv("AGENT_TOOLS_CONFIG", config)
			now := time.Now()
			if other {
				if _, err := c.New(plans.NewOptions{Title: "Other", Slug: "other", Worktrees: []string{parked}, Sessions: []string{"codex:s2"}}, now.Add(-time.Second)); err != nil {
					t.Fatal(err)
				}
			}
			if !tc.noPlan {
				association := target
				if tc.association != "" {
					association = paths.Replace(tc.association)
				}
				made, err := c.New(plans.NewOptions{Title: "Task", Slug: "task", Worktrees: []string{association}, Sessions: []string{"codex:s1"}}, now)
				if err != nil {
					t.Fatal(err)
				}
				if tc.reclaim {
					p, err := c.Open(made.Plan)
					if err != nil {
						t.Fatal(err)
					}
					for _, owner := range []string{"worker", "main"} {
						if _, err := p.SetOwner(target, owner, now); err != nil {
							t.Fatal(err)
						}
					}
				}
				if tc.newer {
					o := plans.NewOptions{Title: "Newer", Slug: "newer", Worktrees: []string{target}, Sessions: []string{"codex:s2"}}
					if tc.sessionOnly {
						o.Worktrees, o.Sessions = []string{main}, []string{"codex:s1"}
					}
					if _, err := c.New(o, now.Add(time.Second)); err != nil {
						t.Fatal(err)
					}
				}
			}
			event := map[string]any{"tool_name": "Edit", "session_id": tc.session, "cwd": target,
				"tool_input": map[string]any{"file_path": filepath.Join(target, "a.go")}}
			if tc.file != "" {
				event["tool_input"] = map[string]any{"file_path": paths.Replace(tc.file)}
			}
			if tc.command != "" {
				event["tool_name"], event["tool_input"] = "Bash", map[string]any{"command": paths.Replace(tc.command)}
			}
			data, _ = json.Marshal(event)
			var out bytes.Buffer
			Main([]string{"--baseline"}, bytes.NewReader(data), &out, io.Discard)
			if denied := strings.Contains(out.String(), `"deny"`); denied != tc.deny {
				t.Fatalf("deny=%v, want %v: %s", denied, tc.deny, out.String())
			}
			if tc.reason != "" && !strings.Contains(out.String(), tc.reason) {
				t.Fatalf("want reason containing %q: %s", tc.reason, out.String())
			}
		})
	}
}

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
		t.Setenv("TMPDIR", scratch)

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
		{"main thread's Edit in the main checkout", true, `{"tool_name":"Edit","tool_input":{"file_path":"MAIN/a.go"}}`, []string{"worktree"}},
		{"main thread's shell write in the main checkout", true, `{"tool_name":"Bash","cwd":"MAIN","tool_input":{"command":"echo x > new.txt"}}`, []string{"worktree"}},
		{"main thread's Write in an owned worktree", true, `{"tool_name":"Write","tool_input":{"file_path":"LINKED/sub/new.go"}}`, []string{"a1", "agent-plan own"}},
		{"main thread's removal of a delegated worktree", true, `{"tool_name":"Bash","cwd":"MAIN","tool_input":{"command":"git worktree remove LINKED"}}`, []string{"a1", "agent-plan own"}},
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
		// gofmt's and goimports' option values are no files, but for the
		// profile and trace they write.
		{"owner's gofmt -r -w in its worktree, run in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"gofmt -r 'gh.GraphQL -> graphQL' -w LINKED/a.go"}}`, nil},
		{"owner's goimports -local -w in its worktree, run in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"goimports -local github.com/x -w LINKED/a.go"}}`, nil},
		{"owner's gofmt --r -w in its worktree, run in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"gofmt --r 'a -> b' -w LINKED/a.go"}}`, nil},
		{"owner's gofmt with -w after its file, run in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"gofmt -l LINKED/a.go -w"}}`, nil},
		{"owner's gofmt -w -r missing its value, run in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"gofmt -w -r"}}`, nil},
		{"owner's gofmt -r -w in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gofmt -r 'a -> b' -w MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's gofmt -w profile into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gofmt -w -cpuprofile MAIN/cpu.out a.go"}}`, []string{"LINKED"}},
		{"owner's gofmt profile into the main checkout without -w", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gofmt -cpuprofile MAIN/cpu.out a.go"}}`, []string{"LINKED"}},
		{"owner's gofmt -l without -w in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gofmt -l MAIN/a.go"}}`, nil},
		{"owner's goimports -w trace into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"goimports -w -trace MAIN/t.out a.go"}}`, []string{"LINKED"}},
		{"owner's gofmt -w memory profile into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gofmt -w --memprofile=MAIN/m.out a.go"}}`, []string{"LINKED"}},
		{"owner's gofmt -r of a channel rule in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"gofmt -w -r '<-a -> <-b' MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's goimports -local of a redirection-like prefix, quiet", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"goimports -w -local '>x' MAIN/a.go 2>/dev/null"}}`, []string{"LINKED"}},
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
		// A redirection is no operand, and an operand is read as the shell
		// expands it: $TMPDIR and $HOME are known, any other variable is
		// the shell's to decide.
		{"owner's mkdir in scratch, quiet, run in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"bash -c 'mkdir SCRATCH/lock 2>/dev/null'"}}`, nil},
		{"owner's cp into the main checkout, quiet", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp a.go MAIN/a.go 2>/dev/null"}}`, []string{"LINKED"}},
		// A quoted operator is no redirection: it neither takes the real
		// redirection's target nor hides the operand after it.
		{"owner's write of a quoted > into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"echo '>' > MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's write of a quoted 2> into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"echo '2>' > MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's tr of a quoted < into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"tr , '<' > MAIN/out.csv"}}`, []string{"LINKED"}},
		{"owner's write of a quoted > into the main checkout under bash -c", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"bash -c 'echo '\\''>'\\'' > MAIN/a.go'"}}`, []string{"LINKED"}},
		{"owner's cp of a file named < into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp '<' MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's touch after a file named < in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"touch '<' MAIN/a.go"}}`, []string{"LINKED"}},
		{"main thread's write of a quoted > into an owned worktree", true, `{"tool_name":"Bash","cwd":"MAIN","tool_input":{"command":"echo '>' > LINKED/a.go"}}`, []string{"a1", "agent-plan own"}},
		// $PWD and $(pwd) are where the command runs; a path is judged up to
		// its first part the shell decides, whole where a .. follows that
		// part, and where it starts with a variable, as the directory the
		// command runs in.
		{"owner's cp to $PWD in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"cp a.go $PWD/a.go"}}`, []string{"LINKED"}},
		{"owner's write to $PWD in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"echo x > $PWD/a.go"}}`, []string{"LINKED"}},
		{"owner's cp to $(pwd) in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"cp a.go $(pwd)/a.go"}}`, []string{"LINKED"}},
		{"owner's cp to the top of the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"cp a.go $(git rev-parse --show-toplevel)/a.go"}}`, []string{"LINKED"}},
		{"owner's cp to a variable's name in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp a.go MAIN/$x.go"}}`, []string{"LINKED"}},
		{"owner's cp to a braced variable's name in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp a.go MAIN/${name}.go"}}`, []string{"LINKED"}},
		{"owner's cp in a loop into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"for f in a b; do cp $f.go MAIN/$f.go; done"}}`, []string{"LINKED"}},
		{"owner's cp to $PWD in its worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp a.go $PWD/b.go"}}`, nil},
		{"owner's cp out of a variable's directory into its worktree", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp a.go MAIN/$x/../../linked/a.go"}}`, nil},
		{"owner's cp out of a variable's directory into another's worktree", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cp a.go MAIN/$x/../../sibling/a.go"}}`, []string{"LINKED"}},
		{"owner's touch in a loop in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"for f in a b; do touch $f.go; done"}}`, []string{"LINKED"}},
		{"owner's cp in a loop into directories of the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"for d in x y; do cp a.go $d/a.go; done"}}`, []string{"LINKED"}},
		{"owner's touch of a variable's file in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"touch $f"}}`, []string{"LINKED"}},
		{"owner's touch -- of a variable's file in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"touch -- $f"}}`, []string{"LINKED"}},
		{"owner's write to a variable's file in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"echo x > $out"}}`, []string{"LINKED"}},
		{"owner's write to a quoted variable's file in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"echo x > \"$out\""}}`, []string{"LINKED"}},
		{"owner's : > of a variable's file in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":": > $f"}}`, []string{"LINKED"}},
		{"owner's bare > of a variable's file in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"> $f"}}`, []string{"LINKED"}},
		{"owner's touch under $OLDPWD in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"touch $OLDPWD/a.go"}}`, []string{"LINKED"}},
		{"owner's touch of a variable's file after cd into the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"cd MAIN && touch $f"}}`, []string{"LINKED"}},
		{"main thread's touch of a variable's file in an owned worktree", true, `{"tool_name":"Bash","cwd":"LINKED","tool_input":{"command":"touch $f"}}`, []string{"a1", "agent-plan own"}},
		{"owner's mkdir under $TMPDIR, run in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"mkdir \"$TMPDIR/lock\""}}`, nil},
		{"owner's log under $TMPDIR, run in the main checkout", false, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"bash -c 'go vet ./... 2>\"$TMPDIR/vet.log\"'"}}`, nil},
		{"owner's mkdir of a variable's path, run in the main checkout", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"mkdir \"$L\""}}`, []string{"LINKED"}},
		// A redirection before the program is judged where it is, and the
		// program after it by its name.
		{"owner's write into the main checkout, redirected first", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":">MAIN/a.go echo x"}}`, []string{"LINKED"}},
		{"owner's write into the main checkout, redirected before env", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"> MAIN/a.go env X=1 echo x"}}`, []string{"LINKED"}},
		{"owner's write into the main checkout, redirected before bash -c", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"> MAIN/a.go bash -c true"}}`, []string{"LINKED"}},
		{"owner's write into the main checkout, redirected before eval", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"> MAIN/a.go eval true"}}`, []string{"LINKED"}},
		{"owner's write into the main checkout, redirected before a variable's program", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":">MAIN/a.go \"$X\""}}`, []string{"LINKED"}},
		{"owner's write into the main checkout, redirected before command -v", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":">MAIN/go-path.txt command -v go"}}`, []string{"LINKED"}},
		{"owner's write into the main checkout, redirected last", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"echo x >MAIN/a.go"}}`, []string{"LINKED"}},
		{"owner's mkdir in the main checkout after a redirection", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"2>/dev/null mkdir MAIN/new"}}`, []string{"LINKED"}},
		{"owner's mkdir in the main checkout after a redirection to a variable", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"2>\"$TMPDIR/e\" mkdir MAIN/new"}}`, []string{"LINKED"}},
		{"owner's redirection into the main checkout before cd", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"2>MAIN/cd.err cd LINKED"}}`, []string{"LINKED"}},
		{"owner's git commit run in the main checkout after a redirection", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"MAIN","tool_input":{"command":"< /dev/null git commit -m x"}}`, []string{"LINKED"}},
		{"owner's touch under $HOME", true, `{"tool_name":"Bash","agent_id":"a1","cwd":"LINKED","tool_input":{"command":"touch \"$HOME/x\""}}`, []string{"LINKED"}},
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
