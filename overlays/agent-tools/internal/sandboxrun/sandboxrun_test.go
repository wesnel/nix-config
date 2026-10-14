package sandboxrun

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeBackend stands in for eca-gondolin and eca-bwrap: it records its
// arguments one to a line and its environment, and behaves as $FAKE_BACKEND
// says.
const fakeBackend = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_LOG/argv"
env > "$FAKE_LOG/env"
echo $$ > "$FAKE_LOG/pid"
case "$FAKE_BACKEND" in
  flood) head -c 1048576 /dev/zero | tr '\0' 'x'; exit 0 ;;
  hang) trap '' TERM; sleep 60 & echo $! > "$FAKE_LOG/child"; wait ;;
  fail) echo "guest says no" >&2; exit 7 ;;
  fork) trap '' TERM; (i=0; while [ $i -lt 300 ]; do sleep 30 > /dev/null 2>&1 & i=$((i+1)); if [ $i = 20 ]; then echo > "$FAKE_LOG/started"; fi; done; wait) > /dev/null 2>&1 & wait ;;
  *) echo "hello from the guest" ;;
esac
`

func setUp(t *testing.T, mode string) (string, string) {
	t.Helper()
	bin, log := t.TempDir(), t.TempDir()
	backend := filepath.Join(bin, "eca-gondolin")
	os.WriteFile(backend, []byte(fakeBackend), 0o755)
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_BACKEND", mode)
	return backend, log
}

func spec(t *testing.T, backend string) Spec {
	ws, tools := t.TempDir(), t.TempDir()
	return Spec{Backend: backend, Workspace: ws, Mounts: map[string]string{"/opt/tools": tools},
		Script: "echo hi", Memory: "4G", CPUs: 2, Timeout: 30 * time.Second, Cap: 1024}
}

func TestRun(t *testing.T) {
	backend, log := setUp(t, "")
	t.Setenv("ECA_SANDBOX_ALLOW_HOSTS", "evil.example")
	t.Setenv("ECA_SANDBOX_STATE", "/somewhere")
	t.Setenv("ECA_SANDBOX_PR_LOOP", "/x")
	s := spec(t, backend)
	r, err := Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Exit != 0 || r.Output != "hello from the guest\n" || r.TimedOut || r.Cut {
		t.Errorf("result %+v", r)
	}
	argv := strings.Split(strings.TrimSpace(string(must(os.ReadFile(filepath.Join(log, "argv"))))), "\n")
	dash := slices.Index(argv, "--")
	if dash < 0 || !slices.Equal(argv[dash+1:], []string{"sh", "-c", "echo hi"}) {
		t.Fatalf("the guest command: %q", argv)
	}
	options := argv[:dash]
	for _, want := range [][]string{{"--offline"}, {"--state", ""}, {"--config", ""}, {"--workspace", s.Workspace},
		{"--guest-path", "/src"}, {"--memory", "4G"}, {"--cpus", "2"}, {"--ro-mount", s.Mounts["/opt/tools"] + ":/opt/tools"}} {
		if !has(options, want) {
			t.Errorf("the backend was not passed %q: %q", want, options)
		}
	}
	// Every option it takes is one of these: nothing writable, networked or
	// of the host's own.
	allowed := map[string]bool{"--offline": true, "--state": true, "--config": true, "--workspace": true,
		"--guest-path": true, "--memory": true, "--cpus": true, "--ro-mount": true}
	for i := 0; i < len(options); i++ {
		if !allowed[options[i]] {
			t.Errorf("unexpected option %q", options[i])
		}
		if options[i] != "--offline" {
			i++
		}
	}
	env := string(must(os.ReadFile(filepath.Join(log, "env"))))
	if strings.Contains(env, "ECA_SANDBOX_") {
		t.Errorf("the backend inherited ECA_SANDBOX_ settings:\n%s", env)
	}
}

func has(args, want []string) bool {
	for i := range args {
		if i+len(want) <= len(args) && slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func TestOutputCapped(t *testing.T) {
	backend, _ := setUp(t, "flood")
	r, err := Run(context.Background(), spec(t, backend))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Output) != 1024 || !r.Cut || r.Exit != 0 {
		t.Errorf("%d bytes kept, cut %v, exit %d", len(r.Output), r.Cut, r.Exit)
	}
}

func TestFailure(t *testing.T) {
	backend, _ := setUp(t, "fail")
	r, err := Run(context.Background(), spec(t, backend))
	if err != nil {
		t.Fatal(err)
	}
	if r.Exit != 7 || !strings.Contains(r.Log, "guest says no") {
		t.Errorf("%+v", r)
	}
}

// TestTimeout: a guest that will not stop is killed, with everything the
// backend started.
func TestTimeout(t *testing.T) {
	backend, log := setUp(t, "hang")
	s := spec(t, backend)
	s.Timeout = time.Second
	s.Grace = time.Second
	start := time.Now()
	r, err := Run(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if !r.TimedOut || time.Since(start) > 10*time.Second {
		t.Errorf("%+v after %s", r, time.Since(start))
	}
	for _, name := range []string{"pid", "child"} {
		pid, _ := strconv.Atoi(strings.TrimSpace(string(must(os.ReadFile(filepath.Join(log, name))))))
		// A killed process is gone once it is reaped.
		for i := 0; i < 20 && pid > 0 && syscall.Kill(pid, 0) == nil; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if pid > 0 && syscall.Kill(pid, 0) == nil {
			t.Errorf("the backend's %s %d is still running", name, pid)
			syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}

func TestRefusesWhatIsNotADirectory(t *testing.T) {
	backend, _ := setUp(t, "")
	s := spec(t, backend)
	s.Mounts["relative"] = t.TempDir()
	if _, err := Run(context.Background(), s); err == nil {
		t.Error("a relative guest path was accepted")
	}
	s = spec(t, backend)
	s.Backend = ""
	if _, err := Run(context.Background(), s); err == nil {
		t.Error("no backend was accepted")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
