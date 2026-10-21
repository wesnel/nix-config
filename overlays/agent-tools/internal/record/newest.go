package record

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// NewestRange returns the range record in owner/repo of pull request pr
// that was reviewed last, leaving out the one at except, with its path.
// It returns nil where there is none. A record of the pull request that
// fails its structure check fails NewestRange with a *DriftError, since
// when it was reviewed cannot be read; a record of another pull request is
// passed over.
func NewestRange(owner, repo string, pr int, except string) (*Range, string, error) {
	if err := checkRepo(owner, repo); err != nil {
		return nil, "", err
	}
	dir := filepath.Join(Dir(), owner, repo)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var newest *Range
	var newestPath string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".org") || strings.HasPrefix(name, "pr-") || strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		if path == except {
			continue
		}
		rec, err := ReadRange(path)
		var drift *DriftError
		if errors.As(err, &drift) {
			if data, _ := os.ReadFile(path); strings.Contains(string(data), "\n:PR: "+strconv.Itoa(pr)+"\n") {
				return nil, "", err
			}
		}
		if err != nil || rec.PR != pr {
			continue
		}
		if newest == nil || reviewed(rec).After(reviewed(newest)) {
			newest, newestPath = rec, path
		}
	}
	return newest, newestPath, nil
}

// reviewed is when r was last reviewed: its newest round's time, or its
// UPDATED where it has no round. Settling a finding rewrites UPDATED and
// adds no round, so it leaves this as it was.
func reviewed(r *Range) time.Time {
	var at time.Time
	for _, rd := range r.Rounds {
		if rd.At.After(at) {
			at = rd.At
		}
	}
	if at.IsZero() {
		return r.Updated
	}
	return at
}
