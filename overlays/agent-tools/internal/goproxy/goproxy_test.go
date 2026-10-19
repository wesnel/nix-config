package goproxy

import (
	"strings"
	"testing"
)

// TestDownloadEnv holds the download to the public proxy and checksum
// database, with no setting of the caller's, no git, no workspace and no
// other toolchain.
func TestDownloadEnv(t *testing.T) {
	for _, kv := range []string{"GOPRIVATE=example.com", "GONOPROXY=*", "GONOSUMDB=*", "GOINSECURE=*",
		"GOFLAGS=-insecure", "GOPROXY=direct", "GOSUMDB=off", "GOTOOLCHAIN=go1.99.0+auto", "GOWORK=/x/go.work",
		"GIT_CONFIG_GLOBAL=/x", "GOENV=/x/env", "GOVCS=*:all"} {
		name, value, _ := strings.Cut(kv, "=")
		t.Setenv(name, value)
	}
	env := DownloadEnv("/cache/gomod", "/tmp/x", "local")
	want := map[string]string{"GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off", "GOFLAGS": "-mod=mod",
		"GOPROXY": "https://proxy.golang.org", "GOSUMDB": "sum.golang.org", "GOVCS": "*:off", "GOPRIVATE": "",
		"GONOPROXY": "", "GONOSUMDB": "", "GOINSECURE": "", "GOMODCACHE": "/cache/gomod",
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0"}
	seen := map[string]int{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		seen[name]++
		if w, ok := want[name]; ok && w != value {
			t.Errorf("%s=%q, want %q", name, value, w)
		}
		if strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "GIT") || strings.HasPrefix(name, "CGO") {
			if _, ok := want[name]; !ok && name != "GOPATH" && name != "GOCACHE" {
				t.Errorf("%s reaches the download", kv)
			}
		}
	}
	for name := range want {
		if seen[name] != 1 {
			t.Errorf("%s is set %d times", name, seen[name])
		}
	}
}
