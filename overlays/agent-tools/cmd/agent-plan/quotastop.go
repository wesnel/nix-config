package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"slices"
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
// --threshold percent, it denies every call but stopAllows' and a Bash
// line that is one agent-plan command. A record it cannot read lets every
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
	reset := "until it resets at " + quota.Clock(w.ResetsAt, now)
	if w.ResetsAt == 0 {
		reset = "with no verified reset; the operator must clear the hold after recovery"
	}
	reason := fmt.Sprintf("Quota stop: the Claude account's %s window is at %d%%, at or above the %d%% stop, %s. "+
		"Until then no sub-agent starts and no tool runs but %s, and agent-plan alone on a Bash line. "+
		"Log where the work stands with agent-plan, then hand back with SubagentHandback or end your turn. "+
		"Nothing in a session lifts the stop: the operator does, by setting wgn.home.claude.quotaStop = null and switching.",
		w.Name, int(math.Floor(w.Used)), *threshold, reset, strings.Join(stopAllows, ", "))
	if err := json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": map[string]string{
		"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": reason}}); err != nil {
		fmt.Fprintln(stderr, reason)
		return 1
	}
	return 0
}
