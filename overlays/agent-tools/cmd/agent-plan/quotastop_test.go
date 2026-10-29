package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runQuotaStop runs the hook at threshold 90 on a call of tool with input.
func runQuotaStop(t *testing.T, tool, input string) (int, string, string) {
	t.Helper()
	event := fmt.Sprintf(`{"session_id":"s-1","cwd":"/nowhere","hook_event_name":"PreToolUse","tool_name":%q,"tool_input":%s}`,
		tool, input)
	return agentPlan(t, event, "quota-stop", "--threshold", "90")
}

// writeUsage writes usage.json with the five_hour window at used, and
// seven_day at 10%.
func writeUsage(t *testing.T, used float64, resets, observed time.Time) {
	t.Helper()
	os.MkdirAll(filepath.Dir(usageFile(t)), 0o700)
	data := fmt.Sprintf(`{"five_hour":{"used_percentage":%g,"resets_at":%d,"observed_at":%d},`+
		`"seven_day":{"used_percentage":10,"resets_at":%d,"observed_at":%d}}`,
		used, resets.Unix(), observed.Unix(), resets.Add(72*time.Hour).Unix(), observed.Unix())
	if err := os.WriteFile(usageFile(t), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// denial is the reason of the hook's denial in out, or "" when out lets
// the call through.
func denial(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		return ""
	}
	var o struct {
		Hook struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &o); err != nil || o.Hook.Event != "PreToolUse" || o.Hook.Decision != "deny" {
		t.Fatalf("not a PreToolUse denial: %q", out)
	}
	return o.Hook.Reason
}

// At or above the threshold in a current window, the quota stop refuses
// starting sub-agents and every call but reading, handing back and
// agent-plan's own, naming the window, its share and its reset, and how
// the operator lifts it.
func TestQuotaStop(t *testing.T) {
	agent := `{"description":"Build P1","prompt":"go"}`
	bash := func(command string) string { return fmt.Sprintf(`{"command":%q}`, command) }
	resets := time.Now().Add(2 * time.Hour).Truncate(time.Minute)
	clock := resets.Format("15:04")

	for _, c := range []struct {
		name, tool, input string
		used              float64
		resets, observed  time.Time
		stops             bool
	}{
		{"below the threshold", "Agent", agent, 89, resets, time.Now(), false},
		{"at it", "Agent", agent, 90, resets, time.Now(), true},
		{"above it", "Bash", bash("go test ./..."), 97.5, resets, time.Now(), true},
		{"a file edit above it", "Edit", `{"file_path":"/x","old_string":"a","new_string":"b"}`, 97.5, resets, time.Now(), true},
		{"an MCP call above it", "mcp__linear__save_issue", `{}`, 97.5, resets, time.Now(), true},
		{"a message to a sub-agent above it", "SendMessage", `{"to":"a1","message":"go on"}`, 97.5, resets, time.Now(), true},
		{"a reading over 5 minutes old", "Agent", agent, 97.5, resets, time.Now().Add(-6 * time.Minute), false},
		{"a window that has reset", "Agent", agent, 97.5, time.Now().Add(-time.Minute), time.Now().Add(-2 * time.Minute), false},
		{"a read above it", "Read", `{"file_path":"/x"}`, 97.5, resets, time.Now(), false},
		{"a search above it", "Grep", `{"pattern":"x"}`, 97.5, resets, time.Now(), false},
		{"a file search above it", "Glob", `{"pattern":"*"}`, 97.5, resets, time.Now(), false},
		{"a tool lookup above it", "ToolSearch", `{"query":"select:Monitor"}`, 97.5, resets, time.Now(), false},
		{"a skill above it", "Skill", `{"skill":"plan-work"}`, 97.5, resets, time.Now(), false},
		{"a question to the operator above it", "AskUserQuestion", `{"questions":[]}`, 97.5, resets, time.Now(), false},
		{"stopping a sub-agent above it", "TaskStop", `{"task_id":"a1"}`, 97.5, resets, time.Now(), false},
		{"a hand-back above it", "SubagentHandback", `{"message":"done"}`, 97.5, resets, time.Now(), false},
		{"agent-plan above it", "Bash", bash("agent-plan log /p.org --item P1 --kind handed-back"), 97.5, resets, time.Now(), false},
		{"agent-plan in a pipeline above it", "Bash", bash("agent-plan status /p.org | go run x"), 97.5, resets, time.Now(), true},
		{"agent-plan after cd above it", "Bash", bash("cd /x && agent-plan status /p.org"), 97.5, resets, time.Now(), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			setup(t)
			writeUsage(t, c.used, c.resets, c.observed)
			code, out, errs := runQuotaStop(t, c.tool, c.input)
			if code != 0 {
				t.Fatalf("quota-stop: %d %q %q", code, out, errs)
			}
			reason := denial(t, out)
			if !c.stops {
				if reason != "" {
					t.Errorf("stopped: %q", reason)
				}
				return
			}
			for _, want := range []string{"5h", fmt.Sprintf("%d%%", int(c.used)), clock, "quotaStop = null", "SubagentHandback"} {
				if !strings.Contains(reason, want) {
					t.Errorf("reason %q does not say %q", reason, want)
				}
			}
		})
	}

	// With nothing recorded, or a record it cannot read, nothing stops.
	for name, data := range map[string]string{"no usage file": "", "a broken usage file": "{"} {
		t.Run(name, func(t *testing.T) {
			setup(t)
			if data != "" {
				os.MkdirAll(filepath.Dir(usageFile(t)), 0o700)
				os.WriteFile(usageFile(t), []byte(data), 0o600)
			}
			if code, out, errs := runQuotaStop(t, "Agent", agent); code != 0 || out != "" {
				t.Errorf("quota-stop: %d %q %q", code, out, errs)
			}
		})
	}

	// A threshold it cannot use is the configuration's error: exit 1, a
	// hook failure, with the reason on stderr.
	for _, args := range [][]string{{"quota-stop"}, {"quota-stop", "--threshold", "0"}, {"quota-stop", "--threshold", "101"},
		{"quota-stop", "--threshold", "ninety"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			setup(t)
			writeUsage(t, 97.5, resets, time.Now())
			if code, out, errs := agentPlan(t, `{"tool_name":"Agent","tool_input":{}}`, args...); code != 1 || out != "" || errs == "" {
				t.Errorf("%d %q %q", code, out, errs)
			}
		})
	}
}

