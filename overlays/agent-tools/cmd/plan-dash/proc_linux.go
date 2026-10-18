package main

import "syscall"

// childAttr is how usage run starts its command: leading a process group
// of its own where group, and on this process's death sent SIGTERM, so
// that a SIGKILL to the caller does not leave it running. The signal
// reaches the command alone; what it runs in turn gets it only if the
// command passes it on.
func childAttr(group bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: group, Pdeathsig: syscall.SIGTERM}
}
