// Package limaimport carries a session's workspace into the managed Lima
// VM as one git bundle. A session runs Export in its repository, which
// writes NAME.bundle into the outbox, the one directory outside its
// workspace it may write; lima-mcp checks the bundle there (Check) and
// copies it into the guest, where GuestScript applies it. A bundle holds
// exactly the refs Refs names, and nothing else crosses: no patch, no
// file outside the repository's history.
package limaimport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// The refs of a bundle besides HEAD: origin/main, for the guest's
// history, and two commits Export makes, the index's tree with parent
// HEAD and the worktree's (every file git add -A takes) with parent the
// index's.
const (
	RefMain     = "refs/remotes/origin/main"
	RefIndex    = "refs/lima/index"
	RefWorktree = "refs/lima/worktree"
)

// Refs are a bundle's refs, as git bundle list-heads names them.
var Refs = []string{"HEAD", RefMain, RefIndex, RefWorktree}

// ErrName is why ParseName refuses a name.
var ErrName = errors.New("is not a bundle name: a lowercase letter or digit, then up to 63 of a-z, 0-9, ., _ and -")

// Name is a bundle's name, which its file in the outbox is with .bundle
// after it.
type Name string

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ParseName checks a bundle's name as a session gives it.
func ParseName(s string) (Name, error) {
	if !namePattern.MatchString(s) {
		return "", fmt.Errorf("%q %w", s, ErrName)
	}
	return Name(s), nil
}

// File is the bundle's file name in the outbox.
func (n Name) File() string { return string(n) + ".bundle" }

// identity is who Export's commits are by, so that it needs no git
// identity of the session's.
var identity = []string{
	"GIT_AUTHOR_NAME=lima-mcp export", "GIT_AUTHOR_EMAIL=lima-mcp@localhost",
	"GIT_COMMITTER_NAME=lima-mcp export", "GIT_COMMITTER_EMAIL=lima-mcp@localhost",
}

