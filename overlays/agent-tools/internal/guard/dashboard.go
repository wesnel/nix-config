package guard

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/agentplans"
)

// The reasons the dashboard session's calls are denied for. They name no
// path: the session's conversation is on claude.ai too.
const (
	dashboardOnly = "The Agent plans dashboard session runs only the agent-plans skill's own commands and tools, with the arguments the skill gives them. " +
		"Follow the `agent-plans` skill as written, or put what you need in your reply for the operator."
	dashboardPage = "The dashboard session reads and writes only the Agent plans page its config names, and the page's documents only through files in the out directory. " +
		"Use that page's URL and the out directory as the `agent-plans` skill does, or tell the operator if the page's URL is not configured."
	dashboardNotice = "The dashboard session sends only the fixed notices the agent-plans skill names. " +
		"Send `devbox plan sync stalled`, `Standup digest ready` or `Demo digest ready` as the skill says, or nothing."
)

const (
	pageRequests = "plan-dash answer and dispatch act on what the Agent plans page asked, which only the dashboard session takes. " +
		"Leave them to that session, or ask the operator."
	sessionMark = "AGENT_PLANS_SESSION marks the Agent plans dashboard session, which its systemd unit alone sets. " +
		"Run the command without it, or ask the operator."
)

// checkSessionMark denies a call that sets AGENT_PLANS_SESSION: by an
// assignment the shell, env or export makes, or by arguments that pass it
// on, as `systemctl --user set-environment`, `declare -x`, fish's `set -x`
// and `launchctl setenv` do. A variable set where the guard does not see
// it, as by a drop-in or environment.d file, passes: the mark is a
// seatbelt, not a wall.
func checkSessionMark(c call) string {
	for _, kv := range c.env {
		if name, _, _ := strings.Cut(kv, "="); name == agentplans.SessionEnv {
			return sessionMark
		}
	}
	setter := len(c.words) > 0 && !c.expands[0] && (base(c.words[0]) == "set" || base(c.words[0]) == "launchctl")
	for i, w := range c.words {
		if c.expands[i] {
			continue
		}
		if strings.HasPrefix(w, agentplans.SessionEnv+"=") || setter && w == agentplans.SessionEnv && i+1 < len(c.words) {
			return sessionMark
		}
	}
	return ""
}

// dashboardCommands are the commands the session runs with Bash, exactly.
var dashboardCommands = []string{"plan-dash export", "plan-dash answer", "plan-dash dispatch"}

// dashboardNotices are the notices it sends with PushNotification.
var dashboardNotices = []string{"devbox plan sync stalled", "Standup digest ready", "Demo digest ready"}

// threadID is a comment thread's ID, as the session resolves it.
var threadID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// checkDashboard is why the dashboard session may not make a call, or ""
// for one of the agent-plans skill's own: plan-dash's export, answer and
// dispatch, watch under Monitor, the page's database and comments through
// files in the out directory alone, foreground sub-agents without
// isolation, the agent-plans skill, ToolSearch and fixed notices. Every
// other tool, Read included, and every other argument to these, is denied:
// nothing the page or the host holds is read into the conversation, which
// is on claude.ai too. What may be uploaded from the out directory the
// page can read anyway.
func checkDashboard(tool string, toolInput json.RawMessage) string {
	var in map[string]json.RawMessage
	if json.Unmarshal(toolInput, &in) != nil {
		return dashboardOnly
	}
	str := func(name string) (string, bool) {
		raw, ok := in[name]
		if !ok {
			return "", false
		}
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	}
	set := func(name string) bool {
		raw, ok := in[name]
		return ok && string(raw) != "null" && string(raw) != `""` && string(raw) != "false"
	}
	switch tool {
	case "Bash":
		if command, ok := str("command"); ok && slices.Contains(dashboardCommands, command) {
			return ""
		}
	case "Monitor":
		if command, ok := str("command"); ok && command == "plan-dash watch" && !set("ws") {
			return ""
		}
	case "ArtifactData":
		return checkArtifactData(in)
	case "ArtifactComments":
		url, ok := str("url")
		if !ok || !isPage(url) {
			return dashboardPage
		}
		action, _ := str("action")
		thread, _ := str("thread_id")
		switch {
		case action == "watch" && !set("replies"):
			return ""
		case action == "resolve" && threadID.MatchString(thread):
			return ""
		}
	case "Agent":
		if !set("isolation") && !set("run_in_background") {
			return ""
		}
	case "Skill":
		if skill, ok := str("skill"); ok && skill == "agent-plans" {
			return ""
		}
	case "ToolSearch":
		return ""
	case "PushNotification":
		message, _ := str("message")
		status, _ := str("status")
		if !slices.Contains(dashboardNotices, message) || status != "" && status != "proactive" {
			return dashboardNotice
		}
		return ""
	}
	return dashboardOnly
}

