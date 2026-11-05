package limaimport

import (
	"crypto/sha256"
	"errors"
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

// git runs git in dir, with a fixed identity and no signing, and returns
// its output.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// source is a repository with an origin/main, a staged change, an
// unstaged one, a deletion of each kind, an untracked file and an ignored
// one, so that its index, worktree and HEAD all differ.
func source(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "src")
	git(t, t.TempDir(), "init", "-q", dir)
	write(t, filepath.Join(dir, ".gitignore"), "ignored.txt\n")
	write(t, filepath.Join(dir, "README"), "one\n")
	write(t, filepath.Join(dir, "cmd/tool/main.go"), "package main\n")
	write(t, filepath.Join(dir, "gone-staged"), "x\n")
	write(t, filepath.Join(dir, "gone-unstaged"), "y\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "first")
	git(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	write(t, filepath.Join(dir, "README"), "two\n")
	git(t, dir, "commit", "-q", "-am", "second")
	write(t, filepath.Join(dir, "README"), "staged\n")
	write(t, filepath.Join(dir, "added"), "new\n")
	git(t, dir, "add", "README", "added")
	git(t, dir, "rm", "-q", "gone-staged")
	write(t, filepath.Join(dir, "README"), "unstaged\n")
	if err := os.Remove(filepath.Join(dir, "gone-unstaged")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "untracked/notes.txt"), "mine\n")
	write(t, filepath.Join(dir, "ignored.txt"), "build output\n")
	return dir
}

// state is what an import must carry over: HEAD, the index, the status
// of every path but the ignored, and the contents of the files.
func state(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(git(t, dir, "rev-parse", "HEAD"))
	b.WriteString(git(t, dir, "ls-files", "--stage"))
	b.WriteString(git(t, dir, "status", "--porcelain", "--untracked-files=all"))
	for _, f := range strings.Fields(git(t, dir, "ls-files", "--cached", "--others", "--exclude-standard")) {
		data, err := os.ReadFile(filepath.Join(dir, f))
		if errors.Is(err, os.ErrNotExist) {
			b.WriteString(f + ": absent\n")
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(f + ": " + string(data))
	}
	return b.String()
}

// apply runs the guest's script with bash, as the guest does, on a local
// directory standing in for the guest's workdir: the test cannot boot a VM.
func apply(t *testing.T, guest, bundle string) {
	t.Helper()
	// The guest's copy, beside the workdir as lima-mcp puts it in the
	// guest's home, which the script removes.
	if err := os.MkdirAll(filepath.Dir(guest), 0o755); err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(filepath.Dir(guest), ".lima-import-0123456789abcdef.bundle")
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(copied, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{"-c", GuestScript, "lima-import"}, guest, copied)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("guest script: %v\n%s", err, out)
	}
	if _, err := os.Stat(copied); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the guest's copy of the bundle is left behind: %v", err)
	}
}

// exported exports src as name into a new outbox, which it returns.
func exported(t *testing.T, src string, name Name) string {
	t.Helper()
	outbox := t.TempDir()
	if err := Export(t.Context(), "git", src, outbox, name); err != nil {
		t.Fatal(err)
	}
	return outbox
}

