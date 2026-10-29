package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// fakeCodexOnPath puts a codex on PATH that answers with the 5h window at
// used percent, resetting at resets.
func fakeCodexOnPath(t *testing.T, used float64, resets time.Time) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("AGENT_PLAN_FAKE_CODEX", "1")
	t.Setenv("FAKE_CODEX_USED", strconv.FormatFloat(used, 'f', -1, 64))
	t.Setenv("FAKE_CODEX_RESETS", strconv.FormatInt(resets.Unix(), 10))
}

func codexUsageFile(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.Getenv("XDG_STATE_HOME"), "agent-plan", "codex-usage.json")
}

// codex-usage records the Codex account's windows from codex app-server,
// apart from Claude's.
func TestCodexUsage(t *testing.T) {
	resets := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	t.Run("recorded", func(t *testing.T) {
		setup(t)
		fakeCodexOnPath(t, 95, resets)
		if code, out, errs := agentPlan(t, "", "codex-usage"); code != 0 {
			t.Fatalf("codex-usage: %d %q %q", code, out, errs)
		}
		data, err := os.ReadFile(codexUsageFile(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{`"five_hour":{"used_percentage":95,"resets_at":` + strconv.FormatInt(resets.Unix(), 10),
			`"seven_day":{"used_percentage":10,`} {
			if !strings.Contains(string(data), want) {
				t.Errorf("codex-usage.json %s lacks %s", data, want)
			}
		}
		if _, err := os.Stat(usageFile(t)); err == nil {
			t.Error("codex-usage wrote Claude's usage.json")
		}
	})
	t.Run("no codex", func(t *testing.T) {
		setup(t)
		t.Setenv("PATH", t.TempDir())
		if code, _, errs := agentPlan(t, "", "codex-usage"); code == 0 || errs == "" {
			t.Errorf("codex-usage without codex: %d %q", code, errs)
		}
		if _, err := os.Stat(codexUsageFile(t)); err == nil {
			t.Error("recorded without a reading")
		}
	})
}
