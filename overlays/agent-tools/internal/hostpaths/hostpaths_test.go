package hostpaths

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
)

func TestPaths(t *testing.T) {
	t.Parallel()
	e := env.New("", "HOME=/env/home", "XDG_CONFIG_HOME=/env/config", "XDG_STATE_HOME=/env/state",
		"AGENT_TOOLS_CONFIG=/env/agent-tools.json")
	t.Run("from the environment", func(t *testing.T) {
		t.Parallel()
		if got := []string{ConfigHome(e), StateHome(e), ConfigFile(e, "AGENT_TOOLS_CONFIG", "agent-tools/config.json")}; strings.Join(got, " ") !=
			"/env/config /env/state /env/agent-tools.json" {
			t.Errorf("%q", got)
		}
	})
	// An empty or relative override names no file, as an empty or
	// relative XDG_CONFIG_HOME names no directory; the plan views in
	// emacs-config follow the same rule.
	for what, value := range map[string]string{"an empty override": "", "a relative override": "agent-tools.json"} {
		t.Run(what, func(t *testing.T) {
			t.Parallel()
			e := e.Set("AGENT_TOOLS_CONFIG=" + value)
			if got := ConfigFile(e, "AGENT_TOOLS_CONFIG", "agent-tools/config.json"); got != "/env/config/agent-tools/config.json" {
				t.Errorf("%q", got)
			}
		})
	}
	// Without XDG directories, they are under HOME, as the Env has it.
	t.Run("under the home directory", func(t *testing.T) {
		t.Parallel()
		e := e.Without("XDG_CONFIG_HOME", "XDG_STATE_HOME")
		if got := []string{ConfigHome(e), StateHome(e)}; strings.Join(got, " ") != "/env/home/.config /env/home/.local/state" {
			t.Errorf("%q", got)
		}
	})
	t.Run("baked in", func(t *testing.T) {
		t.Parallel()
		e := e.Set(TestBaked("/baked/config", "/baked/state")...)
		if got := []string{ConfigHome(e), StateHome(e), ConfigFile(e, "AGENT_TOOLS_CONFIG", "agent-tools/config.json")}; strings.Join(got, " ") !=
			"/baked/config /baked/state /baked/config/agent-tools/config.json" {
			t.Errorf("%q", got)
		}
		if got := []string{BakedConfigHome(e), BakedStateHome(e)}; strings.Join(got, " ") != "/baked/config /baked/state" {
			t.Errorf("baked homes %q", got)
		}
	})
}

// TestBuilt: a program built with the directories baked in, as
// overlays/agent-tools/default.nix builds it, takes nothing from the
// environment, and one built without them does. Neither is a test binary,
// so neither takes the directories a test bakes in (TestBaked).
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
		cmd.Env = append([]string{"HOME=/env/home", "XDG_CONFIG_HOME=/env/config", "XDG_STATE_HOME=/env/state",
			"AGENT_TOOLS_CONFIG=/env/agent-tools.json"}, TestBaked("/test/config", "/test/state")...)
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
