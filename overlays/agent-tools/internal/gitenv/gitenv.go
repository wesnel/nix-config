// Package gitenv holds the environment variables that decide which
// repository git acts on, and the means to clear them.
package gitenv

import (
	"os"
	"slices"
	"strings"
)

// Location is the environment that would point git somewhere other than the
// directory it runs in. git sets it for commands it starts (rebase --exec,
// hooks, aliases), so a process started that way inherits the caller's
// repository.
var Location = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_PREFIX",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
}

// commandConfig is configuration that `git -c` hands down to the commands it
// starts.
var commandConfig = []string{"GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"}

// Without is env, as os.Environ returns it, minus Location: the environment
// for a git command aimed at a directory of its own, or a program started in
// one, which must not act on the repository the caller's environment names.
func Without(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(Location, name) {
			out = append(out, kv)
		}
	}
	return out
}

// Clear removes Location from this process's environment.
func Clear() {
	for _, name := range Location {
		os.Unsetenv(name)
	}
}

// Isolate is for a test binary's TestMain: it clears Location and the
// configuration `git -c` passes down, and keeps git
// from reading the user's or the system's configuration, which would sign
// commits with the hardware key. A test that wants its own global
// configuration sets GIT_CONFIG_GLOBAL itself.
func Isolate() {
	Clear()
	for _, name := range commandConfig {
		os.Unsetenv(name)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}
