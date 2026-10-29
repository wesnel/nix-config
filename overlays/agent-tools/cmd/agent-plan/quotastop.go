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
// fires after the window resets. The denial gives the main session
// resumeCall's call, and tells a sub-agent, which can schedule nothing, to
// hand back. A record it cannot read lets every
// call through. A threshold it cannot use, or a denial it cannot write,
// exits 1, which the hook's onFailure block makes a denial.
//
// With --harness codex it is Codex's PreToolUse hook instead, as
// codexQuotaStop says, and exits 2 where Claude Code's exits 1: Codex runs
// a tool whose hook exits 1, and blocks one whose hook exits 2 with a
// reason on stderr.
func quotaStop(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("agent-plan quota-stop", flag.ContinueOnError)
	flags.SetOutput(stderr)
	harness := flags.String("harness", "claude", "")
	threshold := flags.Int("threshold", 0, "")
	err := flags.Parse(args)
	codex := *harness == "codex"
	fail := 1
	if codex {
		fail = 2
	}
	if err != nil {
		return fail
	}
	if *threshold < 1 || *threshold > 100 || flags.NArg() > 0 || !codex && *harness != "claude" {
		fmt.Fprintln(stderr, "agent-plan quota-stop: takes --threshold N, a percentage from 1 to 100, and --harness claude or codex")
		return fail
	}
	var in struct {
		Tool string `json:"tool_name"`
		// Set only in a sub-agent or a teammate.
		AgentID string `json:"agent_id"`
		Input   struct {
			Command string `json:"command"`
			// ScheduleWakeup's.
			DelaySeconds *int `json:"delaySeconds"`
			Stop         bool `json:"stop"`
			// CronCreate's.
			Cron string `json:"cron"`
		} `json:"tool_input"`
	}
	json.Unmarshal(readAll(stdin), &in)
	if codex {
		return codexQuotaStop(in.Tool, in.Input.Command, in.AgentID != "", *threshold, stdout, stderr)
	}
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
	clock := quota.Clock(w.ResetsAt, now)
	next := "You are a sub-agent, which can schedule nothing: log where the work stands with agent-plan, then hand back with SubagentHandback, " +
		"saying the quota stop holds until " + clock + "; the session that started you schedules the resume."
	if in.AgentID == "" {
		tool, call := resumeCall(w.ResetsAt, clock, now)
		next = fmt.Sprintf("Log where the work stands with agent-plan, then schedule this session's resume, unless it is already scheduled, "+
			"by calling %s with %s (the stop lets it through), and end your turn.", tool, call)
	}
	reason := fmt.Sprintf("Quota stop: the Claude account's %s window is at %d%%, at or above the %d%% stop, until it resets at %s. "+
		"Until then no sub-agent starts and no tool runs but %s, agent-plan alone on a Bash line, "+
		"and a ScheduleWakeup or CronCreate that fires after the reset. %s "+
		"Nothing in a session lifts the stop: the operator does, by setting wgn.home.claude.quotaStop = null and switching.",
		w.Name, int(math.Floor(w.Used)), *threshold, clock, strings.Join(stopAllows, ", "), next)
	if err := json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}}); err != nil {
		fmt.Fprintln(stderr, reason)
		return 1
	}
	return 0
}

// codexStopAllows are the Codex tools the Codex quota stop lets through,
// besides agent-plan alone on a Bash line: Codex reads files through its
// shell, so a session can log, plan, ask the operator, and wait for or close
// a sub-agent, and nothing more.
var codexStopAllows = []string{"update_plan", "request_user_input", "wait_agent", "close_agent"}

// codexQuotaStop is quotaStop for a Codex session's call of tool, its shell
// command, if any, being command: it reads the windows codex-usage records
// in quota.CodexFile. Codex has no tool that schedules a later turn, so
// the refusal tells the session to end its turn for the operator to resume
// it after the reset, and a sub-agent to end its turn with its report.
func codexQuotaStop(tool, command string, subagent bool, threshold int, stdout, stderr io.Writer) int {
	if slices.Contains(codexStopAllows, tool) || tool == "Bash" && guard.ExcludedAlone(command, []string{"agent-plan"}) {
		return 0
	}
	now := time.Now()
	w, over := quota.Over(quota.Load(quota.CodexFile()), float64(threshold), now)
	if !over {
		return 0
	}
	clock := quota.Clock(w.ResetsAt, now)
	next := "Log where the work stands with agent-plan, then end your turn, telling the operator the stop holds until " + clock +
		": Codex cannot schedule its own resume, so the operator resumes this session after the reset."
	if subagent {
		next = "You are a sub-agent: log where the work stands with agent-plan, then end your turn with your report, " +
			"saying the quota stop holds until " + clock + "."
	}
	reason := fmt.Sprintf("Quota stop: the Codex account's %s window is at %d%%, at or above the %d%% stop, until it resets at %s. "+
		"Until then no sub-agent starts and no tool runs but %s, and agent-plan alone on a shell line. %s "+
		"Nothing in a session lifts the stop: the operator does, by setting wgn.home.codex.quotaStop = null and switching.",
		w.Name, int(math.Floor(w.Used)), threshold, clock, strings.Join(codexStopAllows, ", "), next)
	if err := json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}}); err != nil {
		fmt.Fprintln(stderr, reason)
		return 2
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

// resumeCall is the tool and JSON input that schedule a session's resume
// for the first minute after resets (a Unix time, shown as clock) that
// firesAfter accepts: ScheduleWakeup while that minute is within the hour
// its delay reaches, else a one-shot CronCreate pinned to it. Either way
// the stop lets the call through.
func resumeCall(resets int64, clock string, now time.Time) (string, string) {
	at := time.Unix(resets, 0).In(now.Location()).Truncate(time.Minute).Add(time.Minute)
	if !firesAfter(at, resets) {
		at = at.Add(time.Minute)
	}
	prompt := "The quota stop's window reset at " + clock + ". Resume the work it halted: " +
		"run agent-plan find, then agent-plan status on this session's plan, and carry on from where its log stands."
	var tool string
	var call any
	if delay := int(math.Ceil(at.Sub(now).Seconds())); delay <= wakeupMax {
		tool, call = "ScheduleWakeup", struct {
			DelaySeconds int    `json:"delaySeconds"`
			Reason       string `json:"reason"`
			Prompt       string `json:"prompt"`
			Noop         bool   `json:"noop"`
		}{delay, "resume after the quota reset at " + clock, prompt, false}
	} else {
		tool, call = "CronCreate", struct {
			Cron      string `json:"cron"`
			Prompt    string `json:"prompt"`
			Recurring bool   `json:"recurring"`
		}{fmt.Sprintf("%d %d %d %d *", at.Minute(), at.Hour(), at.Day(), int(at.Month())), prompt, false}
	}
	data, _ := json.Marshal(call)
	return tool, string(data)
}

// firesAfter is whether a wakeup due at fires runs after resets, a Unix
// time, even if Claude Code runs it early.
func firesAfter(fires time.Time, resets int64) bool {
	if fires.Minute()%30 == 0 {
		fires = fires.Add(-earlyBy)
	}
	return fires.Unix() > resets
}
