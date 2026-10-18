package tty

import (
	"os"
	"syscall"
	"unsafe"
)

// IsTerminal reports whether f is a terminal: whether it answers a request
// for its terminal attributes, which a file, pipe or /dev/null does not.
func IsTerminal(f *os.File) bool {
	var t syscall.Termios
	//nolint:gosec // the ioctl takes the attributes by pointer
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
