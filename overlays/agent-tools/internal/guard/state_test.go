package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// agent-tools' state, the review receipts, usage readings and branch hints
// stop-check and the status line read, is written by agent-plan and pr-loop
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
		for _, d := range []string{filepath.Join(state, "usage"), filepath.Join(state, "commits"), work} {
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
		{"a receipt", true, "Edit", map[string]string{"file_path": "STATE/commits/s1", "old_string": "a", "new_string": "b"}},
		{"a branch hint", true, "Write", map[string]string{"file_path": "STATE/branch/s1", "content": "main\t/x"}},
		{"a receipt", true, "apply_patch", patch("Add File", "STATE/commits/s2")},
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
		{true, "printf x >> STATE/commits/s1"},
		{true, "tee STATE/branch/s1 < /tmp/x"},
		{true, "cp /tmp/forged STATE/commits/s1"},
		{true, "mv STATE/usage /tmp/old"},
		{true, "rm -r STATE/commits"},
		{true, "truncate -s 0 STATE/usage/claude.json"},
		{true, "sed -i '' s/a/b/ STATE/commits/s1"},
		{true, "cd STATE && echo x > usage/claude.json"},
		{false, "cat STATE/usage/claude.json"},
		{false, "ls STATE/commits"},
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
