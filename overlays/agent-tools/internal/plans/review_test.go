package plans

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A design review is pending, for the session that started it, from its
// start until a plan-reviewed, or a reviewed with role design-reviewer,
// follows it in the same log, whatever was committed before it.
func TestPendingPlanReview(t *testing.T) {
	t.Parallel()
	started := Event{Kind: DesignReviewStarted, Session: "abc", Summary: "Design review of the plan"}
	otherStarted := Event{Kind: DesignReviewStarted, Session: "xyz", Summary: "Design review of the plan"}
	reviewed := Event{Kind: "plan-reviewed", Summary: "2 findings"}
	committed := Event{Kind: "committed", Session: "abc", Commit: "Add the loop"}
	resultReviewed := Event{Kind: "reviewed", Role: "design-reviewer", Summary: "no findings"}
	codeReviewed := Event{Kind: "reviewed", Summary: "round 1: 2 findings"}
	for _, c := range []struct {
		name    string
		events  []Event
		pending bool
	}{
		{"no review started", nil, false},
		{"a review started", []Event{started}, true},
		{"a review another session started", []Event{otherStarted}, false},
		{"a review started, then plan-reviewed", []Event{started, reviewed}, false},
		{"a review started, then reviewed as design-reviewer", []Event{started, resultReviewed}, false},
		// A review run again after the last was answered waits for its own answer.
		{"a second review after the first was answered", []Event{started, reviewed, started}, true},
		// Each answer answers one review: two in flight wait for two.
		{"two reviews started, one answered", []Event{started, started, reviewed}, true},
		{"two reviews started, both answered", []Event{started, started, reviewed, resultReviewed}, false},
		{"a review after a commit", []Event{committed, started}, true},
		// A fresh review round answers no design review.
		{"a review started, then a code review round", []Event{started, codeReviewed}, true},
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

// A review pending in any plan naming the session holds it, and only an
// answer in that plan's own log, after the start, releases it.
func TestPendingPlanReviewOf(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		answeredIn string // "held", "other" or ""
		pending    bool
	}{
		{"a review pending in one of two plans", "", true},
		{"answered in the plan that holds it", "held", false},
		{"answered in another plan naming the session", "other", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			conf := testConfig(t)
			held := newPlan(t, conf, "held")
			made, err := conf.New(NewOptions{Title: "Other", Slug: "other", Sessions: []string{"claude:abc"}, Harness: "claude"}, day)
			if err != nil {
				t.Fatal(err)
			}
			other, err := conf.Open(made.Plan)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := held.Record("", Event{Kind: DesignReviewStarted, Session: "abc", Summary: "Review X7"}, day); err != nil {
				t.Fatal(err)
			}
			answer := map[string]Plan{"held": held, "other": other}[c.answeredIn]
			if c.answeredIn != "" {
				if _, err := answer.Log(Event{Kind: "plan-reviewed", Item: answer.Name, Summary: "no findings"}, day); err != nil {
					t.Fatal(err)
				}
			}
			p, _, pending := conf.PendingPlanReviewOf("abc")
			if pending != c.pending || pending && p.File() != held.File() {
				t.Errorf("pending %v in %s, want %v in %s", pending, p.File(), c.pending, held.File())
			}
		})
	}
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

	// A sub-agent's calls carry agent_id; the design reviewer it starts is
	// its own, and holds no edit of the main thread.
	t.Run("a design reviewer a sub-agent starts", func(t *testing.T) {
		t.Parallel()
		conf := testConfig(t)
		p := newPlan(t, conf, "x")
		var fields map[string]any
		json.Unmarshal(event("design-reviewer", "/nowhere", true), &fields)
		fields["agent_id"] = "a1"
		input, _ := json.Marshal(fields)
		conf.Remind(input)
		if _, pending, err := p.PendingPlanReview("abc"); err != nil || pending {
			t.Errorf("pending %v, %v; want none", pending, err)
		}
	})

	// A start the hook cannot record says so, since the guard then holds
	// nothing.
	t.Run("a start it cannot record", func(t *testing.T) {
		t.Parallel()
		conf := testConfig(t)
		p := newPlan(t, conf, "x")
		log := filepath.Join(p.Dir, p.Name+".log.org")
		if err := os.Chmod(log, 0o444); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(log, 0o644) })
		out := conf.Remind(event("design-reviewer", "/nowhere", true))
		if !strings.Contains(string(out), "not recorded") || !strings.Contains(string(out), p.File()) {
			t.Errorf("output %s; want it to say the start was not recorded in %s", out, p.File())
		}
	})

	// Reviews in two plans, described alike: a finished reviewer's reminder
	// names both plans and how to answer each, since its description cannot
	// tell them apart.
	t.Run("reviews pending in two plans", func(t *testing.T) {
		t.Parallel()
		conf := testConfig(t)
		a, b := newPlan(t, conf, "aaa"), newPlan(t, conf, "zzz")
		for _, p := range []Plan{a, b} {
			if _, err := p.Record("", Event{Kind: DesignReviewStarted, Session: "abc", Summary: "Design review of the plan"}, day); err != nil {
				t.Fatal(err)
			}
		}
		notice, _ := json.Marshal(map[string]any{"session_id": "abc", "cwd": "/nowhere", "hook_event_name": "UserPromptSubmit",
			"prompt": "<task-notification>\n<summary>Agent \"Design review of the plan\" finished</summary>\n</task-notification>"})
		out := string(conf.Remind(notice))
		for _, p := range []Plan{a, b} {
			if !strings.Contains(out, "--item "+p.Name+" --kind plan-reviewed") {
				t.Errorf("reminder %s does not say how to answer the review in %s", out, p.File())
			}
		}
	})

	// Another sub-agent finishing while a review is pending: its part is
	// logged where it always is, and the review's answer is offered only
	// for the case that it was the reviewer.
	t.Run("another sub-agent finishes while a review is pending", func(t *testing.T) {
		t.Parallel()
		conf := testConfig(t)
		newPlan(t, conf, "aaa")
		newPlan(t, conf, "zzz")
		part, _, _ := conf.Locate("abc", "")
		found, _ := conf.Find(FindOptions{Session: "abc"})
		var held Plan
		for _, f := range found {
			if f.Plan != part.File() {
				held, _ = conf.Open(f.Plan)
			}
		}
		if _, err := held.Record("", Event{Kind: DesignReviewStarted, Session: "abc", Summary: "Design review of the plan"}, day); err != nil {
			t.Fatal(err)
		}
		// The session works on in the part's plan, which stays the one
		// Locate gives (underWay).
		if _, err := part.Record("", Event{Kind: "committed", Session: "abc", Commit: "Add the loop"}, day.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		notice, _ := json.Marshal(map[string]any{"session_id": "abc", "cwd": "/nowhere", "hook_event_name": "UserPromptSubmit",
			"prompt": "<task-notification>\n<summary>Agent \"Build the loop\" finished</summary>\n</task-notification>"})
		out := string(conf.Remind(notice))
		if !strings.Contains(out, "outcome in "+part.File()) {
			t.Errorf("reminder %s does not ask for the part's outcome in %s", out, part.File())
		}
		if !strings.Contains(out, "If it was a design reviewer") {
			t.Errorf("reminder %s offers the review's answer for any sub-agent", out)
		}
	})

	// With two plans naming the session, the start goes in the one the
	// reviewer's prompt names, else in the one with the latest event.
	t.Run("which of two plans", func(t *testing.T) {
		t.Parallel()
		for _, c := range []struct {
			name       string
			named      string // "path", "id" or ""
			foreground bool
		}{
			{"the plan the prompt names by its file", "path", false},
			{"the plan the prompt names by its ID", "id", false},
			// A prompt that names the plan under review first, and the other
			// later, as a source of its findings.
			{"the plan the prompt names first", "both", false},
			{"the plan with the latest event", "", false},
			// The reminder to log the reviewer's outcome names the plan
			// that holds its start.
			{"a foreground review's reminder", "path", true},
		} {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				conf := testConfig(t)
				newPlan(t, conf, "aaa")
				newPlan(t, conf, "zzz")
				first, _, ok := conf.Locate("abc", "")
				if !ok {
					t.Fatal("no plan for the session")
				}
				found, _ := conf.Find(FindOptions{Session: "abc"})
				var want Plan
				for _, f := range found {
					if f.Plan != first.File() {
						want, _ = conf.Open(f.Plan)
					}
				}
				prompt := "Review the plan."
				switch c.named {
				case "path":
					prompt = "Review the plan " + want.File() + " before coding."
				case "id":
					prompt = "Review plan " + want.Name + " before coding."
				case "both":
					prompt = "Review plan " + want.Name + ". Its faults were found in plan " + first.Name + "."
				default:
					if _, err := want.Record("", Event{Kind: "committed", Session: "abc", Commit: "Add the loop"}, day.Add(time.Hour)); err != nil {
						t.Fatal(err)
					}
				}
				input, _ := json.Marshal(map[string]any{"session_id": "abc", "cwd": "/nowhere", "hook_event_name": "PostToolUse",
					"tool_name": "Agent", "tool_input": map[string]any{"subagent_type": "design-reviewer", "description": "Design review",
						"prompt": prompt}, "tool_response": map[string]any{"isAsync": !c.foreground}})
				out := conf.Remind(input)
				if !c.foreground {
					// A background review's reminder comes with its task-notification.
					notice, _ := json.Marshal(map[string]any{"session_id": "abc", "cwd": "/nowhere", "hook_event_name": "UserPromptSubmit",
						"prompt": "<task-notification>\n<summary>Agent \"Design review\" finished</summary>\n</task-notification>"})
					out = conf.Remind(notice)
				}
				if !strings.Contains(string(out), want.File()) {
					t.Errorf("reminder %s does not name %s", out, want.File())
				}
				// It asks for the answer that lifts the hold, never for a
				// part's outcome, which answers nothing.
				if !strings.Contains(string(out), "--item "+want.Name+" --kind plan-reviewed") {
					t.Errorf("reminder %s does not say how to answer the review", out)
				}
				if c.foreground && strings.Contains(string(out), "`done` once it passes") {
					t.Errorf("reminder %s asks a design reviewer's part to be logged done", out)
				}
				if _, pending, _ := want.PendingPlanReview("abc"); !pending {
					t.Errorf("no review pending in %s", want.File())
				}
				if _, pending, _ := first.PendingPlanReview("abc"); pending {
					t.Errorf("recorded in %s, the first plan Locate gives", first.File())
				}
			})
		}
	})

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
