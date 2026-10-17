package diagram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
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

// Changes are the paths changed from one commit to another, renames
// followed.
func (g GitRepo) Changes(from, to string) ([]string, map[string]string, error) {
	return TimedRepo{Dir: string(g), Ctx: context.Background()}.Changes(from, to)
}

// Hunks are the changes from one file at one commit to another at another,
// whitespace ignored.
func (g GitRepo) Hunks(from, fromPath, to, toPath string) ([]Hunk, error) {
	return TimedRepo{Dir: string(g), Ctx: context.Background()}.Hunks(from, fromPath, to, toPath)
}

// Root names the repository: see TimedRepo.Root.
func (g GitRepo) Root(rev string) (string, error) {
	return TimedRepo{Dir: string(g), Ctx: context.Background()}.Root(rev)
}

// TimedRepo is GitRepo with each git call stopped when Ctx is done.
type TimedRepo struct {
	Dir string
	Ctx context.Context
}

func (g TimedRepo) git(args ...string) ([]byte, error) {
	return git.BytesContext(g.Ctx, append([]string{"-C", g.Dir}, args...)...)
}

// Root names the repository rev is in, the same in every clone of it: the
// first, by ID, of rev's root commits.
func (g TimedRepo) Root(rev string) (string, error) {
	out, err := g.git("rev-list", "--max-parents=0", rev, "--")
	if err != nil {
		return "", err
	}
	roots := strings.Fields(string(out))
	if len(roots) == 0 {
		return "", fmt.Errorf("%s has no root commit", rev)
	}
	slices.Sort(roots)
	return roots[0], nil
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

// Changes are the paths changed from one commit to another, and the new
// path of each renamed.
func (g TimedRepo) Changes(from, to string) ([]string, map[string]string, error) {
	out, err := g.git("diff", "--no-ext-diff", "--no-textconv", "-M", "--name-status", "-z", from, to, "--")
	if err != nil {
		return nil, nil, err
	}
	var changed []string
	renamed := map[string]string{}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for i := 0; i < len(fields) && fields[i] != ""; {
		status := fields[i]
		switch {
		case (status[0] == 'R' || status[0] == 'C') && i+2 < len(fields):
			if status[0] == 'R' {
				renamed[fields[i+1]] = fields[i+2]
			}
			changed = append(changed, fields[i+1])
			i += 3
		case i+1 < len(fields):
			changed = append(changed, fields[i+1])
			i += 2
		default:
			i++
		}
	}
	return changed, renamed, nil
}

var hunkHeader = regexp.MustCompile(`(?m)^@@ -([0-9]+)(?:,([0-9]+))? \+([0-9]+)(?:,([0-9]+))? @@`)

// Hunks are the changes from fromPath at from to toPath at to, with no
// context and whitespace ignored, as git's diff of the two blobs finds
// them.
func (g TimedRepo) Hunks(from, fromPath, to, toPath string) ([]Hunk, error) {
	out, err := g.git("diff", "--no-ext-diff", "--no-textconv", "--no-color", "-U0", "-w", "--diff-algorithm=myers",
		from+":"+fromPath, to+":"+toPath)
	if err != nil {
		return nil, err
	}
	n := func(s string) int {
		if s == "" {
			return 1
		}
		v, _ := strconv.Atoi(s)
		return v
	}
	var hunks []Hunk
	for _, m := range hunkHeader.FindAllStringSubmatch(string(out), -1) {
		hunks = append(hunks, Hunk{OldStart: n(m[1]), OldLines: n(m[2]), NewStart: n(m[3]), NewLines: n(m[4])})
	}
	return hunks, nil
}
