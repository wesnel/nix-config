package repos

import (
	"encoding/json"
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

// clone makes a repository with one commit and points the package's git at
// it, and returns it with the path of its settings, which do not exist
// until the test writes them.
func clone(t *testing.T) (dir, settings string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir, _ = filepath.EvalSymlinks(t.TempDir())
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, dir, "-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "x")
	git.Dir = dir
	t.Cleanup(func() { git.Dir = "" })
	return dir, filepath.Join(dir, ".git", "info", "agents.json")
}

// put writes the settings file as the operator does, mode 0644: v as it
// is where it is a string, else as JSON.
func put(t *testing.T, settings string, v any) {
	t.Helper()
	data, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		data = string(b)
	}
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestThreatNotes: threat notes are the operator's, inline or in a file
// outside the repository; a file the repository holds, in any of its
// worktrees or its git directory, would let a change write its own, and is
// refused when the settings are looked up for it.
func TestThreatNotes(t *testing.T) {
	dir, settings := clone(t)
	other := filepath.Join(t.TempDir(), "wt")
	gitIn(t, dir, "worktree", "add", "-q", "-b", "side", other)
	outside := filepath.Join(t.TempDir(), "notes.md")
	os.WriteFile(outside, []byte("T1: the token never leaves the host\n"), 0o644)
	link := filepath.Join(t.TempDir(), "link.md")
	os.Symlink(filepath.Join(dir, "THREATS.md"), link)

	for _, c := range []struct{ name, notes, want string }{
		{"inline", "T1: tokens stay on the host", "T1: tokens stay on the host"},
		{"inline, starting with a path", "/etc/agent must stay root's", "/etc/agent must stay root's"},
		{"a file outside", outside, "T1: the token never leaves the host\n"},
		{"none", "", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			put(t, settings, map[string]any{"threatNotes": c.notes})
			s, err := Lookup()
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.Notes(); err != nil || got != c.want {
				t.Errorf("notes %q, %v; want %q", got, err, c.want)
			}
		})
	}
	for name, path := range map[string]string{"in the worktree": filepath.Join(dir, "THREATS.md"),
		"in another worktree": filepath.Join(other, "THREATS.md"), "in the git directory": filepath.Join(dir, ".git", "notes"),
		"through a link": link} {
		t.Run("refused "+name, func(t *testing.T) {
			put(t, settings, map[string]any{"threatNotes": path})
			var r refusal.Refusal
			if s, err := Lookup(); !errors.As(err, &r) || !refusal.SaysInstead(err.Error()) {
				t.Errorf("%s: %+v, %v", path, s, err)
			}
		})
	}

	// A notes file that cannot be read refuses, saying what to fix.
	t.Run("unreadable", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "gone.md")
		put(t, settings, map[string]any{"threatNotes": missing})
		s, err := Lookup()
		if err != nil {
			t.Fatal(err)
		}
		var r refusal.Refusal
		if _, err := s.Notes(); !errors.As(err, &r) || !refusal.SaysInstead(err.Error()) || !strings.Contains(err.Error(), missing) {
			t.Errorf("unreadable: %v", err)
		}
	})
}

