package sandboxrun

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A run stopped while a process the backend started forks leaves none of
// the backend's process group behind: a child forked as the kill lands is
// killed too.
func TestStoppedWhileForking(t *testing.T) {
	backend, log := setUp(t, "fork")
	// The backend ignores SIGTERM and starts a process that forks children
	// without pause, sending the cue to stop the run among them.
	started := filepath.Join(log, "started")
	if err := syscall.Mkfifo(started, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if r, err := os.Open(started); err == nil {
			bufio.NewReader(r).ReadString('\n')
			r.Close()
		}
		cancel()
	}()
	s := spec(t, backend)
	s.Grace = 50 * time.Millisecond
	if _, err := Run(ctx, s); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(log, "pid"))
	pgid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("the backend recorded no process group: %q", data)
	}
	// Whatever survives is this test's to stop.
	t.Cleanup(func() { syscall.Kill(-pgid, syscall.SIGKILL) })
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		left, _ := exec.Command("ps", "-o", "pid,stat,command", "-g", strconv.Itoa(pgid)).CombinedOutput()
		t.Errorf("the backend's process group %d outlived the run (%v):\n%s", pgid, err, left)
	}
}
