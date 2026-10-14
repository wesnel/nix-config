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

// TestContractDefinesEveryKind holds the reviewer's instructions to the
// schema its reply is checked against: each kind the schema accepts is one
// the contract defines.
func TestContractDefinesEveryKind(t *testing.T) {
	kinds := loadSchema(t).Properties.Findings.Items.Properties.Kind.Enum
	if len(kinds) == 0 {
		t.Fatal("the schema names no kinds")
	}
	for _, kind := range kinds {
		if !strings.Contains(Contract, "\n- `"+kind+"`: ") {
			t.Errorf("the contract does not define %s", kind)
		}
	}
}

// TestContractExample holds the reply the contract shows to the fields the
// schema requires, no more and no fewer.
func TestContractExample(t *testing.T) {
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
