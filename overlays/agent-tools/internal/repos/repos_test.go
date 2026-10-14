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

// useConfig points AGENT_TOOLS_CONFIG at a file of the test's own, which
// does not exist until the test writes it.
func useConfig(t *testing.T) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	return config
}

func TestFor(t *testing.T) {
	const settings = `{"repos": {"o/r": {"test": "go test ./...", "testFiles": ["**/*_test.go"]}}}`

	t.Run("without a config", func(t *testing.T) {
		useConfig(t)
		if s, err := For("o/r"); err != nil || s.Repo != "" {
			t.Errorf("without a config: %+v, %v", s, err)
		}
	})

	t.Run("matches without regard to case", func(t *testing.T) {
		os.WriteFile(useConfig(t), []byte(settings), 0o644)
		if s, err := For("O/R"); err != nil || s.Repo != "o/r" || s.Test != "go test ./..." {
			t.Errorf("matched without regard to case: %+v, %v", s, err)
		}
	})

	t.Run("another repository", func(t *testing.T) {
		os.WriteFile(useConfig(t), []byte(settings), 0o644)
		if s, err := For("o/other"); err != nil || s.Repo != "" {
			t.Errorf("another repository: %+v, %v", s, err)
		}
	})
}

func TestLookup(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// repository makes a repository whose origin is O/R and points the
	// package's git at it, and returns it with the path of its config.
	repository := func(t *testing.T) (dir, config string) {
		t.Helper()
		config = useConfig(t)
		dir = t.TempDir()
		gitIn(t, dir, "init", "-q", "-b", "main")
		gitIn(t, dir, "remote", "add", "origin", "https://github.com/O/R.git")
		git.Dir = dir
		t.Cleanup(func() { git.Dir = "" })
		return dir, config
	}
	// configured makes a repository as above, configured for o/r and
	// o/other.
	configured := func(t *testing.T) string {
		t.Helper()
		dir, config := repository(t)
		os.WriteFile(config, []byte(`{"repos": {
		"o/r": {"setup": "make gen", "test": "go test ./...", "build": "go vet ./...", "testFiles": ["**/*_test.go"]},
		"o/other": {"setup": "false", "test": "make check", "testFiles": ["t/**"]}}}`), 0o644)
		return dir
	}

	t.Run("without a config", func(t *testing.T) {
		repository(t)
		if s, err := Lookup(); err != nil || s.Repo != "" || s.Test != "" {
			t.Errorf("without a config: %+v, %v", s, err)
		}
	})

	t.Run("matches without regard to case", func(t *testing.T) {
		configured(t)
		s, err := Lookup()
		if err != nil || s.Repo != "o/r" || s.Setup != "make gen" || s.Test != "go test ./..." ||
			s.Build != "go vet ./..." || len(s.TestFiles) != 1 || s.TestFiles[0] != "**/*_test.go" {
			t.Fatalf("configured, matched without regard to case: %+v, %v", s, err)
		}
	})

	t.Run("two remotes with different settings", func(t *testing.T) {
		dir := configured(t)
		gitIn(t, dir, "remote", "add", "fork", "https://github.com/o/other.git")
		var r refusal.Refusal
		if _, err := Lookup(); !errors.As(err, &r) {
			t.Errorf("two remotes with different settings: %v", err)
		}
	})

	t.Run("two remotes of one repository", func(t *testing.T) {
		dir := configured(t)
		gitIn(t, dir, "remote", "add", "fork", "git@github.com:o/r.git")
		if s, err := Lookup(); err != nil || s.Repo != "o/r" {
			t.Errorf("two remotes of one repository: %+v, %v", s, err)
		}
	})
}
