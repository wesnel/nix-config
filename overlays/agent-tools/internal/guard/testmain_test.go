package guard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	// The scratch roots are $TMPDIR alone, as in the Nix build, whose
	// TMPDIR is not under /tmp. Under a sandbox whose TMPDIR is, /tmp would
	// make every test's temporary directory scratch; a test that needs
	// scratch elsewhere sets TMPDIR in its Env.
	scratchRoots = func(e env.Env) []string { return []string{e.Get("TMPDIR")} }
	// The credentials the cases name, standing in for the list the Nix
	// build gives the guard; TestCredentialPaths sets its own.
	builtCredentialPaths = []string{"~/.ssh/id_", "~/.gnupg/private-keys", "~/.codex/auth.json",
		"~/.claude/.credentials.json", "~/.local/state/mcp-proxy/token", "~/.config/sops/age", "~/.config/ngrok"}
	// Plans, and the worktree owners they record, come from a directory of
	// the tests' own unless a test names another. Every test shares it, and
	// none writes there: a test that makes plans names a directory of its
	// own.
	dir, err := os.MkdirTemp("", "guard-plans")
	if err != nil {
		panic(err)
	}
	testConfig = filepath.Join(dir, "config.json")
	os.WriteFile(testConfig, []byte(`{"plans": {"dir": "`+filepath.Join(dir, "agents")+`", "host": "box"}}`), 0o600)
	fixtures = filepath.Join(dir, "fixtures")
	testBin = filepath.Join(dir, "bin")
	if err := os.Mkdir(testBin, 0o700); err != nil {
		panic(err)
	}
	for _, name := range []string{"git", "ssh", "sleep"} {
		if path, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(path, filepath.Join(testBin, name)); err != nil {
				panic(err)
			}
		}
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// testBin holds links to the programs the guard runs, and the tests' fake
// gits run, as this process's PATH finds them. It is the whole PATH of
// guardEnv, so whether trash is on it, which differs between hosts and the
// Nix build, is each test's to say.
var testBin string

// testConfig is the agent-tools config every test shares unless it names
// its own in its Env: plans in a directory of the tests' own, and no
// settings of the guard's.
var testConfig string

// guardEnv is the Env a test runs the guard under: this process's, with
// PATH testBin alone and AGENT_TOOLS_CONFIG testConfig.
func guardEnv() env.Env {
	return env.Process().Set("PATH="+testBin, "AGENT_TOOLS_CONFIG="+testConfig)
}

// guardCtx is t's context, carrying guardEnv.
func guardCtx(t *testing.T) context.Context { return env.With(t.Context(), guardEnv()) }

// withVars is ctx with each "NAME=value" of vars set in its Env.
func withVars(ctx context.Context, vars ...string) context.Context {
	return env.With(ctx, env.From(ctx).Set(vars...))
}

// withTrash is ctx with a trash program on its Env's PATH, ahead of the
// rest.
func withTrash(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "trash"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return withVars(ctx, "PATH="+dir+string(os.PathListSeparator)+env.From(ctx).Get("PATH"))
}

// inDir is ctx with the working directory of its Env dir.
func inDir(ctx context.Context, dir string) context.Context {
	return env.With(ctx, env.From(ctx).In(dir))
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
