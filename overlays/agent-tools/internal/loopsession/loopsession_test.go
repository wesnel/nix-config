package loopsession

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecord(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	real, _ := filepath.EvalSymlinks(t.TempDir())
	gitDir := filepath.Join(real, ".git")
	os.Mkdir(gitDir, 0o755)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if r, err := Read(gitDir); r != nil || err != nil {
		t.Fatalf("a record where none was written: %+v, %v", r, err)
	}
	want := Record{PID: 10, PGID: 11, GitDir: gitDir, Started: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	if err := Write(want); err != nil {
		t.Fatal(err)
	}
	path := Path(gitDir)
	if !strings.HasPrefix(path, filepath.Join(state, "agent-tools")+string(filepath.Separator)) {
		t.Errorf("the record is at %s, outside the state directory", path)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the record's mode: %v, %v", info, err)
	}

	// The same repository named through a link finds the same record.
	got, err := Read(filepath.Join(link, ".git"))
	if err != nil || got == nil || got.PID != 10 || got.PGID != 11 || !got.Started.Equal(want.Started) {
		t.Fatalf("read back %+v, %v", got, err)
	}

	// Another session's record stays.
	if err := Remove(gitDir, 99); err != nil {
		t.Fatal(err)
	}
	if r, _ := Read(gitDir); r == nil {
		t.Error("another session removed the record")
	}
	if err := Remove(gitDir, 10); err != nil {
		t.Fatal(err)
	}
	if r, _ := Read(gitDir); r != nil {
		t.Errorf("the session's own record outlived it: %+v", r)
	}
	if err := Remove(gitDir, 10); err != nil {
		t.Errorf("removing a record already gone: %v", err)
	}
}
