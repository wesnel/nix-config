package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/env"
)

// fakeCodexAppServer stands in for `codex app-server`, which would reach the
// operator's OpenAI account: it answers initialize, then
// account/rateLimits/read with a 5h window at FAKE_CODEX_USED percent
// resetting at FAKE_CODEX_RESETS and a 7d window at 10%. internal/codexrate
// tests the protocol's edges.
func fakeCodexAppServer() int {
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		switch {
		case strings.Contains(scan.Text(), `"id":1,`):
			fmt.Println(`{"id":1,"result":{"userAgent":"fixture"}}`)
		case strings.Contains(scan.Text(), `"account/rateLimits/read"`):
			resets, _ := strconv.ParseInt(os.Getenv("FAKE_CODEX_RESETS"), 10, 64)
			fmt.Printf(`{"id":2,"result":{"ordinaryUsageAllowed":true,"rateLimits":{"limitId":"codex",`+
				`"primary":{"usedPercent":%s,"windowDurationMins":300,"resetsAt":%d},`+
				`"secondary":{"usedPercent":10,"windowDurationMins":10080,"resetsAt":%d}}}}`+"\n",
				os.Getenv("FAKE_CODEX_USED"), resets, resets+72*3600)
		}
	}
	return 0
}

// fakeCodexOnPath is setup's context, with a codex on PATH that answers
// with the 5h window at used percent, resetting at resets. codex-usage
// finds codex, and codexrate starts it, through this process's
// environment, so the fake goes there too, and a test that uses it runs
// alone.
func fakeCodexOnPath(t *testing.T, used float64, resets time.Time) context.Context {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"PATH": bin, "AGENT_PLAN_FAKE_CODEX": "1",
		"FAKE_CODEX_USED": strconv.FormatFloat(used, 'f', -1, 64), "FAKE_CODEX_RESETS": strconv.FormatInt(resets.Unix(), 10)}
	ctx, _ := setup(t)
	for name, value := range vars {
		t.Setenv(name, value)
		ctx = withVars(ctx, name+"="+value)
	}
	return ctx
}

// codexUsageFile is where codex-usage records, under the state directory
// of ctx's Env.
func codexUsageFile(ctx context.Context) string {
	return filepath.Join(env.From(ctx).Get("XDG_STATE_HOME"), "agent-plan", "codex-usage.json")
}

// codex-usage records the Codex account's windows from codex app-server,
// apart from Claude's.
func TestCodexUsage(t *testing.T) {
	resets := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	t.Run("recorded", func(t *testing.T) {
		ctx := fakeCodexOnPath(t, 95, resets)
		if code, out, errs := agentPlan(ctx, t, "", "codex-usage"); code != 0 {
			t.Fatalf("codex-usage: %d %q %q", code, out, errs)
		}
		data, err := os.ReadFile(codexUsageFile(ctx))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"five_hour":{"used_percentage":95,"resets_at":` + strconv.FormatInt(resets.Unix(), 10),
			`"seven_day":{"used_percentage":10,`} {
			if !strings.Contains(string(data), want) {
				t.Errorf("codex-usage.json %s lacks %s", data, want)
			}
		}
		if _, err := os.Stat(usageFile(ctx)); err == nil {
			t.Error("codex-usage wrote Claude's usage.json")
		}
	})
	t.Run("no codex", func(t *testing.T) {
		ctx, _ := setup(t)
		empty := t.TempDir()
		t.Setenv("PATH", empty)
		ctx = withVars(ctx, "PATH="+empty)
		if code, _, errs := agentPlan(ctx, t, "", "codex-usage"); code == 0 || errs == "" {
			t.Errorf("codex-usage without codex: %d %q", code, errs)
		}
		if _, err := os.Stat(codexUsageFile(ctx)); err == nil {
			t.Error("recorded without a reading")
		}
	})
}
