package usage

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// account/rateLimits/read's result names each window by its length:
// primary and secondary are whichever windows the account has.
func TestFromCodexRateLimits(t *testing.T) {
	now := time.Unix(1793250000, 0)
	t.Run("the operator's reply", func(t *testing.T) {
		r, ok := FromCodexRateLimits(fixture(t, "codex-ratelimits.json"), now)
		want := []Window{
			{Name: "5h", Used: 0, ResetsAt: 1793268465, ObservedAt: 1793250000},
			{Name: "7d", Used: 0, ResetsAt: 1793855265, ObservedAt: 1793250000},
		}
		if !ok || !reflect.DeepEqual(r, want) {
			t.Errorf("got %+v, %v; want %+v", r, ok, want)
		}
	})
	window := func(used string, mins, resets int) string {
		return fmt.Sprintf(`{"usedPercent":%s,"windowDurationMins":%d,"resetsAt":%d}`, used, mins, resets)
	}
	for _, c := range []struct {
		name, result string
		want         []Window
	}{
		{"the weekly window as primary", `{"rateLimits":{"primary":` + window("97.5", 10080, 1793855265) +
			`,"secondary":` + window("40", 300, 1793268465) + `}}`,
			[]Window{{Name: "5h", Used: 40, ResetsAt: 1793268465, ObservedAt: 1793250000},
				{Name: "7d", Used: 97.5, ResetsAt: 1793855265, ObservedAt: 1793250000}}},
		{"no secondary", `{"rateLimits":{"primary":` + window("12", 300, 1793268465) + `,"secondary":null}}`,
			[]Window{{Name: "5h", Used: 12, ResetsAt: 1793268465, ObservedAt: 1793250000}}},
		{"a window of another length", `{"rateLimits":{"primary":` + window("12", 60, 1793268465) + `}}`, nil},
		{"no share used", `{"rateLimits":{"primary":{"windowDurationMins":300,"resetsAt":1793268465}}}`, nil},
		{"no reset", `{"rateLimits":{"primary":{"usedPercent":12,"windowDurationMins":300}}}`, nil},
		{"no rateLimits", `{"ordinaryUsageAllowed":true}`, nil},
		{"not JSON", `{`, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, ok := FromCodexRateLimits([]byte(c.result), now)
			if ok != (c.want != nil) || !reflect.DeepEqual(r, c.want) {
				t.Errorf("got %+v, %v; want %+v", r, ok, c.want)
			}
		})
	}
}

// The Codex account's windows are kept apart from Claude's, which quota-stop
// reads for Claude Code sessions.
func TestCodexFile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got := CodexFile(); got != filepath.Join("/state", "agent-plan", "codex-usage.json") || got == File() {
		t.Errorf("CodexFile() = %q, File() = %q", got, File())
	}
}
