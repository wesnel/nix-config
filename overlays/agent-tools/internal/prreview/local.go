package prreview

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/repos"
)

// LocalTimeout bounds a local range's run in the VM.
const LocalTimeout = 30 * time.Minute

// LocalRedCheck is git fresh-review's review.RedCheckInVM: git red-check on
// base..tip of the repository at top, and the test command at tip, run in
// the VM exactly as a pull request's are, from a self-contained bare copy of
// the range that it removes afterwards.
func LocalRedCheck(ctx context.Context, top, base, tip string, log io.Writer) (string, error) {
	git.Dir = top
	settings, err := repos.Lookup()
	git.Dir = ""
	if err != nil {
		return "", err
	}
	root, err := Root()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(root, "local-")
	if err != nil {
		return "", err
	}
	defer removeAll(tmp)
	clone := filepath.Join(tmp, "clone")
	if _, err := gitRun(ctx, tmp, "-c", "protocol.file.allow=always", "clone", "--quiet", "--bare", "--shared", top, clone); err != nil {
		return "", err
	}
	for ref, rev := range map[string]string{"refs/pr-review/head": tip, "refs/pr-review/base": base} {
		if _, err := gitRun(ctx, clone, "update-ref", ref, rev); err != nil {
			return "", err
		}
	}
	// The VM sees only the copy, so it borrows no objects from top.
	if _, err := gitRun(ctx, clone, "repack", "-a", "-d", "-q"); err != nil {
		return "", err
	}
	if err := os.Remove(filepath.Join(clone, "objects", "info", "alternates")); err != nil {
		return "", err
	}
	pr := &PR{Repo: settings.Repo, Clone: clone, Head: tip, Base: base, Dir: tmp}
	ran, err := runTests(ctx, pr, LocalTimeout)
	if err != nil {
		return "", err
	}
	if ran.notRun != "" {
		return "", errors.New(ran.notRun)
	}
	return ran.prompt, nil
}

// removeAll removes dir, module caches included, which go makes read-only.
func removeAll(dir string) {
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			os.Chmod(p, info.Mode()|0o700) //nolint:gosec // dir is a fresh MkdirTemp that only this process's git and go write, and Walk never follows a link
		}
		return nil
	})
	os.RemoveAll(dir)
}
