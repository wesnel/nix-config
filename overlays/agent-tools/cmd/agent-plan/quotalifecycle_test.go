package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A real systemd user manager would alter operator services; this subprocess
// models its queued stop operation and refuses every other action.
func quotaTestSystemctl() {
	if strings.Join(os.Args[1:], " ") != "--user --no-block stop agent-plans.service" {
		fmt.Fprintln(os.Stderr, "unexpected systemctl operation")
		os.Exit(2)
	}
	if os.Getenv("QUOTA_TEST_STOP_FAILS") == "1" {
		os.Exit(1)
	}
	if err := os.WriteFile(os.Getenv("QUOTA_TEST_SYSTEMCTL"), []byte("stopped"), 0o600); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func quotaLifecycle(t *testing.T) string {
	t.Helper()
	setup(t)
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(dir, "systemctl")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	stopped := filepath.Join(dir, "stopped")
	t.Setenv("QUOTA_TEST_SYSTEMCTL", stopped)
	t.Setenv("QUOTA_TEST_STOP_FAILS", "")
	return stopped
}

func quotaState(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "agent-tools", "review-availability", "claude.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestQuotaLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		blocked     bool
	}{
		{"missing is eligible", "", false},
		{"cleared is eligible", `{}`, false},
		{"disabled", `{"reason":"disabled"}`, true},
		{"unknown reset", `{"reason":"quota"}`, true},
		{"rate limit", `{"reason":"rate_limit"}`, true},
		{"reset passed", `{"reason":"quota","until":1}`, false},
		{"corrupt", `{`, true},
		{"null state", `null`, true},
		{"null reason", `{"reason":null}`, true},
		{"null reset", `{"reason":"quota","until":null}`, true},
		{"invalid reason", `{"reason":"other"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stopped := quotaLifecycle(t)
			if tc.state != "" {
				quotaState(t, tc.state)
			}
			code, _, errs := agentPlan(t, "", "quota-admit")
			if (code == 1) != tc.blocked || code > 1 {
				t.Fatalf("admission exit %d, blocked %v: %s", code, tc.blocked, errs)
			}
			for range 2 {
				if code, _, errs := agentPlan(t, "", "quota-control"); code != 0 {
					t.Fatalf("controller exit %d: %s", code, errs)
				}
			}
			_, err := os.Stat(stopped)
			if tc.blocked && err != nil || !tc.blocked && !os.IsNotExist(err) {
				t.Fatalf("stop requested = %v, want %v", err == nil, tc.blocked)
			}
		})
	}
	t.Run("off remains authoritative", func(t *testing.T) {
		stopped := quotaLifecycle(t)
		dir := filepath.Join(os.Getenv("XDG_STATE_HOME"), "agent-plans")
		if err := os.MkdirAll(filepath.Join(dir, "off"), 0o700); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if code, _, _ := agentPlan(t, "", "quota-admit"); code != 1 {
				t.Fatalf("off admission exit %d", code)
			}
			if code, _, errs := agentPlan(t, "", "quota-control"); code != 0 {
				t.Fatalf("off controller exit %d: %s", code, errs)
			}
		}
		if _, err := os.Stat(stopped); err != nil {
			t.Fatal("off did not stop worker")
		}
	})
	t.Run("old exhausted window and reset", func(t *testing.T) {
		quotaLifecycle(t)
		writeUsage(t, 100, time.Now().Add(time.Hour), time.Now().Add(-time.Hour))
		if code, _, _ := agentPlan(t, "", "quota-admit"); code != 1 {
			t.Fatalf("old exhaustion admission exit %d", code)
		}
		writeUsage(t, 100, time.Unix(1, 0), time.Unix(1, 0))
		if code, _, errs := agentPlan(t, "", "quota-admit"); code != 0 {
			t.Fatalf("reset admission exit %d: %s", code, errs)
		}
	})
	t.Run("unreadable state", func(t *testing.T) {
		stopped := quotaLifecycle(t)
		path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "agent-tools", "review-availability", "claude.json")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := agentPlan(t, "", "quota-admit"); code != 1 {
			t.Fatalf("unreadable state admission exit %d", code)
		}
		if code, _, errs := agentPlan(t, "", "quota-control"); code != 0 {
			t.Fatalf("unreadable state controller exit %d: %s", code, errs)
		}
		if _, err := os.Stat(stopped); err != nil {
			t.Fatal("unreadable state did not stop worker")
		}
	})
}

func TestQuotaFailure(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		held        bool
		code        int
	}{
		{"structured rate limit", `{"hook_event_name":"StopFailure","error":"rate_limit","error_details":"private details"}`, true, 0},
		{"other error prose", `{"hook_event_name":"StopFailure","error":"server_error","last_assistant_message":"rate_limit"}`, false, 0},
		{"wrong event", `{"hook_event_name":"Stop","error":"rate_limit"}`, false, 1},
		{"broken input", `{`, false, 1},
		{"trailing input", `{"hook_event_name":"StopFailure","error":"rate_limit"} {}`, false, 1},
		{"oversized input", `{"hook_event_name":"StopFailure","error":"rate_limit"}` + strings.Repeat(" ", 1<<20), false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stopped := quotaLifecycle(t)
			code, out, errs := agentPlan(t, tc.input, "quota-failure", "--stop-dashboard")
			if code != tc.code || out != "" || strings.Contains(errs, "private details") {
				t.Fatalf("hook exit %d, stdout %q, stderr %q", code, out, errs)
			}
			code, _, _ = agentPlan(t, "", "quota-admit")
			if (code == 1) != tc.held || code > 1 {
				t.Fatalf("admission after hook exit %d, held %v", code, tc.held)
			}
			_, err := os.Stat(stopped)
			if tc.held && err != nil || !tc.held && !os.IsNotExist(err) {
				t.Fatalf("stop requested = %v, want %v", err == nil, tc.held)
			}
			if tc.held {
				data, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "agent-tools", "review-availability", "claude.json"))
				if err != nil || string(data) != `{"reason":"rate_limit"}` {
					t.Fatalf("stored state %s: %v", data, err)
				}
			}
		})
	}
	t.Run("hold failure still stops", func(t *testing.T) {
		stopped := quotaLifecycle(t)
		path := quotaState(t, `{}`)
		if err := os.Mkdir(path+".lock", 0o700); err != nil {
			t.Fatal(err)
		}
		code, _, errs := agentPlan(t, `{"hook_event_name":"StopFailure","error":"rate_limit"}`, "quota-failure", "--stop-dashboard")
		if code != 1 || errs == "" {
			t.Fatalf("persistence failure exit %d: %s", code, errs)
		}
		if _, err := os.Stat(stopped); err != nil {
			t.Fatal("failed persistence prevented stop")
		}
	})
	t.Run("busy hold lock still stops", func(t *testing.T) {
		stopped := quotaLifecycle(t)
		path := quotaState(t, `{}`)
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { lock.Close() })
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, exe, "quota-failure", "--stop-dashboard")
		cmd.Env = append(os.Environ(), "AGENT_PLAN_TEST_MAIN=1")
		cmd.Stdin = strings.NewReader(`{"hook_event_name":"StopFailure","error":"rate_limit"}`)
		output, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			t.Fatal("busy state lock prevented bounded stop request")
		}
		if err == nil || !strings.Contains(string(output), "lock") {
			t.Fatalf("busy lock was not reported: %v %s", err, output)
		}
		if _, err := os.Stat(stopped); err != nil {
			t.Fatal("busy state lock prevented stop")
		}
	})
	t.Run("stop failure preserves hold", func(t *testing.T) {
		quotaLifecycle(t)
		t.Setenv("QUOTA_TEST_STOP_FAILS", "1")
		if code, _, _ := agentPlan(t, `{"hook_event_name":"StopFailure","error":"rate_limit"}`, "quota-failure", "--stop-dashboard"); code != 1 {
			t.Fatalf("stop failure exit %d", code)
		}
		if code, _, _ := agentPlan(t, "", "quota-admit"); code != 1 {
			t.Fatalf("stop failure lost hold: exit %d", code)
		}
	})
	t.Run("record only outside dashboard host", func(t *testing.T) {
		stopped := quotaLifecycle(t)
		if code, _, errs := agentPlan(t, `{"hook_event_name":"StopFailure","error":"rate_limit"}`, "quota-failure"); code != 0 {
			t.Fatalf("record only exit %d: %s", code, errs)
		}
		if _, err := os.Stat(stopped); !os.IsNotExist(err) {
			t.Fatal("record only requested a service stop")
		}
		if code, _, _ := agentPlan(t, "", "quota-admit"); code != 1 {
			t.Fatalf("record only lost hold: exit %d", code)
		}
	})
	for _, tc := range []struct{ name, before, after string }{
		{"disabled stays", `{"reason":"disabled"}`, `{"reason":"disabled"}`},
		{"unknown quota stays", `{"reason":"quota"}`, `{"reason":"quota"}`},
		{"timed quota becomes conservative hold", fmt.Sprintf(`{"reason":"quota","until":%d}`, time.Now().Add(time.Hour).Unix()), `{"reason":"rate_limit"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quotaLifecycle(t)
			path := quotaState(t, tc.before)
			for range 2 {
				if code, _, errs := agentPlan(t, `{"hook_event_name":"StopFailure","error":"rate_limit"}`, "quota-failure"); code != 0 {
					t.Fatalf("hold exit %d: %s", code, errs)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.after {
				t.Fatalf("merged hold %s, want %s: %v", got, tc.after, err)
			}
		})
	}
}
