package checkedfile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

func TestRead(t *testing.T) {
	// file writes a file under a directory of the test's own and returns
	// that directory, symlinks resolved, and the file's path.
	file := func(t *testing.T) (string, string) {
		t.Helper()
		dir, _ := filepath.EvalSymlinks(t.TempDir())
		path := filepath.Join(dir, "text.md")
		if err := os.WriteFile(path, []byte("Which way?\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir, path
	}

	t.Run("in a root", func(t *testing.T) {
		dir, path := file(t)
		data, err := Read(path, []string{dir})
		if err != nil || string(data) != "Which way?\n" {
			t.Errorf("Read: %q, %v", data, err)
		}
	})

	// A credential path may be a glob, as the guard's are, which matches by
	// pattern rather than by prefix.
	t.Run("a credential under a glob", func(t *testing.T) {
		dir, path := file(t)
		secrets := filepath.Join(dir, "secrets.d")
		os.Mkdir(secrets, 0o700)
		token := filepath.Join(secrets, "token")
		os.WriteFile(token, []byte("hunter2\n"), 0o600)
		config := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(config, []byte(`{"guard": {"credentialPaths": ["`+filepath.Join(dir, "*", "token")+`"]}}`), 0o600)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		if _, err := Read(token, []string{dir}); !errors.Is(err, ErrCredential) {
			t.Errorf("Read: %v, want ErrCredential", err)
		}
		if _, err := Read(path, []string{dir}); err != nil {
			t.Errorf("Read of the file beside it: %v", err)
		}
	})

	// The guard counts a .env file as a secret by its name, wherever it is.
	t.Run("a .env file", func(t *testing.T) {
		dir, path := file(t)
		t.Setenv("AGENT_TOOLS_CONFIG", filepath.Join(t.TempDir(), "none.json"))
		for _, name := range []string{".env", ".env.production"} {
			env := filepath.Join(dir, name)
			os.WriteFile(env, []byte("TOKEN=hunter2\n"), 0o600)
			if _, err := Read(env, []string{dir}); !errors.Is(err, ErrCredential) {
				t.Errorf("Read %s: %v, want ErrCredential", name, err)
			}
		}
		if _, err := Read(path, []string{dir}); err != nil {
			t.Errorf("Read of the file beside them: %v", err)
		}
	})

	t.Run("outside every root", func(t *testing.T) {
		_, path := file(t)
		other, _ := filepath.EvalSymlinks(t.TempDir())
		if _, err := Read(path, []string{other}); !errors.Is(err, ErrOutside) {
			t.Errorf("Read: %v, want ErrOutside", err)
		}
	})

	// An empty root would match every path, as "/" does, so a root that is
	// not absolute takes nothing, and leaves the other roots to the rest.
	for _, root := range []string{"", "relative/dir"} {
		t.Run("a root that is not absolute: "+root, func(t *testing.T) {
			dir, path := file(t)
			_, outside := file(t)
			if _, err := Read(outside, []string{dir, root}); !errors.Is(err, ErrOutside) {
				t.Errorf("Read outside %s: %v, want ErrOutside", dir, err)
			}
			if _, err := Read(path, []string{dir, root}); err != nil {
				t.Errorf("Read in %s: %v", dir, err)
			}
		})
	}
}
