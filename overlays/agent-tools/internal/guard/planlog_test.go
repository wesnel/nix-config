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

// With a config agent-plan cannot read, the guard still protects the
// plans directory agent-plan defaults to.
func TestPlansDirs(t *testing.T) {
	h := fakeHome(t)
	config := filepath.Join(h, "config.json")
	os.WriteFile(config, []byte("not JSON"), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	if dirs := plansDirs(); len(dirs) == 0 || dirs[0] != filepath.Join(h, "git", "github.com", "wesnel", "notes", "agents", "plans") {
		t.Errorf("plans directories %v", dirs)
	}
}

// A plan's log is appended to by agent-plan alone: no file tool and no
// shell write may change it, while agent-plan itself passes.
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
		{"a log's name outside the plans", false, "Write", map[string]string{"file_path": "WORKDIR/2026-10-14-x.box.log.org", "content": "mine\n"}},
		{"another log.org", false, "Write", map[string]string{"file_path": "WORKDIR/log.org", "content": "mine\n"}},
	}
	for _, c := range files {
		t.Run(caseName(c.deny, c.tool+" of "+c.name), func(t *testing.T) {
			paths, work := planLogPaths(t)
			data, _ := json.Marshal(c.input)
			input := []byte(paths.Replace(string(data)))
			reason := check(c.tool, input, baseline, work)
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

// A plan file is changed by agent-plan alone, which checks its structure:
// no file tool and no shell write may change one in the plans directory,
// and the refusal names the commands to use. Files of the same names
// elsewhere, and reading plans, pass.
func TestPlanFile(t *testing.T) {
	patch := func(header, path string) map[string]string {
		return map[string]string{"command": "*** Begin Patch\n*** " + header + ": " + path + "\n@@\n+x\n*** End Patch\n"}
	}
	files := []struct {
		name  string
		deny  bool
		tool  string
		input any
	}{
		{"the plan", true, "Edit", map[string]string{"file_path": "PLANFILE", "old_string": "a", "new_string": "b"}},
		{"the plan", true, "Write", map[string]string{"file_path": "PLANFILE", "content": "* Plan\n"}},
		{"the plan", true, "MultiEdit", map[string]any{"file_path": "PLANFILE", "edits": []map[string]string{{"old_string": "a", "new_string": "b"}}}},
		{"a part", true, "Write", map[string]string{"file_path": "PLANSDIR/2026-10-14-x.box.P1.org", "content": "* Plan\n"}},
		{"a new plan", true, "Write", map[string]string{"file_path": "PLANSDIR/2026-10-15-y.box.org", "content": "* Plan\n"}},
		{"another host's plan", true, "Edit", map[string]string{"file_path": "PLANSDIR/2026-10-14-y.other.org", "old_string": "a", "new_string": "b"}},
		{"the plan", true, "apply_patch", patch("Update File", "PLANFILE")},
		{"a plan's name outside the plans", false, "Write", map[string]string{"file_path": "WORKDIR/2026-10-14-x.box.org", "content": "mine\n"}},
		{"another org file in the plans", false, "Write", map[string]string{"file_path": "PLANSDIR/notes.org", "content": "mine\n"}},
	}
	for _, c := range files {
		t.Run(caseName(c.deny, c.tool+" of "+c.name), func(t *testing.T) {
			paths, work := planLogPaths(t)
			data, _ := json.Marshal(c.input)
			input := []byte(paths.Replace(string(data)))
			reason := check(c.tool, input, baseline, work)
			if (reason != "") != c.deny {
				t.Errorf("%s %s: deny=%v, reason %q", c.tool, input, !c.deny, reason)
			}
			if reason != "" && (!saysInstead(reason) || !strings.Contains(reason, "`agent-plan step") || !strings.Contains(reason, "`agent-plan new")) {
				t.Errorf("%s: reason %q does not name the commands to use", c.tool, reason)
			}
		})
	}

	commands := []struct {
		deny         bool
		dir, command string
	}{
		{true, "WORKDIR", "cp /tmp/forged PLANFILE"},
		{true, "WORKDIR", "mv PLANFILE /tmp/old"},
		{true, "WORKDIR", "rm PLANFILE"},
		{true, "WORKDIR", "trash PLANSDIR/2026-10-14-x.box.P1.org"},
		{true, "PLANSDIR", "tee 2026-10-14-x.box.org < /tmp/e"},
		{true, "WORKDIR", "printf x > ~/git/notes/agents/2026-10-14-x.box.org"},
		{false, "WORKDIR", "agent-plan step PLANFILE --title 'Tag it' --text 'Done when > 1 tag'"},
		{false, "WORKDIR", "agent-plan section PLANFILE Goal --text 'Add the loop.'"},
		{false, "WORKDIR", "cat PLANFILE"},
		{false, "WORKDIR", "cp PLANFILE /tmp/copy.org"},
		{false, "WORKDIR", "echo x > /tmp/2026-10-14-x.box.org"},
		// Copied or moved into the plans directory under a plan's name, or
		// as a directory's contents, which may hold any.
		{true, "WORKDIR", "cp /tmp/2026-10-14-x.box.org PLANSDIR"},
		{true, "WORKDIR", "cp /tmp/2026-10-14-x.box.org PLANSDIR/"},
		{true, "WORKDIR", "mv /tmp/2026-10-14-x.box.P1.org PLANSDIR"},
		{true, "WORKDIR", "install -m 644 /tmp/2026-10-14-x.box.org PLANSDIR"},
		{true, "WORKDIR", "cp -t PLANSDIR /tmp/2026-10-14-x.box.org"},
		{true, "WORKDIR", "cp /tmp/2026-10-14-x.box.log.org PLANSDIR"},
		{true, "WORKDIR", "rsync -a /tmp/backup/ PLANSDIR"},
		{true, "WORKDIR", "cp -r /tmp/backup/. PLANSDIR"},
		{true, "PLANSDIR", "cp /tmp/2026-10-14-x.box.org ."},
		// A glob the shell expands, which may name plans and logs.
		{true, "WORKDIR", "cp /tmp/backup/*.org PLANSDIR/"},
		{true, "WORKDIR", "cp /tmp/backup/2026-1*.org PLANSDIR"},
		{true, "WORKDIR", "cp /tmp/backup/*.log.org PLANSDIR/"},
		{true, "WORKDIR", "mv /tmp/backup/x/* PLANSDIR/"},
		{true, "WORKDIR", "cp /tmp/backup/[0-9]? PLANSDIR"},
		{true, "PLANSDIR", "cp /tmp/backup/*.org ."},
		{false, "WORKDIR", "cp /tmp/backup/*.org /tmp/elsewhere/"},
		// Braces, a variable or a command's output, which may name either.
		{true, "WORKDIR", "cp /tmp/backup/2026-10-14-{x,y}.box.org PLANSDIR/"},
		{true, "WORKDIR", "cp /tmp/backup/2026-10-14-x.box.{org,bak} PLANSDIR/"},
		{true, "WORKDIR", "cp /tmp/backup/{a,b} PLANSDIR/"},
		{true, "WORKDIR", "cp $F PLANSDIR/"},
		{true, "WORKDIR", `cp "$F" PLANSDIR/`},
		{true, "WORKDIR", "mv ${F} PLANSDIR/"},
		{true, "WORKDIR", "cp $(ls /tmp/backup/*) PLANSDIR/"},
		{true, "WORKDIR", "cp `echo /tmp/backup/2026-10-14-x.box.org` PLANSDIR/"},
		{false, "WORKDIR", "cp $F /tmp/elsewhere/"},
		// A tar or unzip extracting into the plans directory, or a ditto
		// copying into it.
		{true, "WORKDIR", "tar -xf /tmp/b.tar -C PLANSDIR"},
		{true, "WORKDIR", "tar xzf /tmp/b.tgz --directory PLANSDIR"},
		{true, "WORKDIR", "tar -x -f /tmp/b.tar --directory=PLANSDIR"},
		{true, "PLANSDIR", "tar -xf /tmp/b.tar"},
		{true, "WORKDIR", "bsdtar -xf /tmp/b.tar -C PLANSDIR"},
		{false, "WORKDIR", "tar -xf /tmp/b.tar -C /tmp/elsewhere"},
		{false, "PLANSDIR", "tar -cf /tmp/b.tar ."},
		{false, "WORKDIR", "tar -tf /tmp/b.tar -C PLANSDIR"},
		{true, "WORKDIR", "unzip /tmp/b.zip -d PLANSDIR"},
		{true, "WORKDIR", "unzip -o /tmp/b.zip -dPLANSDIR"},
		{true, "PLANSDIR", "unzip /tmp/b.zip"},
		{false, "WORKDIR", "unzip /tmp/b.zip -d /tmp/elsewhere"},
		{false, "PLANSDIR", "unzip -l /tmp/b.zip"},
		{false, "PLANSDIR", "unzip -p /tmp/b.zip x.txt"},
		{true, "WORKDIR", "ditto /tmp/backup PLANSDIR"},
		{true, "WORKDIR", "ditto -x -k /tmp/b.zip PLANSDIR"},
		{true, "PLANSDIR", "ditto /tmp/backup ."},
		{false, "WORKDIR", "ditto PLANSDIR /tmp/backup"},
		{false, "WORKDIR", "ditto -c -k PLANSDIR /tmp/b.zip"},
		{false, "WORKDIR", "cp /tmp/notes.txt PLANSDIR"},
		{false, "WORKDIR", "cp PLANFILE /tmp"},
		// git writing plan files back from history.
		{true, "WORKDIR", "git checkout HEAD~1 -- PLANFILE"},
		{true, "WORKDIR", "git -C PLANSDIR restore 2026-10-14-x.box.org"},
		{true, "WORKDIR", "git -C PLANSDIR checkout HEAD~1 -- ."},
		{true, "PLANSDIR", "git restore --source HEAD~1 ."},
		{true, "WORKDIR", "git -C PLANSDIR/.. checkout HEAD -- agents"},
		{true, "PLANSDIR", "git checkout HEAD -- '*.org'"},
		{false, "WORKDIR", "git restore README.md"},
		{false, "WORKDIR", "git checkout -b loop"},
		{false, "WORKDIR", "git -C PLANSDIR log -- 2026-10-14-x.box.org"},
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
		"agent-plan status PLANFILE":                        false,
		"agent-plan goal PLANFILE --part P1":                false,
		"agent-plan find --branch x":                        false,
		"agent-plan questions PLANFILE --all":               false,
	} {
		t.Run(caseName(deny, command), func(t *testing.T) {
			paths, _ := planLogPaths(t)
			command := paths.Replace(command)
			input, _ := json.Marshal(map[string]string{"command": command})
			reason := check("Bash", input, reviewer, "")
			if (reason != "") != deny {
				t.Errorf("reviewer %q: deny=%v, reason %q", command, !deny, reason)
			}
			if reason := check("Bash", input, baseline, ""); reason != "" {
				t.Errorf("baseline %q: denied %q", command, reason)
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
			for _, who := range []role{baseline, reviewer} {
				reason := check("Bash", input, who, "")
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
