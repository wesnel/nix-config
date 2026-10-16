package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// plansHome gives the test a home directory whose agent-tools config puts
// the plans under ~/git/notes/agents, and returns a plan's file there.
func plansHome(t *testing.T) (h, plan string) {
	t.Helper()
	h = fakeHome(t)
	plans := filepath.Join(h, "git", "notes", "agents")
	os.MkdirAll(plans, 0o755)
	for _, f := range []string{"2026-10-14-x.box.org", "2026-10-14-x.box.P1.org", "2026-10-14-x.box.log.org",
		"2026-10-14-x.box.log.org.lock"} {
		os.WriteFile(filepath.Join(plans, f), nil, 0o644)
	}
	config := filepath.Join(h, "config.json")
	data, _ := json.Marshal(map[string]any{"plans": map[string]string{"dir": plans, "host": "box"}})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	return h, filepath.Join(plans, "2026-10-14-x.box.org")
}

// planLogPaths makes the home plansHome does, with a work directory in it,
// and returns a replacer that puts its paths for the placeholders PLANSDIR
// (the plans directory), PLANFILE (the plan), LOGFILE (the plan's log) and
// WORKDIR, and the work directory itself.
func planLogPaths(t *testing.T) (*strings.Replacer, string) {
	t.Helper()
	h, plan := plansHome(t)
	plans := filepath.Dir(plan)
	work := filepath.Join(h, "work")
	os.MkdirAll(work, 0o755)
	return strings.NewReplacer("PLANSDIR", plans, "PLANFILE", plan,
		"LOGFILE", filepath.Join(plans, "2026-10-14-x.box.log.org"), "WORKDIR", work), work
}

