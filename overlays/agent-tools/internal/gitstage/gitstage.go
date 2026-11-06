// Package gitstage installs what a confined Git child staged, in a private
// index and object directory, into a linked worktree's real index.
//
// The child is not trusted with the repository: it reads the worktree,
// sees none of the repository's objects, and writes only its private
// directory. Install takes from the child's index only the entries of the
// paths the caller asked for, keeps every other entry as the real index
// holds it under index.lock, checks each blob the child wrote against its
// name and copies it into the shared object store without replacing
// anything there. It reads and writes the index itself rather than run
// git update-index on a copy, so that no Git process runs with the
// server's authority on the repository's configuration. A repository whose
// Git metadata passes through a symlink, a main checkout, a split, sparse or
// SHA-256 index are refused rather than handled.
package gitstage

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // Git's object names, not a security hash.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Layout is a linked worktree with its own Git directory and the
// repository's common one, each a physical path.
type Layout struct{ Worktree, GitDir, CommonDir string }

const metadataLimit = 4096

// Resolve finds a linked worktree's Git directories from its .git file,
// which the worktree's own commands can rewrite, and accepts them only when
// Git's registration agrees: the Git directory sits under the common
// directory's worktrees and names this worktree's .git file back.
func Resolve(worktree string) (Layout, error) {
	if !filepath.IsAbs(worktree) {
		return Layout{}, errors.New("the worktree must be an absolute path")
	}
	worktree = filepath.Clean(worktree)
	if err := physical(worktree); err != nil {
		return Layout{}, err
	}
	dotGit := filepath.Join(worktree, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil {
		return Layout{}, fmt.Errorf("%s is not a Git worktree", worktree)
	}
	if info.IsDir() {
		return Layout{}, fmt.Errorf("%s is a main checkout; staging is supported only in a linked worktree", worktree)
	}
	pointer, err := readSmall(dotGit)
	if err != nil {
		return Layout{}, err
	}
	gitDir, ok := strings.CutPrefix(strings.TrimSuffix(pointer, "\n"), "gitdir: ")
	if !ok || gitDir == "" {
		return Layout{}, fmt.Errorf("%s does not name a Git directory", dotGit)
	}
	gitDir = against(worktree, gitDir)
	if err := physical(gitDir); err != nil {
		return Layout{}, err
	}
	common, err := readSmall(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return Layout{}, err
	}
	commonDir := against(gitDir, strings.TrimSuffix(common, "\n"))
	if err := physical(commonDir); err != nil {
		return Layout{}, err
	}
	back, err := readSmall(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return Layout{}, err
	}
	if filepath.Dir(gitDir) != filepath.Join(commonDir, "worktrees") ||
		against(gitDir, strings.TrimSuffix(back, "\n")) != dotGit {
		return Layout{}, fmt.Errorf("%s is not registered as the Git directory of %s", gitDir, worktree)
	}
	if info, err := os.Lstat(filepath.Join(commonDir, "objects")); err != nil || !info.IsDir() {
		return Layout{}, fmt.Errorf("%s has no object directory, or it is a symlink", commonDir)
	}
	return Layout{Worktree: worktree, GitDir: gitDir, CommonDir: commonDir}, nil
}

// physical refuses a path whose symlinks resolve elsewhere, so that the
// path checked is the path written.
func physical(path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("%s cannot be resolved: %w", path, err)
	}
	if real != path {
		return fmt.Errorf("%s passes through a symlink to %s, which is unsupported", path, real)
	}
	return nil
}

// against is path, relative to dir unless absolute, cleaned.
func against(dir, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path)
}

