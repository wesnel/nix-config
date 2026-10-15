package review

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
)

type schema struct {
	Required   []string `json:"required"`
	Properties struct {
		Findings struct {
			Items struct {
				Required   []string `json:"required"`
				Properties struct {
					Kind struct {
						Enum []string `json:"enum"`
					} `json:"kind"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"findings"`
	} `json:"properties"`
}

func loadSchema(t *testing.T) schema {
	t.Helper()
	var s schema
	if err := json.Unmarshal(Schema, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// TestContract holds the reviewer's instructions to the schema its reply is
// checked against.
func TestContract(t *testing.T) {
	// Each kind the schema accepts is one the contract defines and the check
	// keeps.
	t.Run("defines every kind", func(t *testing.T) {
		schemaKinds := loadSchema(t).Properties.Findings.Items.Properties.Kind.Enum
		if len(schemaKinds) == 0 {
			t.Fatal("the schema names no kinds")
		}
		for _, kind := range schemaKinds {
			if !strings.Contains(Contract, "\n- `"+kind+"`: ") {
				t.Errorf("the contract does not define %s", kind)
			}
			if !kinds[kind] {
				t.Errorf("the check drops findings of kind %s", kind)
			}
		}
		if len(kinds) != len(schemaKinds) {
			t.Errorf("the check keeps %d kinds, the schema has %d", len(kinds), len(schemaKinds))
		}
	})

	// A breach of a rule the repository writes down is a finding of its own
	// kind, which names the doc and the rule.
	t.Run("convention kind", func(t *testing.T) {
		found := false
		for _, kind := range loadSchema(t).Properties.Findings.Items.Properties.Kind.Enum {
			found = found || kind == "convention"
		}
		if !found {
			t.Error("the schema has no convention kind")
		}
		_, rules, _ := strings.Cut(Contract, "## Rules")
		rules, _, _ = strings.Cut(rules, "## Checks")
		if !strings.Contains(rules, "`convention`") {
			t.Errorf("the rules do not say a written rule is a finding:\n%s", rules)
		}
	})

	// The reply the contract shows has the fields the schema requires, no
	// more and no fewer.
	t.Run("example", func(t *testing.T) {
		_, rest, ok := strings.Cut(Contract, "```json\n")
		example, _, closed := strings.Cut(rest, "```")
		if !ok || !closed {
			t.Fatal("the contract shows no JSON reply")
		}
		var reply struct {
			Findings []map[string]any `json:"findings"`
		}
		var top map[string]any
		if err := json.Unmarshal([]byte(example), &top); err != nil {
			t.Fatalf("the example is not JSON: %v", err)
		}
		json.Unmarshal([]byte(example), &reply)
		s := loadSchema(t)
		if got, want := keys(top), sorted(s.Required); got != want {
			t.Errorf("the example's fields are %s, the schema's %s", got, want)
		}
		if len(reply.Findings) == 0 {
			t.Fatal("the example has no finding")
		}
		if got, want := keys(reply.Findings[0]), sorted(s.Properties.Findings.Items.Required); got != want {
			t.Errorf("the example finding's fields are %s, the schema's %s", got, want)
		}
	})
}

func keys(m map[string]any) string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return sorted(out)
}

func sorted(s []string) string {
	s = append([]string{}, s...)
	sort.Strings(s)
	return strings.Join(s, ",")
}
