package repos

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	// The operator's route rules come with the settings, and one that
	// cannot route anything is refused with the config as a whole.
	t.Run("routes", func(t *testing.T) {
		_, config := repository(t)
		os.WriteFile(config, []byte(`{"repos": {"o/r": {"routes": [
		{"route": "security", "paths": ["internal/auth/**"], "why": "authentication"}]}}}`), 0o644)
		s, err := Lookup()
		if err != nil || len(s.Routes) != 1 || s.Routes[0].Paths[0] != "internal/auth/**" || s.Routes[0].Why != "authentication" {
			t.Errorf("routes: %+v, %v", s, err)
		}
		os.WriteFile(config, []byte(`{"repos": {"o/other": {"routes": [{"route": "style", "paths": ["x"], "why": "x"}]}}}`), 0o644)
		var r refusal.Refusal
		if s, err := Lookup(); !errors.As(err, &r) || !strings.Contains(err.Error(), "o/other") {
			t.Errorf("an unknown route: %+v, %v", s, err)
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

	// The caller's git configuration, from wherever git would read it,
	// rewrites github.com away; the settings still match the remote the
	// repository names.
	t.Run("the caller's git configuration", func(t *testing.T) {
		rewrite := "[url \"https://mirror.invalid/\"]\n\tinsteadOf = https://github.com/\n"
		file := func(t *testing.T, path string) string {
			t.Helper()
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(rewrite), 0o644); err != nil {
				t.Fatal(err)
			}
			return path
		}
		// unsetGlobal leaves git to find the global configuration under
		// HOME or XDG_CONFIG_HOME.
		unsetGlobal := func(t *testing.T) {
			t.Setenv("GIT_CONFIG_GLOBAL", "")
			os.Unsetenv("GIT_CONFIG_GLOBAL")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("HOME", t.TempDir())
		}
		for _, c := range []struct {
			name string
			set  func(t *testing.T)
		}{
			{"GIT_CONFIG_GLOBAL", func(t *testing.T) {
				t.Setenv("GIT_CONFIG_GLOBAL", file(t, filepath.Join(t.TempDir(), "global")))
			}},
			{"GIT_CONFIG_SYSTEM", func(t *testing.T) {
				t.Setenv("GIT_CONFIG_NOSYSTEM", "0")
				t.Setenv("GIT_CONFIG_SYSTEM", file(t, filepath.Join(t.TempDir(), "system")))
			}},
			{"HOME", func(t *testing.T) {
				unsetGlobal(t)
				file(t, filepath.Join(os.Getenv("HOME"), ".gitconfig"))
			}},
			{"XDG_CONFIG_HOME", func(t *testing.T) {
				unsetGlobal(t)
				file(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "git", "config"))
			}},
			{"GIT_CONFIG_COUNT", func(t *testing.T) {
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "url.https://mirror.invalid/.insteadOf")
				t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/")
			}},
			{"GIT_CONFIG_PARAMETERS", func(t *testing.T) {
				t.Setenv("GIT_CONFIG_PARAMETERS", "'url.https://mirror.invalid/.insteadof'='https://github.com/'")
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				configured(t)
				c.set(t)
				if out, err := git.Run("remote", "-v"); err != nil || !strings.Contains(out, "mirror.invalid") {
					t.Fatalf("%s did not rewrite the remote for git itself: %q, %v", c.name, out, err)
				}
				if s, err := Lookup(); err != nil || s.Repo != "o/r" {
					t.Errorf("with %s rewriting the remote: %+v, %v", c.name, s, err)
				}
			})
		}
	})

	// A rewrite in the repository's own configuration still applies, as
	// it does for every git command in the checkout.
	t.Run("the repository's own insteadOf", func(t *testing.T) {
		dir := configured(t)
		gitIn(t, dir, "config", "url.https://github.com/.insteadOf", "gh:")
		gitIn(t, dir, "remote", "set-url", "origin", "gh:o/r")
		if s, err := Lookup(); err != nil || s.Repo != "o/r" {
			t.Errorf("an alias the repository rewrites: %+v, %v", s, err)
		}
	})
}
