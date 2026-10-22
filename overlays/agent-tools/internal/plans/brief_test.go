package plans

import (
	"strings"
	"testing"
)

// A brief is what an agent needs from its plan to go on after the
// conversation was compacted.
func TestBrief(t *testing.T) {
	// briefed makes a plan with a goal, steps S1 (done) and S2, a decided
	// Q1, an open Q2, Q3 waiting for Q2, and a follow-up.
	briefed := func(t *testing.T) (Config, Plan) {
		t.Helper()
		c := testConfig(t)
		p := newPlan(t, c, "x")
		setGoal(t, p.file(""), "Add the run loop.")
		addItems(t, p.file(""), "** Steps", "S1", "S2")
		insertUnder(t, p.file(""), "** Questions", question("Q1", "Keep the flag?", "")+question("Q2", "Rename it?", "")+
			question("Q3", "Rename the alias too?", "Q2"))
		if _, err := p.Decide("Q1", DecideOptions{Answer: "no, drop it"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Log(Event{Kind: "done", Item: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Followup(FollowupOptions{Title: "Fix the guard", FoundIn: "S1"}, day); err != nil {
			t.Fatal(err)
		}
		return c, p
	}

	t.Run("the plan", func(t *testing.T) {
		_, p := briefed(t)
		got, err := p.Brief(day)
		if err != nil {
			t.Fatal(err)
		}
		want := "Brief of plan " + p.file("") + ", Add the loop:\n" +
			"Goal:\nAdd the run loop.\n" +
			"Decided:\n- Q1 :: Keep the flag? no, drop it\n" +
			"Steps ready: S2\n" +
			"Parts ready to start: none\n" +
			"Questions for the operator: Q2\n" +
			"Done, not verified: S1\n" +
			"Follow-ups open: 1\n"
		if got != want {
			t.Errorf("brief:\n%s\nwant:\n%s", got, want)
		}
	})

	t.Run("after a compaction in Codex", func(t *testing.T) {
		c, p := briefed(t)
		in := `{"session_id":"abc","hook_event_name":"SessionStart","source":"compact"}`
		if out := string(c.Remind([]byte(in))); !strings.Contains(out, "Brief of plan "+p.file("")) ||
			!strings.Contains(out, "Steps ready: S2") {
			t.Errorf("%s: %s", in, out)
		}
	})
}
