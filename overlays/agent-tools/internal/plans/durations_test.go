package plans

import (
	"strings"
	"testing"
	"time"
)

// status shows how long each step took, or has been running, from the
// stamps of its started, done and abandoned events.
func TestStepDurations(t *testing.T) {
	// ev is an event of item, logged at an offset from day.
	type ev struct {
		item, kind string
		at         time.Duration
	}
	// steps makes a plan of S1 to S4 with events logged in the order given,
	// which is time order, as agent-plan log makes them.
	steps := func(t *testing.T, log ...ev) Plan {
		t.Helper()
		p := newPlan(t, testConfig(t), "x")
		addItems(t, p.file(""), "** Steps", "S1", "S2", "S3", "S4")
		for _, e := range log {
			if _, err := p.Record(e.item, Event{Kind: e.kind, Item: e.item}, day.Add(e.at)); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	step := func(t *testing.T, s Status, id string) Step {
		t.Helper()
		for _, st := range s.Steps {
			if st.ID == id {
				return st
			}
		}
		t.Fatalf("no step %s in %v", id, s.Steps)
		return Step{}
	}
	minutes := func(st Step) string {
		if st.Minutes == nil {
			return "none"
		}
		return (time.Duration(*st.Minutes) * time.Minute).String()
	}
	const m = time.Minute

	p := steps(t,
		// S1 took 12 minutes.
		ev{"S1", "started", 0}, ev{"S3", "started", 0},
		ev{"S2", "started", 5 * m},
		ev{"S3", "done", 10 * m},
		ev{"S1", "done", 12 * m},
		// S2 is started again while it runs: its span still began at 5m.
		ev{"S2", "started", 20 * m},
		// S3 was started again after it was done: its spans are 10m and 15m,
		// not the 45m from its first start to its last done.
		ev{"S3", "started", 30 * m}, ev{"S3", "done", 45 * m},
		// S4 was done without a started event: no span, no duration.
		ev{"S4", "done", 50 * m},
		// S2 is still started at the time of the status.
	)
	s, err := p.StatusAt(day.Add(100 * m))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a done step", func(t *testing.T) {
		if st := step(t, s, "S1"); minutes(st) != "12m0s" || st.Running {
			t.Errorf("S1: %s, running %v", minutes(st), st.Running)
		}
	})
	t.Run("a started step runs to now", func(t *testing.T) {
		if st := step(t, s, "S2"); minutes(st) != "1h35m0s" || !st.Running {
			t.Errorf("S2: %s, running %v", minutes(st), st.Running)
		}
	})
	t.Run("a redone step counts each span", func(t *testing.T) {
		if st := step(t, s, "S3"); minutes(st) != "25m0s" || st.Running {
			t.Errorf("S3: %s, running %v", minutes(st), st.Running)
		}
	})
	t.Run("a step never started has no duration", func(t *testing.T) {
		if st := step(t, s, "S4"); st.Minutes != nil || st.Running {
			t.Errorf("S4: %s, running %v", minutes(st), st.Running)
		}
	})

	// The step table keeps its columns: the Emacs plan views read its rows.
	t.Run("the status lists it beside the table", func(t *testing.T) {
		org := s.Org()
		if !strings.Contains(org, "\n- Time worked: S1 12m; S2 1h 35m so far; S3 25m\n") {
			t.Errorf("no time line in:\n%s", org)
		}
		if !strings.Contains(org, "| Step | Title | Done | Verified | Commit |\n") || strings.Contains(org, "| Time |") {
			t.Errorf("the table's columns changed:\n%s", org)
		}
	})

	t.Run("under a minute", func(t *testing.T) {
		p := steps(t, ev{"S1", "started", 0}, ev{"S1", "done", 0})
		s, err := p.StatusAt(day.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if st := step(t, s, "S1"); minutes(st) != "0s" || !strings.Contains(s.Org(), "\n- Time worked: S1 <1m\n") {
			t.Errorf("S1: %s, org:\n%s", minutes(st), s.Org())
		}
	})

	// A duration shows its two largest units; the exact minutes are in the
	// JSON, so 1d 59m reads "1d" in the status line.
	t.Run("units shown", func(t *testing.T) {
		for _, c := range []struct {
			minutes int
			running bool
			want    string
		}{
			{0, false, "<1m"},
			{59, false, "59m"},
			{60, false, "1h"},
			{95, false, "1h 35m"},
			{24*60 - 1, false, "23h 59m"},
			{24*60 + 59, false, "1d"},
			{25 * 60, false, "1d 1h"},
			{2*24*60 + 3*60 + 12, false, "2d 3h"},
			{95, true, "1h 35m so far"},
		} {
			if got := (Step{Minutes: &c.minutes, Running: c.running}).worked(); got != c.want {
				t.Errorf("%d minutes, running %v: %q, want %q", c.minutes, c.running, got, c.want)
			}
		}
	})

	t.Run("no step started", func(t *testing.T) {
		p := steps(t)
		s, err := p.StatusAt(day)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(s.Org(), "Time worked") {
			t.Errorf("org:\n%s", s.Org())
		}
	})
}