// A plan's log is appended to by agent-plan alone: no file tool and no
// shell write may change it, while the plan files and agent-plan itself
// pass.
func TestPlanLog(t *testing.T) {
	patch := func(header, path string) map[string]string {
		return map[string]string{"command": "*** Begin Patch\n*** " + header + ": " + path + "\n@@\n+x\n*** End Patch\n"}
	}
	files := []struct {
		name  string
		deny  bool
		tool  string
		input any
	}{
		{"the log", true, "Edit", map[string]string{"file_path": "LOGFILE", "old_string": "a", "new_string": "b"}},
		{"the log", true, "Write", map[string]string{"file_path": "LOGFILE", "content": "* done S1\n"}},
		{"the log's lock", true, "Write", map[string]string{"file_path": "LOGFILE.lock", "content": "0 x\n"}},
		{"another host's log", true, "Write", map[string]string{"file_path": "PLANSDIR/2026-10-14-y.other.log.org", "content": "x\n"}},
		{"the log", true, "MultiEdit", map[string]any{"file_path": "LOGFILE", "edits": []map[string]string{{"old_string": "a", "new_string": "b"}}}},
		{"the log", true, "apply_patch", patch("Update File", "LOGFILE")},
		{"the log, deleted", true, "apply_patch", patch("Delete File", "LOGFILE")},
		{"the log", true, "edit_file", map[string]string{"path": "LOGFILE", "original_content": "a", "new_content": "b"}},
		{"the log", true, "write_file", map[string]string{"path": "LOGFILE", "content": "x"}},
		{"the log", true, "move_file", map[string]string{"source": "LOGFILE", "destination": "/tmp/x"}},
		{"the plan", false, "Edit", map[string]string{"file_path": "PLANFILE", "old_string": "a", "new_string": "b"}},
		{"a part", false, "Write", map[string]string{"file_path": "PLANSDIR/2026-10-14-x.box.P1.org", "content": "* Plan\n"}},
		{"a log's name outside the plans", false, "Write", map[string]string{"file_path": "WORKDIR/2026-10-14-x.box.log.org", "content": "mine\n"}},
		{"another log.org", false, "Write", map[string]string{"file_path": "WORKDIR/log.org", "content": "mine\n"}},
		{"the plan", false, "edit_file", map[string]string{"path": "PLANFILE", "original_content": "a", "new_content": "b"}},
	}
	for _, c := range files {
		t.Run(caseName(c.deny, c.tool+" of "+c.name), func(t *testing.T) {
			paths, work := planLogPaths(t)
			data, _ := json.Marshal(c.input)
			input := []byte(paths.Replace(string(data)))
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
		})
	}

	// Each case runs in WORKDIR unless it says PLANSDIR or nowhere ("").
	commands := []struct {
		deny         bool
		dir, command string
	}{
		{true, "WORKDIR", "echo '* done S1' >> LOGFILE"},
		{true, "WORKDIR", "printf x > ~/git/notes/agents/2026-10-14-x.box.log.org"},
		{true, "WORKDIR", "printf x >>$HOME/git/notes/agents/2026-10-14-x.box.log.org"},
		{true, "WORKDIR", "cd PLANSDIR && echo x >> 2026-10-14-x.box.log.org"},
		{true, "PLANSDIR", "echo x > 2026-10-14-x.box.log.org.lock"},
		{true, "WORKDIR", "tee -a LOGFILE < /tmp/e"},
		{true, "WORKDIR", "sed -i '' s/done/verified/ LOGFILE"},
		{true, "WORKDIR", "perl -pi -e s/a/b/ LOGFILE"},
		{true, "WORKDIR", "cp /tmp/forged LOGFILE"},
		{true, "WORKDIR", "mv LOGFILE /tmp/old"},
		{true, "WORKDIR", "rm LOGFILE"},
		{true, "WORKDIR", "truncate -s 0 LOGFILE"},
		{true, "WORKDIR", "dd if=/dev/null of=LOGFILE"},
		{true, "", "echo x >> 2026-10-14-x.box.log.org"},
		{false, "WORKDIR", "agent-plan log PLANFILE --item S1 --kind done --summary 'Add >> the loop'"},
		{false, "WORKDIR", "agent-plan new --title 'Add the loop' --slug add-loop --branch main..loop"},
		{false, "WORKDIR", "agent-plan status PLANFILE --format json > /tmp/status.json"},
		{false, "WORKDIR", "agent-plan goal PLANFILE > /tmp/goal.md"},
		{false, "WORKDIR", "cat LOGFILE"},
		{false, "WORKDIR", "cp LOGFILE /tmp/copy.org"},
		{false, "WORKDIR", "grep -c '^\\* ' LOGFILE"},
		{false, "WORKDIR", "echo x > log.org"},
		{false, "WORKDIR", "echo x >> /tmp/2026-10-14-x.box.log.org"},
		{true, "WORKDIR", "echo x >> PLANFILE"},
	}
	for _, c := range commands {
		t.Run(caseName(c.deny, c.command), func(t *testing.T) {
			paths, _ := planLogPaths(t)
			dir, command := paths.Replace(c.dir), paths.Replace(c.command)
			reason := judge(t, dir, command)
			if (reason != "") != c.deny {
				t.Errorf("%q in %q: deny=%v, reason %q", command, dir, !c.deny, reason)
			}
			if reason != "" && !saysInstead(reason) {
				t.Errorf("%q: reason %q does not say what to do instead", command, reason)
			}
		})
	}
}

// A reviewer reads plans; it neither creates them nor logs to them.
func TestReviewerPlans(t *testing.T) {
	for command, deny := range map[string]bool{
		"agent-plan log PLANFILE --item S1 --kind verified": true,
		"agent-plan new --title T --slug t":                 true,
		"agent-plan import-claude":                          true,
		"agent-plan status PLANFILE":                        false,
		"agent-plan goal PLANFILE --part P1":                false,
		"agent-plan find --branch x":                        false,
		"agent-plan tokens":                                 false,
		"agent-plan questions PLANFILE --all":               false,
	} {
		t.Run(caseName(deny, command), func(t *testing.T) {
			paths, _ := planLogPaths(t)
			command := paths.Replace(command)
			input, _ := json.Marshal(map[string]string{"command": command})
			reason := check("Bash", "", input, reviewer, "")
			if (reason != "") != deny {
				t.Errorf("reviewer %q: deny=%v, reason %q", command, !deny, reason)
			}
			if reason := check("Bash", "", input, owner, ""); reason != "" {
				t.Errorf("owner %q: denied %q", command, reason)
			}
		})
	}
}

