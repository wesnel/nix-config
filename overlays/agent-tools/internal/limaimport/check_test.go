package limaimport

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/wesnel/agent-tools/internal/checkedfile"
)

// credentials points the guard's credential paths at a config of the
// test's own: ~/.aws/ and ~/.netrc, besides .env files, which are always
// credentials.
func credentials(t *testing.T) {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.json")
	data, _ := json.Marshal(map[string]any{"guard": map[string]any{"credentialPaths": []string{"~/.aws/", "~/.netrc"}}})
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_TOOLS_CONFIG", config)
}

func TestCheck(t *testing.T) {
	t.Run("takes a clean bundle into a private copy", func(t *testing.T) {
		credentials(t)
		src := source(t)
		outbox := exported(t, src, "work")
		b, err := Check(t.Context(), "git", outbox, "work", t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if want := strings.TrimSpace(git(t, src, "rev-parse", "HEAD")); b.Head != want {
			t.Errorf("Head = %q, want %q", b.Head, want)
		}
		info, err := os.Stat(filepath.Dir(b.Path))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("the copy's directory: %v, %v; want mode 0700", info.Mode(), err)
		}
		original, _ := os.ReadFile(filepath.Join(outbox, "work.bundle"))
		copied, err := os.ReadFile(b.Path)
		if err != nil || !bytes.Equal(copied, original) {
			t.Errorf("the copy differs from the bundle: %v", err)
		}
		// What changes in the outbox afterwards is not what is imported.
		if err := os.WriteFile(filepath.Join(outbox, "work.bundle"), []byte("other"), 0o600); err != nil {
			t.Fatal(err)
		}
		if again, _ := os.ReadFile(b.Path); !bytes.Equal(again, original) {
			t.Error("the copy follows the outbox's file")
		}
		if err := b.Close(); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(filepath.Dir(b.Path)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Close left the copy: %v", err)
		}
	})

	// Each history holds a credential where one kind of commit alone shows
	// it; present says it is in the source's worktree at the end.
	for _, c := range []struct {
		name, path string
		present    bool
		build      func(t *testing.T, dir string)
	}{
		{"in an intermediate commit", ".env", false, func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, ".env"), "TOKEN=x\n")
			git(t, dir, "add", ".env")
			git(t, dir, "commit", "-q", "-m", "add")
			git(t, dir, "rm", "-q", ".env")
			git(t, dir, "commit", "-q", "-m", "remove")
		}},
		{"in a merged branch's parent", "config/.aws/credentials", false, func(t *testing.T, dir string) {
			git(t, dir, "checkout", "-q", "-b", "side")
			write(t, filepath.Join(dir, "config/.aws/credentials"), "[default]\n")
			git(t, dir, "add", "config")
			git(t, dir, "commit", "-q", "-m", "side")
			git(t, dir, "checkout", "-q", "main")
			git(t, dir, "merge", "-q", "-s", "ours", "-m", "merge", "side")
			git(t, dir, "branch", "-q", "-D", "side")
		}},
		// Only a merge's diff against its parents shows it, which a log
		// without -m leaves out.
		{"added by a merge itself", ".netrc", true, func(t *testing.T, dir string) {
			git(t, dir, "checkout", "-q", "-b", "side")
			write(t, filepath.Join(dir, "side.txt"), "side\n")
			git(t, dir, "add", "side.txt")
			git(t, dir, "commit", "-q", "-m", "side")
			git(t, dir, "checkout", "-q", "main")
			write(t, filepath.Join(dir, "main.txt"), "main\n")
			git(t, dir, "add", "main.txt")
			git(t, dir, "commit", "-q", "-m", "main")
			git(t, dir, "merge", "-q", "--no-commit", "side")
			write(t, filepath.Join(dir, ".netrc"), "machine x\n")
			git(t, dir, "add", ".netrc")
			git(t, dir, "commit", "-q", "-m", "merge")
			git(t, dir, "branch", "-q", "-D", "side")
		}},
		{"only in origin/main", "deploy/.env.production", false, func(t *testing.T, dir string) {
			git(t, dir, "checkout", "-q", "-b", "upstream")
			write(t, filepath.Join(dir, "deploy/.env.production"), "TOKEN=x\n")
			git(t, dir, "add", "deploy")
			git(t, dir, "commit", "-q", "-m", "upstream")
			git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
			git(t, dir, "checkout", "-q", "main")
			git(t, dir, "branch", "-q", "-D", "upstream")
		}},
		{"untracked in the worktree", ".aws/config", true, func(t *testing.T, dir string) {
			write(t, filepath.Join(dir, ".aws/config"), "[default]\n")
		}},
	} {
		t.Run("refuses a credential "+c.name, func(t *testing.T) {
			credentials(t)
			dir := filepath.Join(t.TempDir(), "src")
			git(t, t.TempDir(), "init", "-q", dir)
			write(t, filepath.Join(dir, "README"), "one\n")
			git(t, dir, "add", "README")
			git(t, dir, "commit", "-q", "-m", "first")
			git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
			c.build(t, dir)
			if _, err := os.Stat(filepath.Join(dir, c.path)); (err == nil) != c.present {
				t.Fatalf("%s in the source's worktree: %v, want present=%v", c.path, err, c.present)
			}
			outbox := exported(t, dir, "work")
			_, err := Check(t.Context(), "git", outbox, "work", t.TempDir())
			if !errors.Is(err, ErrCredential) || !strings.Contains(err.Error(), c.path) {
				t.Errorf("Check = %v, want ErrCredential naming %s", err, c.path)
			}
		})
	}

	// bundleIn writes a clean bundle into a new outbox and returns the
	// outbox.
	bundleIn := func(t *testing.T) string {
		t.Helper()
		credentials(t)
		return exported(t, source(t), "work")
	}

	t.Run("refuses a symlink to a bundle in the outbox", func(t *testing.T) {
		outbox := bundleIn(t)
		if err := os.Rename(filepath.Join(outbox, "work.bundle"), filepath.Join(outbox, "real.bundle")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real.bundle", filepath.Join(outbox, "work.bundle")); err != nil {
			t.Fatal(err)
		}
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, ErrLink) {
			t.Errorf("Check = %v, want ErrLink", err)
		}
	})

	t.Run("refuses a symlink out of the outbox", func(t *testing.T) {
		outbox := bundleIn(t)
		elsewhere := filepath.Join(t.TempDir(), "work.bundle")
		if err := os.Rename(filepath.Join(outbox, "work.bundle"), elsewhere); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, filepath.Join(outbox, "work.bundle")); err != nil {
			t.Fatal(err)
		}
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, checkedfile.ErrOutside) {
			t.Errorf("Check = %v, want checkedfile.ErrOutside", err)
		}
	})

	t.Run("refuses a hardlink", func(t *testing.T) {
		outbox := bundleIn(t)
		if err := os.Link(filepath.Join(outbox, "work.bundle"), filepath.Join(t.TempDir(), "other")); err != nil {
			t.Fatal(err)
		}
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, checkedfile.ErrLinked) {
			t.Errorf("Check = %v, want checkedfile.ErrLinked", err)
		}
	})

	// A FIFO with no writer would block an open without O_NONBLOCK, and the
	// test with it.
	t.Run("refuses a FIFO", func(t *testing.T) {
		outbox := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(outbox, "work.bundle"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, checkedfile.ErrNotRegular) {
			t.Errorf("Check = %v, want checkedfile.ErrNotRegular", err)
		}
	})

	t.Run("refuses a file over 256 MiB", func(t *testing.T) {
		outbox := bundleIn(t)
		// Sparse: it takes no space.
		if err := os.Truncate(filepath.Join(outbox, "work.bundle"), 256<<20+1); err != nil {
			t.Fatal(err)
		}
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Check = %v, want ErrTooLarge", err)
		}
	})

	// The test cannot make a file another user owns, so it checks against
	// another uid.
	t.Run("refuses a file another user owns", func(t *testing.T) {
		outbox := bundleIn(t)
		old := uid
		uid = func() int { return os.Getuid() + 1 }
		t.Cleanup(func() { uid = old })
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, ErrOwner) {
			t.Errorf("Check = %v, want ErrOwner", err)
		}
	})

	t.Run("refuses a missing bundle", func(t *testing.T) {
		if _, err := Check(t.Context(), "git", t.TempDir(), "work", t.TempDir()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Check = %v, want os.ErrNotExist", err)
		}
	})

	for _, c := range []struct {
		name string
		refs []string
	}{
		{"a ref too many", []string{"HEAD", RefMain, RefIndex, RefWorktree, "refs/heads/main"}},
		{"no worktree", []string{"HEAD", RefMain, RefIndex}},
	} {
		t.Run("refuses a bundle with "+c.name, func(t *testing.T) {
			credentials(t)
			src := source(t)
			git(t, src, "update-ref", RefIndex, "HEAD")
			git(t, src, "update-ref", RefWorktree, "HEAD")
			outbox := t.TempDir()
			git(t, src, append([]string{"bundle", "create", "-q", filepath.Join(outbox, "work.bundle")}, c.refs...)...)
			if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, ErrRefs) {
				t.Errorf("Check = %v, want ErrRefs", err)
			}
		})
	}

	// A tree entry named .git would write into the guest's repository on
	// checkout; fsck refuses it as it arrives.
	t.Run("refuses a tree holding .git", func(t *testing.T) {
		credentials(t)
		src := source(t)
		git(t, src, "update-ref", RefIndex, "HEAD")
		blob := strings.TrimSpace(gitInput(t, src, "x\n", "hash-object", "-w", "--stdin"))
		hooks := strings.TrimSpace(gitInput(t, src, "100644 blob "+blob+"\tpost-checkout\n", "mktree"))
		dotgit := strings.TrimSpace(gitInput(t, src, "040000 tree "+hooks+"\thooks\n", "mktree"))
		tree := strings.TrimSpace(gitInput(t, src, "040000 tree "+dotgit+"\t.git\n", "mktree"))
		commit := strings.TrimSpace(git(t, src, "commit-tree", "-p", RefIndex, "-m", "evil", tree))
		git(t, src, "update-ref", RefWorktree, commit)
		outbox := t.TempDir()
		git(t, src, append([]string{"bundle", "create", "-q", filepath.Join(outbox, "work.bundle")}, Refs...)...)
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); err == nil || !strings.Contains(err.Error(), "fetch") {
			t.Errorf("Check = %v, want the fetch refused", err)
		}
	})

	// testdata/unreachable.bundle, built by hand, holds a blob no ref
	// reaches, which a fetch would still carry into the guest.
	t.Run("refuses an object no ref reaches", func(t *testing.T) {
		credentials(t)
		data, err := os.ReadFile(filepath.Join("testdata", "unreachable.bundle"))
		if err != nil {
			t.Fatal(err)
		}
		outbox := t.TempDir()
		if err := os.WriteFile(filepath.Join(outbox, "work.bundle"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = Check(t.Context(), "git", outbox, "work", t.TempDir())
		if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "6eb5957d09acf90a286a362110c0ffd916e8d31c") {
			t.Errorf("Check = %v, want ErrUnreachable naming 6eb5957d09acf90a286a362110c0ffd916e8d31c", err)
		}
	})

	// A tag is not a commit, and git log walks no tree a tag names.
	t.Run("refuses a ref to a tag of a tree holding a credential", func(t *testing.T) {
		credentials(t)
		src := source(t)
		git(t, src, "update-ref", RefIndex, "HEAD")
		blob := strings.TrimSpace(gitInput(t, src, "machine x\n", "hash-object", "-w", "--stdin"))
		tree := strings.TrimSpace(gitInput(t, src, "100644 blob "+blob+"\t.netrc\n", "mktree"))
		git(t, src, "tag", "-a", "-m", "a tree", "loot", tree)
		git(t, src, "update-ref", RefWorktree, "refs/tags/loot")
		outbox := t.TempDir()
		git(t, src, append([]string{"bundle", "create", "-q", filepath.Join(outbox, "work.bundle")}, Refs...)...)
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, ErrRefs) {
			t.Errorf("Check = %v, want ErrRefs", err)
		}
	})

	// Only the root commit's diff names it: no later commit touches it.
	t.Run("refuses a credential in the root commit", func(t *testing.T) {
		credentials(t)
		dir := filepath.Join(t.TempDir(), "src")
		git(t, t.TempDir(), "init", "-q", dir)
		write(t, filepath.Join(dir, ".env"), "TOKEN=x\n")
		write(t, filepath.Join(dir, "README"), "one\n")
		git(t, dir, "add", ".env", "README")
		git(t, dir, "commit", "-q", "-m", "root")
		write(t, filepath.Join(dir, "README"), "two\n")
		git(t, dir, "commit", "-q", "-am", "second")
		git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
		outbox := exported(t, dir, "work")
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, ErrCredential) {
			t.Errorf("Check = %v, want ErrCredential", err)
		}
	})

	// The file is at the limit when its size is read, and over it by the
	// time it is copied: the test grows it between the two, and lowers the
	// limit to its size rather than copy 256 MiB.
	t.Run("refuses a file that grows past the limit", func(t *testing.T) {
		outbox := bundleIn(t)
		file := filepath.Join(outbox, "work.bundle")
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		oldMax, oldAfter := maxBundle, afterStat
		maxBundle = info.Size()
		afterStat = func() {
			if err := os.Truncate(file, info.Size()+100); err != nil {
				t.Error(err)
			}
		}
		t.Cleanup(func() { maxBundle, afterStat = oldMax, oldAfter })
		if _, err := Check(t.Context(), "git", outbox, "work", t.TempDir()); !errors.Is(err, ErrTooLarge) {
			t.Errorf("Check = %v, want ErrTooLarge", err)
		}
	})
}

// gitInput is git with input on its standard input.
func gitInput(t *testing.T, dir, input string, args ...string) string {
	t.Helper()
	cmd := gitCommand(dir, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}
