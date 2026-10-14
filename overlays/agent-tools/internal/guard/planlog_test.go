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

// plansHome gives the test a home directory whose agent-tools config puts
// the plans under ~/git/org/agents, and returns a plan's directory there.
func plansHome(t *testing.T) (h, plan string) {
	t.Helper()
	h = fakeHome(t)
	plans := filepath.Join(h, "git", "org", "agents")
	plan = filepath.Join(plans, "box", "2026-10-14-x")
	os.MkdirAll(plan, 0o755)
	for _, f := range []string{"plan.org", "log.org", "log.org.lock"} {
		os.WriteFile(filepath.Join(plan, f), nil, 0o644)
	}
	config := filepath.Join(h, "config.json")
	data, _ := json.Marshal(map[string]any{"plans": map[string]string{"dir": plans, "host": "box"}})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	return h, plan
}

// A plan's log.org is appended to by agent-plan alone: no file tool and no
// shell write may change it, while the plan files and agent-plan itself
// pass.
func TestPlanLog(t *testing.T) {
	h, plan := plansHome(t)
	log := filepath.Join(plan, "log.org")
	work := filepath.Join(h, "work")
	os.MkdirAll(work, 0o755)
	patch := func(header, path string) map[string]string {
		return map[string]string{"command": "*** Begin Patch\n*** " + header + ": " + path + "\n@@\n+x\n*** End Patch\n"}
	}
	files := []struct {
		deny  bool
		tool  string
		input any
	}{
		{true, "Edit", map[string]string{"file_path": log, "old_string": "a", "new_string": "b"}},
		{true, "Write", map[string]string{"file_path": log, "content": "* done S1\n"}},
		{true, "Write", map[string]string{"file_path": log + ".lock", "content": "0 x\n"}},
		{true, "MultiEdit", map[string]any{"file_path": log, "edits": []map[string]string{{"old_string": "a", "new_string": "b"}}}},
		{true, "apply_patch", patch("Update File", log)},
		{true, "apply_patch", patch("Delete File", log)},
		{true, "edit_file", map[string]string{"path": log, "original_content": "a", "new_content": "b"}},
		{true, "write_file", map[string]string{"path": log, "content": "x"}},
		{true, "move_file", map[string]string{"source": log, "destination": "/tmp/x"}},
		{false, "Edit", map[string]string{"file_path": filepath.Join(plan, "plan.org"), "old_string": "a", "new_string": "b"}},
		{false, "Write", map[string]string{"file_path": filepath.Join(plan, "P1.org"), "content": "* Plan\n"}},
		{false, "Write", map[string]string{"file_path": filepath.Join(work, "log.org"), "content": "mine\n"}},
		{false, "edit_file", map[string]string{"path": filepath.Join(plan, "plan.org"), "original_content": "a", "new_content": "b"}},
	}
	for _, c := range files {
		input, _ := json.Marshal(c.input)
		server := ""
		if strings.HasSuffix(c.tool, "_file") {
			server = "eca"
		}
		reason := check(c.tool, server, input, baseline, work)
		if (reason != "") != c.deny {
			t.Errorf("%s %s: deny=%v, reason %q", c.tool, input, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%s: reason %q does not say what to do instead", c.tool, reason)
		}
	}
	commands := []struct {
		deny    bool
		dir     string
		command string
	}{
		{true, work, "echo '* done S1' >> " + log},
		{true, work, "printf x > ~/git/org/agents/box/2026-10-14-x/log.org"},
		{true, work, "printf x >>$HOME/git/org/agents/box/2026-10-14-x/log.org"},
		{true, work, "cd " + plan + " && echo x >> log.org"},
		{true, plan, "echo x > log.org.lock"},
		{true, work, "tee -a " + log + " < /tmp/e"},
		{true, work, "sed -i '' s/done/verified/ " + log},
		{true, work, "perl -pi -e s/a/b/ " + log},
		{true, work, "cp /tmp/forged " + log},
		{true, work, "mv " + log + " /tmp/old"},
		{true, work, "rm " + log},
		{true, work, "truncate -s 0 " + log},
		{true, work, "dd if=/dev/null of=" + log},
		{true, "", "echo x >> log.org"},
		{false, work, "agent-plan log " + plan + " --item S1 --kind done --summary 'Add >> the loop'"},
		{false, work, "agent-plan new --title 'Add the loop' --slug add-loop --branch main..loop"},
		{false, work, "agent-plan status " + plan + " --format json > /tmp/status.json"},
		{false, work, "agent-plan goal " + plan + " > /tmp/goal.md"},
		{false, work, "cat " + log},
		{false, work, "cp " + log + " /tmp/copy.org"},
		{false, work, "grep -c '^\\* ' " + log},
		{false, work, "echo x > log.org"},
		{false, work, "echo x >> /tmp/log.org"},
	}
	for _, c := range commands {
		reason := judge(t, c.dir, c.command)
		if (reason != "") != c.deny {
			t.Errorf("%q in %q: deny=%v, reason %q", c.command, c.dir, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
		}
	}
}

// A reviewer reads plans; it neither creates them nor logs to them.
func TestReviewerPlans(t *testing.T) {
	_, plan := plansHome(t)
	for command, deny := range map[string]bool{
		"agent-plan log " + plan + " --item S1 --kind verified": true,
		"agent-plan new --title T --slug t":                     true,
		"agent-plan status " + plan:                             false,
		"agent-plan goal " + plan + " --part P1":                false,
		"agent-plan find --branch x":                            false,
	} {
		input, _ := json.Marshal(map[string]string{"command": command})
		reason := check("Bash", "", input, reviewer, "")
		if (reason != "") != deny {
			t.Errorf("reviewer %q: deny=%v, reason %q", command, !deny, reason)
		}
		if reason := check("Bash", "", input, owner, ""); reason != "" {
			t.Errorf("owner %q: denied %q", command, reason)
		}
	}
}

// The hook, as Claude Code calls it, lets agent-plan through and stops an
// Edit of a log.
func TestPlanLogHook(t *testing.T) {
	_, plan := plansHome(t)
	for _, c := range []struct{ event, want string }{
		{`{"tool_name":"Bash","cwd":"` + plan + `","tool_input":{"command":"agent-plan log ` + plan + ` --item S1 --kind done"}}`, ""},
		{`{"tool_name":"Edit","cwd":"` + plan + `","tool_input":{"file_path":"` + plan + `/log.org","old_string":"a","new_string":"b"}}`, `"permissionDecision":"deny"`},
	} {
		var out bytes.Buffer
		Main([]string{"--baseline"}, strings.NewReader(c.event), &out, io.Discard)
		if (c.want == "" && out.Len() != 0) || !strings.Contains(out.String(), c.want) {
			t.Errorf("%s: printed %s", c.event, out.String())
		}
	}
}
