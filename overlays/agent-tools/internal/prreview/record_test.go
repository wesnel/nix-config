package prreview

import (
	"encoding/json"
	"testing"

	"github.com/wesnel/agent-tools/internal/review"
)

func TestSeverity(t *testing.T) {
	// Every kind a reviewer may report reaches review-guide with a severity.
	t.Run("of every kind", func(t *testing.T) {
		var s struct {
			Properties struct {
				Findings struct {
					Items struct {
						Properties struct {
							Kind struct {
								Enum []string `json:"enum"`
							} `json:"kind"`
						} `json:"properties"`
					} `json:"items"`
				} `json:"findings"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(review.Schema, &s); err != nil {
			t.Fatal(err)
		}
		kinds := s.Properties.Findings.Items.Properties.Kind.Enum
		if len(kinds) == 0 {
			t.Fatal("the schema names no kinds")
		}
		for _, kind := range kinds {
			if severities[kind] == "" {
				t.Errorf("kind %s has no severity", kind)
			}
		}
	})

	t.Run("of a breach of a written rule", func(t *testing.T) {
		if severities["convention"] != "issue" {
			t.Errorf("a breach of a written rule is %q, want issue", severities["convention"])
		}
	})
}