func TestLookup(t *testing.T) {
	const full = `{"setup": "make gen", "test": "go test ./...", "build": "go vet ./...", "testFiles": ["**/*_test.go"]}`
	configured := func(s *Settings) bool {
		return s != nil && s.Setup == "make gen" && s.Test == "go test ./..." && s.Build == "go vet ./..." &&
			len(s.TestFiles) == 1 && s.TestFiles[0] == "**/*_test.go"
	}

	t.Run("without the file", func(t *testing.T) {
		clone(t)
		if s, err := Lookup(); err != nil || s.Test != "" || s.Setup != "" || len(s.Routes) != 0 {
			t.Errorf("without the file: %+v, %v", s, err)
		}
	})

	t.Run("the clone's", func(t *testing.T) {
		_, settings := clone(t)
		put(t, settings, full)
		if s, err := Lookup(); err != nil || !configured(s) || s.File() != settings {
			t.Errorf("the clone's settings: %+v, %v", s, err)
		}
	})

	// Every worktree of a clone reads the one file in its common git
	// directory, from the top or from below it.
	t.Run("from a linked worktree", func(t *testing.T) {
		dir, settings := clone(t)
		put(t, settings, full)
		wt := filepath.Join(t.TempDir(), "wt")
		gitIn(t, dir, "worktree", "add", "-q", "-b", "side", wt)
		os.MkdirAll(filepath.Join(wt, "sub"), 0o755)
		for _, d := range []string{wt, filepath.Join(wt, "sub")} {
			git.Dir = d
			if s, err := Lookup(); err != nil || !configured(s) {
				t.Errorf("from %s: %+v, %v", d, s, err)
			}
		}
	})

	// A file in the worktree is the repository's, and is never read.
	t.Run("not from the worktree", func(t *testing.T) {
		dir, _ := clone(t)
		put(t, filepath.Join(dir, "info", "agents.json"), full)
		put(t, filepath.Join(dir, ".agents.json"), full)
		if s, err := Lookup(); err != nil || s.Test != "" {
			t.Errorf("a worktree's file was read: %+v, %v", s, err)
		}
	})

	// The environment's git directory names another repository, whose
	// settings are never read: the lookup reads the one it runs in, or
	// refuses.
	t.Run("not from the environment's repository", func(t *testing.T) {
		dir, _ := clone(t)
		other, otherSettings := clone(t)
		put(t, otherSettings, full)
		git.Dir = ""
		t.Chdir(dir)
		for _, name := range []string{"GIT_DIR", "GIT_COMMON_DIR"} {
			t.Run(name, func(t *testing.T) {
				t.Setenv(name, filepath.Join(other, ".git"))
				if s, err := Lookup(); err == nil && s.Test != "" {
					t.Errorf("with %s naming another repository, its settings: %+v", name, s)
				}
			})
		}
	})

	// The operator's route rules come with the settings, and one that
	// cannot route anything is refused with the file as a whole.
	t.Run("routes", func(t *testing.T) {
		_, settings := clone(t)
		put(t, settings, `{"routes": [{"route": "security", "paths": ["internal/auth/**"], "why": "authentication"}]}`)
		s, err := Lookup()
		if err != nil || len(s.Routes) != 1 || s.Routes[0].Paths[0] != "internal/auth/**" || s.Routes[0].Why != "authentication" {
			t.Errorf("routes: %+v, %v", s, err)
		}
		put(t, settings, `{"routes": [{"route": "style", "paths": ["x"], "why": "x"}]}`)
		var r refusal.Refusal
		if s, err := Lookup(); !errors.As(err, &r) || !strings.Contains(err.Error(), settings) || !refusal.SaysInstead(err.Error()) {
			t.Errorf("an unknown route: %+v, %v", s, err)
		}
	})

	// A file the operator cannot have meant, or that someone else could
	// have written, is refused, saying what to fix.
	for _, c := range []struct {
		name string
		make func(t *testing.T, settings string)
	}{
		{"a key Settings does not have", func(t *testing.T, settings string) {
			put(t, settings, `{"repo": "o/r", "test": "true"}`)
		}},
		{"not JSON", func(t *testing.T, settings string) { put(t, settings, `test = true`) }},
		{"a symbolic link", func(t *testing.T, settings string) {
			target := filepath.Join(t.TempDir(), "agents.json")
			put(t, target, full)
			os.Symlink(target, settings)
		}},
		{"a directory", func(t *testing.T, settings string) { os.MkdirAll(settings, 0o755) }},
		{"a FIFO", func(t *testing.T, settings string) {
			if err := exec.Command("mkfifo", settings).Run(); err != nil {
				t.Skip("no mkfifo:", err)
			}
		}},
		{"writable by its group", func(t *testing.T, settings string) {
			put(t, settings, full)
			os.Chmod(settings, 0o664)
		}},
		{"writable by others", func(t *testing.T, settings string) {
			put(t, settings, full)
			os.Chmod(settings, 0o646)
		}},
	} {
		t.Run("refused: "+c.name, func(t *testing.T) {
			_, settings := clone(t)
			os.MkdirAll(filepath.Dir(settings), 0o755)
			c.make(t, settings)
			var r refusal.Refusal
			if s, err := Lookup(); !errors.As(err, &r) || !strings.Contains(err.Error(), settings) || !refusal.SaysInstead(err.Error()) {
				t.Errorf("%s: %+v, %v", c.name, s, err)
			}
		})
	}

	// A pull request's clone is bare, and its settings are its own.
	t.Run("a bare clone", func(t *testing.T) {
		dir, _ := clone(t)
		bare := filepath.Join(t.TempDir(), "clone")
		gitIn(t, dir, "clone", "-q", "--bare", dir, bare)
		git.Dir = bare
		if s, err := Lookup(); err != nil || s.Test != "" {
			t.Errorf("a bare clone without the file: %+v, %v", s, err)
		}
		put(t, filepath.Join(bare, "info", "agents.json"), full)
		if s, err := Lookup(); err != nil || !configured(s) {
			t.Errorf("a bare clone's settings: %+v, %v", s, err)
		}
	})
}
