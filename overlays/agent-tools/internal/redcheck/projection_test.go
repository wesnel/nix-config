package redcheck

import "testing"

// A changed file is the change unless some suite takes it as a test file:
// whole, or inline with its production code unchanged in every suite that
// splits it.
func TestCodeChanged(t *testing.T) {
	whole := &testSelection{whole: []string{"a_test.go"}, inlineCode: map[string]bool{}}
	testsOnly := &testSelection{inlineCode: map[string]bool{"lib.rs": false}}
	withCode := &testSelection{inlineCode: map[string]bool{"lib.rs": true}}
	for _, tc := range []struct {
		name       string
		changed    []string
		selections []*testSelection
		want       bool
	}{
		{"a test file one suite takes whole", []string{"a_test.go"}, []*testSelection{whole, testsOnly}, false},
		{"an inline file whose tests alone changed", []string{"lib.rs"}, []*testSelection{testsOnly}, false},
		{"an inline file whose code changed", []string{"lib.rs"}, []*testSelection{testsOnly, withCode}, true},
		{"a file no suite takes", []string{"main.go"}, []*testSelection{whole, testsOnly}, true},
		{"nothing changed", nil, []*testSelection{whole}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeChanged(tc.changed, tc.selections); got != tc.want {
				t.Errorf("codeChanged(%q) = %v, want %v", tc.changed, got, tc.want)
			}
		})
	}
}
