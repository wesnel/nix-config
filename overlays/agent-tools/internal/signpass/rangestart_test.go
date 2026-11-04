package signpass

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
)

// RangeStart calls a commit unsigned as git does, by the commit object
// alone: one with no signature header for the repository's object format
// is unsigned, and a shallow file cannot hide one below a signed commit.
func TestRangeStart(t *testing.T) {
	// headed writes a commit on parent whose only signature header is
	// header, and returns its ID.
	headed := func(t *testing.T, repo, parent, header string) string {
		t.Helper()
		object := "tree " + gitIn(t, repo, "rev-parse", parent+"^{tree}") + "\nparent " + gitIn(t, repo, "rev-parse", parent) +
			"\nauthor T <t@e> 1700000000 +0000\ncommitter T <t@e> 1700000000 +0000\n" + header +
			" -----BEGIN SSH SIGNATURE-----\n AAAA\n -----END SSH SIGNATURE-----\n\nSigned\n"
		file := filepath.Join(t.TempDir(), "commit")
		if err := os.WriteFile(file, []byte(object), 0o644); err != nil {
			t.Fatal(err)
		}
		return gitIn(t, repo, "hash-object", "--literally", "-t", "commit", "-w", file)
	}
	repo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		gitIn(t, dir, "init", "-q", "-b", "main")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
		useDir(t, dir)
		return dir
	}
	t.Run("a signature header for another object format", func(t *testing.T) {
		dir := repo(t)
		tip := headed(t, dir, "main", "gpgsig-sha256")
		start, _, err := RangeStart("main", tip)
		if want := gitIn(t, dir, "rev-parse", "main"); err != nil || start != tip+"^" || gitIn(t, dir, "rev-parse", start) != want {
			t.Errorf("RangeStart = %q, %v; want %s^, the commit git marks N", start, err, tip)
		}
	})
	t.Run("a commit signed in a SHA-256 repository", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("GIT_CONFIG_GLOBAL", signingConfig(t, dir))
		gitIn(t, dir, "init", "-q", "-b", "main", "--object-format=sha256")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
		useDir(t, dir)
		signedCommit(t, "")
		if start, _, err := RangeStart("main~1", "main"); err != nil || start != "" {
			t.Errorf("RangeStart = %q, %v; want nothing to sign", start, err)
		}
	})
	t.Run("a shallow file over an unsigned commit", func(t *testing.T) {
		dir := repo(t)
		gitIn(t, dir, "switch", "-qc", "one")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Unsigned")
		unsigned := gitIn(t, dir, "rev-parse", "HEAD")
		tip := headed(t, dir, unsigned, "gpgsig")
		if err := os.WriteFile(filepath.Join(dir, ".git", "shallow"), []byte(tip+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		start, _, err := RangeStart("main", tip)
		if err != nil || start != unsigned+"^" {
			t.Errorf("RangeStart = %q, %v; want %s^", start, err, unsigned)
		}
	})
	t.Run("large unsigned message containing signature text", func(t *testing.T) {
		dir := repo(t)
		message := filepath.Join(t.TempDir(), "message")
		if err := os.WriteFile(message, []byte("Unsigned\n\ngpgsig fake\n"+strings.Repeat("x", 1<<20)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-F", message)
		unsigned := gitIn(t, dir, "rev-parse", "HEAD")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Another")
		if start, _, err := RangeStart("main~2", "main"); err != nil || start != unsigned+"^" {
			t.Errorf("RangeStart = %q, %v; want %s^", start, err, unsigned)
		}
	})
	t.Run("invalid object responses are refused", func(t *testing.T) {
		for _, tc := range []struct{ name, mode, response string }{
			{"malformed description", "--batch-check", "printf malformed"},
			{"unterminated description", "--batch-check", "read oid; size=$(\"$REAL_GIT\" cat-file -s \"$oid\"); printf '%s commit %s' \"$oid\" \"$size\""},
			{"malformed object", "--batch", "printf malformed"},
			{"wrong object", "--batch", "printf 'wrong commit 0\\n\\n'"},
			{"wrong type", "--batch", "read oid; printf '%s blob 0\\n\\n' \"$oid\""},
			{"truncated object", "--batch", "read oid; size=$(\"$REAL_GIT\" cat-file -s \"$oid\"); printf '%s commit %s\\nx' \"$oid\" \"$size\""},
			{"extra data", "--batch", "\"$REAL_GIT\" \"$@\"; printf extra"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := repo(t)
				gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Unsigned")
				real, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				bin := t.TempDir()
				marker := filepath.Join(bin, "intercepted")
				wrapper := fmt.Sprintf("#!/bin/sh\nfor arg do\n  if [ \"$arg\" = %s ]; then : > \"$MARKER\"; %s; exit 0; fi\ndone\nexec \"$REAL_GIT\" \"$@\"\n", tc.mode, tc.response)
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("REAL_GIT", real)
				t.Setenv("MARKER", marker)
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				if _, _, err := RangeStart("main~1", "main"); err == nil {
					t.Fatal("invalid Git object response was accepted")
				}
				if _, err := os.Stat(marker); err != nil {
					t.Fatalf("object response was not intercepted: %v", err)
				}
			})
		}
	})
	t.Run("blocked object read reports deadline", func(t *testing.T) {
		dir := repo(t)
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Unsigned")
		real, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bin := t.TempDir()
		fifo, marker := filepath.Join(bin, "fifo"), filepath.Join(bin, "intercepted")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		wrapper := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = --batch ]; then : > \"$MARKER\"; exec cat \"$FIFO\"; fi\ndone\nexec \"$REAL_GIT\" \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("REAL_GIT", real)
		t.Setenv("FIFO", fifo)
		t.Setenv("MARKER", marker)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		old := git.Timeout
		git.Timeout = 500 * time.Millisecond
		t.Cleanup(func() { git.Timeout = old })
		if _, _, err := RangeStart("main~1", "main"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked read: %v; want deadline exceeded", err)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("object read was not intercepted: %v", err)
		}
	})
}
