package relay

import (
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHostPath(t *testing.T) {
	// root makes a workspace with a directory sub, its path resolved.
	root := func(t *testing.T) string {
		t.Helper()
		root := t.TempDir()
		root, _ = filepath.EvalSymlinks(root)
		os.MkdirAll(filepath.Join(root, "sub"), 0o755)
		return root
	}

	t.Run("guest path", func(t *testing.T) {
		root := root(t)
		if got := hostPath("/guest/sub", root, "/guest"); got != filepath.Join(root, "sub") {
			t.Errorf("guest path not mapped: %s", got)
		}
	})

	t.Run("same path on host and guest", func(t *testing.T) {
		root := root(t)
		if got := hostPath(filepath.Join(root, "sub"), root, root); got != filepath.Join(root, "sub") {
			t.Errorf("same path changed: %s", got)
		}
	})
}

func TestHandle(t *testing.T) {
	// probe makes a workspace, resolved as start resolves it, serving one
	// program, probe, which writes its environment to the returned file and
	// echoes its input.
	probe := func(t *testing.T) (root, out string, paths map[string]string) {
		t.Helper()
		root, _ = filepath.EvalSymlinks(t.TempDir())
		out = filepath.Join(root, "env")
		script := filepath.Join(root, "probe")
		os.WriteFile(script, []byte("#!/bin/sh\nenv > "+out+"\ncat\n"), 0o755)
		return root, out, map[string]string{"probe": script}
	}

	t.Run("program not served", func(t *testing.T) {
		root, _, paths := probe(t)
		if r := handle(Request{Program: "sh", Argv: []string{"-c", "true"}, Cwd: root}, root, root, paths); r.Code != 2 {
			t.Errorf("a program that is not served ran: %+v", r)
		}
	})

	t.Run("directory outside the workspace", func(t *testing.T) {
		root, _, paths := probe(t)
		if r := handle(Request{Program: "probe", Cwd: "/"}, root, root, paths); r.Code != 2 {
			t.Errorf("a directory outside the workspace was served: %+v", r)
		}
	})

	t.Run("environment of a served program", func(t *testing.T) {
		root, out, paths := probe(t)
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
	})
}

// A client that stops partway through a request is cut off, so a sandbox
// cannot hold the relay's connections open, while a command that runs
// longer than that still gets its reply.
func TestTimeouts(t *testing.T) {
	defer func(d time.Duration) { readTimeout = d }(readTimeout)
	readTimeout = 100 * time.Millisecond
	root, _ := filepath.EvalSymlinks(t.TempDir())
	slow := filepath.Join(root, "slow")
	os.WriteFile(slow, []byte("#!/bin/sh\nsleep 1\necho done\n"), 0o755)
	listener, _, err := start(root, "", "tcp", "127.0.0.1:0", map[string]string{"slow": slow})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	t.Run("slow command", func(t *testing.T) {
		t.Chdir(root)
		r, err := Call("http://"+listener.Addr().String(), "slow", nil, nil)
		if err != nil || r.Code != 0 || r.Stdout != "done\n" {
			t.Errorf("a command outlasting the read timeout: %+v, %v", r, err)
		}
	})

	for name, partial := range map[string]string{
		"headers": "POST / HTTP/1.1\r\nHost: relay\r\n",
		"body":    "POST / HTTP/1.1\r\nHost: relay\r\nContent-Length: 100\r\n\r\n{",
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.Write([]byte(partial)); err != nil {
				t.Fatal(err)
			}
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.ReadAll(conn); errors.Is(err, os.ErrDeadlineExceeded) {
				t.Errorf("the relay still held the connection after 5s")
			}
		})
	}
}

func TestSession(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
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
	})

	// gitdir runs a session in workspace and returns what it substituted
	// for {gitdir}.
	gitdir := func(t *testing.T, workspace string) string {
		t.Helper()
		out := filepath.Join(t.TempDir(), "gitdir")
		code := Session([]string{"--workspace", workspace, "--program", "sh", "--", "sh", "-c",
			`printf '%s' "$1" > ` + out, "sh", "{gitdir}"}, os.Stderr)
		if code != 0 {
			t.Fatalf("session exited %d", code)
		}
		got, _ := os.ReadFile(out)
		return string(got)
	}

	// {gitdir} is the git directory a linked worktree's commands need,
	// which lies outside the worktree.
	t.Run("gitdir of a linked worktree", func(t *testing.T) {
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
		if got, want := gitdir(t, linked), filepath.Join(main, ".git"); got != want {
			t.Errorf("{gitdir} for %s is %q, want %q", linked, got, want)
		}
	})

	// {gitdir} is the workspace itself where it is no repository.
	t.Run("gitdir of a workspace that is no repository", func(t *testing.T) {
		plain, _ := filepath.EvalSymlinks(t.TempDir())
		if got := gitdir(t, plain); got != plain {
			t.Errorf("{gitdir} for %s is %q, want %q", plain, got, plain)
		}
	})
}
