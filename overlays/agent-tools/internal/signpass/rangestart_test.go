package signpass

import (
	"os"
	"path/filepath"
	"testing"
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
}
