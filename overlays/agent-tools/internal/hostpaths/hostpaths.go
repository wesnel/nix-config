// Package hostpaths names the XDG config and state directories agent-tools
// keeps its config and state in on the host. A build from the home-manager
// module bakes them in (overlays/agent-tools/default.nix), and then takes
// neither them nor a file's override from the environment, so that no
// command an agent runs can point a program at a config or state of the
// agent's own: what git fresh-review --check trusts depends on it. A test,
// and a build that bakes none in, read the environment.
package hostpaths

import (
	"os"
	"path/filepath"
	"testing"
)

// builtConfigHome and builtStateHome are set with -ldflags -X. A test
// ignores them: the Nix build's check phase links its tests with the same
// flags.
var builtConfigHome, builtStateHome string

// testConfigHome and testStateHome are what Bake sets.
var testConfigHome, testStateHome string

// Bake has the calling test's programs take the directories from configHome
// and stateHome, as a build that bakes them in does, until it ends.
func Bake(t testing.TB, configHome, stateHome string) {
	t.Helper()
	oldConfig, oldState := testConfigHome, testStateHome
	testConfigHome, testStateHome = configHome, stateHome
	t.Cleanup(func() { testConfigHome, testStateHome = oldConfig, oldState })
}

// baked is the directory baked in, of built for a program and of test for a
// test, or "".
func baked(built, test string) string {
	if testing.Testing() {
		return test
	}
	return built
}

// dir is the baked directory, or else $env where it is absolute, or else
// fallback under the home directory.
func dir(built, test, env, fallback string) string {
	if d := baked(built, test); d != "" {
		return d
	}
	if d := os.Getenv(env); filepath.IsAbs(d) {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}

// ConfigHome is the XDG config directory.
func ConfigHome() string {
	return dir(builtConfigHome, testConfigHome, "XDG_CONFIG_HOME", ".config")
}

// StateHome is the XDG state directory.
func StateHome() string {
	return dir(builtStateHome, testStateHome, "XDG_STATE_HOME", filepath.Join(".local", "state"))
}

// ConfigFile is rel under ConfigHome, or the file the environment variable
// override names where the config directory is not baked in.
func ConfigFile(override, rel string) string {
	if baked(builtConfigHome, testConfigHome) == "" {
		if p := os.Getenv(override); p != "" {
			return p
		}
	}
	return filepath.Join(ConfigHome(), rel)
}
