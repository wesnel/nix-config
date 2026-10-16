package signpass

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

// openPTY opens a pseudo-terminal and returns its two ends; what the test
// writes to term is read from tty as if typed.
func openPTY(t *testing.T) (term, tty *os.File) {
	t.Helper()
	term, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal here (%v); run the test where /dev/ptmx opens", err)
	}
	t.Cleanup(func() { term.Close() })
	var name [128]byte
	for _, req := range []uintptr{syscall.TIOCPTYGRANT, syscall.TIOCPTYUNLK, syscall.TIOCPTYGNAME} {
		arg := uintptr(0)
		if req == syscall.TIOCPTYGNAME {
			arg = uintptr(unsafe.Pointer(&name[0]))
		}
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, term.Fd(), req, arg); errno != 0 {
			t.Fatalf("ioctl %#x: %v", req, errno)
		}
	}
	tty, err = os.OpenFile(string(name[:bytes.IndexByte(name[:], 0)]), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tty.Close() })
	return term, tty
}
