package relay

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/plans"
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
		if r := handle(Request{Program: "sh", Argv: []string{"-c", "true"}, Cwd: root}, space{root: root, guest: root}, paths); r.Code != 2 {
			t.Errorf("a program that is not served ran: %+v", r)
		}
	})

	t.Run("directory outside the workspace", func(t *testing.T) {
		root, _, paths := probe(t)
		if r := handle(Request{Program: "probe", Cwd: "/"}, space{root: root, guest: root}, paths); r.Code != 2 {
			t.Errorf("a directory outside the workspace was served: %+v", r)
		}
	})

	t.Run("environment of a served program", func(t *testing.T) {
		root, out, paths := probe(t)
		t.Setenv(URLEnv, "unix:/x")
		t.Setenv(RelayEnv, "0")
		t.Setenv("GIT_DIR", "/elsewhere/.git")
		t.Setenv("GIT_WORK_TREE", "/elsewhere")
		r := handle(Request{Program: "probe", Cwd: root, Stdin: "body"}, space{root: root, guest: root}, paths)
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

// TestServe sends requests to a relay listening on a socket, from working
// directories a sandbox can arrange inside its workspace.
func TestServe(t *testing.T) {
	// git runs git in dir, for setting up repositories.
	git := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// repository makes a repository with one commit in a new directory.
	repository := func(t *testing.T) string {
		t.Helper()
		dir, _ := filepath.EvalSymlinks(t.TempDir())
		git(t, dir, "init", "-q", "-b", "main")
		git(t, dir, "commit", "-q", "--allow-empty", "-m", "Start")
		return dir
	}
	// serve starts a relay for workspace serving probe, which writes the
	// git directory it runs against and its environment to the returned
	// file, and returns the relay's URL.
	serve := func(t *testing.T, workspace string) (url, out string) {
		t.Helper()
		bin := t.TempDir()
		out = filepath.Join(bin, "out")
		script := filepath.Join(bin, "probe")
		os.WriteFile(script, []byte("#!/bin/sh\n{ git rev-parse --path-format=absolute --git-common-dir; env; } > "+out+"\n"), 0o755)
		// A socket's path is short, which a test's temporary directory
		// may not be.
		dir, err := os.MkdirTemp("", "relay-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		socket := filepath.Join(dir, "s")
		listener, _, err := start(workspace, "", "unix", socket, map[string]string{"probe": script})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { listener.Close() })
		return "unix:" + socket, out
	}
	// call runs probe through the relay at url from dir.
	call := func(t *testing.T, url, dir string) Reply {
		t.Helper()
		t.Chdir(dir)
		r, err := Call(url, "probe", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// refused checks that the relay refused the call and ran nothing.
	refused := func(t *testing.T, r Reply, out string) {
		t.Helper()
		if r.Code != 2 || !strings.Contains(r.Stderr, "refused") {
			t.Errorf("the call was not refused: %+v", r)
		}
		if data, err := os.ReadFile(out); err == nil {
			common, _, _ := strings.Cut(string(data), "\n")
			t.Errorf("the program ran against %s", common)
		}
	}

	t.Run("gitfile into another repository", func(t *testing.T) {
		workspace, other := repository(t), repository(t)
		sub := filepath.Join(workspace, "sub")
		os.MkdirAll(sub, 0o755)
		os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o644)
		url, out := serve(t, workspace)
		refused(t, call(t, url, sub), out)
	})

	t.Run("repository made inside the workspace", func(t *testing.T) {
		workspace := repository(t)
		planted := filepath.Join(workspace, "planted")
		os.MkdirAll(planted, 0o755)
		git(t, planted, "init", "-q")
		url, out := serve(t, workspace)
		refused(t, call(t, url, planted), out)
	})

	t.Run("gitfile out of a workspace that is no repository", func(t *testing.T) {
		workspace, _ := filepath.EvalSymlinks(t.TempDir())
		other := repository(t)
		url, out := serve(t, workspace)
		// The sandbox writes it once the relay serves.
		os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o644)
		refused(t, call(t, url, workspace), out)
	})

	// A .git the sandbox writes once the relay has checked the call, in a
	// directory of the workspace's repository or over a linked worktree's
	// own, does not move the program's git commands.
	t.Run("gitfile written after the check", func(t *testing.T) {
		for _, dir := range []string{"sub", filepath.Join(".pr-loop", "7")} {
			workspace, other := repository(t), repository(t)
			at := filepath.Join(workspace, dir)
			if dir == "sub" {
				os.MkdirAll(at, 0o755)
			} else {
				git(t, workspace, "worktree", "add", "-q", at)
			}
			url, out := serve(t, workspace)
			checked = func() {
				os.WriteFile(filepath.Join(at, ".git"), []byte("gitdir: "+filepath.Join(other, ".git")+"\n"), 0o644)
			}
			r := call(t, url, at)
			checked = func() {}
			if r.Code != 0 {
				t.Fatalf("%s: %+v", dir, r)
			}
			data, _ := os.ReadFile(out)
			if common, _, _ := strings.Cut(string(data), "\n"); common != filepath.Join(workspace, ".git") {
				t.Errorf("%s: the program ran against %s", dir, common)
			}
		}
	})

	t.Run("linked worktree inside the workspace", func(t *testing.T) {
		workspace := repository(t)
		linked := filepath.Join(workspace, ".pr-loop", "7")
		git(t, workspace, "worktree", "add", "-q", linked)
		url, out := serve(t, workspace)
		if r := call(t, url, linked); r.Code != 0 {
			t.Fatalf("a worktree of the workspace's repository was refused: %+v", r)
		}
		data, _ := os.ReadFile(out)
		if common, _, _ := strings.Cut(string(data), "\n"); common != filepath.Join(workspace, ".git") {
			t.Errorf("the program ran against %s", common)
		}
	})

	t.Run("repository inside a workspace that is no repository", func(t *testing.T) {
		workspace, _ := filepath.EvalSymlinks(t.TempDir())
		clone := filepath.Join(workspace, "clone")
		os.MkdirAll(clone, 0o755)
		git(t, clone, "init", "-q")
		url, _ := serve(t, workspace)
		if r := call(t, url, clone); r.Code != 0 {
			t.Fatalf("a repository inside the workspace was refused: %+v", r)
		}
	})

	// A relayed program finds its plan by the directory it runs in, not by
	// the session that happened to start the relay.
	t.Run("session of the relay's caller", func(t *testing.T) {
		workspace := repository(t)
		for _, name := range plans.SessionEnv {
			t.Setenv(name, "launcher")
		}
		url, out := serve(t, workspace)
		if r := call(t, url, workspace); r.Code != 0 {
			t.Fatalf("probe: %+v", r)
		}
		data, _ := os.ReadFile(out)
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasSuffix(line, "=launcher") {
				t.Errorf("a relayed program inherited the relay's session: %s", line)
			}
		}
	})
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
