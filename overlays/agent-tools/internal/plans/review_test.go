package plans

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// A design review of the plan is pending, for the session that started it,
// from its start until the plan is logged plan-reviewed; one started once
// work is committed reviews the result, and leaves edits alone.
func TestPendingPlanReview(t *testing.T) {
	t.Parallel()
	started := Event{Kind: DesignReviewStarted, Session: "abc", Summary: "Design review of the plan"}
	otherStarted := Event{Kind: DesignReviewStarted, Session: "xyz", Summary: "Design review of the plan"}
	reviewed := Event{Kind: "plan-reviewed", Summary: "2 findings"}
	committed := Event{Kind: "committed", Commit: "Add the loop"}
	resultReviewed := Event{Kind: "reviewed", Role: "design-reviewer", Summary: "no findings"}
	for _, c := range []struct {
		name    string
		events  []Event
		pending bool
	}{
		{"no review started", nil, false},
		{"a review started", []Event{started}, true},
		{"a review another session started", []Event{otherStarted}, false},
		{"a review started, then plan-reviewed", []Event{started, reviewed}, false},
		{"a second plan review after the first", []Event{started, reviewed, started}, false},
		{"a result review after a commit", []Event{committed, started}, false},
		// Only plan-reviewed answers a plan review.
		{"a review started, then reviewed as design-reviewer", []Event{started, resultReviewed}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			p := newPlan(t, testConfig(t), "x")
			for _, e := range c.events {
				if _, err := p.Record("", e, day); err != nil {
					t.Fatal(err)
				}
			}
			e, pending, err := p.PendingPlanReview("abc")
			if err != nil || pending != c.pending {
				t.Fatalf("pending %v, %v; want %v", pending, err, c.pending)
			}
			if pending && e.Kind != DesignReviewStarted {
				t.Errorf("pending on %+v, want the design-review-started event", e)
			}
		})
	}

	// Only agent-tools' hooks log the start: an agent that logs it is refused.
	t.Run("an agent cannot log it", func(t *testing.T) {
		t.Parallel()
		p := newPlan(t, testConfig(t), "x")
		if _, err := p.Log(Event{Kind: DesignReviewStarted, Item: p.Name}, day); err == nil {
			t.Error("Log took design-review-started")
		}
	})
}

// A session's commit in any plan naming it, before a review was recorded,
// makes that review one of the result, wherever the hook recorded it.
func TestPendingPlanReviewOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		commitLate bool
		pending    bool
	}{
		{"a commit in another plan before the review", false, false},
		{"a commit in another plan after the review", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			conf := testConfig(t)
			reviewed := newPlan(t, conf, "reviewed")
			made, err := conf.New(NewOptions{Title: "Other", Slug: "other", Sessions: []string{"claude:abc"}, Harness: "claude"}, day)
			if err != nil {
				t.Fatal(err)
			}
			committed, err := conf.Open(made.Plan)
			if err != nil {
				t.Fatal(err)
			}
			at, commitAt := day.Add(2*time.Minute), day
			if c.commitLate {
				commitAt = day.Add(4 * time.Minute)
			}
			if _, err := reviewed.Record("", Event{Kind: DesignReviewStarted, Session: "abc", Summary: "Review X7"}, at); err != nil {
				t.Fatal(err)
			}
			if _, err := committed.Record("", Event{Kind: "committed", Commit: "Add the loop"}, commitAt); err != nil {
				t.Fatal(err)
			}
			if _, _, pending := conf.PendingPlanReviewOf("abc"); pending != c.pending {
				t.Errorf("pending %v, want %v", pending, c.pending)
			}
		})
	}

	// A commit of an earlier task, before the plan holding the review was
	// made, does not answer that review.
	t.Run("a commit of an earlier plan", func(t *testing.T) {
		t.Parallel()
		conf := testConfig(t)
		earlier := newPlan(t, conf, "earlier")
		if _, err := earlier.Record("", Event{Kind: "committed", Commit: "Add the loop"}, day); err != nil {
			t.Fatal(err)
		}
		made, err := conf.New(NewOptions{Title: "Later", Slug: "later", Sessions: []string{"claude:abc"}, Harness: "claude"},
			day.Add(5*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		later, err := conf.Open(made.Plan)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := later.Record("", Event{Kind: DesignReviewStarted, Session: "abc", Summary: "Review X7"},
			day.Add(6*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if p, _, pending := conf.PendingPlanReviewOf("abc"); !pending || p.File() != later.File() {
			t.Errorf("pending %v in %s, want the later plan held", pending, p.File())
		}
	})
}

// Remind records a design-reviewer's spawn, in the foreground or the
// background, in a plan naming the session, and no other sub-agent's.
func TestRemindRecordsDesignReview(t *testing.T) {
	t.Parallel()
	describedEvent := func(agent, cwd, description string, async bool) []byte {
		data, _ := json.Marshal(map[string]any{"session_id": "abc", "cwd": cwd, "hook_event_name": "PostToolUse",
			"tool_name": "Agent", "tool_input": map[string]any{"subagent_type": agent, "description": description},
			"tool_response": map[string]any{"isAsync": async}})
		return data
	}
	event := func(agent, cwd string, async bool) []byte {
		return describedEvent(agent, cwd, "Design review of the plan", async)
	}
	for _, c := range []struct {
		name    string
		input   []byte
		pending bool
	}{
		{"a design reviewer in the background", event("design-reviewer", "/nowhere", true), true},
		{"a design reviewer in the foreground", event("design-reviewer", "/nowhere", false), true},
		{"another sub-agent", event("builder", "/nowhere", true), false},
		// A summary holds one line, so the description is cut, not dropped.
		{"a description over two lines", describedEvent("design-reviewer", "/nowhere", "Review the plan\nand its steps", true), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			conf := testConfig(t)
			p := newPlan(t, conf, "x")
			conf.Remind(c.input)
			if _, pending, err := p.PendingPlanReview("abc"); err != nil || pending != c.pending {
				t.Errorf("pending %v, %v; want %v", pending, err, c.pending)
			}
		})
	}

	// From a worktree another session's plan names, the start still goes
	// in the plan naming this session, where the guard looks.
	t.Run("not in a plan matched by worktree alone", func(t *testing.T) {
		t.Parallel()
		conf := testConfig(t)
		repo, _ := filepath.EvalSymlinks(t.TempDir())
		if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		mine := newPlan(t, conf, "mine")
		made, err := conf.New(NewOptions{Title: "Theirs", Slug: "theirs", Worktrees: []string{repo},
			Sessions: []string{"claude:xyz"}, Harness: "claude"}, day)
		if err != nil {
			t.Fatal(err)
		}
		theirs, err := conf.Open(made.Plan)
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
		conf.Remind(event("design-reviewer", filepath.Join(repo, "sub"), true))
		if _, pending, _ := mine.PendingPlanReview("abc"); !pending {
			t.Error("the session's own plan has no pending review")
		}
		events, _ := theirs.Events()
		for _, e := range events {
			if e.Kind == DesignReviewStarted {
				t.Errorf("recorded in the plan matched by worktree: %+v", e)
			}
		}
	})
}
