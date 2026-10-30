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

// TestPendingPlanReview covers the edits the baseline hook holds while the
// session's plan has a design review of the plan started and not yet
// answered with plan-reviewed. Each case starts from a fresh plan, for
// session s1, and its own scratch root.
func TestPendingPlanReview(t *testing.T) {
	// reviewing makes the plan and returns its repository directory, a
	// scratch directory, the plan, and a function that runs the hook on a
	// call of tool with input and returns its decision and reason.
	reviewing := func(t *testing.T) (repo, scratch string, p plans.Plan, call func(session, tool string, input map[string]string, agent ...string) (string, string)) {
		t.Helper()
		root, _ := filepath.EvalSymlinks(t.TempDir())
		repo, scratch = filepath.Join(root, "repo"), filepath.Join(root, "scratch")
		os.Mkdir(repo, 0o755)
		os.Mkdir(scratch, 0o755)
		gitIn(t, repo, "init", "-q", "-b", "main")
		old := scratchRoots
		scratchRoots = func() []string { return []string{scratch} }
		t.Cleanup(func() { scratchRoots = old })
		c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
		config := filepath.Join(root, "config.json")
		data, _ := json.Marshal(map[string]any{"plans": c})
		os.WriteFile(config, data, 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		made, err := c.New(plans.NewOptions{Title: "Add the loop", Slug: "loop", Sessions: []string{"claude:s1"}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if p, err = c.Open(made.Plan); err != nil {
			t.Fatal(err)
		}
		call = func(session, tool string, input map[string]string, agent ...string) (string, string) {
			t.Helper()
			fields := map[string]any{"session_id": session, "tool_name": tool, "cwd": repo, "tool_input": input}
			if len(agent) > 0 {
				fields["agent_id"] = agent[0]
			}
			event, _ := json.Marshal(fields)
			var out bytes.Buffer
			Main([]string{"--baseline"}, bytes.NewReader(event), &out, io.Discard)
			var got struct {
				Specific struct {
					Decision string `json:"permissionDecision"`
					Reason   string `json:"permissionDecisionReason"`
				} `json:"hookSpecificOutput"`
			}
			json.Unmarshal(out.Bytes(), &got)
			return got.Specific.Decision, got.Specific.Reason
		}
		return repo, scratch, p, call
	}
	record := func(t *testing.T, p plans.Plan, kind string) {
		t.Helper()
		if _, err := p.Record("", plans.Event{Kind: kind, Session: "s1", Summary: "Review plan X7"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	edit := func(path string) map[string]string {
		return map[string]string{"file_path": path, "content": "x"}
	}

	t.Run("refused while the review is pending", func(t *testing.T) {
		repo, _, p, call := reviewing(t)
		record(t, p, plans.DesignReviewStarted)
		decision, reason := call("s1", "Write", edit(filepath.Join(repo, "a.go")))
		if decision != "deny" || !strings.Contains(reason, "plan-reviewed") || !strings.Contains(reason, p.File()) ||
			!strings.Contains(reason, "Review plan X7") || !strings.Contains(reason, "--item "+p.Name+" ") || !saysInstead(reason) {
			t.Errorf("decision %q, reason %q", decision, reason)
		}
	})

	t.Run("a shell write refused while pending", func(t *testing.T) {
		repo, _, p, call := reviewing(t)
		record(t, p, plans.DesignReviewStarted)
		decision, reason := call("s1", "Bash", map[string]string{"command": "echo x > " + filepath.Join(repo, "b.txt")})
		if decision != "deny" || !strings.Contains(reason, "plan-reviewed") {
			t.Errorf("decision %q, reason %q", decision, reason)
		}
	})

	t.Run("scratch allowed while pending", func(t *testing.T) {
		_, scratch, p, call := reviewing(t)
		record(t, p, plans.DesignReviewStarted)
		if decision, reason := call("s1", "Write", edit(filepath.Join(scratch, "notes.md"))); decision == "deny" {
			t.Errorf("decision %q, reason %q", decision, reason)
		}
	})

	t.Run("allowed once plan-reviewed is logged", func(t *testing.T) {
		repo, _, p, call := reviewing(t)
		record(t, p, plans.DesignReviewStarted)
		if _, err := p.Log(plans.Event{Kind: "plan-reviewed", Item: p.Name, Summary: "no findings"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if decision, reason := call("s1", "Write", edit(filepath.Join(repo, "a.go"))); decision == "deny" {
			t.Errorf("decision %q, reason %q", decision, reason)
		}
	})

	t.Run("allowed with no review started", func(t *testing.T) {
		repo, _, _, call := reviewing(t)
		if decision, reason := call("s1", "Write", edit(filepath.Join(repo, "a.go"))); decision == "deny" {
			t.Errorf("decision %q, reason %q", decision, reason)
		}
	})

	// A command whose code the guard cannot read could write anywhere, so
	// while the review is pending it is held as writing its working
	// directory; commands the guard reads, and runs in scratch, are not.
	t.Run("code the guard cannot read", func(t *testing.T) {
		for _, c := range []struct {
			command string
			held    bool
		}{
			{"./gen.sh", true},
			{"bash gen.sh", true},
			{"python3 gen.py", true},
			// Options that give no code, and a script's own arguments.
			{"python3 -E gen.py", true},
			{"python3 gen.py -c x", true},
			{"perl -p gen.pl", true},
			{"ruby -E UTF-8 gen.rb", true},
			{"go -C tools run ./cmd/gen", true},
			{"cargo +nightly run", true},
			{"cargo r", true},
			{"source ./setup.sh", true},
			{". ./setup.sh", true},
			{"emacsclient-30 --eval '(save-buffer)'", true},
			// Run from scratch, the script is still the repository's.
			{"cd SCRATCH && REPO/gen.sh", true},
			{"cd SCRATCH && python3 REPO/gen.py", true},
			{"cd SCRATCH && python3 -W ignore REPO/gen.py", true},
			{"cd SCRATCH && python3 < REPO/gen.py", true},
			{"cd SCRATCH && bash < REPO/gen.sh", true},
			{"cd SCRATCH && go -C REPO run ./cmd/gen", true},
			{"cd SCRATCH && python3 probe.py", false},
			{"cd SCRATCH && go run REPO/gen.go", true},
			{"cd SCRATCH && uv run REPO/gen.py", true},
			{`cd "$X" && python3 p.py`, true},
			// Inline code, bundled or after an option's value, or on
			// standard input from the line itself.
			{"perl -ne 'print' a.txt", false},
			{"node -pe '1+1'", false},
			{"python3 -W ignore -c 'print(1)'", false},
			{"python3 <<< 'print(1)'", false},
			{"python3 -I -c 'print(1)'", false},
			{"perl -W -e 'print 1'", false},
			{"node --eval='1'", false},
			{"cd SCRATCH && go -C REPO generate ./...", true},
			{"bash <<< 'echo hi'", false},
			{"cargo --config net.offline=true run", true},
			{"cargo --color always run", true},
			{"cargo -Z unstable-options run", true},
			{"python3.12 gen.py", true},
			{"pypy3 gen.py", true},
			{"uv run scripts/gen.py", true},
			{"poetry run gen", true},
			{"uv pip list", false},
			{"cargo build", false},
			{"node -e 'console.log(1)'", false},
			{"perl -E 'say 1'", false},
			{"go run ./cmd/x", true},
			{"cargo run --bin x", true},
			{"npm run build", true},
			{"just gen", true},
			{"emacsclient --eval '(save-buffer)'", true},
			{"vim -c wq a.go", true},
			{"$TOOL build", true},
			{"env FOO=1 ./gen.sh", true},
			{"git status", false},
			{"go test ./...", false},
			{"go vet ./...", false},
			{"nix build --no-link .#x", false},
			{"agent-plan log PLAN --item S1 --kind started", false},
			{"bash -c 'git status'", false},
			{"python3 -c 'print(1)'", false},
			{"cd SCRATCH && ./probe.sh", false},
		} {
			t.Run(c.command, func(t *testing.T) {
				repo, scratch, p, call := reviewing(t)
				record(t, p, plans.DesignReviewStarted)
				// The directories hold the test's name, which may hold && or
				// spaces: quoted, they stay one word.
				command := strings.NewReplacer("SCRATCH", "'"+scratch+"'", "REPO", "'"+repo+"'").Replace(c.command)
				decision, reason := call("s1", "Bash", map[string]string{"command": command})
				held := decision == "deny" && strings.Contains(reason, "code the guard cannot read")
				if held != c.held {
					t.Errorf("held %v, want %v: decision %q, reason %q", held, c.held, decision, reason)
				}
				// The reason names where the run is, or says it is unknown.
				if held && (!strings.Contains(reason, "plan-reviewed") || !saysInstead(reason) || strings.Contains(reason, "from . ")) {
					t.Errorf("reason %q", reason)
				}
			})
		}
		// The same run is free once the review is answered: the hold, not
		// another rule, refused it.
		_, _, p, call := reviewing(t)
		record(t, p, plans.DesignReviewStarted)
		if _, err := p.Log(plans.Event{Kind: "plan-reviewed", Item: p.Name, Summary: "no findings"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if decision, reason := call("s1", "Bash", map[string]string{"command": "./gen.sh"}); decision == "deny" {
			t.Errorf("after plan-reviewed: decision %q, reason %q", decision, reason)
		}
	})

	// Both sessions are listed in the plan; only the one that started the
	// review is held.
	t.Run("another session of the same plan allowed", func(t *testing.T) {
		repo, _, _, call := reviewing(t)
		c, err := plans.Load()
		if err != nil {
			t.Fatal(err)
		}
		made, err := c.New(plans.NewOptions{Title: "Share the loop", Slug: "shared", Sessions: []string{"claude:s1", "claude:s9"}},
			time.Now())
		if err != nil {
			t.Fatal(err)
		}
		shared, err := c.Open(made.Plan)
		if err != nil {
			t.Fatal(err)
		}
		record(t, shared, plans.DesignReviewStarted)
		if decision, reason := call("s9", "Write", edit(filepath.Join(repo, "a.go"))); decision == "deny" {
			t.Errorf("s9: decision %q, reason %q", decision, reason)
		}
		if decision, _ := call("s1", "Write", edit(filepath.Join(repo, "a.go"))); decision != "deny" {
			t.Errorf("s1, which started it: decision %q, want deny", decision)
		}
	})

	// A session listed in several plans is held by a review pending in any
	// of them: here the older plan, which a lookup of the session's newest
	// plan alone would miss.
	t.Run("a review pending in the session's older plan holds", func(t *testing.T) {
		repo, _, older, call := reviewing(t)
		c, err := plans.Load()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.New(plans.NewOptions{Title: "Fix the loop", Slug: "fix-loop", Sessions: []string{"claude:s1"}},
			time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		record(t, older, plans.DesignReviewStarted)
		decision, reason := call("s1", "Write", edit(filepath.Join(repo, "a.go")))
		if decision != "deny" || !strings.Contains(reason, older.File()) {
			t.Errorf("decision %q, reason %q", decision, reason)
		}
	})

	// A sub-agent is held to its own worktree already; the coordinator's
	// pending review does not stall it there.
	t.Run("a sub-agent's edit in its own worktree allowed", func(t *testing.T) {
		repo, _, p, call := reviewing(t)
		if _, err := p.SetOwner(repo, "a1", time.Now()); err != nil {
			t.Fatal(err)
		}
		record(t, p, plans.DesignReviewStarted)
		if decision, reason := call("s1", "Write", edit(filepath.Join(repo, "a.go")), "a1"); decision == "deny" {
			t.Errorf("decision %q, reason %q", decision, reason)
		}
		// The main thread is still held, outside any worktree an agent owns,
		// so the allowance above is the sub-agent's.
		other := filepath.Join(filepath.Dir(repo), "other")
		os.Mkdir(other, 0o755)
		if decision, reason := call("s1", "Write", edit(filepath.Join(other, "b.go"))); decision != "deny" ||
			!strings.Contains(reason, "plan-reviewed") {
			t.Errorf("the main thread's edit: decision %q, reason %q; want this rule's deny", decision, reason)
		}
	})
}
