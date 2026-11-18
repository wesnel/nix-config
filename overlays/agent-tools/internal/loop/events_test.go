package loop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/repos"
)

// loopPlan makes a plan, in a plans directory of the test's own, that
// names worktrees and branches and the Claude Code session the test runs
// pr-loop in: the worktrees are main checkouts, which pick no plan. Its
// config goes where repos.Path reads it: AGENT_TOOLS_CONFIG, or the config
// home a helper such as operatorProfile baked, which overrides it.
func loopPlan(t *testing.T, worktrees, branches []string) plans.Plan {
	t.Helper()
	t.Setenv("CLAUDE_CODE_SESSION_ID", "s-loop")
	root, _ := filepath.EvalSymlinks(t.TempDir())
	c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
	t.Setenv("AGENT_TOOLS_CONFIG", filepath.Join(root, "config.json"))
	data, _ := json.Marshal(map[string]any{"plans": c})
	if err := os.MkdirAll(filepath.Dir(repos.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(repos.Path(), data, 0o644); err != nil {
		t.Fatal(err)
	}
	for i, w := range worktrees {
		worktrees[i], _ = filepath.EvalSymlinks(w)
	}
	made, err := c.New(plans.NewOptions{Title: "Answer the bots", Slug: "loop", Worktrees: worktrees, Branches: branches,
		Sessions: []string{"claude:s-loop"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// loopEvents are the kind and summary of each event in p's log but the
// property events the plan starts with.
func loopEvents(t *testing.T, p plans.Plan) []string {
	t.Helper()
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		if e.Kind != "property" {
			out = append(out, e.Kind+": "+e.Summary)
		}
	}
	return out
}

func expectEvents(t *testing.T, p plans.Plan, want ...string) {
	t.Helper()
	if got := loopEvents(t, p); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("logged:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// What pr-loop does reaches the plan of the session that ran it as events,
// whether or not the session has hooks: the replies, resolutions and CI
// outcomes.
func TestLoopEvents(t *testing.T) {
	t.Run("reply and resolve", func(t *testing.T) {
		fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		p := loopPlan(t, []string{git.Dir}, nil)
		if _, err := Reply("bot1", "done"); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve("bot1"); err != nil {
			t.Fatal(err)
		}
		if _, err := Reply("theirs", "x"); err == nil {
			t.Fatal("replied to a thread the policy refuses")
		}
		expectEvents(t, p, "replied: in thread bot1 on #1", "resolved: thread bot1 on #1")
	})

	// Each thread a reply file answers is a finding settled with the
	// file's outcome, in the round given, so the agent need not log it;
	// a thread already resolved, answered by nothing, is not.
	t.Run("a reply file settles findings", func(t *testing.T) {
		fakeRepo(t)
		t.Setenv(ModeEnv, "bots")
		p := loopPlan(t, []string{git.Dir}, nil)
		plan, err := ParseReplies(replyFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ReplyAll(plan, 3); err != nil {
			t.Fatal(err)
		}
		events, err := p.Events()
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, e := range events {
			if e.Kind == "finding-settled" {
				got = append(got, e.Item+" "+e.Review+" "+e.Outcome)
			}
		}
		want := []string{p.Name + " 3.bot1 fix", p.Name + " 3.bot2 defer"}
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("settled:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
	})

	// Codex runs pr-loop open as an MCP tool, which no shell hook sees, so
	// open records the pull request itself: in PULL_REQUESTS, linking a
	// plan that names the same pull request, and as the push that opened
	// it. A Codex session is found by its thread, as a Claude Code one is by
	// its session.
	for _, harness := range []string{"claude", "codex"} {
		t.Run("open/"+harness, func(t *testing.T) { testOpenRecords(t, harness, false) })
	}
	// A pull request GitHub opened is recorded though a local git write
	// after it fails, since it exists whatever open then reports.
	t.Run("open/local failure", func(t *testing.T) { testOpenRecords(t, "claude", true) })

	t.Run("CI", func(t *testing.T) {
		f := ciRepo(t)
		f.checks["a1"] = passing
		p := loopPlan(t, []string{git.Dir}, nil)
		if _, _, err := WaitCI([]string{"1"}, "", time.Hour, time.Minute, 3*time.Minute); err != nil {
			t.Fatal(err)
		}
		expectEvents(t, p, "ci-waited: passed: #1")
	})
}

// testOpenRecords opens a pull request from a session of harness and
// checks its plan records it. With lockTracking, the local tracking ref is
// locked, so open fails after GitHub opened the pull request.
func testOpenRecords(t *testing.T, harness string, lockTracking bool) {
	_, dir := pushedOperatorBranch(t)
	p := loopPlan(t, []string{dir}, nil)
	if harness == "codex" {
		t.Setenv("CLAUDE_CODE_SESSION_ID", "")
		t.Setenv("CODEX_THREAD_ID", "t-loop")
		if err := p.SetProperty("", "SESSIONS", []string{"codex:t-loop"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	url := "https://github.com/o/r/pull/7"
	c := plans.Config{Dir: filepath.Dir(p.File()), Host: "box"}
	made, err := c.New(plans.NewOptions{Title: "Review it", Slug: "review"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	reviewing, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewing.AddToProperty("", "PULL_REQUESTS", url, time.Now()); err != nil {
		t.Fatal(err)
	}
	if lockTracking {
		lock := filepath.Join(dir, ".git", "refs", "remotes", "origin", "feat.lock")
		if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lock, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Open("feat", "", "Add feat", "Why it is needed.", false); (err != nil) != lockTracking {
		t.Fatalf("open: %v, want an error only with the tracking ref locked", err)
	}
	text, _ := os.ReadFile(p.File())
	for _, want := range []string{":PULL_REQUESTS: " + url + "\n", "][Review it]]: Both name pull request " + url + ".\n"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("the plan lacks %q:\n%s", want, text)
		}
	}
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var pushed []string
	for _, e := range events {
		if e.Kind == "pushed" {
			pushed = append(pushed, e.Branch+" "+e.Summary)
		}
	}
	if got := strings.Join(pushed, "\n"); got != "feat opened "+url {
		t.Errorf("pushed events:\n%s\nwant: feat opened %s", got, url)
	}
}
