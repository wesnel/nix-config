package guard

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/env"
)

// IsCredential reports whether the absolute, clean path is a secret as the
// guard judges a command's file arguments: a .env file by its name, or a
// path under one of the credential paths by spelling, case folded where
// the file system folds it, by glob, and by what the file system would
// open, with the credential paths of the config ctx's Env names. A command
// that sends a file elsewhere calls it to keep secrets out of what it sends.
func IsCredential(ctx context.Context, path string) bool {
	if envFile(path) {
		return true
	}
	for _, p := range credentialPaths(ctx) {
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
// writes files that are not the repository's, as the Env of ctx has them.
func ScratchRoots(ctx context.Context) []string { return scratchRoots(env.From(ctx)) }
