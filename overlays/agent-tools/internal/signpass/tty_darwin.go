package signpass

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&t))) //nolint:gosec // TIOCGETA fills a Termios, the type of t
	return errno == 0
}
