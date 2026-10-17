package queue

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// OpenOut makes Out and Out/collection where they are missing and opens
// the collection's directory, refusing either where it is not a directory,
// a link included. Everything done there goes through the handle, so a
// directory swapped for a link afterwards is never followed.
func OpenOut(p Paths, collection string) (*os.Root, error) {
	dir := filepath.Join(p.Out(), collection)
	var info fs.FileInfo
	for _, d := range []string{p.Out(), dir} {
		var err error
		info, err = os.Lstat(d)
		if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(d, 0o700); err != nil {
				return nil, err
			}
			info, err = os.Lstat(d)
		}
		switch {
		case err != nil:
			return nil, err
		case !info.IsDir():
			return nil, fmt.Errorf("%s is not a directory", d)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	if opened, err := root.Stat("."); err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, fmt.Errorf("%s changed while it was opened", dir)
	}
	return root, nil
}

// ReadNames lists dir's entries, by name.
func ReadNames(dir *os.Root) ([]fs.DirEntry, error) {
	f, err := dir.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	slices.SortFunc(entries, func(a, b fs.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return entries, err
}

// ReadObject decodes into v a JSON object from a regular file in dir of at
// most limit bytes, never followed through a link; it reports whether it
// could.
func ReadObject(dir *os.Root, name string, limit int64, v any) bool {
	f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return false
	}
	data = bytes.TrimSpace(data)
	return len(data) > 0 && data[0] == '{' && json.Unmarshal(data, v) == nil
}

// WriteObject replaces name in dir with v as JSON, by renaming a new file
// over it.
func WriteObject(dir *os.Root, name string, v any) error {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return err
	}
	tmp := fmt.Sprintf(".%s.%d.%d", name, os.Getpid(), time.Now().UnixNano())
	f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer dir.Remove(tmp)
	_, err = f.Write(b.Bytes())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return dir.Rename(tmp, name)
}