// With both windows over the threshold, the stop holds until the later of
// their resets, and names that one: a resume at the earlier reset would
// meet the stop again.
func TestQuotaStopBothWindows(t *testing.T) {
	setup(t)
	now := time.Now()
	five, seven := now.Add(time.Hour).Truncate(time.Minute), now.Add(50*time.Hour).Truncate(time.Minute)
	os.MkdirAll(filepath.Dir(usageFile(t)), 0o700)
	data := fmt.Sprintf(`{"five_hour":{"used_percentage":95,"resets_at":%d,"observed_at":%d},`+
		`"seven_day":{"used_percentage":92,"resets_at":%d,"observed_at":%d}}`,
		five.Unix(), now.Unix(), seven.Unix(), now.Unix())
	if err := os.WriteFile(usageFile(t), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, _ := runQuotaStop(t, "Agent", `{}`)
	reason := denial(t, out)
	if want := seven.Format("Mon 2 15:04"); !strings.Contains(reason, "7d window is at 92%") || !strings.Contains(reason, want) {
		t.Errorf("reason %q does not name the 7d window and its reset %s", reason, want)
	}
}

// cronAt is CronCreate's input for a one-shot at t's minute, in local time
// as Claude Code reads a cron expression.
func cronAt(t time.Time) string {
	t = t.Local()
	return fmt.Sprintf(`{"cron":"%d %d %d %d *","prompt":"resume","recurring":false}`, t.Minute(), t.Hour(), t.Day(), int(t.Month()))
}

// wakeIn is ScheduleWakeup's input for a wakeup in d.
func wakeIn(d time.Duration) string {
	return fmt.Sprintf(`{"delaySeconds":%d,"reason":"quota reset","prompt":"resume","noop":false}`, int(d/time.Second))
}

// The stop lets a session schedule its resume: a ScheduleWakeup or
// CronCreate that fires after the reset it names goes through, and one
// that would fire before it, into the stop, is refused. ScheduleWakeup
// clamps its delay to an hour, so a longer one fires at the hour.
func TestQuotaStopResume(t *testing.T) {
	now := time.Now()
	resets := now.Add(40 * time.Minute).Truncate(time.Minute)
	for _, c := range []struct {
		name, tool, input string
		resets            time.Time
		stops             bool
	}{
		{"a wakeup before the reset", "ScheduleWakeup", wakeIn(20 * time.Minute), resets, true},
		{"a wakeup after it", "ScheduleWakeup", wakeIn(50 * time.Minute), resets, false},
		{"a wakeup past the hour it is clamped to", "ScheduleWakeup", wakeIn(2 * time.Hour), now.Add(90 * time.Minute), true},
		{"ending the wakeups", "ScheduleWakeup", `{"stop":true}`, resets, true},
		{"a one-shot before the reset", "CronCreate", cronAt(resets.Add(-10 * time.Minute)), resets, true},
		{"a one-shot after it", "CronCreate", cronAt(resets.Add(10 * time.Minute)), resets, false},
		{"a one-shot after a reset over an hour away", "CronCreate", cronAt(now.Add(3 * time.Hour)), now.Add(150 * time.Minute), false},
		{"a cron that fires every five minutes", "CronCreate", `{"cron":"*/5 * * * *","prompt":"resume"}`, resets, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			setup(t)
			writeUsage(t, 95, c.resets, now)
			_, out, _ := runQuotaStop(t, c.tool, c.input)
			if reason := denial(t, out); (reason != "") != c.stops {
				t.Errorf("stops %v, want %v: %q", reason != "", c.stops, reason)
			}
		})
	}

	// A one-shot on :00 or :30 may run up to 90 seconds early, so one due
	// a minute after a reset may still run before it.
	at := func(clock string) time.Time {
		v, _ := time.ParseInLocation("15:04", clock, time.Local)
		return v
	}
	for _, c := range []struct {
		fires, resets string
		after         bool
	}{{"18:30", "18:29", false}, {"18:31", "18:30", true}, {"18:30", "18:28", true}, {"18:30", "18:30", false}} {
		if got := firesAfter(at(c.fires), at(c.resets).Unix()); got != c.after {
			t.Errorf("a wakeup at %s after a reset at %s: %v, want %v", c.fires, c.resets, got, c.after)
		}
	}

	// With no window over the threshold there is no reset to wait for,
	// and nothing is stopped.
	for _, c := range []struct{ tool, input string }{
		{"ScheduleWakeup", wakeIn(2 * time.Minute)},
		{"CronCreate", `{"cron":"*/5 * * * *","prompt":"resume"}`},
	} {
		t.Run(c.tool+" with no known reset", func(t *testing.T) {
			setup(t)
			if _, out, errs := runQuotaStop(t, c.tool, c.input); out != "" {
				t.Errorf("stopped: %q %q", out, errs)
			}
		})
	}
}

