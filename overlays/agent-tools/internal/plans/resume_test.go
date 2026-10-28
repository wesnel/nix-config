package plans

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A resume brief is what a session needs from its plan to go on after
// /clear or a compaction, within the 10,000 characters a SessionStart
// hook's context keeps.
func TestResumeBrief(t *testing.T) {
	// resumed makes a plan with a goal, S1 started, S2 ready, Q1 decided
	// before a hand-back, Q2 decided after it, an open Q3, and P1 waiting
	// for Q3.
	resumed := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		setGoal(t, p.file(""), "Add the run loop.")
		addItems(t, p.file(""), "** Steps", "S1", "S2")
		insertUnder(t, p.file(""), "** Questions", question("Q1", "Keep the flag?", "")+question("Q2", "Rename it?", "")+
			question("Q3", "Rename the alias too?", ""))
		insertUnder(t, p.file(""), "** Distribution", "*** Part one\n:PROPERTIES:\n:ID: P1\n:AFTER: Q3\n:END:\n")
		decide := func(id, answer string) {
			if _, err := p.Decide(id, DecideOptions{Answer: answer}, day); err != nil {
				t.Fatal(err)
			}
		}
		decide("Q1", "no, drop it")
		if _, err := p.Log(Event{Kind: "handed-back", Item: p.Name, Summary: "the loop's first half"}, day); err != nil {
			t.Fatal(err)
		}
		decide("Q2", "yes, to run")
		if _, err := p.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		return c, p
	}

	t.Run("the plan", func(t *testing.T) {
		_, p := resumed(t)
		got, err := p.ResumeBrief(day, ResumeLimit)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"Resume brief of plan " + p.File() + ", Add the loop",
			"Goal:\nAdd the run loop.\n",
			"Open questions for the operator:\n- Q3 :: Rename the alias too? Recommended: yes.\n",
			"In progress: S1\n",
			"Steps ready: S2\n",
			"Waiting: P1 on Q3\n",
			"Branches: main..loop\n",
			"Decided since the last checkpoint (handed-back: the loop's first half):\n- Q2 :: Rename it? yes, to run\n",
			"Decided before it: Q1; `agent-plan decisions " + p.File() + "` lists every decision.\n",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("no %q in\n%s", want, got)
			}
		}
		if strings.Contains(got, "- Q1 ::") {
			t.Errorf("a decision from before the checkpoint is spelled out:\n%s", got)
		}
	})

	t.Run("a plan with many decisions stays under the cap", func(t *testing.T) {
		c := testConfig(t)
		p := newPlan(t, c, "x")
		setGoal(t, p.file(""), strings.Repeat("Add the run loop. ", 400))
		var qs strings.Builder
		for i := 1; i <= 150; i++ {
			qs.WriteString(question(fmt.Sprintf("Q%d", i), fmt.Sprintf("Question %d %s?", i, strings.Repeat("why ", 30)), ""))
		}
		insertUnder(t, p.file(""), "** Questions", qs.String())
		for i := 1; i <= 140; i++ {
			if _, err := p.Decide(fmt.Sprintf("Q%d", i), DecideOptions{Answer: strings.Repeat("because ", 30)}, day); err != nil {
				t.Fatal(err)
			}
		}
		got, err := p.ResumeBrief(day, ResumeLimit)
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(got)); n > ResumeLimit || len(got) > ResumeLimit {
			t.Errorf("%d characters, %d bytes, over %d", n, len(got), ResumeLimit)
		}
		t.Logf("%d characters", len([]rune(got)))
		for _, want := range []string{
			"Resume brief of plan " + p.File(),
			"Goal:\nAdd the run loop.",
			"- Q150 :: Question 150",
			"- Q140 :: Question 140",
			"Decided earlier, not shown: Q1–Q",
			"Branches: main..loop\n",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("no %q in the brief", want)
			}
		}
		if strings.Contains(got, "- Q1 ::") {
			t.Errorf("the oldest decision is spelled out over the newest")
		}
	})

	t.Run("after /clear and a compaction in Claude Code", func(t *testing.T) {
		c, p := resumed(t)
		for _, source := range []string{"clear", "compact"} {
			out, err := c.SessionHook([]byte(`{"session_id":"abc","source":"`+source+`","cwd":"/nowhere"}`), "claude")
			if err != nil {
				t.Fatal(err)
			}
			context := hookContext(t, out, "SessionStart")
			if !strings.Contains(context, "Resume brief of plan "+p.File()) || !strings.Contains(context, "- Q2 :: Rename it? yes, to run") {
				t.Errorf("%s: context %q", source, context)
			}
			if len(context) > ResumeLimit {
				t.Errorf("%s: %d characters", source, len(context))
			}
		}
		out, _ := c.SessionHook([]byte(`{"session_id":"abc","source":"resume","cwd":"/nowhere"}`), "claude")
		if context := hookContext(t, out, "SessionStart"); strings.Contains(context, "brief of") {
			t.Errorf("a resume carries a brief: %q", context)
		}
	})

	t.Run("after /clear, which starts a new session", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		c, p := resumed(t)
		end := `{"session_id":"abc","hook_event_name":"SessionEnd","reason":"clear","cwd":"/nowhere"}`
		if err := c.SessionEndHook([]byte(end), time.Now()); err != nil {
			t.Fatal(err)
		}
		out, err := c.SessionHook([]byte(`{"session_id":"def","source":"clear","cwd":"/nowhere"}`), "claude")
		if err != nil {
			t.Fatal(err)
		}
		if context := hookContext(t, out, "SessionStart"); !strings.Contains(context, "Resume brief of plan "+p.File()) {
			t.Errorf("context %q", context)
		}
		if found, err := c.Find(FindOptions{Session: "claude:def"}); err != nil || len(found) != 1 || found[0].Plan != p.File() {
			t.Errorf("the new session is not the plan's: %+v %v", found, err)
		}
		out, _ = c.SessionHook([]byte(`{"session_id":"ghi","source":"clear","cwd":"/nowhere"}`), "claude")
		if len(out) != 0 {
			t.Errorf("a hand-over taken twice: %s", out)
		}
	})

	t.Run("no plan prints nothing", func(t *testing.T) {
		c := testConfig(t)
		out, err := c.SessionHook([]byte(`{"session_id":"nobody","source":"clear","cwd":"/nowhere"}`), "claude")
		if err != nil || len(out) != 0 {
			t.Errorf("%q %v", out, err)
		}
	})
}
