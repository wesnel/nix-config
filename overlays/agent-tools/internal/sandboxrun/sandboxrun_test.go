package sandboxrun

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeBackend stands in for eca-gondolin and eca-bwrap, which start a VM:
// it records its arguments one to a line, its environment and, by default,
// its standard input, and behaves as $FAKE_BACKEND says. A hanging backend
// ignores SIGTERM from its first
// line, holds $FAKE_LOG/alive open for writing with the child it starts
// until both exit, and writes $FAKE_LOG/ready once that child is started.
const fakeBackend = `#!/bin/sh
[ "$FAKE_BACKEND" = hang ] && trap '' TERM
printf '%s\n' "$@" > "$FAKE_LOG/argv"
env > "$FAKE_LOG/env"
echo $$ > "$FAKE_LOG/pid"
case "$FAKE_BACKEND" in
  flood) head -c 1048576 /dev/zero | tr '\0' 'x'; exit 0 ;;
  hang) exec 3> "$FAKE_LOG/alive"; sleep 60 & echo $! > "$FAKE_LOG/child"; echo > "$FAKE_LOG/ready"; wait ;;
  fail) echo "guest says no" >&2; exit 7 ;;
  fork) trap '' TERM; (i=0; while [ $i -lt 300 ]; do sleep 30 > /dev/null 2>&1 & i=$((i+1)); if [ $i = 20 ]; then echo > "$FAKE_LOG/started"; fi; done; wait) > /dev/null 2>&1 & wait ;;
  *) cat > "$FAKE_LOG/stdin"; echo "hello from the guest" ;;
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
	t.Helper()
	ws, tools := t.TempDir(), t.TempDir()
	return Spec{Backend: backend, Workspace: ws, Mounts: map[string]string{"/opt/tools": tools},
		Script: "echo hi", Memory: "4G", CPUs: 2, Timeout: 30 * time.Second, Cap: 1024}
}

func has(args, want []string) bool {
	for i := range args {
		if i+len(want) <= len(args) && slices.Equal(args[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

func TestRun(t *testing.T) {
	t.Run("backend arguments and environment", func(t *testing.T) {
		backend, log := setUp(t, "")
		t.Setenv("ECA_SANDBOX_ALLOW_HOSTS", "evil.example")
		t.Setenv("ECA_SANDBOX_STATE", "/somewhere")
		t.Setenv("ECA_SANDBOX_PR_LOOP", "/x")
		s := spec(t, backend)
		r, err := Run(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		if r.Exit != 0 || r.Output != "hello from the guest\n" || r.TimedOut || r.Cut {
			t.Errorf("result %+v", r)
		}
		argv := strings.Split(strings.TrimSpace(string(must(os.ReadFile(filepath.Join(log, "argv"))))), "\n")
		dash := slices.Index(argv, "--")
		// The script goes on standard input, out of the guest's command
		// line, which its processes can read.
		if dash < 0 || !slices.Equal(argv[dash+1:], []string{"sh", "-s"}) {
			t.Fatalf("the guest command: %q", argv)
		}
		if stdin := string(must(os.ReadFile(filepath.Join(log, "stdin")))); stdin != "echo hi" {
			t.Errorf("the script on standard input: %q", stdin)
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
	})

	t.Run("output capped", func(t *testing.T) {
		backend, _ := setUp(t, "flood")
		r, err := Run(t.Context(), spec(t, backend))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Output) != 1024 || !r.Cut || r.Exit != 0 {
			t.Errorf("%d bytes kept, cut %v, exit %d", len(r.Output), r.Cut, r.Exit)
		}
	})

	t.Run("failure", func(t *testing.T) {
		backend, _ := setUp(t, "fail")
		r, err := Run(t.Context(), spec(t, backend))
		if err != nil {
			t.Fatal(err)
		}
		if r.Exit != 7 || !strings.Contains(r.Log, "guest says no") {
			t.Errorf("%+v", r)
		}
	})

	// A guest that will not stop is killed, with everything the backend
	// started. The run is stopped once the backend has started its child,
	// so no step depends on how fast the machine is.
	t.Run("stopped", func(t *testing.T) {
		backend, log := setUp(t, "hang")
		alive, ready := filepath.Join(log, "alive"), filepath.Join(log, "ready")
		for _, fifo := range []string{alive, ready} {
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		// Reading alive ends once every process holding it, the backend and
		// its child, has exited. The test holds it too, so that the read
		// cannot end before the backend opens it, until Run returns: a
		// backend killed before it opens alive never will.
		reader, err := os.OpenFile(alive, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatal(err)
		}
		// Opening without a writer needs O_NONBLOCK; reading must wait.
		if err := syscall.SetNonblock(int(reader.Fd()), false); err != nil {
			t.Fatal(err)
		}
		held, err := os.OpenFile(alive, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		exited := make(chan error, 1)
		go func() {
			_, err := io.Copy(io.Discard, reader)
			reader.Close()
			exited <- err
		}()
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() {
			if r, err := os.Open(ready); err == nil {
				bufio.NewReader(r).ReadString('\n')
				r.Close()
			}
			cancel()
		}()
		s := spec(t, backend)
		s.Grace = 100 * time.Millisecond
		r, err := Run(ctx, s)
		held.Close()
		if err != nil {
			t.Fatal(err)
		}
		if r.Exit != -1 || r.TimedOut {
			t.Errorf("a stopped run gave %+v", r)
		}
		select {
		case err := <-exited:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			running := false
			for _, name := range []string{"pid", "child"} {
				data, _ := os.ReadFile(filepath.Join(log, name))
				pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
				if pid > 0 && syscall.Kill(pid, 0) == nil {
					t.Errorf("the backend's %s %d is still running", name, pid)
					syscall.Kill(pid, syscall.SIGKILL)
					running = true
				}
			}
			if !running {
				t.Error("a process the backend started still holds " + alive)
			}
		}
	})

	t.Run("timeout", func(t *testing.T) {
		backend, _ := setUp(t, "hang")
		s := spec(t, backend)
		s.Timeout = 100 * time.Millisecond
		s.Grace = 100 * time.Millisecond
		r, err := Run(t.Context(), s)
		if err != nil {
			t.Fatal(err)
		}
		if !r.TimedOut {
			t.Errorf("a run past its timeout gave %+v", r)
		}
	})

	t.Run("refuses a relative guest path", func(t *testing.T) {
		backend, _ := setUp(t, "")
		s := spec(t, backend)
		s.Mounts["relative"] = t.TempDir()
		if _, err := Run(t.Context(), s); err == nil {
			t.Error("a relative guest path was accepted")
		}
	})

	t.Run("refuses no backend", func(t *testing.T) {
		backend, _ := setUp(t, "")
		s := spec(t, backend)
		s.Backend = ""
		if _, err := Run(t.Context(), s); err == nil {
			t.Error("no backend was accepted")
		}
	})
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
