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
	listed, err := Ranges(owner, repo, "")
	if err != nil {
		return nil, "", err
	}
	var newest *Range
	var newestPath string
	for _, l := range listed {
		if l.Path == except {
			continue
		}
		var drift *DriftError
		if errors.As(l.Err, &drift) {
			if data, _ := os.ReadFile(l.Path); strings.Contains(string(data), "\n:PR: "+strconv.Itoa(pr)+"\n") {
				return nil, "", l.Err
			}
		}
		if l.Err != nil || l.Range.PR != pr {
			continue
		}
		if newest == nil || reviewed(l.Range).After(reviewed(newest)) {
			newest, newestPath = l.Range, l.Path
		}
	}
	return newest, newestPath, nil
}

// Listed is a range record found by Ranges: its path, BASE and HEAD from
// its file name, and the record, or Err where it cannot be read, such as a
// *DriftError for one that fails its structure check.
type Listed struct {
	Path       string
	Base, Head string
	Range      *Range
	Err        error
}

// Ranges lists the range records in owner/repo whose file name has base as
// its BASE, or every one where base is "", in file name order. A record
// that cannot be read is listed with its Err, so each caller decides
// whether to pass over it.
func Ranges(owner, repo, base string) ([]Listed, error) {
	if err := checkRepo(owner, repo); err != nil {
		return nil, err
	}
	dir := filepath.Join(Dir(), owner, repo)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var listed []Listed
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".org") || strings.HasPrefix(name, "pr-") || strings.HasPrefix(name, ".") {
			continue
		}
		b, h, ok := strings.Cut(strings.TrimSuffix(name, ".org"), "..")
		if !ok || (base != "" && b != base) {
			continue
		}
		path := filepath.Join(dir, name)
		rec, err := ReadRange(path)
		listed = append(listed, Listed{Path: path, Base: b, Head: h, Range: rec, Err: err})
	}
	return listed, nil
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
