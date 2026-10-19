// Package goproxy is the environment a Go module download runs in when the
// modules are someone else's choice: the public proxy and checksum
// database alone, so that no private module path, version control tool or
// setting of the caller's is reached.
package goproxy

import (
	"os"
	"path/filepath"
)

var (
	// Proxy and SumDB are where modules and their checksums come from;
	// tests serve a directory and turn the database off.
	Proxy = "https://proxy.golang.org"
	SumDB = "sum.golang.org"
)

// DownloadEnv is the whole environment of a download: the public proxy
// and checksum database with no `direct` fallback, no private paths, no
// version control (GOVCS=*:off), git's configuration emptied, no
// workspace and no settings of the caller's. modcache is GOMODCACHE, tmp
// the home and temporary directory, and toolchain GOTOOLCHAIN.
func DownloadEnv(modcache, tmp, toolchain string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + tmp,
		"TMPDIR=" + tmp,
		"GOENV=off",
		"GOTOOLCHAIN=" + toolchain,
		"GOWORK=off",
		"GOFLAGS=-mod=mod",
		"GOPROXY=" + Proxy,
		"GOSUMDB=" + SumDB,
		"GOVCS=*:off",
		"GOPRIVATE=",
		"GONOPROXY=",
		"GONOSUMDB=",
		"GOINSECURE=",
		"GOMODCACHE=" + modcache,
		"GOPATH=" + filepath.Join(tmp, "gopath"),
		"GOCACHE=" + filepath.Join(tmp, "gocache"),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	}
}
