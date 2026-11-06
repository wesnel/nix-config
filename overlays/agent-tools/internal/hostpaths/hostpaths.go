// Package hostpaths names the XDG config and state directories agent-tools
// keeps its config and state in on the host. A build from the home-manager
// module bakes them in (overlays/agent-tools/default.nix), and then takes
// neither them nor a file's override from the environment, so that no
// command an agent runs can point a program at a config or state of the
// agent's own: the guard's settings depend on it. A test bakes them in
// through its Env (TestBaked); a build that bakes none in reads the Env.
package hostpaths

import (
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
)

// builtConfigHome and builtStateHome are set with -ldflags -X. A test
// ignores them: the Nix build's check phase links its tests with the same
// flags.
var builtConfigHome, builtStateHome string

// The variables of an Env that bake the directories in, in a test binary
// alone, so no installed program reads them. A test binary that runs a
// program's main in a child process hands its directories on through them.
const (
	TestConfigHomeEnv = "AGENT_TOOLS_TEST_CONFIG_HOME"
	TestStateHomeEnv  = "AGENT_TOOLS_TEST_STATE_HOME"
)

// TestBaked is what a test sets on an Env to bake configHome and stateHome
// in: env.New(dir, TestBaked(c, s)...) or e.Set(TestBaked(c, s)...).
func TestBaked(configHome, stateHome string) []string {
	return []string{TestConfigHomeEnv + "=" + configHome, TestStateHomeEnv + "=" + stateHome}
}

// baked is the directory baked in, or "": built in a program, and e's
// $test in a test binary.
func baked(e env.Env, built, test string) string {
	if testing.Testing() {
		return e.Get(test)
	}
	return built
}

// dir is the baked directory, or else e's $name where it is absolute, or
// else fallback under e's $HOME, as os.UserHomeDir reads it.
func dir(e env.Env, built, test, name, fallback string) string {
	if d := baked(e, built, test); d != "" {
		return d
	}
	if d := e.Get(name); filepath.IsAbs(d) {
		return d
	}
	return filepath.Join(e.Get("HOME"), fallback)
}

// ConfigHome is the XDG config directory of e.
func ConfigHome(e env.Env) string {
	return dir(e, builtConfigHome, TestConfigHomeEnv, "XDG_CONFIG_HOME", ".config")
}

// BakedConfigHome is the installed config authority, without any environment
// fallback. Empty means this build has no protected config authority.
func BakedConfigHome(e env.Env) string {
	return baked(e, builtConfigHome, TestConfigHomeEnv)
}

// BakedStateHome is the installed state authority, without environment fallback.
func BakedStateHome(e env.Env) string {
	return baked(e, builtStateHome, TestStateHomeEnv)
}

// StateHome is the XDG state directory of e.
func StateHome(e env.Env) string {
	return dir(e, builtStateHome, TestStateHomeEnv, "XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// ConfigFile is rel under ConfigHome, or, where the config directory is
// not baked in, the file e's variable override names when it is absolute:
// an empty or relative one is unset, as for ConfigHome. The plan views in
// emacs-config read the same file by the same rule.
func ConfigFile(e env.Env, override, rel string) string {
	if BakedConfigHome(e) == "" {
		if p := e.Get(override); filepath.IsAbs(p) {
			return p
		}
	}
	return filepath.Join(ConfigHome(e), rel)
}
