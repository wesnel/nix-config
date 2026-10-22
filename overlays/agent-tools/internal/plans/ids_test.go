package plans

import (
	"slices"
	"testing"
)

func TestIDs(t *testing.T) {
	_, p := openQuestions(t)
	ids, err := p.IDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"P1", "Q1", "Q2", "Q3", "P1.Q1"} {
		if !slices.Contains(ids, want) {
			t.Errorf("IDs() = %v, lacking %s", ids, want)
		}
	}
}
