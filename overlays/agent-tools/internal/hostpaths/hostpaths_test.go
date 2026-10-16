package hostpaths

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/env/config")
	t.Setenv("XDG_STATE_HOME", "/env/state")
	t.Setenv("AGENT_TOOLS_CONFIG", "/env/agent-tools.json")
	t.Run("from the environment", func(t *testing.T) {
		if got := []string{ConfigHome(), StateHome(), ConfigFile("AGENT_TOOLS_CONFIG", "agent-tools/config.json")}; strings.Join(got, " ") !=
			"/env/config /env/state /env/agent-tools.json" {
			t.Errorf("%q", got)
		}
	})
	t.Run("baked in", func(t *testing.T) {
		Bake(t, "/baked/config", "/baked/state")
		if got := []string{ConfigHome(), StateHome(), ConfigFile("AGENT_TOOLS_CONFIG", "agent-tools/config.json")}; strings.Join(got, " ") !=
			"/baked/config /baked/state /baked/config/agent-tools/config.json" {
			t.Errorf("%q", got)
		}
	})
}

// TestBuilt: a program built with the directories baked in, as
// overlays/agent-tools/default.nix builds it, takes nothing from the
// environment, and one built without them does.
func TestBuilt(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go here")
	}
	bin := t.TempDir()
	build := func(name string, ldflags ...string) string {
		t.Helper()
		out := filepath.Join(bin, name)
		cmd := exec.Command("go", "build", "-o", out, "-ldflags="+strings.Join(ldflags, " "), "./testdata/paths")
		if msg, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, msg)
		}
		return out
	}
	run := func(program string) string {
		t.Helper()
		cmd := exec.Command(program)
		cmd.Env = []string{"HOME=/env/home", "XDG_CONFIG_HOME=/env/config", "XDG_STATE_HOME=/env/state",
			"AGENT_TOOLS_CONFIG=/env/agent-tools.json"}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	baked := build("baked", "-X", "github.com/wesnel/agent-tools/internal/hostpaths.builtConfigHome=/baked/config",
		"-X", "github.com/wesnel/agent-tools/internal/hostpaths.builtStateHome=/baked/state")
	if got, want := run(baked), "/baked/config\n/baked/state\n/baked/config/agent-tools/config.json"; got != want {
		t.Errorf("baked in:\n%s\nnot\n%s", got, want)
	}
	if got, want := run(build("plain")), "/env/config\n/env/state\n/env/agent-tools.json"; got != want {
		t.Errorf("without:\n%s\nnot\n%s", got, want)
	}
}
