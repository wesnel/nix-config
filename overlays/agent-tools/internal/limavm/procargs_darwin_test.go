package limavm

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// zombieQEMU starts a copy of /bin/sleep named qemu-system-test that
// exits at once, and returns its PID once it has exited, left unreaped
// until the test ends.
func zombieQEMU(t *testing.T) int {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "qemu-system-test")
	src, err := os.Open("/bin/sleep")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(exe, os.O_CREATE|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Wait() })
	pid := cmd.Process.Pid
	// Wait for the exit, which leaves the process a zombie, without
	// reaping it: kqueue's NOTE_EXIT, or ESRCH if it has exited already.
	kq, err := unix.Kqueue()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(kq)
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}
	events := make([]unix.Kevent_t, 1)
	if _, err := unix.Kevent(kq, []unix.Kevent_t{change}, events, nil); err != nil && !errors.Is(err, unix.ESRCH) {
		t.Fatal(err)
	}
	return pid
}

func TestFindQEMUZombie(t *testing.T) {
	pid := zombieQEMU(t)
	all, err := UserProcesses()
	if err != nil {
		t.Fatal(err)
	}
	// Only the zombie: the user's other QEMU, if any, are not the test's.
	i := slices.IndexFunc(all, func(p Proc) bool { return p.PID == pid })
	if i < 0 || all[i].Comm != "qemu-system-test" {
		t.Fatalf("the process table lacks the zombie qemu-system-test %d", pid)
	}
	found, err := FindQEMU(probeConfig(t), all[i:i+1], ProcessArgs)
	if err != nil || len(found) != 0 {
		t.Fatalf("found %+v, %v; want the exited, unreaped QEMU passed over", found, err)
	}
}

func TestUserProcesses(t *testing.T) {
	procs, err := UserProcesses()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(procs, func(p Proc) bool { return p.PID == os.Getpid() })
	if i < 0 || !slices.ContainsFunc(procs, func(p Proc) bool { return p.PID == os.Getppid() }) {
		t.Fatalf("the user's processes lack the test's %d or its parent %d", os.Getpid(), os.Getppid())
	}
	// The command name is the executable's, cut to 16 bytes.
	exe, _ := os.Executable()
	if want := filepath.Base(exe); !strings.HasPrefix(want, procs[i].Comm) || len(procs[i].Comm) < min(len(want), 16) {
		t.Fatalf("the test's command name is %q; want %q cut to 16 bytes", procs[i].Comm, want)
	}
}

func TestProcessArgs(t *testing.T) {
	exe, argv, err := ProcessArgs(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if want, _ := os.Executable(); exe != want {
		t.Errorf("executable %q; want %q", exe, want)
	}
	if !slices.Equal(argv, os.Args) {
		t.Errorf("argv %q; want %q", argv, os.Args)
	}
}
