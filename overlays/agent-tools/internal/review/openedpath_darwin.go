package review

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// openedPath is the path the kernel has for the file f has open, symlinks
// resolved: fcntl's F_GETPATH.
func openedPath(f *os.File) (string, error) {
	// F_GETPATH fills at most MAXPATHLEN bytes.
	var buf [1024]byte
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0]))) //nolint:gosec // F_GETPATH fills a MAXPATHLEN buffer, the size of buf
	if errno != 0 {
		return "", &os.PathError{Op: "fcntl F_GETPATH", Path: f.Name(), Err: errno}
	}
	if n := bytes.IndexByte(buf[:], 0); n >= 0 {
		return string(buf[:n]), nil
	}
	return string(buf[:]), nil
}