// readSmall reads a regular metadata file, never through a symlink.
func readSmall(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > metadataLimit {
		return "", fmt.Errorf("%s is not a regular Git metadata file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Lock holds a worktree's index.lock from Lock until Install or Release.
type Lock struct {
	layout Layout
	root   *os.Root
	file   *os.File
	held   os.FileInfo
	index  []byte
	done   bool
}

// Lock creates the worktree's index.lock, as Git does before it writes the
// index. A lock that exists belongs to another process and refuses.
func (l Layout) Lock() (*Lock, error) {
	root, err := os.OpenRoot(l.GitDir)
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile("index.lock", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		root.Close()
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%s is held by another Git process; try again once it finishes", filepath.Join(l.GitDir, "index.lock"))
		}
		return nil, err
	}
	held, err := file.Stat()
	if err != nil {
		file.Close()
		_ = root.Remove("index.lock")
		root.Close()
		return nil, err
	}
	return &Lock{layout: l, root: root, file: file, held: held}, nil
}

// ours reports whether index.lock is still the file this Lock created.
func (k *Lock) ours() bool {
	info, err := k.root.Lstat("index.lock")
	return err == nil && os.SameFile(info, k.held)
}

// Release removes the lock unless Install already replaced the index with
// it, and never a lock another process made in its place.
func (k *Lock) Release() {
	if k == nil || k.done {
		return
	}
	k.done = true
	k.file.Close()
	if k.ours() {
		_ = k.root.Remove("index.lock")
	}
	k.root.Close()
}

const indexLimit = 256 << 20

// Snapshot reads the real index under the lock and gives the child a copy
// of it, private/index, and an empty private/objects.
func (k *Lock) Snapshot(private string) error {
	info, err := k.root.Lstat("index")
	if err != nil || !info.Mode().IsRegular() || info.Size() > indexLimit {
		return fmt.Errorf("%s has no regular index file", k.layout.GitDir)
	}
	data, err := k.root.ReadFile("index")
	if err != nil {
		return err
	}
	if _, err := parseIndex(data); err != nil {
		return err
	}
	k.index = data
	if err := os.WriteFile(filepath.Join(private, "index"), data, 0o600); err != nil { //nolint:gosec // private is the caller's own temporary directory.
		return err
	}
	return os.Mkdir(filepath.Join(private, "objects"), 0o700)
}

// Install writes the index: the real one's entries but for paths, which
// come from the child's private index, or are removed where it has none.
// Every new blob is checked and promoted before the index is replaced.
func (k *Lock) Install(private string, paths []string) error {
	if k.done || k.index == nil {
		return errors.New("the index lock is not held, or no snapshot was taken")
	}
	real, err := parseIndex(k.index)
	if err != nil {
		return err
	}
	privateRoot, err := os.OpenRoot(private)
	if err != nil {
		return err
	}
	defer privateRoot.Close()
	info, err := privateRoot.Lstat("index")
	if err != nil || !info.Mode().IsRegular() || info.Size() > indexLimit {
		return errors.New("the private index is not a regular file")
	}
	data, err := privateRoot.ReadFile("index")
	if err != nil {
		return err
	}
	child, err := parseIndex(data)
	if err != nil {
		return fmt.Errorf("private index: %w", err)
	}
	requested := map[string]bool{}
	for _, p := range paths {
		if err := literal(p); err != nil {
			return err
		}
		requested[p] = true
	}
	var entries []entry
	for _, e := range real.entries {
		if !requested[e.name] {
			entries = append(entries, e)
		}
	}
	var added []entry
	for _, e := range child.entries {
		if !requested[e.name] {
			continue
		}
		switch {
		case e.stage() != 0:
			return fmt.Errorf("the child left %s at merge stage %d", e.name, e.stage())
		case e.mode != 0o100644 && e.mode != 0o100755:
			return fmt.Errorf("the child staged %s with mode %o, not a regular file's", e.name, e.mode)
		case e.flags&(extendedFlag|assumeValidFlag) != 0:
			// Git acts on these: assume-valid hides the path's later changes.
			return fmt.Errorf("the child staged %s with assume-valid or extended flags", e.name)
		}
		added = append(added, e)
	}
	seen := map[string]bool{}
	for _, e := range added {
		if seen[e.name] {
			return fmt.Errorf("the child staged %s twice", e.name)
		}
		seen[e.name] = true
	}
	entries = append(entries, added...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].name != entries[j].name {
			return entries[i].name < entries[j].name
		}
		return entries[i].stage() < entries[j].stage()
	})
	names := map[string]bool{}
	for i, e := range entries {
		if i > 0 {
			a := entries[i-1]
			if a.name == e.name && (a.stage() == 0 || a.stage() == e.stage()) {
				return fmt.Errorf("%s would have more than one entry at a stage", a.name)
			}
		}
		names[e.name] = true
	}
	// Every leading directory, not only the neighbour: a-b sorts between
	// a and a/b.
	for name := range names {
		for i := strings.IndexByte(name, '/'); i >= 0; i = nextSlash(name, i) {
			if names[name[:i]] {
				return fmt.Errorf("%s would be staged as both a file and a directory", name[:i])
			}
		}
	}
	objects, err := privateRoot.OpenRoot("objects")
	if err != nil {
		return err
	}
	defer objects.Close()
	shared, err := os.OpenRoot(filepath.Join(k.layout.CommonDir, "objects"))
	if err != nil {
		return err
	}
	defer shared.Close()
	var promote []loose
	// Every entry's blob, unchanged content included: nothing else the
	// repository names proves an object is a blob.
	for _, e := range added {
		o, err := verifiedBlob(objects, e.oid)
		if err != nil {
			return err
		}
		promote = append(promote, o)
	}
	for _, o := range promote {
		if err := o.promote(shared); err != nil {
			return err
		}
	}
	return k.replace(writeIndex(real.version, entries))
}