// Export writes the bundle name in outbox from the repository holding dir:
// HEAD, origin/main, and refs/lima/index and refs/lima/worktree, new
// commits of the index and of the worktree. It writes nothing into the
// repository, whose .git a sandbox may keep read-only: the new objects go
// into a temporary object directory, which a temporary bare repository
// borrows with the repository's objects to hold the refs and write the
// bundle. It runs git, the session's own, with the session's
// configuration, as the session would, but with none of the variables
// that point git at another repository. It refuses a repository without
// origin/main, or with unmerged paths.
func Export(ctx context.Context, git, dir, outbox string, name Name) error {
	if info, err := os.Stat(outbox); err != nil || !info.IsDir() {
		return fmt.Errorf("the outbox %s is not a directory: home-manager activation makes it", outbox)
	}
	tmp, err := os.MkdirTemp("", "lima-export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	env := gitenv.Without(os.Environ())
	run := func(extra []string, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Dir = dir
		cmd.Env = append(append([]string{}, env...), extra...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", subcommand(args), err, strings.TrimSpace(stderr.String()))
		}
		return strings.TrimSpace(string(out)), nil
	}
	top, err := run(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	dir = top
	head, err := run(nil, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return err
	}
	main, err := run(nil, "rev-parse", "--verify", "--end-of-options", RefMain+"^{commit}")
	if err != nil {
		return fmt.Errorf("the repository has no origin/main, which the guest's history starts from: fetch it first (%v)", err)
	}
	paths, err := run(nil, "rev-parse", "--path-format=absolute", "--git-path", "index", "--git-path", "objects")
	if err != nil {
		return err
	}
	indexFile, sourceObjects, _ := strings.Cut(paths, "\n")
	objects := filepath.Join(tmp, "objects")
	if err := os.Mkdir(objects, 0o700); err != nil {
		return err
	}
	// Each tree is written from a copy of the index, which keeps the
	// index's record of unchanged files, so git add -A rereads only those
	// that changed, and the index itself is left as it is.
	scratch := filepath.Join(tmp, "index")
	if err := copyFile(indexFile, scratch); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	inTmp := []string{"GIT_OBJECT_DIRECTORY=" + objects, "GIT_ALTERNATE_OBJECT_DIRECTORIES=" + sourceObjects, "GIT_INDEX_FILE=" + scratch}
	// The copy is written whole: a split index would write its shared part
	// into the repository's .git, and a file system monitor keeps state
	// there too.
	scratchGit := func(args ...string) (string, error) {
		return run(inTmp, append([]string{"-c", "core.splitIndex=false", "-c", "core.fsmonitor=false", "-c", "index.threads=1"}, args...)...)
	}
	commit := func(tree, parent, message string) (string, error) {
		return run(append(inTmp, identity...), "commit-tree", "--no-gpg-sign", "-p", parent, "-m", message, tree)
	}
	// git write-tree leaves an intent-to-add file out of the index's tree,
	// and the guest would show it untracked, not added.
	planned, err := scratchGit("diff-files", "--name-only", "--diff-filter=A", "-z")
	if err != nil {
		return err
	}
	if names := strings.Split(strings.TrimRight(planned, "\x00"), "\x00"); planned != "" {
		return fmt.Errorf("intent-to-add files cannot be exported, since the guest would show them untracked: git add or git rm --cached them first: %s", strings.Join(names, ", "))
	}
	indexTree, err := scratchGit("write-tree")
	if err != nil {
		return err
	}
	index, err := commit(indexTree, head, "The index, for lima_dev_import")
	if err != nil {
		return err
	}
	if _, err := scratchGit("add", "-A"); err != nil {
		return err
	}
	worktreeTree, err := scratchGit("write-tree")
	if err != nil {
		return err
	}
	worktree, err := commit(worktreeTree, index, "The worktree, for lima_dev_import")
	if err != nil {
		return err
	}
	bare := filepath.Join(tmp, "bundle.git")
	if _, err := run(nil, "init", "-q", "--bare", bare); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(bare, "objects", "info", "alternates"), []byte(sourceObjects+"\n"+objects+"\n"), 0o600); err != nil {
		return err
	}
	if _, err := run(nil, "-C", bare, "update-ref", "--no-deref", "HEAD", head); err != nil {
		return err
	}
	for ref, c := range map[string]string{RefMain: main, RefIndex: index, RefWorktree: worktree} {
		if _, err := run(nil, "-C", bare, "update-ref", ref, c); err != nil {
			return err
		}
	}
	// git writes the bundle beside it and renames it into place, so lima-mcp
	// never reads part of one.
	_, err = run(nil, append([]string{"-C", bare, "bundle", "create", "-q", filepath.Join(outbox, name.File())}, Refs...)...)
	return err
}

func copyFile(from, to string) error {
	in, err := os.Open(from) //nolint:gosec // the repository's own index, which git names
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // in a directory of Export's own
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// subcommand is the git subcommand args run, after git's own options.
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] != "-C" && args[i] != "-c" {
			return args[i]
		}
		i++
	}
	return ""
}

// GuestScript applies a bundle in the guest, run by bash as
// `bash -c GuestScript lima-import DIR BUNDLE`, DIR being the guest's
// workdir and BUNDLE its copy of the bundle, which it removes. It makes
// DIR a repository on the first import; fetches the bundle's refs into
// refs/remotes/import; checks out HEAD as the branch work, discarding the
// guest's changes and its untracked files but the ignored; writes the
// worktree's tree into the files; and sets the index to the index's tree.
// The guest's HEAD, index, worktree and untracked files are then the
// source's, and its last line of output is HEAD.
//
// lima-mcp runs one import at a time and no guest command beside it, so
// the script removes what an import killed by its timeout left: other
// bundles beside BUNDLE and the index's lock. Where the guest has flock,
// a lock in .git is a further check that it runs alone.
const GuestScript = `set -eu
dir=$1 bundle=$2
trap 'rm -f -- "$bundle"' EXIT
for f in "${bundle%/*}"/.lima-import-*.bundle; do
  [ "$f" = "$bundle" ] || rm -f -- "$f"
done
mkdir -p -- "$dir"
cd -- "$dir"
[ -d .git ] || git init -q
if command -v flock >/dev/null; then
  exec 9>.git/lima-import.lock
  flock -n 9 || { echo "another import is running in $dir" >&2; exit 1; }
fi
rm -f .git/index.lock
git fetch -q --no-tags --no-write-fetch-head "$bundle" \
  +HEAD:refs/remotes/import/head \
  +refs/remotes/origin/main:refs/remotes/import/main \
  +refs/lima/index:refs/remotes/import/index \
  +refs/lima/worktree:refs/remotes/import/worktree
git checkout -q -f -B work refs/remotes/import/head
git clean -q -f -d
git read-tree --reset -u refs/remotes/import/worktree
git read-tree refs/remotes/import/index
git rev-parse HEAD
`
