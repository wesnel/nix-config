package signpass

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // Git object identity
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/operatorkey"
)

// Nix binds both transport dependencies; ambient PATH and CA overrides
// cannot choose the program or the trust roots for upstream authorization.
var upstreamGit, upstreamCA string

// upstreamTransport is how a pass observes GitHub's default history: the
// git program and CA bundle, and the observation, which runs the command.
type upstreamTransport struct {
	git, ca   string
	advertise func(cmd *exec.Cmd) ([]byte, error)
}

// transportKey holds a *upstreamTransport in a context. Only hermetic tests
// set it, to replace the external GitHub observation; nothing outside this
// package can, so no environment or caller chooses the program or the
// trust roots.
type transportKey struct{}

// transport is the upstreamTransport of ctx, or the installed one.
func transport(ctx context.Context) *upstreamTransport {
	if t, ok := ctx.Value(transportKey{}).(*upstreamTransport); ok {
		return t
	}
	return &upstreamTransport{git: upstreamGit, ca: upstreamCA, advertise: func(cmd *exec.Cmd) ([]byte, error) { return cmd.Output() }}
}

var upstreamOID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var upstreamBranch = regexp.MustCompile(`^refs/heads/[A-Za-z0-9][A-Za-z0-9._/-]*$`)

func mirrorUpstream(ctx context.Context, destination string) (string, error) {
	p, err := operatorkey.Load(ctx)
	if err != nil {
		return "", err
	}
	slug, err := p.MirrorUpstream(ctx, destination)
	if err != nil || slug == "" {
		return "", err
	}
	if err := unchangedMirrorURL(ctx, destination); err != nil {
		return "", err
	}
	via := transport(ctx)
	if !filepath.IsAbs(via.git) || !filepath.IsAbs(via.ca) {
		return "", refuse("mirror upstream proof needs the installed Git and CA bundle")
	}
	root := hostpaths.BakedStateHome(env.From(ctx))
	if !filepath.IsAbs(root) {
		return "", refuse("mirror upstream proof needs a protected state directory")
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil || real != filepath.Clean(root) {
		return "", refuse("mirror upstream state directory is unavailable or aliased")
	}
	dir, err := os.MkdirTemp(root, "mirror-upstream-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	env := []string{
		"HOME=" + dir, "XDG_CONFIG_HOME=" + dir, "TMPDIR=" + dir,
		// Git's ceiling is an ancestor of the command directory, so the
		// state directory's or its parents' repository config stays out.
		"GIT_CEILING_DIRECTORIES=" + root,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_COUNT=0", "GIT_CONFIG_PARAMETERS=", "GIT_TERMINAL_PROMPT=0",
		"GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1",
		"GIT_GRAFT_FILE=/dev/null", "GIT_SHALLOW_FILE=/dev/null",
		"GIT_ALLOW_PROTOCOL=https", "LC_ALL=C",
	}
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, via.git, args...)
		cmd.Dir, cmd.Env, cmd.WaitDelay = dir, env, time.Second
		return cmd
	}
	url := "https://github.com/" + slug + ".git"
	listed, err := via.advertise(command("-c", "credential.helper=", "-c", "http.sslVerify=true", "-c", "http.sslCAInfo="+via.ca,
		"-c", "http.followRedirects=false", "-c", "http.proxy=", "ls-remote", "--symref", url, "HEAD", "refs/heads/*"))
	if err != nil {
		return "", fmt.Errorf("cannot authenticate GitHub default history for %s: %w", slug, err)
	}
	tip, err := defaultAdvertisement(string(listed))
	if err != nil {
		return "", err
	}
	objects, err := operatorkey.Git(ctx, nil, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return "", err
	}
	format := "sha1"
	if len(tip) == 64 {
		format = "sha256"
	}
	if out, err := command("init", "-q", "--bare", "--template=", "--object-format="+format).CombinedOutput(); err != nil {
		return "", fmt.Errorf("cannot prepare upstream history proof: %w: %s", err, out)
	}
	env = append(env, "GIT_OBJECT_DIRECTORY="+strings.TrimSpace(string(objects)))
	if err := completeAncestry(command("-c", "core.commitGraph=false", "cat-file", "--batch"), tip); err != nil {
		return "", fmt.Errorf("cannot prove complete local GitHub default history: %w; fetch %s in the operator's terminal and retry", err, url)
	}
	return tip, nil
}

