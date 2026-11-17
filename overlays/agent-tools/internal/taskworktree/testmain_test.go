package taskworktree

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// TestMain gives every git the tests run a global config whose lfs filter
// stands in for git-lfs, which the operator's global config defines: its
// smudge writes, in place of a file's content, the GIT_LFS_SKIP_SMUDGE it
// was given, which git-lfs reads to leave pointers alone. Global config is
// the one scope CheckConfig lets define a filter; it is set here, once,
// before any test runs.
func TestMain(m *testing.M) {
	gitenv.Isolate()
	dir, err := os.MkdirTemp("", "taskworktree")
	if err != nil {
		panic(err)
	}
	smudge := filepath.Join(dir, "smudge")
	config := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(smudge, []byte("#!/bin/sh\ncat > /dev/null\nprintf 'skip=%s\\n' \"$GIT_LFS_SKIP_SMUDGE\"\n"), 0o700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(config, []byte(fmt.Sprintf("[filter \"lfs\"]\n\tclean = cat\n\tsmudge = %s\n", smudge)), 0o600); err != nil {
		panic(err)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", config)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
