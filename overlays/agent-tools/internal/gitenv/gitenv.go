// Package gitenv holds the environment variables that decide which
// repository git acts on, and the means to clear them, and isolates a test
// binary from the user's configuration and the network.
package gitenv

import (
	"net"
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
//
// It also fails the network closed: HTTP and HTTPS requests from the
// binary and the programs it starts, git and gh included, go to a proxy
// of its own that drops every connection, so a request that escapes a
// test's local stand-ins fails instead of reaching the internet. Requests
// to localhost, such as an httptest server's, bypass it. Go reads the
// proxy variables once per process, so Isolate must run before the first
// request.
func Isolate() {
	Clear()
	for _, name := range commandConfig {
		os.Unsetenv(name)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	refuseNetwork()
}

// refuseNetwork points the proxy variables at a listener that closes every
// connection it accepts, for the rest of the process.
func refuseNetwork() {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic("gitenv: no listener for the refusing proxy: " + err.Error())
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	proxy := "http://" + ln.Addr().String()
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		os.Setenv(name, proxy)
	}
	for _, name := range []string{"NO_PROXY", "no_proxy"} {
		os.Setenv(name, "localhost,127.0.0.1,::1")
	}
}
