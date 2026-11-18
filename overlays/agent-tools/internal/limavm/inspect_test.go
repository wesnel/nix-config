package limavm

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// inspection is an Inspector over fakes of limactl list and the process
// table, which a test cannot run against a VM, for an instance in a Lima
// home of the test's own.
type inspection struct {
	c       *Config
	in      Inspector
	listed  Listed
	listErr error
	procErr error
	argsErr error
	qemu    []string // the QEMU's argv, or none running
}

func newInspection(t *testing.T) *inspection {
	t.Helper()
	dir := t.TempDir()
	template := filepath.Join(dir, "template.yaml")
	if err := os.WriteFile(template, []byte("plain: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ParseConfig(configJSON(t, func(m map[string]any) {
		m["limaHome"] = filepath.Join(dir, "lima")
		m["template"] = template
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.InstanceDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.InstanceDir(), "lima.yaml"), []byte("plain: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeDisk(t, &c.GuardConfig, qcow2Header(3, 0, 0))
	x := &inspection{c: c}
	x.listed.Status = "Running"
	x.in = Inspector{
		Config: c,
		List: func(context.Context) (Listed, error) {
			return x.listed, x.listErr
		},
		Processes: func() ([]Proc, error) {
			return []Proc{{PID: 1, Comm: "launchd"}, {PID: 42, Comm: "qemu-system-aarc"}}, x.procErr
		},
		ProcessArgs: func(pid int) (string, []string, error) {
			if x.argsErr != nil {
				return "", nil, x.argsErr
			}
			if x.qemu == nil {
				return "", nil, ErrExited
			}
			return x.qemu[0], x.qemu, nil
		},
	}
	boot := []string{"-m", "2048", "-netdev", "user,id=net0,net=192.168.5.0/24,dhcpstart=192.168.5.15,hostfwd=tcp:127.0.0.1:2222-:22",
		"-pidfile", filepath.Join(c.InstanceDir(), "qemu.pid")}
	launch, err := Guard(&c.GuardConfig, boot)
	if err != nil {
		t.Fatal(err)
	}
	x.qemu = launch.Argv[3:]
	return x
}

func TestInspect(t *testing.T) {
	decides := func(t *testing.T, x *inspection, want State, reason string) {
		t.Helper()
		a, err := x.in.Inspect(t.Context())
		if err != nil || a.State != want || !strings.Contains(a.Reason, reason) {
			t.Fatalf("Inspect = %+v, %v; want %s, a reason with %q", a, err, want, reason)
		}
	}
	fails := func(t *testing.T, x *inspection, want error) {
		t.Helper()
		a, err := x.in.Inspect(t.Context())
		if err == nil || (want != nil && !errors.Is(err, want)) {
			t.Fatalf("Inspect = %+v, %v; want the error %v, and no decision", a, err, want)
		}
	}

	t.Run("attests the guard's QEMU", func(t *testing.T) {
		decides(t, newInspection(t), StateAttested, "")
	})
	t.Run("an absent instance", func(t *testing.T) {
		x := newInspection(t)
		os.RemoveAll(x.c.InstanceDir())
		decides(t, x, StateAbsent, "")
	})
	t.Run("a stopped instance", func(t *testing.T) {
		x := newInspection(t)
		x.listed.Status = "Stopped"
		decides(t, x, StateStopped, "")
	})
	t.Run("no QEMU of the instance runs", func(t *testing.T) {
		x := newInspection(t)
		x.qemu = nil
		decides(t, x, StateUnattested, "no store QEMU")
	})
	t.Run("a QEMU the guard did not launch", func(t *testing.T) {
		x := newInspection(t)
		x.qemu = append([]string{probeQEMU, "-pidfile", filepath.Join(x.c.InstanceDir(), "qemu.pid")}, "-netdev", "user,id=net0")
		decides(t, x, StateMismatch, "")
	})
	t.Run("an override", func(t *testing.T) {
		x := newInspection(t)
		os.MkdirAll(filepath.Join(x.c.LimaHome, "_config"), 0o700)
		os.WriteFile(filepath.Join(x.c.LimaHome, "_config", "override.yaml"), nil, 0o600)
		decides(t, x, StateMismatch, "override.yaml")
	})
	t.Run("no lima.yaml", func(t *testing.T) {
		x := newInspection(t)
		os.Remove(filepath.Join(x.c.InstanceDir(), "lima.yaml"))
		decides(t, x, StateMismatch, "lima-dev delete")
	})
	t.Run("another lima.yaml", func(t *testing.T) {
		x := newInspection(t)
		os.WriteFile(filepath.Join(x.c.InstanceDir(), "lima.yaml"), []byte("plain: false\n"), 0o600)
		decides(t, x, StateMismatch, "lima-dev delete")
	})

	t.Run("fails when limactl list fails", func(t *testing.T) {
		x := newInspection(t)
		x.listErr = errors.New("limactl list: exit status 1")
		fails(t, x, x.listErr)
	})
	t.Run("fails when the process table cannot be read", func(t *testing.T) {
		x := newInspection(t)
		x.procErr = syscall.EPERM
		fails(t, x, syscall.EPERM)
	})
	t.Run("fails when a QEMU's arguments cannot be read", func(t *testing.T) {
		x := newInspection(t)
		x.argsErr = syscall.EPERM
		fails(t, x, syscall.EPERM)
	})
	t.Run("fails when the overrides cannot be looked for", func(t *testing.T) {
		x := newInspection(t)
		dir := filepath.Join(x.c.LimaHome, "_config")
		os.MkdirAll(dir, 0o700)
		os.Chmod(dir, 0)
		t.Cleanup(func() { os.Chmod(dir, 0o700) })
		fails(t, x, fs.ErrPermission)
	})
	t.Run("fails when lima.yaml cannot be read", func(t *testing.T) {
		x := newInspection(t)
		f := filepath.Join(x.c.InstanceDir(), "lima.yaml")
		os.Chmod(f, 0)
		t.Cleanup(func() { os.Chmod(f, 0o600) })
		fails(t, x, fs.ErrPermission)
	})
}
