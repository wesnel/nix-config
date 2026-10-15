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
		{"the operator only in the reason", "Pruning is the operator's call; leave it to them.", false},
		{"command handed to the operator after the reason", "Pruning is a decision. Leave `pr-loop end --prune` to them.", true},
		{"full stop inside backticks", "`git x. y` is an alias; ask the operator.", true},
		{"the operator after the reason", "Pruning is a decision. The operator decides.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SaysInstead(tc.reason); got != tc.want {
				t.Errorf("SaysInstead(%q) = %v", tc.reason, got)
			}
		})
	}
}
