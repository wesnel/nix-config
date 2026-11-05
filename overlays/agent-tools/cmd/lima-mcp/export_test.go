package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExport(t *testing.T) {
	// repo is a repository with a commit and an origin/main.
	repo := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		for _, args := range [][]string{
			{"init", "-q"},
			{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "first"},
			{"update-ref", "refs/remotes/origin/main", "HEAD"},
		} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		return dir
	}

	t.Run("writes the named bundle into the outbox", func(t *testing.T) {
		outbox := t.TempDir()
		if err := exportMain(t.Context(), []string{"--outbox", outbox, "work"}, repo(t)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(outbox, "work.bundle")); err != nil {
			t.Error(err)
		}
	})

	t.Run("refuses a bad name", func(t *testing.T) {
		outbox := t.TempDir()
		err := exportMain(t.Context(), []string{"--outbox", outbox, "../work"}, repo(t))
		if err == nil || !strings.Contains(err.Error(), "bundle name") {
			t.Errorf("export ../work = %v, want a refusal of the name", err)
		}
		if entries, _ := os.ReadDir(outbox); len(entries) != 0 {
			t.Errorf("the outbox holds %v", entries)
		}
	})

	t.Run("needs the outbox", func(t *testing.T) {
		if err := exportMain(t.Context(), []string{"work"}, repo(t)); err == nil || !strings.Contains(err.Error(), "usage") {
			t.Errorf("export without --outbox = %v, want its usage", err)
		}
	})
}
