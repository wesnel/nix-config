package plans

import "testing"

func TestItemTiming(t *testing.T) {
	_, p := entryPlan(t)
	if _, err := p.SetAfter("S2", []string{"S1", "Q1"}, day); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Postpone("S2", "2026-12-01 10:30", day); err != nil {
		t.Fatal(err)
	}
	got, err := p.ItemTiming()
	if err != nil {
		t.Fatal(err)
	}
	s := got["S2"]
	if len(s.After) != 2 || s.After[0] != "S1" || s.After[1] != "Q1" || !s.ScheduledHasTime || s.Scheduled.Format("2006-01-02 15:04") != "2026-12-01 10:30" {
		t.Fatalf("dependency and schedule metadata missing: %+v", s)
	}
}
