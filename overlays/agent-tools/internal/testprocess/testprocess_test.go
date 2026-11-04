package testprocess

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	cleanup := gitenvtest.IsolateUser()
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func fixtureOnly(t *testing.T) {
	t.Helper()
	if os.Getenv("AGENT_TOOLS_TEST_FIXTURE") != "" {
		t.Skip("run by the parent test")
	}
}

func TestDirect(t *testing.T) {
	fixtureOnly(t)
	t.Setenv(child, "1")
	ran := false
	Run(t, "case", "", func(t *testing.T) { ran = true })
	if !ran {
		t.Fatal("direct case did not run")
	}
}

func TestTop(t *testing.T) {
	fixtureOnly(t)
	for _, tc := range []struct {
		action, want string
		fail         bool
	}{
		{"top", "--- PASS: TestTopFixture", false},
		{"topfail", "expected top failure", true},
		{"topskip", "--- SKIP: TestTopFixture", false},
	} {
		t.Run(tc.action, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v")
			cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE="+tc.action, "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(t.TempDir(), "pid"))
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(out), tc.want) {
				t.Fatalf("top fixture: %v\n%s", err, out)
			}
		})
	}
}

func TestTopFixture(t *testing.T) {
	action := os.Getenv("AGENT_TOOLS_TEST_FIXTURE")
	if !strings.HasPrefix(action, "top") {
		t.Skip("run by TestTop")
	}
	pidFile := os.Getenv("AGENT_TOOLS_TEST_PID_FILE")
	tmp := SocketTemp(t)
	if action == "topparallel" {
		tmp = Temp(t)
	}
	if os.Getenv(child) == "" {
		isolated := tmp != ""
		if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d:%t", os.Getpid(), isolated)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !Top(t, tmp) {
		return
	}
	if action == "topfail" {
		t.Fatal("expected top failure")
	}
	if action == "topskip" {
		t.Skip("expected top skip")
	}
	pid, err := os.ReadFile(pidFile)
	if action == "topparallel" && (err != nil || strings.Split(string(pid), ":")[0] == fmt.Sprint(os.Getpid())) {
		t.Fatalf("long-root top case did not run in a child: %s, %v", pid, err)
	}
	if err != nil || (strings.Split(string(pid), ":")[0] == fmt.Sprint(os.Getpid())) != strings.HasSuffix(string(pid), ":false") {
		t.Fatalf("top case shared its parent process: %s, %v", pid, err)
	}
}

func TestTopFilter(t *testing.T) {
	fixtureOnly(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v", "-test.run=^TestTopFixture$")
	cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE=topdirect", "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(t.TempDir(), "pid"))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestTopFixture") {
		t.Fatalf("filtered top fixture: %v\n%s", err, out)
	}
}

func TestLongTemp(t *testing.T) {
	fixtureOnly(t)
	for _, action := range []string{"toplong", "topparallel", "longsub", "longparallel"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			long := filepath.Join(dir, strings.Repeat("x", 90))
			if err := os.Mkdir(long, 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v")
			cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE="+action, "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(dir, "pid"), "TMPDIR="+long)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("long temporary root: %v\n%s", err, out)
			}
		})
	}
}

func TestIsolatedCases(t *testing.T) {
	fixtureOnly(t)
	for _, tc := range []struct {
		name, want string
		fail       bool
	}{
		{"pass", "--- PASS: TestIsolatedCaseFixture/case", false},
		{"argv0", "--- PASS: TestIsolatedCaseFixture/case", false},
		{"fail", "expected child failure", true},
		{"skip", "--- SKIP: TestIsolatedCaseFixture/case", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "pid")
			cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v")
			cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE="+tc.name, "AGENT_TOOLS_TEST_PID_FILE="+pidFile)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(out), tc.want) {
				t.Fatalf("fixture exit %v, want failure %v and %q:\n%s", err, tc.fail, tc.want, out)
			}
		})
	}
}

func TestIsolatedNames(t *testing.T) {
	fixtureOnly(t)
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v")
	cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE=names", "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(dir, "pid"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	for _, name := range []string{"a.b", "axb"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("case %s did not run: %v", name, err)
		}
	}
}

func TestIsolatedNestedSelection(t *testing.T) {
	fixtureOnly(t)
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v", `-test.run=^TestIsolatedCaseFixture$/^a\.b$/^keep$`)
	cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE=nested", "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(dir, "pid"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
		t.Fatalf("selected nested case did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other")); !os.IsNotExist(err) {
		t.Fatalf("unselected nested case ran or could not be checked: %v", err)
	}
}

func TestIsolatedSkipFlag(t *testing.T) {
	fixtureOnly(t)
	dir := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v", `-test.skip=^TestIsolatedCaseFixture$/^a\.b$/^other$`)
	cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE=nested", "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(dir, "pid"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
		t.Fatalf("selected nested case did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "other")); !os.IsNotExist(err) {
		t.Fatalf("skipped nested case ran or could not be checked: %v", err)
	}
}

func TestIsolatedShort(t *testing.T) {
	fixtureOnly(t)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v", "-test.short")
	cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE=short", "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(t.TempDir(), "pid"))
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "--- PASS: TestIsolatedCaseFixture/case") {
		t.Fatalf("short fixture: %v\n%s", err, out)
	}
}

