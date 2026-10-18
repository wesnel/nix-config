package plans

import (
	"testing"
	"time"
)

// A checkpoint is a natural stopping point the plan's log shows: a
// hand-back, a sign-pass handed over or signed, or a decision that left no
// question to ask.
func TestCheckpoint(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	insertUnder(t, p.file(""), "** Questions", question("Q1", "Keep the flag?", "")+question("Q2", "Rename it?", ""))
	addItems(t, p.file(""), "** Steps", "S1")
	at := func() (Checkpoint, bool) { return c.Checkpoint("abc", "") }
	if _, ok := at(); ok {
		t.Error("a new plan is at a checkpoint")
	}
	if _, err := p.Decide("Q1", DecideOptions{Answer: "no"}, day); err != nil {
		t.Fatal(err)
	}
	if _, ok := at(); ok {
		t.Error("a decision with Q2 still open is a checkpoint")
	}
	d, err := p.Decide("Q2", DecideOptions{Answer: "yes"}, day)
	if err != nil {
		t.Fatal(err)
	}
	if cp, ok := at(); !ok || cp.Kind != "decided" || cp.Key != d.Event.ID || cp.Plan != p.File() {
		t.Errorf("questions settled: %+v %v (want key %s)", cp, ok, d.Event.ID)
	}
	if _, err := p.Log(Event{Kind: "started", Item: "S1"}, day); err != nil {
		t.Fatal(err)
	}
	if _, ok := at(); ok {
		t.Error("a step started after the checkpoint is still at it")
	}
	back, err := p.Log(Event{Kind: "handed-back", Item: p.Name}, day.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if cp, ok := at(); !ok || cp.Kind != "handed-back" || cp.Key != back.ID {
		t.Errorf("hand-back: %+v %v", cp, ok)
	}
}
