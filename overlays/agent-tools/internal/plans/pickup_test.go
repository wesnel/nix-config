package plans

import (
	"strings"
	"testing"
	"time"
)

// A fresh session picks a plan up from the one line carry prints: pickup
// adds the session to the plan and prints what it needs to go on.
func TestPickup(t *testing.T) {
	t.Parallel()
	later := day.Add(time.Hour)
	carried := func(t *testing.T) (Plan, Plan, Carried) {
		t.Helper()
		c, p := carryFixture(t)
		got, err := c.Carry(p.File(), CarryOptions{}, later)
		if err != nil {
			t.Fatal(err)
		}
		n, err := c.Open(got.Plan)
		if err != nil {
			t.Fatal(err)
		}
		return p, n, got
	}

	t.Run("carry prints the line to paste", func(t *testing.T) {
		_, _, got := carried(t)
		if !strings.Contains(got.Prompt, "`agent-plan pickup "+got.Plan+"`") {
			t.Errorf("prompt %q does not run pickup on %s", got.Prompt, got.Plan)
		}
	})

	t.Run("adds the session once", func(t *testing.T) {
		_, n, _ := carried(t)
		out, err := n.Pickup("claude:fresh", later)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Picked up plan " + n.File() + " in session claude:fresh",
			"Load these skills before going on: design", "- F1 Guard accepts --draft=false", "Goal:\nAdd the run loop.\n",
			"Steps ready: S2\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("no %q in:\n%s", want, out)
			}
		}
		props, _ := planHeading(n.file(""))
		if props["SESSIONS"] != "claude:fresh" || props["STATUS"] != "working" {
			t.Errorf("sessions %q, status %q", props["SESSIONS"], props["STATUS"])
		}
		before := planFiles(t, n)
		if _, err := n.Pickup("claude:fresh", later); err != nil {
			t.Fatal(err)
		}
		if after := planFiles(t, n); after != before {
			t.Errorf("a second pickup changed the plan:\n%s\nwas:\n%s", after, before)
		}
	})

	t.Run("a halted plan is picked up as it is", func(t *testing.T) {
		c := testConfig(t)
		made, err := c.New(NewOptions{Title: "Planned", Slug: "planned", Status: "halted"}, day)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := c.Open(made.Plan)
		addItems(t, p.file(""), "** Steps", "S1")
		if _, err := p.Pickup("codex:next", later); err != nil {
			t.Fatal(err)
		}
		if props, _ := planHeading(p.file("")); props["STATUS"] != "working" || props["SESSIONS"] != "codex:next" {
			t.Errorf("properties %v", props)
		}
	})

	t.Run("refused/a carried plan", func(t *testing.T) {
		p, n, _ := carried(t)
		_, err := p.Pickup("claude:fresh", later)
		isRefusal(t, "a pickup of the plan carried from", err)
		if err != nil && !strings.Contains(err.Error(), n.File()) {
			t.Errorf("the refusal does not name the plan to pick up: %v", err)
		}
	})

	t.Run("refused/no session", func(t *testing.T) {
		_, n, _ := carried(t)
		_, err := n.Pickup("", later)
		isRefusal(t, "a pickup with no session", err)
	})
}
