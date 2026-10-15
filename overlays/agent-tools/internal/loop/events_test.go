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
)

// loopPlan makes a plan, in a plans directory of the test's own, that
// names worktrees and branches.
func loopPlan(t *testing.T, worktrees, branches []string) plans.Plan {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	c := plans.Config{Dir: filepath.Join(root, "agents"), Host: "box"}
	config := filepath.Join(root, "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	for i, w := range worktrees {
		worktrees[i], _ = filepath.EvalSymlinks(w)
	}
	made, err := c.New(plans.NewOptions{Title: "Answer the bots", Slug: "loop", Worktrees: worktrees, Branches: branches}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Open(made.Plan)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// loopEvents are the kind and summary of each event in p's log.
func loopEvents(t *testing.T, p plans.Plan) []string {
	t.Helper()
	events, err := p.Events()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Kind+": "+e.Summary)
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
// whether or not the session has hooks: the replies, resolutions, CI
// outcomes and rounds.
func TestLoopEvents(t *testing.T) {
	t.Run("reply and resolve", func(t *testing.T) {
		fakeRepo(t)
		p := loopPlan(t, []string{git.Dir}, nil)
		if _, err := Reply("human1", "done"); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve("human1"); err != nil {
			t.Fatal(err)
		}
		if _, err := Reply("theirs", "x"); err == nil {
			t.Fatal("replied to a thread the policy refuses")
		}
		expectEvents(t, p, "replied: in thread human1 on #1", "resolved: thread human1 on #1")
	})

	// A relayed loop session names no session and runs in a worktree of
	// its own: the plan that names the loop's branch takes its events.
	t.Run("by the loop's branch", func(t *testing.T) {
		fakeRepo(t)
		t.Setenv(sessionEnv, "1")
		registerLoop(t)
		p := loopPlan(t, nil, []string{"main..feat"})
		if _, err := ReplyAll([]PlannedReply{{Thread: "bot1", Body: "fixed", Resolve: true}}); err != nil {
			t.Fatal(err)
		}
		expectEvents(t, p, "replied: in thread bot1 on #1", "resolved: thread bot1 on #1")
	})

	t.Run("a round", func(t *testing.T) {
		ciRepo(t)
		p := loopPlan(t, []string{git.Dir}, nil)
		tip, _ := git.Run("rev-parse", "feat")
		if err := snapshotRound(&State{Order: []string{"feat"}, Round: 2}); err != nil {
			t.Fatal(err)
		}
		expectEvents(t, p, "round: round 2: feat "+tip)
	})

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
