package review

import (
	"os"
	"strconv"
)

// openedPath is the path the kernel has for the file f has open, symlinks
// resolved: its link under /proc/self/fd. A file removed since it was
// opened reads as "PATH (deleted)", which is in the same directory as
// PATH, so the checks on it hold as they would for PATH.
func openedPath(f *os.File) (string, error) {
	return os.Readlink("/proc/self/fd/" + strconv.FormatUint(uint64(f.Fd()), 10))
}
