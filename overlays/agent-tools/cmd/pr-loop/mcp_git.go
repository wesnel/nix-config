package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gitstage"
	"github.com/wesnel/agent-tools/internal/review"
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

// gitStageProfile names the Codex permissions profile gitStageSandbox
// defines.
const gitStageProfile = "git-stage"

// gitStageSandbox is the arguments to codex, through --, that run a command
// in the sandbox of git_stage's child, with private as its workspace and
// working directory. The command may write only private. It may read what
// macOS needs (":minimal"), the Nix store, codex, self, the worktree, its
// Git directory, and the parts of the common directory Git reads to stage,
// never the shared objects; it may not reach the network. The temporary
// directories, which :minimal would let it read and write, are denied; a
// grant inside one, private's included, holds as the more specific rule.
// codex and self must be real paths: Codex executes itself in its sandbox
// by the path it was started by.
func gitStageSandbox(layout gitstage.Layout, private, codex, self string) ([]string, error) {
	var fs strings.Builder
	fs.WriteString(`{":minimal"="read", ":workspace_roots"={"."="write"}`)
	// A TOML table holds each key once, or Codex cannot load the profile.
	named := map[string]string{}
	add := func(path, access string) error {
		switch named[path] {
		case "":
			named[path] = access
		case access:
			return nil
		default:
			return fmt.Errorf("git_stage's sandbox would name %s both %s and %s", path, named[path], access)
		}
		quoted, err := review.TOMLString(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&fs, `, %s="%s"`, quoted, access)
		return nil
	}
	reads := []string{"/nix/store", codex, self, layout.Worktree, layout.GitDir}
	for _, p := range []string{"HEAD", "config", "refs", "packed-refs", "info"} {
		reads = append(reads, filepath.Join(layout.CommonDir, p))
	}
	for _, p := range reads {
		if err := add(p, "read"); err != nil {
			return nil, err
		}
	}
	// The shared objects are denied, not only left out, so that no read
	// above, wherever the repository sits, can reach them.
	for _, p := range []string{"/tmp", "/private/tmp", "/private/var/folders", filepath.Clean(os.TempDir()),
		filepath.Join(layout.CommonDir, "objects")} {
		if err := add(p, "deny"); err != nil {
			return nil, err
		}
	}
	fs.WriteString("}")
	return []string{"sandbox",
		"-c", `default_permissions="` + gitStageProfile + `"`,
		"-c", "permissions." + gitStageProfile + ".filesystem=" + fs.String(),
		"-P", gitStageProfile, "-C", private, "--"}, nil
}

// gitStageTimeout bounds the confined child.
const gitStageTimeout = 2 * time.Minute

// gitStage stages paths in a linked worktree: under the worktree's
// index.lock, a Git child confined by gitStageSandbox stages them in a
// private index and object directory, and gitstage installs the result,
// taking only those paths' entries and verified blobs from it. The child
// runs only after gitStageProbe, in the same sandbox, shows the sandbox
// holds.
func gitStage(ctx context.Context, worktree string, paths []string, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, err)
		return 1
	}
	codex := os.Getenv(gitStageCodexEnv)
	if !filepath.IsAbs(codex) {
		return fail(fmt.Errorf("git_stage is off on this machine (wgn.home.codex.gitStage), so %s names no Codex to confine its Git child; stage with git add, which asks the operator", gitStageCodexEnv))
	}
	codex, err := filepath.EvalSymlinks(codex)
	if err != nil {
		return fail(err)
	}
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
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
	sandbox, err := gitStageSandbox(layout, private, codex, self)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := context.WithTimeout(ctx, gitStageTimeout)
	defer cancel()
	env := append(os.Environ(), "PATH="+review.RealDirs(os.Getenv("PATH")))
	// The child runs only where the sandbox holds: the probe, in the same
	// sandbox, must write its workspace and be refused the repository.
	probeArgs := slices.Concat(sandbox, []string{self, "git-stage-probe", private, layout.Worktree, layout.GitDir,
		layout.CommonDir, filepath.Join(layout.CommonDir, "objects"), filepath.Join(layout.CommonDir, "refs")})
	//nolint:gosec // the launcher is an absolute path from the server's own configuration, which sandboxed commands cannot set; its arguments are fixed.
	probe := exec.CommandContext(ctx, codex, probeArgs...)
	probe.Dir, probe.Env = private, env
	var probeErr bytes.Buffer
	probe.Stderr = &probeErr
	if out, err := probe.Output(); err != nil || string(out) != "confined\n" {
		return fail(fmt.Errorf("Codex's sandbox does not hold for git_stage's Git child, so it does not run (%v): %s",
			err, strings.TrimSpace(probeErr.String())))
	}
	argv := slices.Concat(sandbox, []string{self, "git-stage-child", layout.Worktree,
		filepath.Join(private, "index"), filepath.Join(private, "objects")}, paths)
	//nolint:gosec // the launcher is an absolute path from the server's own configuration, which sandboxed commands cannot set; its arguments are fixed but the literal paths, which follow -- and the child checks.
	cmd := exec.CommandContext(ctx, codex, argv...)
	cmd.Dir, cmd.Env = private, env
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
