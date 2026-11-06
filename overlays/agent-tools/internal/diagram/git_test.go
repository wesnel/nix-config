package diagram

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
)

// TestGitRepo holds a blob's read to the Env of the caller's context: the
// git that runs is the one on that Env's PATH.
func TestGitRepo(t *testing.T) {
	t.Parallel()
	// Stands in for git, so that its answer shows which git ran: it answers
	// as git does of a four-byte file, in a directory that is no repository.
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n*'cat-file -s'*) echo 4 ;;\n*'cat-file blob'*) echo env ;;\n*) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := env.With(t.Context(), env.New(t.TempDir(), "PATH="+bin))
	if got, err := GitRepo(t.TempDir()).Blob(ctx, "HEAD", "f"); err != nil || string(got) != "env\n" {
		t.Errorf("Blob = %q, %v; want the Env's git's \"env\\n\"", got, err)
	}
}
