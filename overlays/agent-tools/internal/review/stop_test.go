package review

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
)

// A review stopped while Codex forks leaves none of Codex's process group
// behind: a child forked as the kill lands is killed too.
func TestCodexStoppedWhileForking(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "fork")
	// Codex records its process group, then starts children without pause,
	// sending the cue to stop the review among them.
	started := filepath.Join(f.log, "started")
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
	var stdout, stderr strings.Builder
	if code := Main(ctx, []string{"--goal", ".git/goal.md", f.base + "..HEAD"}, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1\n%s", code, stderr.String())
	}
	data, _ := os.ReadFile(filepath.Join(f.log, "group"))
	pgid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("Codex recorded no process group: %q", data)
	}
	// Whatever survives is this test's to stop.
	t.Cleanup(func() { syscall.Kill(-pgid, syscall.SIGKILL) })
	if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
		left, _ := exec.Command("ps", "-o", "pid,stat,command", "-g", strconv.Itoa(pgid)).CombinedOutput()
		t.Errorf("Codex's process group %d outlived the review (%v):\n%s", pgid, err, left)
	}
	f.checkCleanedUp(t)
}
