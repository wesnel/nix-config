package limavm

import (
	"errors"
	"fmt"
	"slices"
	"syscall"
	"testing"
)

func TestFindQEMU(t *testing.T) {
	g := probeConfig(t)
	ours := []string{probeQEMU, "-name", "lima-probe", "-pidfile", g.InstanceDir() + "/qemu.pid"}
	// A fake of the process table and kern.procargs2, which the test does
	// not own. comm is what kern.proc.all gives, at most 16 bytes.
	const comm = "qemu-system-aarc"
	table := map[int][]string{
		10: {"/bin/zsh"},
		11: ours,
		12: {probeQEMU, "-pidfile", g.InstanceDir() + "2/qemu.pid"},
		13: {"/opt/homebrew/bin/qemu-system-aarch64", "-pidfile", g.InstanceDir() + "/qemu.pid"},
	}
	readErr := map[int]error{20: ErrExited, 21: syscall.EPERM}
	args := func(pid int) (string, []string, error) {
		if err := readErr[pid]; err != nil {
			return "", nil, fmt.Errorf("kern.procargs2 of %d: %w", pid, err)
		}
		a, ok := table[pid]
		if !ok {
			t.Fatalf("read the arguments of %d, which is not a QEMU", pid)
		}
		return a[0], a, nil
	}
	procs := func(pids ...int) []Proc {
		var out []Proc
		for _, pid := range pids {
			c := comm
			if pid == 10 {
				c = "zsh"
			}
			out = append(out, Proc{PID: pid, Comm: c})
		}
		return out
	}

	t.Run("finds the store QEMU naming the instance directory", func(t *testing.T) {
		found, err := FindQEMU(g, procs(11, 12, 13), args)
		if err != nil || len(found) != 1 || found[0].PID != 11 || !slices.Equal(found[0].Argv, ours) {
			t.Fatalf("found %+v, %v", found, err)
		}
	})
	t.Run("reads only processes named as QEMU", func(t *testing.T) {
		// 10 and 99 are no QEMU: reading either fails the test.
		if _, err := FindQEMU(g, append(procs(10, 11), Proc{PID: 99, Comm: "launchd"}), args); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("finds every one", func(t *testing.T) {
		table[14] = ours
		defer delete(table, 14)
		if found, err := FindQEMU(g, procs(11, 14), args); err != nil || len(found) != 2 {
			t.Fatalf("found %+v, %v", found, err)
		}
	})
	t.Run("finds none", func(t *testing.T) {
		if found, err := FindQEMU(g, procs(12, 13), args); err != nil || len(found) != 0 {
			t.Fatalf("found %+v, %v", found, err)
		}
	})
	t.Run("passes over a QEMU that has exited", func(t *testing.T) {
		if found, err := FindQEMU(g, procs(20, 11), args); err != nil || len(found) != 1 {
			t.Fatalf("found %+v, %v", found, err)
		}
	})
	t.Run("fails on a QEMU it cannot read", func(t *testing.T) {
		if found, err := FindQEMU(g, procs(21, 11), args); !errors.Is(err, syscall.EPERM) {
			t.Fatalf("found %+v, err %v; want the read's error", found, err)
		}
	})
}
