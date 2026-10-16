package signpass

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Notify logs how the pass with sign-pass ID id ended, given what Run,
// Resume or Plan returned, in the plan whose log requested it (see
// plans.RecordSign), and returns that plan's file. The exit status is the
// one the command exits with: 0, 2 for a refusal, 1 for any other error.
func Notify(id string, result any, err error, now time.Time) (string, error) {
	c, lerr := plans.Load()
	if lerr != nil {
		return "", lerr
	}
	return c.RecordSign(id, summarize(result, err), now)
}

// summarize is a pass's result as one line: how many commits it signed,
// across every branch of a multi-branch pass, and which branches it pushed;
// or the error.
func summarize(result any, err error) plans.SignResult {
	if err != nil {
		var r refusal.Refusal
		if errors.As(err, &r) {
			return plans.SignResult{Exit: 2, Summary: err.Error()}
		}
		return plans.SignResult{Exit: 1, Summary: err.Error()}
	}
	var r struct {
		Signed   any                        `json:"signed"`
		Branches map[string]json.RawMessage `json:"branches"`
		Pushed   map[string]string          `json:"pushed"`
	}
	data, _ := json.Marshal(result)
	json.Unmarshal(data, &r)
	signed, counted := 0, false
	count := func(v any) {
		if n, ok := v.(float64); ok {
			signed += int(n)
			counted = true
		}
	}
	count(r.Signed)
	for _, b := range r.Branches {
		var step struct {
			Signed any `json:"signed"`
		}
		json.Unmarshal(b, &step)
		count(step.Signed)
	}
	var parts []string
	switch {
	case counted:
		parts = append(parts, fmt.Sprintf("%d signed", signed))
	case len(r.Pushed) == 0:
		parts = append(parts, "nothing to sign")
	}
	if len(r.Pushed) > 0 {
		pushed := make([]string, 0, len(r.Pushed))
		for b := range r.Pushed {
			pushed = append(pushed, b)
		}
		slices.Sort(pushed)
		parts = append(parts, "pushed "+strings.Join(pushed, ", "))
	}
	return plans.SignResult{Exit: 0, Summary: strings.Join(parts, "; ")}
}
