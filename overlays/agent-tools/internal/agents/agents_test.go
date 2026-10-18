package agents

import "testing"

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

func TestAccessorsCopy(t *testing.T) {
	Roles()["x"] = "y"
	Claude()[0] = "x"
	if _, ok := Roles()["x"]; ok {
		t.Error("Roles shares its map")
	}
	if Claude()[0] == "x" {
		t.Error("Claude shares its slice")
	}
}