// checkArtifactData allows a call on the page alone, whose every file is
// in the out directory: a read's out_dir, a write's file_path and each
// batch entry's.
func checkArtifactData(in map[string]json.RawMessage) string {
	var args struct {
		URL      *string `json:"url"`
		OutDir   *string `json:"out_dir"`
		FilePath *string `json:"file_path"`
		Writes   *[]struct {
			FilePath *string `json:"file_path"`
		} `json:"writes"`
	}
	raw, _ := json.Marshal(in)
	if json.Unmarshal(raw, &args) != nil || args.URL == nil || !isPage(*args.URL) {
		return dashboardPage
	}
	paths := []*string{args.FilePath}
	if args.Writes != nil {
		for _, w := range *args.Writes {
			paths = append(paths, w.FilePath)
		}
	}
	for _, p := range paths {
		if p != nil && !inOut(*p, false) {
			return dashboardPage
		}
	}
	if args.OutDir != nil && !inOut(*args.OutDir, true) {
		return dashboardPage
	}
	return ""
}

// isPage reports whether url is the page the operator's config names; with
// none configured, nothing is.
func isPage(url string) bool {
	page, err := agentplans.URL()
	return err == nil && url == page
}

// inOut reports whether path, absolute, is in the out directory, or with
// self is that directory, both as written and once the links of the part
// of it that exists are resolved.
func inOut(path string, self bool) bool {
	out := agentplans.Out()
	within := func(p, dir string) bool { return self && p == dir || strings.HasPrefix(p, dir+"/") }
	if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) || !within(filepath.Clean(path), out) {
		return false
	}
	realOut, err := filepath.EvalSymlinks(out)
	if err != nil {
		realOut = out
	}
	real, ok := resolveExisting(filepath.Clean(path))
	return ok && within(real, realOut)
}

// resolveExisting is path with the links of its longest existing part
// resolved, and the rest as it is.
func resolveExisting(path string) (string, bool) {
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		real, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(real, rest), true
		}
		// What exists but cannot be resolved, such as a dangling link, is
		// no place to write.
		if _, lerr := os.Lstat(p); lerr == nil || !errors.Is(err, fs.ErrNotExist) {
			return "", false
		}
		if p == filepath.Dir(p) {
			return path, true
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// inDashboardSession reports whether the hook runs in the dashboard
// session, which its unit alone marks.
func inDashboardSession() bool { return os.Getenv(agentplans.SessionEnv) != "" }

// dashboardMain is the dashboard session's own hook, on every tool: it
// allows the session's own calls and denies, never asks, every other,
// since the phone could approve a prompt. A call it cannot read is denied.
func dashboardMain(stdin io.Reader, stdout io.Writer) int {
	var in input
	reason := dashboardOnly
	if json.NewDecoder(stdin).Decode(&in) == nil {
		reason = checkDashboard(in.ToolName, in.ToolInput)
	}
	decision := "allow"
	if reason != "" {
		decision = "deny"
	}
	return decide(stdout, decision, reason)
}

// decide writes a Claude Code PreToolUse decision, and returns the hook's
// exit status: 2, which blocks the call, where it cannot.
func decide(stdout io.Writer, decision, reason string) int {
	out := map[string]string{"hookEventName": "PreToolUse", "permissionDecision": decision}
	if reason != "" {
		out["permissionDecisionReason"] = reason
	}
	if json.NewEncoder(stdout).Encode(map[string]any{"hookSpecificOutput": out}) != nil {
		return 2
	}
	return 0
}
