package routes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
)

// The package runs the git its caller's Env names, not the process's: a
// fake git first on the Env's PATH answers DefaultRef and MergeBase, where
// the real git, run in a repository with no refs, answers "" for both.
func TestGitFromEnv(t *testing.T) {
	t.Parallel()
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	bin := t.TempDir()
	// A fake: it stands for a git other than the process's, which only the
	// Env's PATH can select; it reports every ref as present and the merge
	// base as "fakebase".
	fake := "#!/bin/sh\ncase \"$*\" in *merge-base*) echo fakebase;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCtx := env.With(t.Context(), env.Process().In(repo).Set("PATH="+bin+string(os.PathListSeparator)+env.Process().Get("PATH")))

	if got := DefaultRef(fakeCtx, repo); got != "refs/remotes/origin/HEAD" {
		t.Errorf("DefaultRef with a fake git on the Env's PATH = %q, want refs/remotes/origin/HEAD", got)
	}
	if got := MergeBase(fakeCtx, repo, "HEAD"); got != "fakebase" {
		t.Errorf("MergeBase with a fake git on the Env's PATH = %q, want fakebase", got)
	}
	if got := DefaultRef(t.Context(), repo); got != "" {
		t.Errorf("DefaultRef with the real git in a repository with no refs = %q, want empty", got)
	}
}
