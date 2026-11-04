package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gitstage"
)

// gitStageCodexEnv names the Codex executable, an absolute path, whose
// sandbox confines git_stage's Git child.
const gitStageCodexEnv = "PR_LOOP_CODEX"

func init() {
	mcpTools = append(mcpTools, mcpTool{
		name: "git_stage",
		desc: "Stage literal paths in an authorized task worktree.",
		params: []mcpParam{
			{name: "paths", kind: texts, required: true, desc: "literal worktree-relative paths to stage"},
			{name: "authorization", kind: literal, desc: "one-use hook authorization"},
		},
		// callTool sets worktree, resolved, after the grant and the
		// ownership recheck.
		argv: func(a mcpArgs) []string { return append([]string{a.str("worktree")}, a.strs("paths")...) },
		run: func(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
			return gitStage(ctx, argv[0], argv[1:], stdout, stderr)
		},
	})
}

// gitStageTimeout bounds the confined child.
const gitStageTimeout = 2 * time.Minute

// gitStage stages paths in a linked worktree: under the worktree's
// index.lock, a Git child confined by Codex's sandbox stages them in a
// private index and object directory, and gitstage installs the result,
// taking only those paths' entries and verified blobs from it.
func gitStage(ctx context.Context, worktree string, paths []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, err)
		return 1
	}
	codex := os.Getenv(gitStageCodexEnv)
	if !filepath.IsAbs(codex) {
		return fail(fmt.Errorf("git_stage is off on this machine (wgn.home.codex.gitStage), so %s names no Codex to confine its Git child; stage with git add, which asks the operator", gitStageCodexEnv))
	}
	self, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	layout, err := gitstage.Resolve(worktree)
	if err != nil {
		return fail(err)
	}
	lock, err := layout.Lock()
	if err != nil {
		return fail(err)
	}
	defer lock.Release()
	scratch, err := os.MkdirTemp("", "git-stage-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(scratch)
	private, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		return fail(err)
	}
	if err := lock.Snapshot(private); err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, gitStageTimeout)
	defer cancel()
	argv := append([]string{"sandbox", "--", self, "git-stage-child", layout.Worktree,
		filepath.Join(private, "index"), filepath.Join(private, "objects")}, paths...)
	//nolint:gosec // the launcher is an absolute path from the server's own configuration, which sandboxed commands cannot set; its arguments are fixed but the literal paths, which follow -- and the child checks.
	cmd := exec.CommandContext(ctx, codex, argv...)
	// The sandbox's workspace is the private directory, the only place the
	// child writes.
	cmd.Dir = private
	var childErr bytes.Buffer
	cmd.Stderr = &childErr
	if err := cmd.Run(); err != nil {
		return fail(fmt.Errorf("the confined Git child failed (%v): %s", err, strings.TrimSpace(childErr.String())))
	}
	if err := lock.Install(private, paths); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "staged %s\n", strings.Join(paths, " "))
	return 0
}
