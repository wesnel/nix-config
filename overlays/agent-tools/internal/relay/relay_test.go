package relay

import (
	"os"
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
}

func TestSession(t *testing.T) {
	out := filepath.Join(t.TempDir(), "env")
	code := Session([]string{"--workspace", t.TempDir(), "--program", "sh", "--", "sh", "-c",
		`printf '%s\n%s\n' "$SANDBOX_RELAY_URL" "$1" > ` + out + ` && test -S "$SANDBOX_RELAY_SOCKET" && ` +
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
