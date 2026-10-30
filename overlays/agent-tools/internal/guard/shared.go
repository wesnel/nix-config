package guard

import (
	"path/filepath"
	"strings"
)

// IsCredential reports whether the absolute, clean path is a secret as the
// guard judges a command's file arguments: a .env file by its name, or a
// path under one of the credential paths by spelling, case folded where
// the file system folds it, by glob, and by what the file system would
// open. A command that sends a file elsewhere calls it to keep secrets out
// of what it sends.
func IsCredential(path string) bool {
	if envFile(path) {
		return true
	}
	for _, p := range credentialPaths() {
		if credentialUnder(path, p, false) {
			return true
		}
	}
	return false
}

// envFile reports whether path names a .env file, .env or .env.NAME, which
// the guard counts as a secret wherever it is.
func envFile(path string) bool {
	b := fold(filepath.Base(path))
	return b == ".env" || strings.HasPrefix(b, ".env.")
}

// ScratchRoots are the session's temporary directories, where an agent
// writes files that are not the repository's.
func ScratchRoots() []string { return scratchRoots() }
