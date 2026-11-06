package limaimport

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/checkedfile"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/guard"
)

// MaxBundle is the largest bundle Check takes, in bytes.
const MaxBundle = 256 << 20

// maxBundle is MaxBundle, which a test lowers rather than copy 256 MiB;
// afterStat runs between reading the bundle's size and copying it, where
// a test grows the file.
var (
	maxBundle int64 = MaxBundle
	afterStat       = func() {}
)

// The reasons Check refuses a bundle, besides checkedfile's
// (ErrNotRegular, ErrLinked, ErrOutside, ErrCredential) and a missing
// file's (os.ErrNotExist), each wrapped with what it found.
var (
	ErrLink        = errors.New("is a symbolic link, or reached through one")
	ErrOwner       = errors.New("is not the user's")
	ErrTooLarge    = errors.New("is larger than 256 MiB")
	ErrRefs        = errors.New("does not hold exactly HEAD, refs/remotes/origin/main, refs/lima/index and refs/lima/worktree, each a commit")
	ErrCredential  = errors.New("holds a credential's path in its history")
	ErrUnreachable = errors.New("holds an object none of its refs reaches")
)

// uid is the user a bundle must belong to: lima-mcp's, who is the
// session's.
var uid = os.Getuid

// Bundle is a bundle Check took: its private copy, which every later step
// reads, and the commit its HEAD names.
type Bundle struct {
	Path, Head string
	dir        string
}

// Close removes the copy.
func (b *Bundle) Close() error { return os.RemoveAll(b.dir) }

// Check takes the bundle name from outbox, the directory sessions write
// bundles into, for import, or says why not. It opens the bundle through
// checkedfile with outbox as the root, without following a link at its
// name, and requires a regular file with one link, the user's, of at most
// MaxBundle bytes. It copies it once into a new directory under parent,
// mode 0700, and checks that copy with git, the pinned one, run with no
// configuration but the repository's, no lazy fetch and no other
// environment: fetched with fsck into a bare repository, the bundle holds
// exactly Refs, each a commit, and only objects they reach, and no commit
// of any of them, merges
// against each parent, adds, changes or deletes a path guard.IsRepoCredential
// matches.
func Check(ctx context.Context, git, outbox string, name Name, parent string) (_ *Bundle, err error) {
	file := filepath.Join(outbox, name.File())
	f, real, err := checkedfile.OpenIn(file, []string{outbox})
	if err != nil {
		return nil, err
	}
	defer f.Close()
	root, err := filepath.EvalSymlinks(outbox)
	if err != nil {
		return nil, err
	}
	if real != filepath.Join(root, name.File()) {
		return nil, fmt.Errorf("%s %w", file, ErrLink)
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !ok || int64(st.Uid) != int64(uid()):
		return nil, fmt.Errorf("%s %w", file, ErrOwner)
	case info.Size() > maxBundle:
		return nil, fmt.Errorf("%s %w", file, ErrTooLarge)
	}
	afterStat()
	dir, err := os.MkdirTemp(parent, "import-")
	if err != nil {
		return nil, err
	}
	b := &Bundle{Path: filepath.Join(dir, name.File()), dir: dir}
	defer func() {
		if err != nil {
			b.Close()
		}
	}()
	if err := copyAtMost(f, b.Path); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	repo := filepath.Join(dir, "repo.git")
	env := append(gitenv.WithoutConfig([]string{"HOME=" + dir, "PATH=/usr/bin:/bin", "TMPDIR=" + dir}),
		"GIT_NO_LAZY_FETCH=1", "GIT_TERMINAL_PROMPT=0")
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, git, args...) //nolint:gosec // the pinned git the store configuration names, with constant arguments and paths of Check's own
		cmd.Dir, cmd.Env = dir, env
		return cmd
	}
	run := func(args ...string) (string, error) {
		var stderr bytes.Buffer
		cmd := command(args...)
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", subcommand(args), err, strings.TrimSpace(stderr.String()))
		}
		return string(out), nil
	}
	if _, err := run("init", "-q", "--bare", repo); err != nil {
		return nil, err
	}
	heads, err := run("-C", repo, "bundle", "list-heads", b.Path)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(heads), "\n") {
		_, ref, _ := strings.Cut(line, " ")
		refs = append(refs, ref)
	}
	slices.Sort(refs)
	want := slices.Sorted(slices.Values(Refs))
	if !slices.Equal(refs, want) {
		return nil, fmt.Errorf("%s holds %v: it %w", file, refs, ErrRefs)
	}
	if _, err := run("-C", repo, "-c", "fetch.fsckObjects=true", "fetch", "-q", "--no-tags", "--no-write-fetch-head",
		b.Path, "+refs/*:refs/*", "+HEAD:refs/lima/head"); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	types, err := run("-C", repo, "for-each-ref", "--format=%(objecttype)")
	if err != nil {
		return nil, err
	}
	// The three refs and refs/lima/head, HEAD's, each a commit.
	kinds := strings.Fields(types)
	if len(kinds) != len(Refs) || slices.ContainsFunc(kinds, func(k string) bool { return k != "commit" }) {
		return nil, fmt.Errorf("%s %w", file, ErrRefs)
	}
	// The guest fetches the whole pack, so an object no ref reaches would
	// reach the guest unscanned.
	all, err := run("-C", repo, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)")
	if err != nil {
		return nil, err
	}
	listed, err := run("-C", repo, "rev-list", "--objects", "--all")
	if err != nil {
		return nil, err
	}
	reached := map[string]bool{}
	for _, line := range strings.Split(listed, "\n") {
		if id, _, _ := strings.Cut(line, " "); id != "" {
			reached[id] = true
		}
	}
	for _, id := range strings.Fields(all) {
		if !reached[id] {
			return nil, fmt.Errorf("%s %w: %s", file, ErrUnreachable, id)
		}
	}
	if err := scan(command("-C", repo, "-c", "log.showRoot=true", "log", "--all", "-m", "--no-renames", "--format=", "--name-only", "-z"), file); err != nil {
		return nil, err
	}
	head, err := run("-C", repo, "rev-parse", "--verify", "refs/lima/head^{commit}")
	if err != nil {
		return nil, err
	}
	b.Head = strings.TrimSpace(head)
	return b, nil
}

// copyAtMost copies f to a new file at path, refusing more than
// MaxBundle bytes, since the file may grow after its size was read.
func copyAtMost(f *os.File, path string) error {
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // in Check's own new directory
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(f, maxBundle+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxBundle {
		err = ErrTooLarge
	}
	return err
}

// scan runs the log cmd, which lists each commit's paths, NUL separated,
// and refuses the first that names a credential.
func scan(cmd *exec.Cmd, file string) error {
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	lines := bufio.NewScanner(out)
	lines.Buffer(make([]byte, 64<<10), 1<<20)
	lines.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	var found string
	for lines.Scan() {
		// A commit's paths follow a line break.
		if p := strings.TrimLeft(lines.Text(), "\n"); p != "" && guard.IsRepoCredential(p) {
			found = p
			break
		}
	}
	scanErr := lines.Err()
	if found != "" {
		cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case found != "":
		return fmt.Errorf("%s %w: %s", file, ErrCredential, found)
	case scanErr != nil:
		return scanErr
	case waitErr != nil:
		return fmt.Errorf("git log: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}
