package loop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// loopPlan makes a plan, in a plans directory of the test's own, that
// names worktrees and branches and the Claude Code session the test runs
// pr-loop in: the worktrees are main checkouts, which pick no plan. It
// returns the plan and the variables, for pr-loop's Env, that name its
// config and that session.
func loopPlan(t *testing.T, worktrees, branches []string) (plans.Plan, []string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
	config := filepath.Join(root, "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(config, data, 0o644)
	for i, w := range worktrees {
		worktrees[i], _ = filepath.EvalSymlinks(w)
	}
	made, err := c.New(t.Context(), plans.NewOptions{Title: "Answer the bots", Slug: "loop", Worktrees: worktrees, Branches: branches,
		Sessions: []string{"claude:s-loop"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return p, []string{"CLAUDE_CODE_SESSION_ID=s-loop", "AGENT_TOOLS_CONFIG=" + config}
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
	t.Parallel()
	t.Run("reply and resolve", func(t *testing.T) {
		t.Parallel()
		f := fakeRepo(t)
		p, vars := loopPlan(t, []string{f.dir}, nil)
		ctx := withEnv(f.in("bots"), vars...)
		if _, err := Reply(ctx, "bot1", "done"); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(ctx, "bot1"); err != nil {
			t.Fatal(err)
		}
		if _, err := Reply(ctx, "theirs", "x"); err == nil {
			t.Fatal("replied to a thread the policy refuses")
		}
		expectEvents(t, p, "replied: in thread bot1 on #1", "resolved: thread bot1 on #1")
	})

	t.Run("CI", func(t *testing.T) {
		t.Parallel()
		f := ciRepo(t)
		f.checks["a1"] = passing
		p, vars := loopPlan(t, []string{f.dir}, nil)
		if _, _, err := WaitCI(withEnv(f.ctx, vars...), []string{"1"}, "", time.Hour, time.Minute, 3*time.Minute); err != nil {
			t.Fatal(err)
		}
		expectEvents(t, p, "ci-waited: passed: #1")
	})
}
