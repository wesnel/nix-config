package guard

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// physical is the absolute path as the file system walks it: each link
// followed where it stands, so a .. after a link climbs from where the
// link leads, not from the directory holding the link. Names that do not
// exist are kept as written. A relative path is only cleaned.
func physical(path string) string {
	if !filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	resolved := "/"
	todo := strings.Split(path, "/")
	for links := 0; len(todo) > 0; {
		c := todo[0]
		todo = todo[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}
		next := filepath.Join(resolved, c)
		info, err := os.Lstat(next) //nolint:gosec // the guard looks at a path it judges, to tell where it leads, and opens nothing
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			resolved = next
			continue
		}
		target, err := os.Readlink(next)
		if links++; err != nil || links > 255 {
			return filepath.Clean(path)
		}
		if filepath.IsAbs(target) {
			resolved = "/"
		}
		todo = append(strings.Split(target, "/"), todo...)
	}
	return resolved
}

// Reaches reports whether opening path, absolute, would reach target, or
// with under anything inside target, judged by what the file system opens
// rather than by spelling:
//   - where path or one of its parents exists, by file identity
//     (os.SameFile), after following its links, so a case or Unicode
//     variant the volume folds, a link and a hard link all count;
//   - where path names something that does not exist yet, by its parent's
//     identity and its name: in target's own directory, a name differing
//     from target's only in ASCII case reaches it, and so does any name
//     holding a character outside ASCII. APFS folds case and Unicode
//     (`ſ` is `s`, `ﬁ` is `fi`, a decomposed accent is the composed one),
//     and the guard keeps no tables of either, so such names are refused
//     in the few directories that hold a credential or a protected file.
//
// Reaches judges a path a command or tool is about to create or write.
// For a path that is only read, reaches(path, target, under, false) leaves
// a name outside ASCII to its ASCII comparison: a file that does not exist
// yet holds nothing to read, and every word of a command run from $HOME
// is judged as a path there.
func Reaches(path, target string, under bool) bool {
	return reaches(path, target, under, true)
}

// reaches is Reaches, with creating false for a path that is only read.
func reaches(path, target string, under, creating bool) bool {
	path, target = physical(path), physical(filepath.Clean(target))
	targetInfo, err := os.Stat(target) //nolint:gosec // the guard looks at a path it judges, to tell where it leads, and opens nothing
	exists := err == nil
	if exists {
		for q := path; ; q = filepath.Dir(q) {
			if info, err := os.Stat(q); err == nil && os.SameFile(info, targetInfo) {
				return q == path || under
			}
			if q == filepath.Dir(q) {
				break
			}
		}
		if under && targetInfo.IsDir() && hardLinkUnder(path, target) {
			return true
		}
	}
	parent, name, rest := missing(path)
	if name == "" {
		return false
	}
	parentInfo, err := os.Stat(parent)
	targetDir, err2 := os.Stat(filepath.Dir(target)) //nolint:gosec // as above
	if err != nil || err2 != nil || !os.SameFile(parentInfo, targetDir) {
		return false
	}
	if creating && !ascii(name) || strings.EqualFold(name, filepath.Base(target)) {
		return rest == "" || under
	}
	return false
}

// reachesPrefix reports whether path, absolute, reaches a file in dir
// whose name starts with prefix, or anything inside one, as reaches judges
// it with creating. A name ending in .pub is a public key, and is left out.
func reachesPrefix(path, dir, prefix string, creating bool) bool {
	path = physical(path)
	if _, err := os.Stat(path); err == nil {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if n := e.Name(); strings.HasPrefix(n, prefix) && !strings.HasSuffix(n, ".pub") &&
				reaches(path, filepath.Join(dir, n), true, creating) {
				return true
			}
		}
		return false
	}
	parent, name, _ := missing(path)
	if name == "" || strings.HasSuffix(name, ".pub") {
		return false
	}
	parentInfo, err := os.Stat(parent)
	dirInfo, err2 := os.Stat(dir)
	if err != nil || err2 != nil || !os.SameFile(parentInfo, dirInfo) {
		return false
	}
	return creating && !ascii(name) || strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix))
}

// missing splits path, absolute and physical, at its deepest existing
// parent: that parent, the first name below it that does not exist, and
// what follows that name. name is "" when path exists.
func missing(path string) (parent, name, rest string) {
	for q := path; ; q = filepath.Dir(q) {
		if _, err := os.Stat(q); err == nil {
			if q == path {
				return q, "", ""
			}
			below := strings.TrimPrefix(strings.TrimPrefix(path, q), "/")
			name, rest, _ = strings.Cut(below, "/")
			return q, name, rest
		}
		if q == filepath.Dir(q) {
			return "", "", ""
		}
	}
}

// hardLinkUnder reports whether path is a regular file with more than one
// name, one of them inside dir. It reads at most 10000 entries of dir.
func hardLinkUnder(path, dir string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || uint64(st.Nlink) < 2 {
		return false
	}
	found, seen := false, 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error { //nolint:gosec // a directory the guard protects, walked only to stat each name
		if seen++; err != nil || seen > 10000 {
			return fs.SkipAll
		}
		if d.Type().IsRegular() {
			if other, err := os.Stat(p); err == nil && os.SameFile(info, other) {
				found = true
				return fs.SkipAll
			}
		}
		return nil
	})
	return found
}

// ascii reports whether s is ASCII alone.
func ascii(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