// A resumed pass holds the literal destination it leased. Git can rewrite
// that literal again after configuration changes; accepting upstream history
// must not authorize sending it to a different effective destination.
func unchangedMirrorURL(ctx context.Context, destination string) error {
	out, err := git.BytesContext(ctx, nil, "--no-pager", "config", "--null", "--get-regexp", `^url\..*\.(insteadof|pushinsteadof)$`)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil
		}
		return err
	}
	for _, kind := range []string{".insteadof", ".pushinsteadof"} {
		longest, rewritten := -1, destination
		for _, entry := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
			key, prefix, ok := strings.Cut(entry, "\n")
			if !ok {
				return refuse("cannot inspect mirror URL rewrites")
			}
			if !strings.HasSuffix(key, kind) || !strings.HasPrefix(destination, prefix) || len(prefix) <= longest {
				continue
			}
			longest = len(prefix)
			rewritten = strings.TrimSuffix(strings.TrimPrefix(key, "url."), kind) + strings.TrimPrefix(destination, prefix)
		}
		if rewritten != destination {
			return refuse("the protected mirror destination is rewritten by Git configuration; restore its exact URL before resuming")
		}
	}
	return nil
}

func defaultAdvertisement(listed string) (string, error) {
	var branch, head string
	refs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(listed, "\n"), "\n") {
		value, ref, ok := strings.Cut(line, "\t")
		if !ok {
			return "", refuse("malformed GitHub default advertisement")
		}
		if symbolic, ok := strings.CutPrefix(value, "ref: "); ok {
			if ref != "HEAD" || branch != "" || !upstreamBranch.MatchString(symbolic) || strings.Contains(symbolic, "..") || strings.HasSuffix(symbolic, ".") {
				return "", refuse("GitHub HEAD is not one symbolic default branch")
			}
			for _, part := range strings.Split(symbolic, "/") {
				if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
					return "", refuse("invalid GitHub default branch")
				}
			}
			branch = symbolic
			continue
		}
		if !upstreamOID.MatchString(value) || refs[ref] != "" {
			return "", refuse("invalid or duplicate GitHub ref")
		}
		refs[ref] = value
		if ref == "HEAD" {
			head = value
		}
	}
	if branch == "" || head == "" || refs[branch] != head {
		return "", refuse("GitHub default branch and HEAD do not agree")
	}
	return head, nil
}

// Walk raw parents and hash every commit, including roots, before trusting
// an exclusion. A negative rev-list boundary alone can hide missing history.
func completeAncestry(cmd *exec.Cmd, tip string) error {
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	reader := bufio.NewReader(out)
	pending, seen := []string{tip}, map[string]bool{}
	for len(pending) != 0 {
		oid := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[oid] {
			continue
		}
		seen[oid] = true
		if _, err := fmt.Fprintln(in, oid); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != oid || fields[1] != "commit" {
			return refuse("upstream commit %.12s is unavailable", oid)
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || size > 64<<20 {
			return refuse("invalid upstream commit size")
		}
		raw := make([]byte, size)
		if _, err := io.ReadFull(reader, raw); err != nil {
			return err
		}
		if newline, err := reader.ReadByte(); err != nil || newline != '\n' {
			return refuse("invalid upstream commit framing")
		}
		object := append([]byte(fmt.Sprintf("commit %d\x00", size)), raw...)
		var named string
		if len(oid) == 40 {
			named = fmt.Sprintf("%x", sha1.Sum(object)) //nolint:gosec // Git object identity
		} else {
			named = fmt.Sprintf("%x", sha256.Sum256(object))
		}
		if named != oid {
			return refuse("hash mismatch for upstream commit %.12s", oid)
		}
		headers, _, ok := strings.Cut(string(raw), "\n\n")
		if !ok {
			return refuse("malformed upstream commit")
		}
		for _, line := range strings.Split(headers, "\n") {
			if parent, ok := strings.CutPrefix(line, "parent "); ok {
				if !upstreamOID.MatchString(parent) || len(parent) != len(tip) {
					return refuse("invalid upstream parent")
				}
				pending = append(pending, parent)
			}
		}
	}
	if err := in.Close(); err != nil {
		return err
	}
	return cmd.Wait()
}
