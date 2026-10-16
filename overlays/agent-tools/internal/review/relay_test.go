package review

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/relay"
)

// relayedFixture is setUp's repository as github.com/o/r with origin/main
// at its base, whose review loop registered branch loop as pull request 7
// and unplanned as 8, and gave loop its worktree, f.repo, where the test
// runs; a plan that names loop alone; and Main running as a relay's
// program on the host.
func relayedFixture(t *testing.T) (*fixture, plans.Plan) {
	t.Helper()
	f := setUp(t, false)
	run(t, f.repo, "git", "branch", "loop")
	run(t, f.repo, "git", "branch", "unplanned")
	run(t, f.repo, "git", "switch", "-q", "--detach")
	run(t, f.repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
	run(t, f.repo, "git", "update-ref", "refs/remotes/origin/main", f.base)
	os.MkdirAll(filepath.Join(f.repo, ".git", "pr-loop"), 0o755)
	os.WriteFile(filepath.Join(f.repo, ".git", "pr-loop", "state.json"), []byte(`{"repo": "o/r", "order": ["loop", "unplanned"],
		"branches": {"loop": {"pr": 7, "base": "main"}, "unplanned": {"pr": 8, "base": "main"}}}`), 0o644)
	f.repo = loopWorktree(t, f.repo, "loop", "7")
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, must(json.Marshal(map[string]any{"plans": c})), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	made, err := c.New(plans.NewOptions{Title: "Grow the code", Slug: "grow", Branches: []string{"main..loop"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(relay.RelayEnv, "1")
	return f, must(c.Open(made.Plan))
}

// loopWorktree checks branch out where `pr-loop worktrees` puts pull
// request number's, in the repository whose main worktree is main, and
// moves the test there.
func loopWorktree(t *testing.T, main, branch, number string) string {
	t.Helper()
	dir := filepath.Join(main, ".pr-loop", number)
	run(t, main, "git", "worktree", "add", "-q", dir, branch)
	t.Chdir(dir)
	return dir
}

// relayed runs Main as the relay runs it, with inputs, the files a client
// read, as its stdin.
func relayed(t *testing.T, inputs map[string]string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := Main(t.Context(), args, strings.NewReader(string(must(json.Marshal(inputs)))), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// Through the relay, a review loop prints a review's prompt and checks
// its reply on the host, whose plan for the branch takes the issued
// prompt, the routes and the receipts; the files come on stdin.
func TestRelayedReview(t *testing.T) {
	goal := map[string]string{"goal": "Grow the code to three lines.", "top": "/sandbox/view"}

	t.Run("security review", func(t *testing.T) {
		f, p := relayedFixture(t)
		code, prompt, stderr := relayed(t, goal, "--print-prompt", "--focus", "security", "--goal", "-", f.base+"..HEAD")
		m := nonce.FindStringSubmatch(prompt)
		if code != 0 || m == nil || !strings.Contains(prompt, "Grow the code to three lines.") ||
			!strings.Contains(prompt, "the repository at /sandbox/view") {
			t.Fatalf("exit %d\n%s\n%s", code, prompt, stderr)
		}
		if issued := eventsOf(t, p, "review-issued"); len(issued) != 1 || issued[0].Nonce != m[1] {
			t.Fatalf("issued %+v", issued)
		}
		reply := `{"findings": [], "unverified": [], "checked": ` + checkedAll(t, "") + `, "tip": "` + f.tip + `", "issued": "` + m[1] + `"}`
		code, out, stderr := relayed(t, map[string]string{"reply": reply}, "--check", "-", f.base+"..HEAD")
		if code != 0 || !strings.Contains(out, `"focus": "security"`) || len(eventsOf(t, p, "route-receipt")) != 1 {
			t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
		}
	})

	t.Run("general review routes", func(t *testing.T) {
		f, p := relayedFixture(t)
		os.WriteFile(filepath.Join(f.repo, "run.go"), []byte("package main\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n"), 0o644)
		run(t, f.repo, "git", "add", "run.go")
		run(t, f.repo, "git", "commit", "-q", "-m", "Run a helper")
		tip := run(t, f.repo, "git", "rev-parse", "HEAD")
		code, prompt, stderr := relayed(t, goal, "--reviewer", "subagent", "--goal", "-", f.base+"..HEAD")
		m := nonce.FindStringSubmatch(prompt)
		if code != 0 || m == nil {
			t.Fatalf("exit %d\n%s\n%s", code, prompt, stderr)
		}
		reply := `{"findings": [], "unverified": [], "tip": "` + tip + `", "issued": "` + m[1] + `"}`
		code, out, stderr := relayed(t, map[string]string{"reply": reply}, "--check", "-", f.base+"..HEAD")
		if code != 0 || len(eventsOf(t, p, "review-receipt")) != 1 || len(eventsOf(t, p, "routed")) != 1 {
			t.Fatalf("exit %d\n%s\n%s", code, out, stderr)
		}
	})

	// The relay waits for its program however long it runs, so a relayed
	// review bounds its own work, red-check in the VM above all.
	t.Run("deadline", func(t *testing.T) {
		f, p := relayedFixture(t)
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		config := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(config, must(json.Marshal(map[string]any{"plans": plans.Config{Dir: p.Dir, Host: "box"},
			"repos": map[string]any{"o/r": map[string]any{"test": "true", "testFiles": []string{"*_test.txt"}}}})), 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		var deadline time.Time
		var bounded bool
		old := RedCheckInVM
		t.Cleanup(func() { RedCheckInVM = old })
		RedCheckInVM = func(ctx context.Context, top, base, tip string, log io.Writer) (*VMResult, error) {
			deadline, bounded = ctx.Deadline()
			return &VMResult{Text: "red-check from the VM"}, nil
		}
		start := time.Now()
		if code, _, stderr := relayed(t, goal, "--reviewer", "subagent", "--goal", "-", f.base+"..HEAD"); code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		if !bounded || deadline.After(start.Add(relayedTimeout+time.Minute)) {
			t.Errorf("red-check ran with deadline %v, %v", bounded, deadline)
		}
	})

	// A relayed review prints and checks, and nothing else: it never
	// runs Codex or opens a path its caller names on the host.
	t.Run("refused", func(t *testing.T) {
		f, _ := relayedFixture(t)
		given := f.base + "..HEAD"
		for what, args := range map[string][]string{
			"a goal file":  {"--print-prompt", "--goal", ".git/goal.md", given},
			"a reply file": {"--check", ".git/goal.md", given},
			"a Codex run":  {"--goal", "-", given},
			"a model":      {"--print-prompt", "--model", "m", "--goal", "-", given},
			"ask":          {"ask", "--question", "-", "code.txt"},
		} {
			code, _, stderr := relayed(t, goal, args...)
			refused(t, what, code, stderr)
		}
	})

	// Without a plan for the branch, a loop is told to hand back, since
	// it cannot start one.
	t.Run("no plan", func(t *testing.T) {
		f, _ := relayedFixture(t)
		loopWorktree(t, filepath.Dir(filepath.Dir(f.repo)), "unplanned", "8")
		code, _, stderr := relayed(t, goal, "--print-prompt", "--focus", "security", "--goal", "-", f.base+"..HEAD")
		refused(t, "no plan", code, stderr)
		if !strings.Contains(stderr, "hand back to the operator") || strings.Contains(stderr, "agent-plan new") {
			t.Errorf("stderr %s", stderr)
		}
	})

	// A loop reviews its own branches, each in the worktree the loop gave
	// it: never another branch's range, whose receipt would cover that
	// branch, nor from another worktree, whose plan would take it.
	t.Run("not the loop's", func(t *testing.T) {
		f, p := relayedFixture(t)
		main := filepath.Dir(filepath.Dir(f.repo))
		other := filepath.Join(main, ".claude", "worktrees", "other")
		run(t, main, "git", "worktree", "add", "-q", "-b", "other", other, "loop")
		os.WriteFile(filepath.Join(other, "other.txt"), []byte("x\n"), 0o644)
		run(t, other, "git", "add", "other.txt")
		run(t, other, "git", "commit", "-q", "-m", "Add other")
		c := plans.Config{Dir: p.Dir, Host: "box"}
		mains, err := c.New(plans.NewOptions{Title: "Add other", Slug: "other", Branches: []string{"main..other"},
			Worktrees: []string{other}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for what, at := range map[string]struct{ dir, given string }{
			"another branch's range":    {f.repo, f.base + "..other"},
			"another branch's worktree": {other, f.base + "..HEAD"},
			"the main worktree":         {main, f.base + "..loop"},
		} {
			t.Chdir(at.dir)
			code, _, stderr := relayed(t, goal, "--print-prompt", "--focus", "security", "--goal", "-", at.given)
			refused(t, what, code, stderr)
		}
		for _, plan := range []plans.Plan{p, must(c.Open(mains.Plan))} {
			if issued := eventsOf(t, plan, "review-issued"); len(issued) != 0 {
				t.Errorf("%s: issued %+v", plan.Name, issued)
			}
		}
	})
}

// In a sandbox with a relay, a prompt's printing and a reply's check go to
// the relay, with the files read where they are named; a Codex run does
// not.
func TestRelayClient(t *testing.T) {
	f := setUp(t, false)
	os.WriteFile(filepath.Join(f.repo, ".git", "items.md"), []byte("the threads\n"), 0o644)
	os.WriteFile(filepath.Join(f.repo, ".git", "reply.md"), []byte(`{"findings": []}`), 0o644)
	var got []relay.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req relay.Request
		json.NewDecoder(r.Body).Decode(&req)
		got = append(got, req)
		json.NewEncoder(w).Encode(relay.Reply{Code: 3, Stdout: "from the host\n", Stderr: "said the host\n"})
	}))
	t.Cleanup(server.Close)
	t.Setenv(relay.URLEnv, server.URL)
	t.Setenv("PR_LOOP_SESSION", "1")
	given := f.base + "..HEAD"

	var stdout, stderr strings.Builder
	code := Main(t.Context(), []string{"--print-prompt", "--focus", "security", "--goal", ".git/goal.md", "--items", ".git/items.md",
		given}, nil, &stdout, &stderr)
	if code != 3 || stdout.String() != "from the host\n" || stderr.String() != "said the host\n" || len(got) != 1 {
		t.Fatalf("exit %d, %d calls\n%s\n%s", code, len(got), stdout.String(), stderr.String())
	}
	var inputs map[string]string
	json.Unmarshal([]byte(got[0].Stdin), &inputs)
	if got[0].Program != "git-fresh-review" ||
		!slices.Equal(got[0].Argv, []string{"--print-prompt", "--focus", "security", "--goal", "-", "--items", "-", given}) ||
		inputs["goal"] != "Grow the code to three lines.\n" || inputs["items"] != "the threads\n" || inputs["top"] == "" {
		t.Errorf("request %+v", got[0])
	}

	code = Main(t.Context(), []string{"--check", ".git/reply.md", given}, nil, &stdout, &stderr)
	json.Unmarshal([]byte(got[len(got)-1].Stdin), &inputs)
	if code != 3 || len(got) != 2 || !slices.Equal(got[1].Argv, []string{"--check", "-", given}) || inputs["reply"] != `{"findings": []}` {
		t.Errorf("check: exit %d, %+v", code, got)
	}

	if code := Main(t.Context(), []string{"--goal", ".git/goal.md", given}, nil, &stdout, &stderr); code != 4 || len(got) != 2 {
		t.Errorf("a Codex run: exit %d, %d calls", code, len(got))
	}
}