// nextSlash is the index of the first / in name after i, or -1.
func nextSlash(name string, i int) int {
	j := strings.IndexByte(name[i+1:], '/')
	if j < 0 {
		return -1
	}
	return i + 1 + j
}

// replace writes data to index.lock and renames it over the index.
func (k *Lock) replace(data []byte) error {
	if !k.ours() {
		return errors.New("index.lock was replaced by another process")
	}
	if _, err := k.file.Write(data); err != nil {
		return err
	}
	if err := k.file.Sync(); err != nil {
		return err
	}
	if err := k.file.Close(); err != nil {
		return err
	}
	if !k.ours() {
		return errors.New("index.lock was replaced by another process")
	}
	if err := k.root.Rename("index.lock", "index"); err != nil {
		return err
	}
	k.done = true
	k.root.Close()
	return nil
}

// literal refuses a path that is not a plain worktree-relative file name.
func literal(p string) error {
	if p == "" || p == "." || filepath.IsAbs(p) || filepath.Clean(p) != p || p == ".." ||
		strings.HasPrefix(p, "../") || strings.ContainsRune(p, 0) {
		return fmt.Errorf("%q is not a literal worktree-relative path", p)
	}
	for _, part := range strings.Split(p, "/") {
		if strings.EqualFold(part, ".git") {
			return fmt.Errorf("%q names Git metadata", p)
		}
	}
	return nil
}

const blobLimit = 64 << 20

// loose is a verified blob not yet in the shared store: its name and the
// compressed bytes to write there.
type loose struct {
	oid  string
	data []byte
}

// verifiedBlob is the blob oid as the child wrote it, checked to be a blob
// that hashes to its name. The child sees no shared objects, so it writes
// every blob it stages; one the shared store holds already is not proof
// of a blob, since any object type has a name.
func verifiedBlob(objects *os.Root, oid [sha1.Size]byte) (loose, error) {
	name := hex.EncodeToString(oid[:])
	path := name[:2] + "/" + name[2:]
	info, err := objects.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > blobLimit {
		return loose{}, fmt.Errorf("the child wrote no regular object %s", name)
	}
	data, err := objects.ReadFile(path)
	if err != nil {
		return loose{}, err
	}
	z, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return loose{}, fmt.Errorf("object %s is not compressed as Git writes objects", name)
	}
	content, err := io.ReadAll(io.LimitReader(z, blobLimit+64))
	if err != nil || len(content) > blobLimit+32 {
		return loose{}, fmt.Errorf("object %s cannot be read", name)
	}
	// The header exactly as Git writes it: a size with a sign or leading
	// zero hashes fine but is one Git cannot read.
	header, body, ok := bytes.Cut(content, []byte{0})
	if !ok || string(header) != "blob "+strconv.Itoa(len(body)) ||
		sha1.Sum(content) != oid { //nolint:gosec // Git's object name.
		return loose{}, fmt.Errorf("object %s is not the blob its name says", name)
	}
	return loose{oid: name, data: data}, nil
}

// promote writes the object into the shared store without replacing one
// already there: a temporary file linked to its name, as Git does.
func (o loose) promote(shared *os.Root) error {
	dir := o.oid[:2]
	if err := shared.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if info, err := shared.Lstat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("object directory %s is not a directory", dir)
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := dir + "/tmp_obj_" + hex.EncodeToString(random[:])
	f, err := shared.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return err
	}
	_, err = f.Write(o.data)
	if syncErr := f.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = shared.Link(tmp, dir+"/"+o.oid[2:])
		if errors.Is(err, fs.ErrExist) {
			err = nil
		}
	}
	_ = shared.Remove(tmp)
	return err
}