// Syncing and resealing plans are the watcher's and the operator's, in every
// session and role; the deploy key the sync pushes with is a credential.
func TestPlansForTheOperator(t *testing.T) {
	for command, deny := range map[string]bool{
		"agent-plan sync":                      true,
		"agent-plan sync --pull":               true,
		"agent-plan reseal PLANFILE":           true,
		"cd /tmp && agent-plan sync":           true,
		"bash -c 'agent-plan reseal x'":        true,
		"cat ~/.ssh/id_notes_deploy":           true,
		"ssh -i ~/.ssh/id_notes_deploy github": true,
		"agent-plan status PLANFILE":           false,
		"grep -n 'agent-plan sync' SKILL.md":   false,
	} {
		t.Run(caseName(deny, command), func(t *testing.T) {
			paths, _ := planLogPaths(t)
			command := paths.Replace(command)
			input, _ := json.Marshal(map[string]string{"command": command})
			for _, who := range []role{baseline, coordinator, owner, reviewer} {
				reason := check("Bash", "", input, who, "")
				if (reason != "") != deny {
					t.Errorf("role %d %q: deny=%v, reason %q", who, command, !deny, reason)
				}
				if reason != "" && !saysInstead(reason) {
					t.Errorf("%q: reason %q does not say what to do instead", command, reason)
				}
			}
		})
	}
}

// The hook, as Claude Code calls it, lets agent-plan through and stops an
// Edit of a log.
func TestPlanLogHook(t *testing.T) {
	for _, c := range []struct{ name, event, want string }{
		{"agent-plan log", `{"tool_name":"Bash","cwd":"PLANSDIR","tool_input":{"command":"agent-plan log PLANFILE --item S1 --kind done"}}`, ""},
		{"Edit of a log", `{"tool_name":"Edit","cwd":"PLANSDIR","tool_input":{"file_path":"PLANSDIR/2026-10-14-x.box.log.org","old_string":"a","new_string":"b"}}`, `"permissionDecision":"deny"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			paths, _ := planLogPaths(t)
			event := paths.Replace(c.event)
			var out bytes.Buffer
			Main([]string{"--baseline"}, strings.NewReader(event), &out, io.Discard)
			if (c.want == "" && out.Len() != 0) || !strings.Contains(out.String(), c.want) {
				t.Errorf("%s: printed %s", event, out.String())
			}
		})
	}
}

// A finding red-check raised (RC-…) is rebutted only by whoever owns the
// branch: the session outside a loop, or a loop's pr-owner, never its
// coordinator or a reviewer. Other settlements pass as before.
func TestMechanicalRebuttal(t *testing.T) {
	const p = "agent-plan log /p/2026-10-14-x.box.org --item S1 --kind finding-settled "
	cases := []struct {
		command string
		denied  []role
	}{
		{p + "--review 1.RC-0123456789ab --outcome rebut --evidence 'a characterisation test'", []role{coordinator, reviewer}},
		{p + "--outcome=rebut --review=2.RC-0123456789ab --evidence x", []role{coordinator, reviewer}},
		{"cd /tmp && " + p + "--review 1.RC-0123456789ab --outcome rebut --evidence x", []role{coordinator, reviewer}},
		{p + "--review 1.RC-0123456789ab --outcome fix --evidence 'fixed in a fixup'", []role{reviewer}},
		{p + "--review 1.F3 --outcome rebut --evidence 'not a bug'", []role{reviewer}},
	}
	for _, c := range cases {
		for _, who := range []role{baseline, coordinator, owner, reviewer} {
			input, _ := json.Marshal(map[string]string{"command": c.command})
			reason := check("Bash", "", input, who, t.TempDir())
			if want := slices.Contains(c.denied, who); (reason != "") != want {
				t.Errorf("role %d, %q: deny=%v, reason %q", who, c.command, !want, reason)
			}
			if reason != "" && !saysInstead(reason) {
				t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
			}
		}
	}
}
