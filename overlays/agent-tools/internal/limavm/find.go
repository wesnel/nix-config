package limavm

import (
	"errors"
	"strings"
)

// ErrExited is ProcessArgs's error for a process that has exited, whether
// or not its parent has reaped it.
var ErrExited = errors.New("the process has exited")

// Proc is a process of the process table: its ID and command name, as
// kern.proc.all gives it, at most 16 bytes.
type Proc struct {
	PID  int
	Comm string
}

// Process is one process's executable and argv, as kern.procargs2 gives
// them.
type Process struct {
	PID  int
	Exe  string
	Argv []string
}

// FindQEMU returns the processes among procs that are a store QEMU whose
// arguments name the instance directory: the managed VM's QEMU, found
// from the process table rather than qemu.pid, which QEMU writes and the
// guest's QEMU could rewrite. It reads the arguments only of processes
// named qemu-system-*, passes over one that has exited (ErrExited), a
// zombie included, and fails on any other read error, which says nothing
// of whether the VM attests. Attestation requires exactly one.
func FindQEMU(g *GuardConfig, procs []Proc, args func(pid int) (string, []string, error)) ([]Process, error) {
	var found []Process
	dir := g.InstanceDir() + "/"
	for _, p := range procs {
		if !strings.HasPrefix(p.Comm, "qemu-system-") {
			continue
		}
		exe, argv, err := args(p.PID)
		if errors.Is(err, ErrExited) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !storeQEMU.MatchString(exe) {
			continue
		}
		for _, a := range argv {
			if strings.Contains(a, dir) {
				found = append(found, Process{PID: p.PID, Exe: exe, Argv: argv})
				break
			}
		}
	}
	return found, nil
}
