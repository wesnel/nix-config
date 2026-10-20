package plans

import (
	"strings"
	"testing"
)

// A hand-back can name the command the operator runs, and the item waits
// on them until an event that settles it is logged.
func TestWaiting(t *testing.T) {
	c := testConfig(t)
	p := newPlan(t, c, "x")
	addItems(t, p.file(""), "** Distribution", "P1", "P2")
	e, err := p.Log(Event{Kind: "handed-back", Item: "P1", Summary: "signed and pushed next", Command: "git sign-pass --push",
		Dir: "/w/loop"}, day)
	if err != nil {
		t.Fatal(err)
	}
	if e.Command != "git sign-pass --push" || e.Dir != "/w/loop" {
		t.Errorf("logged %+v", e)
	}
	if _, err := p.Log(Event{Kind: "handed-back", Item: "P2", Command: "gh pr ready 7"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Log(Event{Kind: "handed-back", Item: p.Name}, day); err != nil {
		t.Fatal(err)
	}
	events, _ := p.Events()
	if last := events[len(events)-3]; last.Command != "git sign-pass --push" || last.Dir != "/w/loop" {
		t.Errorf("read back %+v", last)
	}

	got, err := p.Waiting()
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, w := range got {
		shown = append(shown, w.Item+" "+w.Command+" "+w.Dir)
	}
	if strings.Join(shown, "; ") != "P1 git sign-pass --push /w/loop; P2 gh pr ready 7 " || got[0].Summary != "signed and pushed next" ||
		got[0].Event == "" || got[0].Time == "" {
		t.Errorf("waiting %+v", got)
	}

	if _, err := p.Log(Event{Kind: "verified", Item: "P2"}, day); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Waiting(); len(got) != 1 || got[0].Item != "P1" {
		t.Errorf("once P2 moved on: %+v", got)
	}
	// A change to the plan's text or schedule settles nothing.
	if _, err := p.SetAfter("P1", []string{"P2"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Postpone("P1", "2099-01-01", day); err != nil {
		t.Fatal(err)
	}
	if got, _ := p.Waiting(); len(got) != 1 || got[0].Item != "P1" || got[0].Command != "git sign-pass --push" {
		t.Errorf("after planned and postponed events: %+v", got)
	}

	for what, e := range map[string]Event{
		"a command on another kind": {Kind: "done", Item: "P1", Command: "git sign-pass"},
		"a directory alone":         {Kind: "handed-back", Item: "P1", Dir: "/w"},
		"a relative directory":      {Kind: "handed-back", Item: "P1", Command: "make", Dir: "w"},
		"a command on two lines":    {Kind: "handed-back", Item: "P1", Command: "make\nrm -rf /"},
	} {
		t.Run("refused/"+what, func(t *testing.T) {
			_, err := p.Log(e, day)
			isRefusal(t, what, err)
		})
	}
}
