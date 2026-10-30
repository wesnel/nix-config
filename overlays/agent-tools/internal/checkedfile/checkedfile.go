// Package checkedfile reads a file whose contents leave the host or the
// sandbox, such as a question sent to Codex or text pushed with a plan,
// checking where the file is on the path the kernel has for the file
// opened, so a link cannot lead the read elsewhere.
package checkedfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/guard"
)

// The reasons Read and Open refuse a file, wrapped with its path; each
// caller words its own refusal from them.
var (
	ErrLinked     = errors.New("has other hard links")
	ErrCredential = errors.New("is a credential")
	ErrOutside    = errors.New("is outside every root")
)

// Read is the contents of the file at path, relative to the process's
// directory or absolute, if Open takes it, it is not a credential
// (guard.IsCredential) by its name or by the opened path, and the
// opened path is in one of roots, each an absolute directory whose
// symlinks are resolved before the check. A root that is not absolute
// takes nothing, since an empty one would take every path.
func Read(path string, roots []string) ([]byte, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	f, real, err := Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if guard.IsCredential(abs) || guard.IsCredential(real) {
		return nil, fmt.Errorf("%s %w", path, ErrCredential)
	}
	within := func(root string) bool {
		if !filepath.IsAbs(root) {
			return false
		}
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
		return real == root || strings.HasPrefix(real, strings.TrimSuffix(root, "/")+"/")
	}
	if !slices.ContainsFunc(roots, within) {
		return nil, fmt.Errorf("%s %w", path, ErrOutside)
	}
	return io.ReadAll(f)
}

// Open opens path to read, if it is a regular file, and returns it with
// the path the kernel has for the file opened, symlinks resolved. A check
// made on that path holds for what is read from the file, which a link put
// in place after the open cannot change. A FIFO is refused without waiting
// for a writer. A file with more than one hard link is refused with
// ErrLinked: that path is only one of its names, any of which may be
// anywhere on its file system.
func Open(path string) (*os.File, string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	real, err := openedPath(f)
	if err == nil {
		var info os.FileInfo
		if info, err = f.Stat(); err == nil {
			err = regularOnly(path, info)
		}
	}
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, real, nil
}

// regularOnly is why Open refuses the file at path, or nil.
func regularOnly(path string, info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.Mode().IsRegular():
		return fmt.Errorf("%s is not a regular file", path)
	case !ok || st.Nlink > 1:
		return fmt.Errorf("%s %w", path, ErrLinked)
	}
	return nil
}
