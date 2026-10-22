package diagram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
)

// errTooBig is Blob's error for a file over MaxBlob.
var errTooBig = errors.New("too big")

// GitRepo is the repository at a directory, read through git's objects
// alone: nothing in it runs.
type GitRepo string

// Blob is path's content at rev.
func (g GitRepo) Blob(rev, path string) ([]byte, error) {
	return TimedRepo{Dir: string(g), Ctx: context.Background()}.Blob(rev, path)
}

// TimedRepo is GitRepo with each git call stopped when Ctx is done.
type TimedRepo struct {
	Dir string
	Ctx context.Context
}

func (g TimedRepo) git(args ...string) ([]byte, error) {
	return git.BytesContext(g.Ctx, append([]string{"-C", g.Dir}, args...)...)
}

// Blob is path's content at rev, refused past MaxBlob before it is read.
func (g TimedRepo) Blob(rev, path string) ([]byte, error) {
	size, err := g.git("cat-file", "-s", rev+":"+path)
	if err != nil {
		return nil, err
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(size))); err != nil || n > MaxBlob {
		return nil, fmt.Errorf("%s at %s is %s bytes, more than %d: %w", path, rev, strings.TrimSpace(string(size)), MaxBlob,
			errTooBig)
	}
	return g.git("cat-file", "blob", rev+":"+path)
}
