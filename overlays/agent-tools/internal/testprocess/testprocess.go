// Package testprocess runs independent test cases in separate processes when
// their code changes process-wide state.
package testprocess

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/sshmux"
)

const (
	child = "AGENT_TOOLS_TEST_ISOLATED_CHILD"
	// parentPipe marks a child given, as file descriptor 3, the read end of
	// a pipe whose write end only its parent holds.
	parentPipe = "AGENT_TOOLS_TEST_ISOLATED_PARENT_PIPE"
)

var slots = make(chan struct{}, 12)

// A child leads a process group of its own, so an interrupt of its parent's
// group never reaches it. Its parent writes nothing to the pipe, so a read
// returns end of file only once the parent has exited, however it ended;
// the child then kills its group: itself and its Git and SSH descendants.
func init() {
	if os.Getenv(parentPipe) == "" {
		return
	}
	// Commands the child runs inherit neither the mark nor the pipe.
	os.Unsetenv(parentPipe)
	syscall.CloseOnExec(3)
	parent := os.NewFile(3, "isolated test parent")
	go func() {
		if _, err := parent.Read(make([]byte, 1)); err == io.EOF {
			_ = syscall.Kill(0, syscall.SIGKILL)
		}
	}()
}

// Temp returns a parent-owned directory for isolated cases, or empty when
// they must run directly.
func Temp(t *testing.T) string {
	t.Helper()
	if !Enabled() {
		return ""
	}
	return t.TempDir()
}

// SocketTemp also requires enough room for an SSH control socket.
func SocketTemp(t *testing.T) string {
	t.Helper()
	dir := Temp(t)
	if dir == "" {
		return ""
	}
	if !sshmux.Fits(dir) {
		return ""
	}
	return dir
}

// Top runs a top-level test with no subtests in a child when isolation is
// enabled. Its caller returns when Top is false; otherwise it runs the test.
func Top(t *testing.T, tmp string) bool {
	t.Helper()
	if tmp == "" {
		return true
	}
	t.Parallel()
	runChild(t, tmp)
	return false
}

// Enabled reports whether cases can run in children without changing Go's
// filtering, coverage or other explicitly requested test modes.
func Enabled() bool {
	if os.Getenv(child) != "" || testing.CoverMode() != "" || testing.Short() {
		return false
	}
	for _, name := range []string{"test.run", "test.skip", "test.cpu", "test.bench", "test.fuzz"} {
		if f := flag.Lookup(name); f != nil && f.Value.String() != "" {
			return false
		}
	}
	if f := flag.Lookup("test.failfast"); f != nil && f.Value.String() == "true" {
		return false
	}
	if f := flag.Lookup("test.shuffle"); f != nil && f.Value.String() != "off" {
		return false
	}
	return true
}

// Run schedules a case in a child test process. tmp is a parent test's
// temporary directory. The parent must not change process-wide state before Run.
// Filtered and coverage runs execute the case directly.
func Run(t *testing.T, name, tmp string, f func(*testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if !Enabled() || tmp == "" {
			f(t)
			return
		}
		t.Parallel()
		runChild(t, tmp)
	})
}

func runChild(t *testing.T, tmp string) {
	t.Helper()
	ctx := t.Context()
	if deadline, ok := t.Deadline(); ok {
		reserve := min(time.Second, time.Until(deadline)/2)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-reserve))
		defer cancel()
	}
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		t.Fatalf("waiting for isolated case: %v", ctx.Err())
	}
	defer func() { <-slots }()
	parts := strings.Split(t.Name(), "/")
	for i, part := range parts {
		parts[i] = "^" + regexp.QuoteMeta(part) + "$"
	}
	args := []string{"-test.v", "-test.count=1", "-test.run=" + strings.Join(parts, "/")}
	if f := flag.Lookup("test.parallel"); f != nil {
		args = append(args, "-test.parallel="+f.Value.String())
	}
	if deadline, ok := t.Deadline(); ok {
		args = append(args, "-test.timeout="+time.Until(deadline).String())
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating isolated test executable: %v", err)
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = time.Second
	alive, held, err := os.Pipe()
	if err != nil {
		t.Fatalf("making isolated case's parent pipe: %v", err)
	}
	defer alive.Close()
	defer held.Close()
	cmd.ExtraFiles = []*os.File{alive}
	cmd.Env = append(os.Environ(), child+"=1", parentPipe+"=1", "TMPDIR="+tmp)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated case failed: %v\n%s", err, out)
	}
	passed := strings.Contains(string(out), "--- PASS: "+t.Name()+" (")
	skipped := strings.Contains(string(out), "--- SKIP: "+t.Name()+" (")
	if !passed && !skipped {
		t.Fatalf("isolated case did not run:\n%s", out)
	}
	if skipped {
		t.Skipf("isolated case skipped:\n%s", out)
	}
	if testing.Verbose() {
		t.Logf("isolated case output:\n%s", out)
	}
}
