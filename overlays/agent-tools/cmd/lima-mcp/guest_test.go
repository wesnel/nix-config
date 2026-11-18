package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestGuestJobLinux runs the real systemd/sudo contract in a disposable
// Linux guest. The normal macOS test run cannot provide systemd or guest root.
func TestGuestJobLinux(t *testing.T) {
	if runtime.GOOS != "linux" || os.Getenv("LIMA_GUEST_JOB_TEST") != "1" {
		t.Skip("in the disposable Linux guest: LIMA_GUEST_JOB_TEST=1 go test ./cmd/lima-mcp -run TestGuestJobLinux -count=1")
	}
	if os.Getuid() == 0 {
		t.Fatal("run as the ordinary guest user with passwordless sudo, not as root")
	}
	if out, err := exec.Command("sudo", "-n", "true").CombinedOutput(); err != nil {
		t.Fatalf("guest passwordless sudo: %v: %s", err, out)
	}
	for i, action := range []string{"exit status and environment", "timeout", "cancel", "cancel before launch"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "state")
			id := fmt.Sprintf("test-%d-%d", os.Getpid(), i)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			command := func(args []string) *exec.Cmd {
				t.Helper()
				for i := range args {
					args[i] = strings.ReplaceAll(args[i], "/run/lima-mcp-jobs", state)
				}
				cmd := exec.CommandContext(ctx, args[0], args[1:]...)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "LIMA_JOB_TEST=literal $HOME 100% value")
				cmd.WaitDelay = time.Second
				return cmd
			}
			t.Cleanup(func() {
				cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
				defer done()
				exec.CommandContext(cleanup, "sudo", "-n", "systemctl", "stop", "lima-mcp-job-"+id+".service").Run()
				if _, err := os.Stat(state); err == nil {
					owner := strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
					if out, err := exec.CommandContext(cleanup, "sudo", "-n", "chown", "-R", owner, state).CombinedOutput(); err != nil {
						t.Errorf("return test state to TempDir cleanup: %v: %s", err, out)
					}
				}
			})
			if action == "exit status and environment" {
				out, err := command(guestJob(id, 10, `printf '%s' "$LIMA_JOB_TEST"; exit 7`)).CombinedOutput()
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 || string(out) != "literal $HOME 100% value" {
					t.Fatalf("guest exit/environment: %v: %q", err, out)
				}
				return
			}
			if action == "cancel before launch" {
				if out, err := command(guestKill(id)).CombinedOutput(); err != nil {
					t.Fatalf("pre-cancel: %v: %s", err, out)
				}
				command(guestJob(id, 10, "touch launched")).CombinedOutput()
				if _, err := os.Stat(filepath.Join(dir, "launched")); !os.IsNotExist(err) {
					t.Fatalf("cancelled job launched: %v", err)
				}
				return
			}
			seconds := 20
			if action == "timeout" {
				seconds = 2
			}
			cmd := command(guestJob(id, seconds, `printf 'USER:%s:%s\n' "$(id -u)" "$PWD"; sudo -n sh -c 'echo ROOT:$$; exec sleep 300'`))
			output, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(output)
			if !scanner.Scan() || scanner.Text() != fmt.Sprintf("USER:%d:%s", os.Getuid(), dir) {
				t.Fatalf("guest identity/workdir %q", scanner.Text())
			}
			if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "ROOT:") {
				t.Fatalf("root child readiness %q", scanner.Text())
			}
			pid, err := strconv.Atoi(strings.TrimPrefix(scanner.Text(), "ROOT:"))
			if err != nil {
				t.Fatal(err)
			}
			if action == "cancel" {
				if out, err := command(guestKill(id)).CombinedOutput(); err != nil {
					t.Fatalf("cancel: %v: %s", err, out)
				}
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("terminated job reported success")
			}
			if ctx.Err() != nil {
				t.Fatalf("host deadline ended the job: %v", ctx.Err())
			}
			status, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if err == nil && !strings.Contains(string(status), ") Z ") {
				t.Fatalf("root child survived %s: %s", action, status)
			}
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}
