package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// agent-tools' state, the usage readings, quota choices and the marks of
// quota asks the status line and the ask read, is written by agent-plan
// alone: no file tool and no shell write may change it, while reading it
// and the programs themselves pass.
func TestAgentToolsState(t *testing.T) {
	// statePaths gives the test a home directory, with XDG_STATE_HOME set to
	// a directory of its own when custom, and returns a replacer for STATE
	// (agent-tools' state directory), OTHER (another program's state) and
	// WORKDIR, and the work directory.
	statePaths := func(t *testing.T, custom bool) (*strings.Replacer, string) {
		t.Helper()
		h := fakeHome(t)
		base := filepath.Join(h, ".local", "state")
		if custom {
			base = filepath.Join(h, "xdg-state")
			t.Setenv("XDG_STATE_HOME", base)
		} else {
			t.Setenv("XDG_STATE_HOME", "")
		}
		state := filepath.Join(base, "agent-tools")
		work := filepath.Join(h, "work")
		for _, d := range []string{filepath.Join(state, "usage", "asked"), work} {
			os.MkdirAll(d, 0o755)
		}
		return strings.NewReplacer("STATE", state, "OTHER", filepath.Join(base, "other"), "WORKDIR", work), work
	}
	patch := func(header, path string) map[string]string {
		return map[string]string{"command": "*** Begin Patch\n*** " + header + ": " + path + "\n@@\n+x\n*** End Patch\n"}
	}
	files := []struct {
		name  string
		deny  bool
		tool  string
		input any
	}{
		{"a usage reading", true, "Write", map[string]string{"file_path": "STATE/usage/claude.json", "content": "{}"}},
		{"a quota choice", true, "Edit", map[string]string{"file_path": "STATE/usage/choice-claude.json", "old_string": "a", "new_string": "b"}},
		{"an ask's mark", true, "Write", map[string]string{"file_path": "STATE/usage/asked/claude-5h-1-75", "content": ""}},
		{"an ask's mark", true, "apply_patch", patch("Add File", "STATE/usage/asked/claude-5h-1-90")},
		{"a usage reading", true, "write_file", map[string]string{"path": "STATE/usage/codex.json", "content": "{}"}},
		{"another program's state", false, "Write", map[string]string{"file_path": "OTHER/x", "content": "x"}},
		{"a file of the work", false, "Write", map[string]string{"file_path": "WORKDIR/usage.json", "content": "x"}},
	}
	for _, custom := range []bool{false, true} {
		for _, c := range files {
			t.Run(caseName(c.deny, c.tool+" of "+c.name), func(t *testing.T) {
				paths, work := statePaths(t, custom)
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
	}

	commands := []struct {
		deny    bool
		command string // run in WORKDIR
	}{
		{true, "echo '{}' > STATE/usage/claude.json"},
		{true, "printf x >> STATE/usage/choice-claude.json"},
		{true, "tee STATE/usage/asked/claude-5h-1-75 < /tmp/x"},
		{true, "cp /tmp/forged STATE/usage/choice-claude.json"},
		{true, "mv STATE/usage /tmp/old"},
		{true, "rm -r STATE/usage/asked"},
		{true, "truncate -s 0 STATE/usage/claude.json"},
		{true, "sed -i '' s/a/b/ STATE/usage/choice-claude.json"},
		{true, "cd STATE && echo x > usage/claude.json"},
		{false, "cat STATE/usage/claude.json"},
		{false, "ls STATE/usage/asked"},
		{false, "cp STATE/usage/claude.json /tmp/copy.json"},
		{false, "agent-plan usage --json > /tmp/usage.json"},
		{false, "echo x > OTHER/x"},
	}
	for _, custom := range []bool{false, true} {
		for _, c := range commands {
			t.Run(caseName(c.deny, c.command), func(t *testing.T) {
				paths, work := statePaths(t, custom)
				command := paths.Replace(c.command)
				reason := judge(t, work, command)
				if (reason != "") != c.deny {
					t.Errorf("%q: deny=%v, reason %q", command, !c.deny, reason)
				}
				if reason != "" && !saysInstead(reason) {
					t.Errorf("%q: reason %q does not say what to do instead", command, reason)
				}
			})
		}
	}
}

// The Agent plans dashboard's state, the review queue, the requests the
// page lists, the kill switch, and plan-dash's status and GitHub cache, is
// written by plan-dash alone: no file tool and no shell write may change
// it, while reading it and plan-dash itself pass.
func TestDashboardState(t *testing.T) {
	h := fakeHome(t)
	t.Setenv("XDG_STATE_HOME", "")
	baked := filepath.Join(h, "baked-state")
	hostpaths.Bake(t, filepath.Join(h, "baked-config"), baked)
	work := filepath.Join(h, "work")
	os.MkdirAll(work, 0o755)
	for _, base := range []string{filepath.Join(h, ".local", "state"), baked} {
		plansDir, dashDir := filepath.Join(base, "agent-plans"), filepath.Join(base, "plan-dash")
		os.MkdirAll(filepath.Join(plansDir, "out", "dispatch"), 0o755)
		os.MkdirAll(dashDir, 0o755)
		paths := strings.NewReplacer("PLANS", plansDir, "DASH", dashDir)
		for _, file := range []string{"PLANS/queue.org", "PLANS/requests.json", "PLANS/off", "PLANS/out/dispatch/o@r@7.json",
			"DASH/github.json", "DASH/status.org"} {
			input, _ := json.Marshal(map[string]string{"file_path": paths.Replace(file), "content": "x"})
			if reason := check("Write", "", input, baseline, work); reason == "" || !strings.Contains(reason, "plan-dash") ||
				!saysInstead(reason) {
				t.Errorf("a Write of %s: %q", paths.Replace(file), reason)
			}
		}
		for _, c := range []struct {
			deny    bool
			command string
		}{
			{true, "printf '* QUEUED o/r#7' > PLANS/queue.org"},
			{true, "echo '{}' >> PLANS/requests.json"},
			{true, "rm PLANS/off"},
			{true, "touch PLANS/off"},
			{true, "mv /tmp/x PLANS/requests.json"},
			{true, "cp /tmp/doc.json PLANS/out/dispatch/o@r@7.json"},
			{true, "rm -r PLANS"},
			{true, "cd DASH && echo x > github.json"},
			{false, "cat PLANS/queue.org"},
			{false, "ls PLANS/out/dispatch"},
			{false, "plan-dash dispatch"},
			{false, "cp PLANS/queue.org /tmp/queue.org"},
		} {
			command := paths.Replace(c.command)
			reason := judge(t, work, command)
			if (reason != "") != c.deny {
				t.Errorf("%q: deny=%v, reason %q", command, !c.deny, reason)
			}
		}
	}
}

// The review VM's results, which git fresh-review --check trusts, are
// agent-tools' state where the build bakes the state directory in as well
// as where the guard's environment puts it, and the refusal says what
// writes them.
func TestFreshReviewState(t *testing.T) {
	h := fakeHome(t)
	t.Setenv("XDG_STATE_HOME", "")
	baked := filepath.Join(h, "baked-state")
	hostpaths.Bake(t, filepath.Join(h, "baked-config"), baked)
	for _, base := range []string{filepath.Join(h, ".local", "state"), baked} {
		file := filepath.Join(base, "agent-tools", "fresh-review", "0123.json")
		input, _ := json.Marshal(map[string]string{"file_path": file, "content": "{}"})
		reason := check("Write", "", input, baseline, h)
		if reason == "" || !strings.Contains(reason, "git fresh-review") || !saysInstead(reason) {
			t.Errorf("a write of %s: %q", file, reason)
		}
	}
}