func TestIsolatedTimeout(t *testing.T) {
	fixtureOnly(t)
	if testing.CoverMode() != "" {
		t.Skip("coverage runs cases in process")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	tmp := t.TempDir()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.v", "-test.timeout=3s")
	cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE=hang", "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(t.TempDir(), "pid"), "AGENT_TOOLS_TEST_BLOCK_ADDR="+listener.Addr().String(), "TMPDIR="+tmp)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if tcp, ok := listener.(*net.TCPListener); ok {
		tcp.SetDeadline(time.Now().Add(5 * time.Second))
	}
	conn, err := listener.Accept()
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("grandchild did not connect: %v", err)
	}
	defer conn.Close()
	err = cmd.Wait()
	if err == nil || !strings.Contains(out.String(), "isolated case failed") {
		t.Fatalf("fixture exit %v, want isolated child timeout:\n%s", err, &out)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Errorf("temporary state after timeout: %v, %v", entries, err)
	}
}

// TestIsolatedInterrupt interrupts the parent as a terminal's Ctrl-C does,
// signalling its process group alone; the isolated child and its grandchild
// lead and share a group of their own, so they never see that signal.
func TestIsolatedInterrupt(t *testing.T) {
	fixtureOnly(t)
	if testing.CoverMode() != "" {
		t.Skip("coverage runs cases in process")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	// The timeout bounds how long a child that outlives its parent runs.
	cmd := exec.Command(os.Args[0], "-test.v", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_FIXTURE=hang", "AGENT_TOOLS_TEST_PID_FILE="+filepath.Join(t.TempDir(), "pid"), "AGENT_TOOLS_TEST_BLOCK_ADDR="+listener.Addr().String(), "TMPDIR="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() })
	if tcp, ok := listener.(*net.TCPListener); ok {
		tcp.SetDeadline(time.Now().Add(5 * time.Second))
	}
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("grandchild did not connect: %v\n%s", err, &out)
	}
	defer conn.Close()
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatalf("interrupted parent exited zero:\n%s", &out)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil {
		t.Fatalf("isolated case outlived its interrupted parent: %v", err)
	}
}

func TestIsolatedBlockProcess(t *testing.T) {
	if os.Getenv("AGENT_TOOLS_TEST_BLOCK_CHILD") == "" {
		t.Skip("run by TestIsolatedTimeout")
	}
	conn, err := net.Dial("tcp", os.Getenv("AGENT_TOOLS_TEST_BLOCK_ADDR"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.Copy(io.Discard, conn)
}

func TestIsolatedCaseFixture(t *testing.T) {
	action := os.Getenv("AGENT_TOOLS_TEST_FIXTURE")
	if action == "" || strings.HasPrefix(action, "top") {
		t.Skip("run by TestIsolatedCases")
	}
	pidFile := os.Getenv("AGENT_TOOLS_TEST_PID_FILE")
	tmp := Temp(t)
	if action == "longsub" {
		tmp = SocketTemp(t)
	}
	if action == "hang" && Enabled() {
		tmp = t.TempDir()
	}
	if action == "argv0" && os.Getenv(child) == "" && tmp != "" {
		os.Args[0] = filepath.Join(t.TempDir(), "missing-executable")
	}
	if os.Getenv(child) == "" {
		if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d:%t", os.Getpid(), tmp != "")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if action == "nested" {
		Run(t, "a.b", tmp, func(t *testing.T) {
			for _, name := range []string{"keep", "other"} {
				t.Run(name, func(t *testing.T) {
					if err := os.WriteFile(filepath.Join(filepath.Dir(pidFile), name), nil, 0o600); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
		return
	}
	if action == "names" {
		for _, name := range []string{"a.b", "axb"} {
			Run(t, name, tmp, func(t *testing.T) {
				f, err := os.OpenFile(filepath.Join(filepath.Dir(pidFile), name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				f.Close()
			})
		}
		return
	}
	Run(t, "case", tmp, func(t *testing.T) {
		switch action {
		case "pass":
			pid, err := os.ReadFile(pidFile)
			if err != nil || (strings.Split(string(pid), ":")[0] == fmt.Sprint(os.Getpid())) != strings.HasSuffix(string(pid), ":false") {
				t.Fatalf("case shared its parent process: %s, %v", pid, err)
			}
		case "fail":
			t.Fatal("expected child failure")
		case "skip":
			t.Skip("expected child skip")
		case "short":
			if !testing.Short() {
				t.Fatal("short mode was not preserved")
			}
		case "longsub":
			pid, err := os.ReadFile(pidFile)
			if err != nil || string(pid) != fmt.Sprintf("%d:false", os.Getpid()) {
				t.Fatalf("long-root case did not run directly: %s, %v", pid, err)
			}
		case "longparallel":
			pid, err := os.ReadFile(pidFile)
			if err != nil || !strings.HasSuffix(string(pid), ":true") || strings.Split(string(pid), ":")[0] == fmt.Sprint(os.Getpid()) {
				t.Fatalf("long-root case did not run in a child: %s, %v", pid, err)
			}
		case "hang":
			cmd := exec.Command(os.Args[0], "-test.run=^TestIsolatedBlockProcess$")
			cmd.Env = append(os.Environ(), "AGENT_TOOLS_TEST_BLOCK_CHILD=1")
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
			<-make(chan struct{})
		}
	})
}
