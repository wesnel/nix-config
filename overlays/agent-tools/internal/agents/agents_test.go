package agents

import (
	"slices"
	"testing"
)

func TestLookup(t *testing.T) {
	m := map[string]int{"builder": 1, "reviewer": 2}
	for _, c := range []struct {
		agentType string
		want      int
		ok        bool
	}{
		{"builder", 1, true},
		{"reviewer", 2, true},
		{"plugin:reviewer", 2, true},
		{"a:b:reviewer", 2, true},
		{"adversarial-reviewer", 0, false},
		{"reviewer:x", 0, false},
		{"plugin:", 0, false},
		{"", 0, false},
	} {
		if got, ok := Lookup(m, c.agentType); got != c.want || ok != c.ok {
			t.Errorf("Lookup(%q) = %v, %v, want %v, %v", c.agentType, got, ok, c.want, c.ok)
		}
	}
}

// TestUsageAgentsAreClaudes holds usage to agents the operator defines.
func TestUsageAgentsAreClaudes(t *testing.T) {
	claude := Claude()
	if len(Usage()) == 0 {
		t.Fatal("agents.json has no usage")
	}
	for name := range Usage() {
		if !slices.Contains(claude, name) {
			t.Errorf("usage names %q, which is not in claude %v", name, claude)
		}
	}
}

func TestAccessorsCopy(t *testing.T) {
	if len(Claude()) == 0 {
		t.Fatal("agents.json has no claude agents")
	}
	Roles()["x"] = "y"
	Usage()["x"] = "y"
	Claude()[0] = "x"
	if _, ok := Roles()["x"]; ok {
		t.Error("Roles shares its map")
	}
	if _, ok := Usage()["x"]; ok {
		t.Error("Usage shares its map")
	}
	if Claude()[0] == "x" {
		t.Error("Claude shares its slice")
	}
}
