package record

import (
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// NewestRange returns the range record of owner/repo's pull request pr
// that was reviewed last, leaving out the one at except, with its path.
// It returns nil where there is none. A record of the pull request that
// fails its structure check fails NewestRange with a *DriftError, since
// when it was reviewed cannot be read.
func NewestRange(owner, repo string, pr int, except string) (*Range, string, error) {
	return newestRange(Dir(), owner, repo, pr, except)
}

func newestRange(dir, owner, repo string, pr int, except string) (*Range, string, error) {
	listed, err := prRanges(dir, owner, repo, pr)
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
			return nil, "", l.Err
		}
		if l.Err != nil {
			continue
		}
		if newest == nil || reviewed(l.Range).After(reviewed(newest)) {
			newest, newestPath = l.Range, l.Path
		}
	}
	return newest, newestPath, nil
}

// PRRanges lists the range records of owner/repo's pull request pr, as
// Ranges does: by REPO and PR where a record reads, and by those lines of
// its text where it does not, so a record of another repository's or pull
// request's is passed over either way.
func PRRanges(owner, repo string, pr int) ([]Listed, error) {
	return prRanges(Dir(), owner, repo, pr)
}

func prRanges(dir, owner, repo string, pr int) ([]Listed, error) {
	if err := checkRepo(owner, repo); err != nil {
		return nil, err
	}
	listed, err := ranges(dir, owner, repo, "")
	if err != nil {
		return nil, err
	}
	slug := owner + "/" + repo
	var of []Listed
	for _, l := range listed {
		if l.Err == nil && (l.Range.PR != pr || !strings.EqualFold(l.Range.Repo, slug)) {
			continue
		}
		if l.Err != nil {
			text, _ := os.ReadFile(l.Path)
			lower := strings.ToLower(string(text))
			if !strings.Contains(lower, "\n:pr: "+strconv.Itoa(pr)+"\n") || !strings.Contains(lower, "\n:repo: "+strings.ToLower(slug)+"\n") {
				continue
			}
		}
		of = append(of, l)
	}
	return of, nil
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

// Ranges lists the range records whose file name has base as its BASE, or
// every one where base is "", in file name order: those under ranges/, of
// any repository, and, where owner and repo are a GitHub name rather than
// "", those under owner/repo, but for one whose file name a
// record under ranges/ has, which wins. A record that cannot be read is
// listed with its Err, so each caller decides whether to pass over it.
func Ranges(owner, repo, base string) ([]Listed, error) {
	return ranges(Dir(), owner, repo, base)
}

func ranges(dir, owner, repo, base string) ([]Listed, error) {
	dirs := []string{filepath.Join(dir, "ranges")}
	if checkRepo(owner, repo) == nil {
		dirs = append(dirs, filepath.Join(dir, owner, repo))
	}
	byName := map[string]Listed{}
	for _, in := range dirs {
		entries, err := os.ReadDir(in)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			name := e.Name()
			if _, kept := byName[name]; kept || e.IsDir() || !strings.HasSuffix(name, ".org") ||
				strings.HasPrefix(name, "pr-") || strings.HasPrefix(name, ".") {
				continue
			}
			b, h, ok := strings.Cut(strings.TrimSuffix(name, ".org"), "..")
			if !ok || (base != "" && b != base) {
				continue
			}
			path := filepath.Join(in, name)
			rec, err := ReadRange(path)
			byName[name] = Listed{Path: path, Base: b, Head: h, Range: rec, Err: err}
		}
	}
	listed := make([]Listed, 0, len(byName))
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		listed = append(listed, byName[name])
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
