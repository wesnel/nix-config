package refusal

import "testing"

func TestSaysInstead(t *testing.T) {
	for reason, want := range map[string]bool{
		"Pushing goes through `pr-loop push`.":                       false,
		"No. Try something else.":                                    false,
		"Pushing is checked. Use `pr-loop push`.":                    true,
		"Pruning is Wesley's call; leave it to him.":                 false,
		"Pruning is a decision. Leave `pr-loop end --prune` to him.": true,
		"`git x. y` is an alias; ask Wesley.":                        true,
	} {
		if got := SaysInstead(reason); got != want {
			t.Errorf("SaysInstead(%q) = %v", reason, got)
		}
	}
}
