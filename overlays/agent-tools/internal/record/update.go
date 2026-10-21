package record

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// doc is a record kind: *Range or *PR.
type doc interface {
	Render() ([]byte, error)
	parse(data []byte) error
}

func (r *Range) parse(data []byte) error {
	got, err := ParseRange(data)
	if err == nil {
		*r = *got
	}
	return err
}

func (p *PR) parse(data []byte) error {
	got, err := ParsePR(data)
	if err == nil {
		*p = *got
	}
	return err
}

// Update changes the record at path, the only way any record is written:
// it holds the file's `.lock` with flock, parses the file (a new one starts
// from the zero record), refuses a file that fails the structure check with
// a *DriftError, applies apply, renders, checks that the rendering parses
// back, and renames a temporary file in the same directory into place. An
// error from apply, the renderer or that check writes nothing.
func Update[T any, P interface {
	*T
	doc
}](path string, apply func(P) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	var v T
	rec := P(&v)
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		if err := rec.parse(data); err != nil {
			return inFile(path, err)
		}
	}
	if err := apply(rec); err != nil {
		return err
	}
	out, err := rec.Render()
	if err != nil {
		return err
	}
	// What is written must pass the structure check the next write makes,
	// or that write would refuse the file.
	if err := P(new(T)).parse(out); err != nil {
		return fmt.Errorf("record: what would be written to %s does not read back, so nothing was written: %v", path, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ReadRange reads the range's record at path, which a writer replaces
// whole, so it needs no lock.
func ReadRange(path string) (*Range, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := ParseRange(data)
	return r, inFile(path, err)
}

// ReadPR reads the pull request's record at path, as ReadRange does.
func ReadPR(path string) (*PR, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	p, err := ParsePR(data)
	return p, inFile(path, err)
}

// inFile names path in a *DriftError.
func inFile(path string, err error) error {
	var drift *DriftError
	if errors.As(err, &drift) {
		drift.Path = path
	}
	return err
}
