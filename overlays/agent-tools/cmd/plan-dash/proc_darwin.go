package main

import "syscall"

// childAttr is how usage run starts its command: leading a process group
// of its own where group. macOS has no parent-death signal, so a command
// in a group of its own outlives a SIGKILL to the caller's group, and to
// this process.
func childAttr(group bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: group}
}
