package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/guard"
	quota "github.com/wesnel/agent-tools/internal/usage"
)

// stopAllows are the tools the quota stop lets through, besides agent-plan
// alone on a Bash line: enough to read the plan, log, ask the operator,
// stop running sub-agents and hand back.
var stopAllows = []string{"Read", "Grep", "Glob", "ToolSearch", "Skill", "AskUserQuestion", "TaskStop", "SubagentHandback"}

// quotaStop is Claude Code's PreToolUse hook for the quota stop: while a
// window recorded in quota.File that still counts is at or above
// --threshold percent, it denies every call but stopAllows', a Bash line
// that is one agent-plan command, and a ScheduleWakeup or CronCreate that
// fires after the window resets. A record it cannot read lets every
// call through. A threshold it cannot use, or a denial it cannot write,
// exits 1, which the hook's onFailure block makes a denial.
func quotaStop(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("agent-plan quota-stop", flag.ContinueOnError)
	flags.SetOutput(stderr)
	threshold := flags.Int("threshold", 0, "")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if *threshold < 1 || *threshold > 100 || flags.NArg() > 0 {
		fmt.Fprintln(stderr, "agent-plan quota-stop: takes --threshold N, a percentage from 1 to 100")
		return 1
	}
	var in struct {
		Tool  string `json:"tool_name"`
		Input struct {
			Command string `json:"command"`
			// ScheduleWakeup's.
			DelaySeconds *int `json:"delaySeconds"`
			Stop         bool `json:"stop"`
			// CronCreate's.
			Cron string `json:"cron"`
		} `json:"tool_input"`
	}
	json.Unmarshal(readAll(stdin), &in)
	if slices.Contains(stopAllows, in.Tool) || in.Tool == "Bash" && guard.ExcludedAlone(in.Input.Command, []string{"agent-plan"}) {
		return 0
	}
	now := time.Now()
	w, over := quota.Over(quota.Load(quota.File()), float64(*threshold), now)
	if !over {
		return 0
	}
	var fires time.Time
	switch in.Tool {
	case "ScheduleWakeup":
		if !in.Input.Stop && in.Input.DelaySeconds != nil {
			fires = wakeupFires(*in.Input.DelaySeconds, now)
		}
	case "CronCreate":
		fires = cronFires(in.Input.Cron, now)
	}
	if !fires.IsZero() && firesAfter(fires, w.ResetsAt) {
		return 0
	}
	reason := fmt.Sprintf("Quota stop: the Claude account's %s window is at %d%%, at or above the %d%% stop, until it resets at %s. "+
		"Until then no sub-agent starts and no tool runs but %s, and agent-plan alone on a Bash line. "+
		"Log where the work stands with agent-plan, then hand back with SubagentHandback or end your turn. "+
		"Nothing in a session lifts the stop: the operator does, by setting wgn.home.claude.quotaStop = null and switching.",
		w.Name, int(math.Floor(w.Used)), *threshold, quota.Clock(w.ResetsAt, now), strings.Join(stopAllows, ", "))
	if err := json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}}); err != nil {
		fmt.Fprintln(stderr, reason)
		return 1
	}
	return 0
}

// The bounds Claude Code clamps ScheduleWakeup's delaySeconds to, as its
// tool schema says ("Clamped to [60, 3600] by the runtime").
const (
	wakeupMin = 60
	wakeupMax = 3600
)

// earlyBy is how early Claude Code may run a one-shot scheduled for :00 or
// :30, which its scheduled-tasks documentation gives as up to 90 seconds.
const earlyBy = 90 * time.Second

// wakeupFires is the minute a ScheduleWakeup of delay seconds made at now
// fires in: the delay clamped as Claude Code clamps it, and the time
// truncated to the minute, since the wakeup is held as a one-shot cron.
func wakeupFires(delay int, now time.Time) time.Time {
	return now.Add(time.Duration(min(max(delay, wakeupMin), wakeupMax)) * time.Second).Truncate(time.Minute)
}

// cronFires is when CronCreate's expression next fires after now: a
// one-shot's form, minute, hour, day of the month and month each one
// number, in now's zone, and any day of the week. It is zero for every
// other form, which the stop cannot place and so refuses.
func cronFires(expr string, now time.Time) time.Time {
	f := strings.Fields(expr)
	if len(f) != 5 || f[4] != "*" {
		return time.Time{}
	}
	var n [4]int
	for i, limit := range [4][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}} {
		v, err := strconv.Atoi(f[i])
		if err != nil || v < limit[0] || v > limit[1] || strconv.Itoa(v) != f[i] {
			return time.Time{}
		}
		n[i] = v
	}
	for year := now.Year(); year <= now.Year()+1; year++ {
		t := time.Date(year, time.Month(n[3]), n[2], n[1], n[0], 0, 0, now.Location())
		if t.Day() == n[2] && t.After(now) {
			return t
		}
	}
	return time.Time{}
}

// firesAfter is whether a wakeup due at fires runs after resets, a Unix
// time, even if Claude Code runs it early.
func firesAfter(fires time.Time, resets int64) bool {
	if fires.Minute()%30 == 0 {
		fires = fires.Add(-earlyBy)
	}
	return fires.Unix() > resets
}
