package repos

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestLookup(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/O/R.git")
	git.Dir = dir
	defer func() { git.Dir = "" }()

	if s, err := Lookup(); err != nil || s.Repo != "" || s.Test != "" {
		t.Errorf("without a config: %+v, %v", s, err)
	}
	os.WriteFile(config, []byte(`{"repos": {
		"o/r": {"setup": "make gen", "test": "go test ./...", "build": "go vet ./...", "testFiles": ["**/*_test.go"]},
		"o/other": {"setup": "false", "test": "make check", "testFiles": ["t/**"]}}}`), 0o644)
	s, err := Lookup()
	if err != nil || s.Repo != "o/r" || s.Setup != "make gen" || s.Test != "go test ./..." ||
		s.Build != "go vet ./..." || len(s.TestFiles) != 1 || s.TestFiles[0] != "**/*_test.go" {
		t.Fatalf("configured, matched without regard to case: %+v, %v", s, err)
	}

	gitIn(t, dir, "remote", "add", "fork", "https://github.com/o/other.git")
	var r refusal.Refusal
	if _, err := Lookup(); !errors.As(err, &r) {
		t.Errorf("two remotes with different settings: %v", err)
	}
	gitIn(t, dir, "remote", "set-url", "fork", "git@github.com:o/r.git")
	if s, err := Lookup(); err != nil || s.Repo != "o/r" {
		t.Errorf("two remotes of one repository: %+v, %v", s, err)
	}
}
