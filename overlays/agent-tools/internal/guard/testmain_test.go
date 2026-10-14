package guard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	// Whether trash is on PATH differs between hosts and the Nix build;
	// TestTrash sets it.
	trashOnPath = func() bool { return false }
	// Plans, and the worktree owners they record, come from a directory of
	// the tests' own unless a test names another. Every test shares it, and
	// none writes there: a test that makes plans names a directory of its
	// own.
	dir, err := os.MkdirTemp("", "guard-plans")
	if err != nil {
		panic(err)
	}
	config := filepath.Join(dir, "config.json")
	os.WriteFile(config, []byte(`{"plans": {"dir": "`+filepath.Join(dir, "agents")+`", "host": "box"}}`), 0o600)
	os.Setenv("AGENT_TOOLS_CONFIG", config)
	fixtures = filepath.Join(dir, "fixtures")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fixtures is where the repositories several tests' cases share are made,
// each once, by the sync.OnceValues that builds it.
var fixtures string

// fixture returns what build made, failing the test if it could not.
func fixture[T any](t *testing.T, build func() (T, error)) T {
	t.Helper()
	v, err := build()
	if err != nil {
		t.Fatalf("building a shared fixture: %v", err)
	}
	return v
}

// fixtureGit runs git in dir for a shared fixture, which no test owns.
func fixtureGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitenv.Without(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %v\n%s", args, err, out)
	}
	return nil
}

// fixtureGits runs each of commands, a directory and git's arguments, in
// turn, stopping at the first that fails.
func fixtureGits(commands ...[]string) error {
	for _, c := range commands {
		if err := fixtureGit(c[0], c[1:]...); err != nil {
			return err
		}
	}
	return nil
}
