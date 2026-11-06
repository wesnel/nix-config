package gitstage

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

// realTempDir is a temporary directory without symlinks on its path;
// $TMPDIR may be one, such as /tmp on macOS.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func gitIn(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
	return string(out)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixture is a repository with code.txt and other.txt committed and a
// linked worktree, task, on branch task.
type fixture struct {
	main, task string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := realTempDir(t)
	f := fixture{main: filepath.Join(root, "main"), task: filepath.Join(root, "task")}
	gitIn(t, root, nil, "init", "-q", "-b", "main", f.main)
	write(t, filepath.Join(f.main, "code.txt"), "a\n")
	write(t, filepath.Join(f.main, "other.txt"), "o\n")
	gitIn(t, f.main, nil, "add", "--", "code.txt", "other.txt")
	gitIn(t, f.main, nil, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	gitIn(t, f.main, nil, "worktree", "add", "-q", "-b", "task", f.task)
	return f
}

// childStage does what the confined child does: git add of paths with a
// private index and object directory (privateEnv).
func childStage(t *testing.T, l Layout, private string, paths ...string) {
	t.Helper()
	gitIn(t, l.Worktree, privateEnv(private), append([]string{"add", "--all", "--"}, paths...)...)
}

// staged is what the real index holds at path: its blob's content.
func staged(t *testing.T, worktree, path string) string {
	t.Helper()
	return gitIn(t, worktree, nil, "show", ":"+path)
}

func TestResolve(t *testing.T) {
	t.Run("a linked worktree resolves to its own and the common Git directory", func(t *testing.T) {
		f := newFixture(t)
		l, err := Resolve(f.task)
		if err != nil {
			t.Fatal(err)
		}
		want := Layout{Worktree: f.task, GitDir: filepath.Join(f.main, ".git", "worktrees", "task"), CommonDir: filepath.Join(f.main, ".git")}
		if l != want {
			t.Fatalf("layout %+v, want %+v", l, want)
		}
	})
	t.Run("the main checkout is refused", func(t *testing.T) {
		f := newFixture(t)
		if _, err := Resolve(f.main); err == nil || !strings.Contains(err.Error(), "linked worktree") {
			t.Fatalf("main checkout: %v", err)
		}
	})
	t.Run("a .git file naming another worktree's Git directory is refused", func(t *testing.T) {
		f := newFixture(t)
		other := filepath.Join(filepath.Dir(f.task), "other")
		gitIn(t, f.main, nil, "worktree", "add", "-q", "-b", "other", other)
		write(t, filepath.Join(f.task, ".git"), "gitdir: "+filepath.Join(f.main, ".git", "worktrees", "other")+"\n")
		if _, err := Resolve(f.task); err == nil || !strings.Contains(err.Error(), "registered") {
			t.Fatalf("redirected .git file: %v", err)
		}
	})
	t.Run("a symlinked Git directory is refused", func(t *testing.T) {
		f := newFixture(t)
		gitDir := filepath.Join(f.main, ".git", "worktrees", "task")
		moved := filepath.Join(realTempDir(t), "moved")
		if err := os.Rename(gitDir, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, gitDir); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(f.task); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlinked Git directory: %v", err)
		}
	})
	t.Run("a worktree path through a symlink is refused", func(t *testing.T) {
		f := newFixture(t)
		alias := filepath.Join(realTempDir(t), "alias")
		if err := os.Symlink(f.task, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(alias); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("aliased worktree: %v", err)
		}
	})
	t.Run("a symlinked object directory is refused", func(t *testing.T) {
		f := newFixture(t)
		objects := filepath.Join(f.main, ".git", "objects")
		moved := filepath.Join(realTempDir(t), "objects")
		if err := os.Rename(objects, moved); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(moved, objects); err != nil {
			t.Fatal(err)
		}
		if _, err := Resolve(f.task); err == nil || !strings.Contains(err.Error(), "object directory") {
			t.Fatalf("symlinked objects: %v", err)
		}
	})
}

