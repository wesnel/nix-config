package plans

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The notes files are the files agent-plan writes in the notes repository
// outside the plans directory: each week's demo digest, DigestFile, and
// each year's brag file, BragFile. Only the host Config.NotesHost names
// writes them, and its sync alone sends them.

var (
	// digestName is a demo digest's file name in its year's directory
	// under digests/: MM-DD-demo.org, for the day it is named for.
	digestName = regexp.MustCompile(`^([0-9]{2})-([0-9]{2})-demo\.org$`)
	yearName   = regexp.MustCompile(`^[0-9]{4}$`)
	bragName   = regexp.MustCompile(`^[0-9]{4}\.org$`)
)

// notesDirs are the directories, relative to the notes repository, that
// hold the notes files.
var notesDirs = []string{"digests", "brag"}

// DigestFile is the demo digest of day, as a slash path relative to the
// notes repository: digests/YYYY/MM-DD-demo.org.
func DigestFile(day time.Time) string {
	return path.Join("digests", fmt.Sprintf("%04d", day.Year()), day.Format("01-02")+"-demo.org")
}

// DigestDay is the day, in loc, that the file named name in the directory
// digests/YEAR is the demo digest of; ok is false for any name DigestFile
// does not give, a day that does not exist among them.
func DigestDay(year int, name string, loc *time.Location) (day time.Time, ok bool) {
	m := digestName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	day, err := time.ParseInLocation("2006-01-02", fmt.Sprintf("%04d-%s-%s", year, m[1], m[2]), loc)
	return day, err == nil
}

// BragFile is year's brag file, as a slash path relative to the notes
// repository: brag/YYYY.org.
func BragFile(year int) string { return fmt.Sprintf("brag/%04d.org", year) }

// NotesFile reports whether rel, a slash path relative to the notes
// repository, is a notes file: a digest DigestFile names, or a brag file
// BragFile names.
func NotesFile(rel string) bool {
	parts := strings.Split(rel, "/")
	switch {
	case len(parts) == 3 && parts[0] == "digests" && yearName.MatchString(parts[1]):
		year, _ := strconv.Atoi(parts[1])
		_, ok := DigestDay(year, parts[2], time.UTC)
		return ok
	case len(parts) == 2 && parts[0] == "brag":
		return bragName.MatchString(parts[1])
	}
	return false
}

// ownsNotes reports whether this host is the one that writes the notes
// files.
func (c Config) ownsNotes() bool { return c.NotesHost != "" && c.NotesHost == c.Host }

// notesFiles are the notes files in the repository's working tree, as
// slash paths relative to it: regular files in directories that are not
// links.
func (c Config) notesFiles() ([]string, error) {
	var out []string
	for _, dir := range notesDirs {
		if info, err := os.Lstat(filepath.Join(c.repo(), dir)); err != nil || !info.IsDir() {
			continue
		}
		// A digest is a level deeper than a brag file, in its year's
		// directory.
		if err := c.walkNotes(dir, 1, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c Config) walkNotes(rel string, depth int, out *[]string) error {
	entries, err := os.ReadDir(filepath.Join(c.repo(), filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := rel + "/" + e.Name()
		switch {
		case e.Type().IsRegular() && NotesFile(name):
			*out = append(*out, name)
		case e.IsDir() && depth > 0 && yearName.MatchString(e.Name()):
			if err := c.walkNotes(name, depth-1, out); err != nil {
				return err
			}
		}
	}
	return nil
}
