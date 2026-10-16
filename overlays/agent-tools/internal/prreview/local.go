package prreview

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/repos"
	"github.com/wesnel/agent-tools/internal/review"
)

// LocalTimeout bounds a local range's run in the VM.
const LocalTimeout = 30 * time.Minute

// VMTools is git fresh-review's review.VMTools: the sandbox backend and the
// guest tools, git-red-check among them, as the config names them with
// their symlinks resolved, which are store paths that change with them; an
// error where the config cannot be read.
func VMTools() (string, error) {
	config, err := loadRunConfig()
	if err != nil {
		return "", err
	}
	var named []string
	for _, p := range []string{config.Backend, config.GuestTools} {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			p = real
		}
		named = append(named, p)
	}
	return strings.Join(named, "\x00"), nil
}

// LocalRedCheck is git fresh-review's review.RedCheckInVM: git red-check on
// base..tip of the repository at top, and the test command at tip, run in
// the VM exactly as a pull request's are, from a self-contained bare copy of
// the range that it removes afterwards.
func LocalRedCheck(ctx context.Context, top, base, tip string, log io.Writer) (*review.VMResult, error) {
	git.Dir = top
	settings, err := repos.Lookup()
	git.Dir = ""
	if err != nil {
		return nil, err
	}
	root, err := Root()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(root, "local-")
	if err != nil {
		return nil, err
	}
	defer removeAll(tmp)
	clone := filepath.Join(tmp, "clone")
	if _, err := gitRun(ctx, tmp, "-c", "protocol.file.allow=always", "clone", "--quiet", "--bare", "--shared", top, clone); err != nil {
		return nil, err
	}
	for ref, rev := range map[string]string{"refs/pr-review/head": tip, "refs/pr-review/base": base} {
		if _, err := gitRun(ctx, clone, "update-ref", ref, rev); err != nil {
			return nil, err
		}
	}
	// The VM sees only the copy, so it borrows no objects from top.
	if _, err := gitRun(ctx, clone, "repack", "-a", "-d", "-q"); err != nil {
		return nil, err
	}
	if err := os.Remove(filepath.Join(clone, "objects", "info", "alternates")); err != nil {
		return nil, err
	}
	pr := &PR{Repo: settings.Repo, Clone: clone, Head: tip, Base: base, Dir: tmp}
	ran, err := runTests(ctx, pr, LocalTimeout)
	if err != nil {
		return nil, err
	}
	if ran.notRun != "" {
		return nil, errors.New(ran.notRun)
	}
	return &review.VMResult{Text: ran.prompt, Verdicts: ran.verdicts}, nil
}

// removeAll removes dir, module caches included, which go makes read-only.
func removeAll(dir string) {
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			os.Chmod(p, info.Mode()|0o700)
		}
		return nil
	})
	os.RemoveAll(dir)
}