// givenCall is the call a refusal tells the session to make to schedule
// its resume, as tool and input, or "" where it names none.
func givenCall(t *testing.T, reason string) (string, string) {
	t.Helper()
	for _, tool := range []string{"ScheduleWakeup", "CronCreate"} {
		_, rest, ok := strings.Cut(reason, tool+" with ")
		if !ok {
			continue
		}
		var input json.RawMessage
		if err := json.NewDecoder(strings.NewReader(rest)).Decode(&input); err != nil {
			t.Fatalf("%s call in %q: %v", tool, reason, err)
		}
		return tool, string(input)
	}
	return "", ""
}

// A stopped session's refusal gives the exact call that schedules its
// resume just after the reset, which the stop then lets through: a
// ScheduleWakeup within the hour it can reach, else a one-shot CronCreate.
// A sub-agent, which has neither tool, is told to hand back instead.
func TestQuotaStopResumeCall(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name, tool string
		resets     time.Time
	}{
		{"a reset within the hour", "ScheduleWakeup", now.Add(40 * time.Minute).Truncate(time.Minute)},
		{"a reset hours away", "CronCreate", now.Add(3 * time.Hour).Truncate(time.Minute)},
	} {
		t.Run(c.name, func(t *testing.T) {
			setup(t)
			writeUsage(t, 95, c.resets, now)
			_, out, _ := runQuotaStop(t, "Bash", `{"command":"go test ./..."}`)
			tool, input := givenCall(t, denial(t, out))
			if tool != c.tool {
				t.Fatalf("the refusal gives a %q call, want %s: %q", tool, c.tool, out)
			}
			var call struct {
				DelaySeconds int    `json:"delaySeconds"`
				Reason       string `json:"reason"`
				Prompt       string `json:"prompt"`
				Noop         *bool  `json:"noop"`
				Cron         string `json:"cron"`
				Recurring    *bool  `json:"recurring"`
			}
			json.Unmarshal([]byte(input), &call)
			if !strings.Contains(call.Prompt, "agent-plan status") || !strings.Contains(call.Prompt, c.resets.Format("15:04")) {
				t.Errorf("the prompt %q does not say to resume the plan after the %s reset", call.Prompt, c.resets.Format("15:04"))
			}
			var fires time.Time
			if tool == "ScheduleWakeup" {
				if call.Reason == "" || call.Noop == nil {
					t.Errorf("ScheduleWakeup needs reason and noop: %s", input)
				}
				fires = now.Add(time.Duration(call.DelaySeconds) * time.Second)
			} else {
				if call.Recurring == nil || *call.Recurring {
					t.Errorf("CronCreate is not a one-shot: %s", input)
				}
				var m, h, d, mo int
				fmt.Sscanf(call.Cron, "%d %d %d %d *", &m, &h, &d, &mo)
				fires = time.Date(c.resets.Year(), time.Month(mo), d, h, m, 0, 0, time.Local)
			}
			if !fires.After(c.resets) || fires.After(c.resets.Add(3*time.Minute)) {
				t.Errorf("the resume fires at %s, not just after the reset at %s: %s", fires, c.resets, input)
			}
			if _, out, _ := runQuotaStop(t, tool, input); out != "" {
				t.Errorf("the stop refused the call it gave: %q", denial(t, out))
			}
		})
	}

	t.Run("a sub-agent", func(t *testing.T) {
		setup(t)
		writeUsage(t, 95, now.Add(40*time.Minute), now)
		event := `{"session_id":"s-1","agent_id":"a1","agent_type":"builder","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"go test ./..."}}`
		_, out, _ := agentPlan(t, event, "quota-stop", "--threshold", "90")
		reason := denial(t, out)
		if tool, _ := givenCall(t, reason); tool != "" || !strings.Contains(reason, "SubagentHandback") {
			t.Errorf("a sub-agent is not told to hand back, or is given a %q call: %q", tool, reason)
		}
	})
}

