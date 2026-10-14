package refusal

import "testing"

func TestSaysInstead(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		want         bool
	}{
		{"command only in the reason", "Pushing goes through `pr-loop push`.", false},
		{"no hand-off after the reason", "No. Try something else.", false},
		{"command after the reason", "Pushing is checked. Use `pr-loop push`.", true},
		{"Wesley only in the reason", "Pruning is Wesley's call; leave it to him.", false},
		{"command handed to Wesley after the reason", "Pruning is a decision. Leave `pr-loop end --prune` to him.", true},
		{"full stop inside backticks", "`git x. y` is an alias; ask Wesley.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SaysInstead(tc.reason); got != tc.want {
				t.Errorf("SaysInstead(%q) = %v", tc.reason, got)
			}
		})
	}
}
