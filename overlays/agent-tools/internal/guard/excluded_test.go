package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExcludedAlone covers the commands Claude Code's sandbox excludes,
// which run outside it only as a command line of their own: anywhere else
// on a line they run sandboxed and fail as if missing, so the guard
// refuses the line and says to run them bare.
func TestExcludedAlone(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"guard": {"excludedCommands": `+
		`["gh-read *", "git delegate *", "git-delegate *", "pr-loop wait *", "agent-plan log *"]}}`), 0o600)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	work := t.TempDir()
	// hook runs the baseline hook on a shell call, as Claude Code makes it
	// or, with codex, as Codex does, and returns its decision and reason.
	hook := func(t *testing.T, line string, codex bool) (string, string) {
		t.Helper()
		event := map[string]any{"tool_name": "Bash", "cwd": work, "tool_input": map[string]string{"command": line}}
		if codex {
			event["turn_id"] = "t"
		}
		data, _ := json.Marshal(event)
		var out bytes.Buffer
		Main([]string{"--baseline"}, bytes.NewReader(data), &out, io.Discard)
		var decision struct {
			HookSpecificOutput struct{ PermissionDecision, PermissionDecisionReason string }
		}
		json.Unmarshal(out.Bytes(), &decision)
		return decision.HookSpecificOutput.PermissionDecision, decision.HookSpecificOutput.PermissionDecisionReason
	}
	// Each form a line can give a command, the first the command alone.
	// Claude Code runs some of them outside its sandbox all the same (the
	// timeout, nice and nohup wrappers, 2>&1, and && or ; between excluded
	// commands); they are refused by design, since the command alone always
	// runs outside it, and the reason says only that the line may not.
	forms := []func(cmd string) string{
		func(cmd string) string { return cmd },
		func(cmd string) string { return cmd + " | cat" },
		func(cmd string) string { return "true && " + cmd },
		func(cmd string) string { return cmd + "; true" },
		func(cmd string) string { return cmd + " &" },
		func(cmd string) string { return "cd " + work + " && " + cmd },
		func(cmd string) string { return "FOO=1 " + cmd },
		func(cmd string) string { return "env X=1 " + cmd },
		func(cmd string) string { return "timeout 60 " + cmd },
		func(cmd string) string { return "command " + cmd },
		func(cmd string) string { return cmd + " > out.json" },
		func(cmd string) string { return cmd + " 2>&1" },
		func(cmd string) string { return cmd + " $(cat args)" },
		func(cmd string) string { return "bash -c '" + cmd + "'" },
		// Redirections before the program, spaced and attached.
		func(cmd string) string { return "< /dev/null " + cmd },
		func(cmd string) string { return "2>/dev/null " + cmd },
		func(cmd string) string { return "2>&1 " + cmd },
		func(cmd string) string { return "</dev/null timeout 60 " + cmd },
		func(cmd string) string { return "FOO=1 > out.json " + cmd },
		func(cmd string) string { return "true; < /dev/null " + cmd },
		// The program's name quoted, which Claude Code's match of the
		// line's text does not take for it.
		func(cmd string) string {
			program, rest, _ := strings.Cut(cmd, " ")
			return `"` + program + `" ` + rest
		},
	}
	for _, c := range []struct {
		cmd       string
		aloneDeny bool   // refused even alone
		says      string // what the reason holds
	}{
		{"agent-plan log /p.org --item S1 --kind started --summary '> a | b'", false, "may run sandboxed"},
		{"git delegate --plan /p/2026-10-14-x.box.org main..topic", false, "may run sandboxed"},
		{"gh-read pr view 5 -R o/r", false, "may run sandboxed"},
		{"pr-loop wait ci 5", false, "may run sandboxed"},
		// gh cannot read its login in the sandbox, in any form.
		{"gh pr view 5 -R o/r", true, "`gh-read`"},
	} {
		for i, form := range forms {
			line, deny := form(c.cmd), i > 0 || c.aloneDeny
			t.Run(caseName(deny, line), func(t *testing.T) {
				decision, reason := hook(t, line, false)
				if (decision == "deny") != deny {
					t.Fatalf("decision %q, reason %q", decision, reason)
				}
				if deny && (!strings.Contains(reason, c.says) || !saysInstead(reason)) {
					t.Errorf("reason %q does not hold %q", reason, c.says)
				}
			})
		}
	}
	// Lines the shell reads otherwise than as the command alone: a
	// redirection after a # that is no comment's, a program named in a
	// quoting form, and a command after a comment line.
	for _, line := range []string{
		`gh-read pr view $'\' #' > /tmp/zq49`,
		`gh-read pr view ${x:- #} > /tmp/zq49`,
		`gh-read pr view $"x #" > /tmp/zq49`,
		`$'gh-read' pr view 1`,
		`$"gh-read" pr view 1`,
		"# c\ngh-read pr view 1",
	} {
		t.Run(caseName(true, line), func(t *testing.T) {
			if decision, reason := hook(t, line, false); decision != "deny" || !strings.Contains(reason, "may run sandboxed") {
				t.Errorf("decision %q, reason %q", decision, reason)
			}
		})
	}
	// A headless reviewer's sandbox excludes nothing, so its hook leaves
	// these lines to whatever else it checks.
	for _, line := range []string{"true && gh-read pr view 5 -R o/r", "gh pr view 5 -R o/r | cat"} {
		data, _ := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": work, "tool_input": map[string]string{"command": line}})
		var out bytes.Buffer
		Main([]string{"--reviewer"}, bytes.NewReader(data), &out, io.Discard)
		if strings.Contains(out.String(), "may run sandboxed") || strings.Contains(out.String(), "runs in the sandbox") {
			t.Errorf("a reviewer's %q: %s", line, out.String())
		}
	}
	// git's own options before delegate keep the line from matching. The
	// hint names what works: delegate takes no -C, and resolves its range
	// from any worktree.
	decision, reason := hook(t, "git -C "+work+" delegate --plan /p/2026-10-14-x.box.org main..topic", false)
	if decision != "deny" || strings.Contains(reason, "-C") || !strings.Contains(reason, "any worktree") {
		t.Errorf("git -C DIR delegate: %q, %q", decision, reason)
	}
	// A subcommand left in the sandbox runs there in any line, and Codex,
	// whose sandbox excludes nothing, runs any line as it is.
	for _, c := range []struct {
		line  string
		codex bool
	}{
		{"agent-plan status /p.org | head", false},
		// A comment's > is no redirection.
		{"pr-loop wait ci 5 # comment > x", false},
		{"gh --version", false},
		// Not a read gh-read runs: it is left to fail, or not, as it would.
		{"gh api graphql -f query=x", false},
		{"cd " + work + " && pr-loop wait ci 5", true},
		{"gh pr view 5 -R o/r", true},
	} {
		if decision, reason := hook(t, c.line, c.codex); decision == "deny" {
			t.Errorf("%q (codex %v) is refused: %q", c.line, c.codex, reason)
		}
	}
}
