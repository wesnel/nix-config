package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/env"
)

// usageFile is where the status line records the quota windows, under the
// state directory of ctx's Env, which setup points.
func usageFile(ctx context.Context) string {
	return filepath.Join(env.From(ctx).Get("XDG_STATE_HOME"), "agent-plan", "usage.json")
}

// recorded is usage.json as the status line wrote it.
type recorded map[string]struct {
	Used       *float64 `json:"used_percentage"`
	ResetsAt   int64    `json:"resets_at"`
	ObservedAt int64    `json:"observed_at"`
}

func readUsage(ctx context.Context, t *testing.T) recorded {
	t.Helper()
	data, err := os.ReadFile(usageFile(ctx))
	if err != nil {
		t.Fatalf("no usage file: %v", err)
	}
	var r recorded
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("usage file %q: %v", data, err)
	}
	return r
}

// The status line records each quota window it is given, with when it saw
// it, for the quota stop to read.
func TestStatusLineRecordsQuota(t *testing.T) {
	t.Parallel()
	statusInput := func(fiveHour float64, resets int64) string {
		return fmt.Sprintf(`{"session_id":"s-1","cwd":"/nowhere","rate_limits":{`+
			`"five_hour":{"used_percentage":%g,"resets_at":%d},`+
			`"seven_day":{"used_percentage":31,"resets_at":%d}}}`, fiveHour, resets, resets+86400)
	}

	t.Run("each window with when it was seen", func(t *testing.T) {
		t.Parallel()
		ctx, _ := setup(t)
		resets := time.Now().Add(time.Hour).Unix()
		before := time.Now().Unix()
		if code, out, _ := agentPlan(ctx, t, statusInput(42, resets), "statusline"); code != 0 || out != "5h 42% · 7d 31%\n" {
			t.Fatalf("statusline: %d %q", code, out)
		}
		after := time.Now().Unix()
		r := readUsage(ctx, t)
		five, seven := r["five_hour"], r["seven_day"]
		if five.Used == nil || *five.Used != 42 || five.ResetsAt != resets {
			t.Errorf("five_hour: %+v", five)
		}
		if seven.Used == nil || *seven.Used != 31 || seven.ResetsAt != resets+86400 {
			t.Errorf("seven_day: %+v", seven)
		}
		for name, w := range r {
			if w.ObservedAt < before || w.ObservedAt > after {
				t.Errorf("%s observed at %d, not between %d and %d", name, w.ObservedAt, before, after)
			}
		}
	})

	t.Run("a state directory it cannot write", func(t *testing.T) {
		t.Parallel()
		ctx, _ := setup(t)
		blocked := filepath.Join(t.TempDir(), "file")
		os.WriteFile(blocked, nil, 0o600)
		ctx = withVars(ctx, "XDG_STATE_HOME="+blocked)
		code, out, errs := agentPlan(ctx, t, statusInput(42, time.Now().Add(time.Hour).Unix()), "statusline")
		if code != 0 || out != "5h 42% · 7d 31%\n" || errs != "" {
			t.Errorf("statusline: %d %q %q", code, out, errs)
		}
	})
}