func TestExportAndApply(t *testing.T) {
	t.Run("the guest matches the source, first import and again", func(t *testing.T) {
		src := source(t)
		outbox := t.TempDir()
		guest := filepath.Join(t.TempDir(), "home", "src", "ngrok-agent")
		before := state(t, src) + git(t, src, "for-each-ref") + git(t, src, "count-objects")
		if err := Export(t.Context(), "git", src, outbox, "work"); err != nil {
			t.Fatal(err)
		}
		// The source keeps no ref or object of the export's, so a sandbox
		// that keeps .git read-only does not stop it.
		if after := state(t, src) + git(t, src, "for-each-ref") + git(t, src, "count-objects"); after != before {
			t.Errorf("Export changed the source:\n%s\nwas:\n%s", after, before)
		}
		bundle := filepath.Join(outbox, "work.bundle")
		heads := git(t, src, "bundle", "list-heads", bundle)
		for _, ref := range []string{" HEAD\n", " refs/remotes/origin/main\n", " refs/lima/index\n", " refs/lima/worktree\n"} {
			if !strings.Contains(heads, ref) {
				t.Errorf("the bundle lacks%q:\n%s", strings.TrimSpace(ref), heads)
			}
		}
		if n := strings.Count(heads, "\n"); n != 4 {
			t.Errorf("the bundle holds %d refs, want 4:\n%s", n, heads)
		}
		apply(t, guest, bundle)
		if got, want := state(t, guest), state(t, src); got != want {
			t.Errorf("guest after the first import:\n%s\nsource:\n%s", got, want)
		}
		status := git(t, guest, "status", "--porcelain", "--untracked-files=all")
		for _, line := range []string{"MM README\n", "A  added\n", "D  gone-staged\n", " D gone-unstaged\n", "?? untracked/notes.txt\n"} {
			if !strings.Contains(status, line) {
				t.Errorf("guest status lacks %q:\n%s", line, status)
			}
		}
		if _, err := os.Stat(filepath.Join(guest, "ignored.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("an ignored file reached the guest: %v", err)
		}

		// The source moves on: an untracked file goes, another comes, and
		// the staged change is committed.
		if err := os.RemoveAll(filepath.Join(src, "untracked")); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(src, "later.txt"), "later\n")
		git(t, src, "commit", "-q", "-m", "third")
		if err := Export(t.Context(), "git", src, outbox, "work"); err != nil {
			t.Fatal(err)
		}
		apply(t, guest, bundle)
		if got, want := state(t, guest), state(t, src); got != want {
			t.Errorf("guest after the second import:\n%s\nsource:\n%s", got, want)
		}
		if _, err := os.Stat(filepath.Join(guest, "untracked", "notes.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("an untracked file the source removed is still in the guest: %v", err)
		}
	})

	// The guest is a copy: what was changed only there goes.
	t.Run("overwrites the guest's own edits", func(t *testing.T) {
		src := source(t)
		outbox := exported(t, src, "work")
		guest := filepath.Join(t.TempDir(), "home", "src", "ngrok-agent")
		apply(t, guest, filepath.Join(outbox, "work.bundle"))
		write(t, filepath.Join(guest, "README"), "the guest's\n")
		git(t, guest, "add", "README")
		write(t, filepath.Join(guest, "README"), "the guest's, unstaged\n")
		write(t, filepath.Join(guest, "cmd/tool/main.go"), "package guest\n")
		// The source commits its own change to the same files, so the
		// checkout moves to a commit the guest's edits conflict with.
		git(t, src, "commit", "-q", "-am", "the source's")
		if err := Export(t.Context(), "git", src, outbox, "work"); err != nil {
			t.Fatal(err)
		}
		apply(t, guest, filepath.Join(outbox, "work.bundle"))
		if got, want := state(t, guest), state(t, src); got != want {
			t.Errorf("guest after re-import:\n%s\nsource:\n%s", got, want)
		}
	})

	// A run killed by its timeout leaves its bundle and maybe the index's
	// lock; the next import, which lima-mcp runs alone, removes them.
	t.Run("removes what a killed import left", func(t *testing.T) {
		src := source(t)
		outbox := exported(t, src, "work")
		guest := filepath.Join(t.TempDir(), "home", "src", "ngrok-agent")
		apply(t, guest, filepath.Join(outbox, "work.bundle"))
		stale := filepath.Join(filepath.Dir(guest), ".lima-import-feedfacefeedface.bundle")
		write(t, stale, "old")
		write(t, filepath.Join(guest, ".git", "index.lock"), "")
		apply(t, guest, filepath.Join(outbox, "work.bundle"))
		if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the stale bundle is left: %v", err)
		}
		if got, want := state(t, guest), state(t, src); got != want {
			t.Errorf("guest after the import:\n%s\nsource:\n%s", got, want)
		}
	})

	// git write-tree leaves an intent-to-add file out of the index's tree,
	// so the guest would show it untracked.
	t.Run("refuses an intent-to-add file", func(t *testing.T) {
		src := source(t)
		write(t, filepath.Join(src, "planned.go"), "package planned\n")
		git(t, src, "add", "-N", "planned.go")
		outbox := t.TempDir()
		err := Export(t.Context(), "git", src, outbox, "work")
		if err == nil || !strings.Contains(err.Error(), "planned.go") {
			t.Errorf("Export = %v, want a refusal naming planned.go", err)
		}
		if _, err := os.Stat(filepath.Join(outbox, "work.bundle")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a bundle was written: %v", err)
		}
	})

	// A split index writes its shared part into the repository's .git, so
	// the export's own index must not be one.
	t.Run("leaves a split index's repository as it was", func(t *testing.T) {
		src := source(t)
		git(t, src, "config", "core.splitIndex", "true")
		wt := filepath.Join(t.TempDir(), "wt")
		git(t, src, "worktree", "add", "-q", wt)
		git(t, wt, "update-index", "--split-index")
		// Enough new files that git would write a new shared index.
		for i := range 20 {
			write(t, filepath.Join(wt, fmt.Sprintf("new%d.txt", i)), "new\n")
		}
		before := sums(t, filepath.Join(src, ".git"))
		if err := Export(t.Context(), "git", wt, t.TempDir(), "work"); err != nil {
			t.Fatal(err)
		}
		if after := sums(t, filepath.Join(src, ".git")); after != before {
			t.Errorf("Export changed .git:\n%s\nwas:\n%s", after, before)
		}
	})

	t.Run("refuses a repository without origin/main", func(t *testing.T) {
		src := source(t)
		git(t, src, "update-ref", "-d", "refs/remotes/origin/main")
		outbox := t.TempDir()
		if err := Export(t.Context(), "git", src, outbox, "work"); err == nil || !strings.Contains(err.Error(), "origin/main") {
			t.Errorf("Export = %v, want a refusal naming origin/main", err)
		}
		if _, err := os.Stat(filepath.Join(outbox, "work.bundle")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a bundle was written: %v", err)
		}
	})
}

// sums is every file under dir with a hash of its contents, one per line.
func sums(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %x\n", strings.TrimPrefix(p, dir), sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestParseName(t *testing.T) {
	for _, s := range []string{"work", "ngrok-agent.v4_2", "0", strings.Repeat("a", 64)} {
		if _, err := ParseName(s); err != nil {
			t.Errorf("ParseName(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", ".hidden", "-flag", "Work", "a/b", "../x", "a b", "a\x00", strings.Repeat("a", 65), "work.bundle/"} {
		if _, err := ParseName(s); !errors.Is(err, ErrName) {
			t.Errorf("ParseName(%q) = %v, want ErrName", s, err)
		}
	}
}
