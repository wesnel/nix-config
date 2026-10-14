package relay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostPath(t *testing.T) {
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	if got := hostPath("/guest/sub", root, "/guest"); got != filepath.Join(root, "sub") {
		t.Errorf("guest path not mapped: %s", got)
	}
	if got := hostPath(filepath.Join(root, "sub"), root, root); got != filepath.Join(root, "sub") {
		t.Errorf("same path changed: %s", got)
	}
}

func TestHandle(t *testing.T) {
	// As start resolves it.
	root, _ := filepath.EvalSymlinks(t.TempDir())
	out := filepath.Join(root, "env")
	script := filepath.Join(root, "probe")
	os.WriteFile(script, []byte("#!/bin/sh\nenv > "+out+"\ncat\n"), 0o755)
	paths := map[string]string{"probe": script}

	if r := handle(Request{Program: "sh", Argv: []string{"-c", "true"}, Cwd: root}, root, root, paths); r.Code != 2 {
		t.Errorf("a program that is not served ran: %+v", r)
	}
	if r := handle(Request{Program: "probe", Cwd: "/"}, root, root, paths); r.Code != 2 {
		t.Errorf("a directory outside the workspace was served: %+v", r)
	}

	t.Setenv(URLEnv, "unix:/x")
	t.Setenv(RelayEnv, "0")
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	t.Setenv("GIT_WORK_TREE", "/elsewhere")
	r := handle(Request{Program: "probe", Cwd: root, Stdin: "body"}, root, root, paths)
	if r.Code != 0 || r.Stdout != "body" {
		t.Fatalf("probe: %+v", r)
	}
	env, _ := os.ReadFile(out)
	if !strings.Contains(string(env), "\n"+RelayEnv+"=1\n") || strings.Count(string(env), RelayEnv+"=") != 1 {
		t.Errorf("the relay marker is not exactly the relay's:\n%s", env)
	}
	if strings.Contains(string(env), URLEnv+"=") {
		t.Error("a relayed program could relay again")
	}
	// It runs in its own directory, not the repository the relay's
	// environment names.
	if strings.Contains(string(env), "GIT_DIR=") || strings.Contains(string(env), "GIT_WORK_TREE=") {
		t.Errorf("a relayed program inherited the relay's repository:\n%s", env)
	}
}

// TestSessionGitDir holds {gitdir} to the git directory a linked worktree's
// commands need, which lies outside the worktree, and to the workspace itself
// where it is no repository.
func TestSessionGitDir(t *testing.T) {
	main, _ := filepath.EvalSymlinks(t.TempDir())
	linked := filepath.Join(t.TempDir(), "linked")
	for _, args := range [][]string{
		{"-C", main, "init", "-q"},
		{"-C", main, "-c", "user.name=T", "-c", "user.email=t@e", "commit", "-q", "--allow-empty", "-m", "Start"},
		{"-C", main, "worktree", "add", "-q", linked},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	plain, _ := filepath.EvalSymlinks(t.TempDir())
	for _, c := range []struct{ workspace, want string }{
		{linked, filepath.Join(main, ".git")},
		{plain, plain},
	} {
		out := filepath.Join(t.TempDir(), "gitdir")
		code := Session([]string{"--workspace", c.workspace, "--program", "sh", "--", "sh", "-c",
			`printf '%s' "$1" > ` + out, "sh", "{gitdir}"}, os.Stderr)
		if code != 0 {
			t.Fatalf("session exited %d", code)
		}
		if got, _ := os.ReadFile(out); string(got) != c.want {
			t.Errorf("{gitdir} for %s is %q, want %q", c.workspace, got, c.want)
		}
	}
}

func TestSession(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env")
	t.Setenv("GIT_DIR", "/elsewhere/.git")
	code := Session([]string{"--workspace", t.TempDir(), "--program", "sh", "--", "sh", "-c",
		`printf '%s\n%s\n' "$SANDBOX_RELAY_URL" "$1" > ` + out + ` && test -S "$SANDBOX_RELAY_SOCKET" && ` +
			// The session runs in the workspace, not the repository the
			// caller's environment names.
			`test -z "${GIT_DIR+set}" && ` +
			// A sandbox compares real paths, so the socket's must be one.
			`test "$(cd "$(dirname "$SANDBOX_RELAY_SOCKET")" && pwd -P)" = "$(dirname "$SANDBOX_RELAY_SOCKET")"`, "sh", "{socket}"}, os.Stderr)
	if code != 0 {
		t.Fatalf("session exited %d", code)
	}
	data, _ := os.ReadFile(out)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != "unix:"+lines[1] {
		t.Errorf("session environment: %q", lines)
	}
	if _, err := os.Stat(lines[1]); err == nil {
		t.Error("the socket outlived the session")
	}
}