// Every open session's status line records the windows; the stop holds
// whichever of them reports last.
func TestQuotaStopAcrossSessions(t *testing.T) {
	status := func(used float64, resets time.Time) string {
		return fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":%g,"resets_at":%d}}}`, used, resets.Unix())
	}
	resets := time.Now().Add(2 * time.Hour)
	stopped := func(t *testing.T) bool {
		t.Helper()
		_, out, _ := runQuotaStop(t, "Agent", `{}`)
		return denial(t, out) != ""
	}

	// An idle session repeats its reading of the window before this one.
	t.Run("an idle session's earlier window", func(t *testing.T) {
		setup(t)
		agentPlan(t, status(95, resets), "statusline")
		agentPlan(t, status(50, time.Now().Add(-3*time.Hour)), "statusline")
		if !stopped(t) {
			t.Error("a reading of a window that has reset lifted the stop")
		}
	})

	// A reading over the threshold seen 6 minutes ago no longer counts
	// alone, but a fresh, lower one of the same window still over it does.
	t.Run("a fresh, lower reading over the threshold", func(t *testing.T) {
		setup(t)
		writeUsage(t, 95, resets, time.Now().Add(-6*time.Minute))
		agentPlan(t, status(91, resets), "statusline")
		if !stopped(t) {
			t.Error("a fresh 91% reading did not hold the stop at 90%")
		}
	})
}