func TestInstall(t *testing.T) {
	setup := func(t *testing.T) (fixture, Layout, *Lock, string) {
		t.Helper()
		f := newFixture(t)
		l, err := Resolve(f.task)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := l.Lock()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(lock.Release)
		private := realTempDir(t)
		if err := lock.Snapshot(private); err != nil {
			t.Fatal(err)
		}
		return f, l, lock, private
	}
	t.Run("stages requested paths and keeps unrelated staged entries", func(t *testing.T) {
		f := newFixture(t)
		write(t, filepath.Join(f.task, "other.txt"), "staged before\n")
		gitIn(t, f.task, nil, "add", "--", "other.txt")
		l, err := Resolve(f.task)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := l.Lock()
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		private := realTempDir(t)
		if err := lock.Snapshot(private); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(f.task, "code.txt"), "brokered\n")
		write(t, filepath.Join(f.task, "new.txt"), "created\n")
		childStage(t, l, private, "code.txt", "new.txt")
		if err := lock.Install(private, []string{"code.txt", "new.txt"}); err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]string{"code.txt": "brokered\n", "new.txt": "created\n", "other.txt": "staged before\n"} {
			if got := staged(t, f.task, path); got != want {
				t.Fatalf("%s staged %q, want %q", path, got, want)
			}
		}
		if out := gitIn(t, f.task, nil, "fsck", "--no-dangling"); out != "" {
			t.Fatalf("fsck: %s", out)
		}
		if _, err := os.Lstat(filepath.Join(l.GitDir, "index.lock")); !os.IsNotExist(err) {
			t.Fatalf("lock remains after install: %v", err)
		}
	})
	t.Run("stages a deletion", func(t *testing.T) {
		f, l, lock, private := setup(t)
		if err := os.Remove(filepath.Join(f.task, "code.txt")); err != nil {
			t.Fatal(err)
		}
		childStage(t, l, private, "code.txt")
		if err := lock.Install(private, []string{"code.txt"}); err != nil {
			t.Fatal(err)
		}
		if out := gitIn(t, f.task, nil, "ls-files", "--", "code.txt"); out != "" {
			t.Fatalf("deleted file still staged: %q", out)
		}
		if got := staged(t, f.task, "other.txt"); got != "o\n" {
			t.Fatalf("other.txt staged %q", got)
		}
	})
	t.Run("ignores the child's changes to paths not requested", func(t *testing.T) {
		f, l, lock, private := setup(t)
		write(t, filepath.Join(f.task, "code.txt"), "requested\n")
		write(t, filepath.Join(f.task, "other.txt"), "not requested\n")
		childStage(t, l, private, "code.txt", "other.txt")
		if err := lock.Install(private, []string{"code.txt"}); err != nil {
			t.Fatal(err)
		}
		// The whole index, so an extra entry for other.txt shows.
		want := "100644 " + blobID("requested\n") + " 0\tcode.txt\n100644 " + blobID("o\n") + " 0\tother.txt\n"
		if got := gitIn(t, f.task, nil, "ls-files", "--stage"); got != want {
			t.Fatalf("index:\n%s\nwant:\n%s", got, want)
		}
	})
	t.Run("refuses a private object whose content does not match its name", func(t *testing.T) {
		f, l, lock, private := setup(t)
		write(t, filepath.Join(f.task, "code.txt"), "honest\n")
		childStage(t, l, private, "code.txt")
		oid := blobID("honest\n")
		var forged bytes.Buffer
		z := zlib.NewWriter(&forged)
		fmt.Fprintf(z, "blob 7\x00forged\n")
		z.Close()
		object := filepath.Join(private, "objects", oid[:2], oid[2:])
		if err := os.Chmod(object, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(object, forged.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(filepath.Join(l.GitDir, "index"))
		if err != nil {
			t.Fatal(err)
		}
		if err := lock.Install(private, []string{"code.txt"}); err == nil || !strings.Contains(err.Error(), "object") {
			t.Fatalf("forged object: %v", err)
		}
		after, err := os.ReadFile(filepath.Join(l.GitDir, "index"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("index changed after refusal: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(l.CommonDir, "objects", oid[:2], oid[2:])); !os.IsNotExist(err) {
			t.Fatalf("forged object promoted: %v", err)
		}
	})
	t.Run("refuses a requested path the child left in a conflicted stage", func(t *testing.T) {
		f, l, lock, private := setup(t)
		write(t, filepath.Join(f.task, "code.txt"), "changed\n")
		childStage(t, l, private, "code.txt")
		oid := strings.Fields(gitIn(t, f.task, privateEnv(private), "ls-files", "-s", "--", "code.txt"))[1]
		cmd := exec.Command("git", "-C", f.task, "update-index", "--index-info")
		cmd.Env = append(os.Environ(), privateEnv(private)...)
		cmd.Stdin = strings.NewReader("0 " + strings.Repeat("0", 40) + "\tcode.txt\n100644 " + oid + " 2\tcode.txt\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("make conflict: %s: %v", out, err)
		}
		if err := lock.Install(private, []string{"code.txt"}); err == nil || !strings.Contains(err.Error(), "stage") {
			t.Fatalf("conflicted entry: %v", err)
		}
		if got := staged(t, f.task, "code.txt"); got != "a\n" {
			t.Fatalf("code.txt staged %q after refusal", got)
		}
	})
	t.Run("refuses a file whose directory another kept entry holds", func(t *testing.T) {
		// a-b sorts between a and a/b, so a and a/b are not neighbours.
		f := newFixture(t)
		if err := os.Mkdir(filepath.Join(f.task, "a"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(f.task, "a", "b"), "inside\n")
		write(t, filepath.Join(f.task, "a-b"), "beside\n")
		gitIn(t, f.task, nil, "add", "--", "a/b", "a-b")
		if err := os.RemoveAll(filepath.Join(f.task, "a")); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(f.task, "a"), "now a file\n")
		l, err := Resolve(f.task)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := l.Lock()
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		private := realTempDir(t)
		if err := lock.Snapshot(private); err != nil {
			t.Fatal(err)
		}
		childStage(t, l, private, "a")
		if err := lock.Install(private, []string{"a"}); err == nil || !strings.Contains(err.Error(), "directory") {
			t.Fatalf("file over a kept directory entry: %v", err)
		}
		if got := gitIn(t, f.task, nil, "ls-files", "--", "a", "a/b"); got != "a/b\n" {
			t.Fatalf("index after refusal: %q", got)
		}
	})
	t.Run("refuses a blob whose header Git would not read", func(t *testing.T) {
		f, l, lock, private := setup(t)
		// Hashes to its name, but Git reads sizes as plain digits.
		content := "blob +7\x00forged\n"
		sum := sha1.Sum([]byte(content))
		oid := hex.EncodeToString(sum[:])
		var compressed bytes.Buffer
		z := zlib.NewWriter(&compressed)
		z.Write([]byte(content))
		z.Close()
		if err := os.MkdirAll(filepath.Join(private, "objects", oid[:2]), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(private, "objects", oid[:2], oid[2:]), compressed.Bytes(), 0o444); err != nil {
			t.Fatal(err)
		}
		gitIn(t, f.task, privateEnv(private), "update-index", "--cacheinfo", "100644,"+oid+",code.txt")
		if err := lock.Install(private, []string{"code.txt"}); err == nil || !strings.Contains(err.Error(), "not the blob") {
			t.Fatalf("malformed header: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(l.CommonDir, "objects", oid[:2], oid[2:])); !os.IsNotExist(err) {
			t.Fatalf("malformed object promoted: %v", err)
		}
	})
	t.Run("refuses an entry naming an object of the shared store that is not a blob", func(t *testing.T) {
		f, l, lock, private := setup(t)
		tree := strings.TrimSpace(gitIn(t, f.task, nil, "rev-parse", "HEAD^{tree}"))
		if _, err := os.Lstat(filepath.Join(l.CommonDir, "objects", tree[:2], tree[2:])); err != nil {
			t.Fatalf("the fixture's tree is not a loose object: %v", err)
		}
		gitIn(t, f.task, privateEnv(private), "update-index", "--cacheinfo", "100644,"+tree+",code.txt")
		if err := lock.Install(private, []string{"code.txt"}); err == nil {
			t.Fatal("a tree was staged as code.txt's blob")
		}
		if got := staged(t, f.task, "code.txt"); got != "a\n" {
			t.Fatalf("code.txt staged %q after refusal", got)
		}
	})
	t.Run("refuses a file entry reusing a gitlink's commit", func(t *testing.T) {
		f := newFixture(t)
		commit := strings.TrimSpace(gitIn(t, f.task, nil, "rev-parse", "HEAD"))
		gitIn(t, f.task, nil, "update-index", "--add", "--cacheinfo", "160000,"+commit+",sub")
		l, err := Resolve(f.task)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := l.Lock()
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Release()
		private := realTempDir(t)
		if err := lock.Snapshot(private); err != nil {
			t.Fatal(err)
		}
		gitIn(t, f.task, privateEnv(private), "update-index", "--cacheinfo", "100644,"+commit+",sub")
		if err := lock.Install(private, []string{"sub"}); err == nil {
			t.Fatal("a commit was staged as sub's blob")
		}
		if got := gitIn(t, f.task, nil, "ls-files", "--stage", "--", "sub"); got != "160000 "+commit+" 0\tsub\n" {
			t.Fatalf("index after refusal: %q", got)
		}
	})
	t.Run("stages a mode change", func(t *testing.T) {
		f, l, lock, private := setup(t)
		if err := os.Chmod(filepath.Join(f.task, "code.txt"), 0o755); err != nil {
			t.Fatal(err)
		}
		childStage(t, l, private, "code.txt")
		if err := lock.Install(private, []string{"code.txt"}); err != nil {
			t.Fatal(err)
		}
		want := "100755 " + blobID("a\n") + " 0\tcode.txt\n"
		if got := gitIn(t, f.task, nil, "ls-files", "--stage", "--", "code.txt"); got != want {
			t.Fatalf("index %q, want %q", got, want)
		}
	})
	t.Run("refuses a requested entry marked assume-unchanged", func(t *testing.T) {
		f, l, lock, private := setup(t)
		write(t, filepath.Join(f.task, "code.txt"), "changed\n")
		childStage(t, l, private, "code.txt")
		gitIn(t, f.task, privateEnv(private), "update-index", "--assume-unchanged", "--", "code.txt")
		if err := lock.Install(private, []string{"code.txt"}); err == nil || !strings.Contains(err.Error(), "flags") {
			t.Fatalf("assume-unchanged entry: %v", err)
		}
		if got := gitIn(t, f.task, nil, "ls-files", "-v", "--", "code.txt"); got != "H code.txt\n" {
			t.Fatalf("index after refusal: %q", got)
		}
	})
}

// privateEnv points Git at the private index and objects alone, as the
// child runs.
func privateEnv(private string) []string {
	return []string{
		"GIT_INDEX_FILE=" + filepath.Join(private, "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(private, "objects"),
	}
}

func TestLock(t *testing.T) {
	t.Run("another process's lock refuses and stays", func(t *testing.T) {
		f := newFixture(t)
		l, err := Resolve(f.task)
		if err != nil {
			t.Fatal(err)
		}
		held := filepath.Join(l.GitDir, "index.lock")
		write(t, held, "theirs")
		if _, err := l.Lock(); err == nil || !strings.Contains(err.Error(), "lock") {
			t.Fatalf("second lock: %v", err)
		}
		if data, err := os.ReadFile(held); err != nil || string(data) != "theirs" {
			t.Fatalf("their lock changed: %q %v", data, err)
		}
	})
	t.Run("release removes only its own lock", func(t *testing.T) {
		f := newFixture(t)
		l, err := Resolve(f.task)
		if err != nil {
			t.Fatal(err)
		}
		lock, err := l.Lock()
		if err != nil {
			t.Fatal(err)
		}
		held := filepath.Join(l.GitDir, "index.lock")
		if err := os.Remove(held); err != nil {
			t.Fatal(err)
		}
		write(t, held, "theirs")
		lock.Release()
		if data, err := os.ReadFile(held); err != nil || string(data) != "theirs" {
			t.Fatalf("release removed another lock: %q %v", data, err)
		}
	})
}

func blobID(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(sum[:])
}
