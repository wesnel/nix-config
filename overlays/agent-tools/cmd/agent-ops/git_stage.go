package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
)

// gitStageProbe is run by gitStage, with args WORKSPACE TARGET..., in the
// sandbox its child will run in, before the child: it prints "confined" only
// when it can create a file in the directory WORKSPACE and is refused one in
// each directory TARGET. Any other failure, such as a missing target, fails
// it. It removes each file it creates.
func gitStageProbe(args []string, stdout, stderr io.Writer) int {
	if len(args) < 2 {
		fmt.Fprintln(stderr, "the probe requires its workspace and at least one target")
		return 1
	}
	create := func(dir string) error {
		f, err := os.CreateTemp(dir, ".git-stage-probe-")
		if err != nil {
			return err
		}
		name := f.Name()
		//nolint:gosec // the probe's own file, just created by CreateTemp in a directory gitStage names.
		return errors.Join(f.Close(), os.Remove(name))
	}
	if err := create(args[0]); err != nil {
		fmt.Fprintln(stderr, "the probe cannot write its workspace:", err)
		return 1
	}
	for _, target := range args[1:] {
		switch err := create(target); {
		case err == nil:
			fmt.Fprintln(stderr, "the sandbox lets git_stage's child write", target)
			return 1
		case !errors.Is(err, fs.ErrPermission):
			fmt.Fprintln(stderr, "the probe could not test", target+":", err)
			return 1
		}
	}
	fmt.Fprintln(stdout, "confined")
	return 0
}

// gitStageChild is git_stage's Git child, run by gitStage in Codex's
// sandbox with args WORKTREE PRIVATE/index PRIVATE/objects PATH...: it
// stages the literal paths with git add into the private index and object
// directory, after refusing what it cannot stage faithfully (filters,
// conversion, sparse or split indexes, non-regular files). It sees none of
// the repository's objects, so Git writes every blob it stages there
// rather than reusing one; gitstage.Install checks each. It writes
// nothing else; Install decides what of its result reaches the repository.
func gitStageChild(args []string, stdout, stderr io.Writer) int {
	output, err := stageInChild(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, _ = stdout.Write(output)
	return 0
}

// stageInChild is gitStageChild's work, each check in the order it is
// made: git add's output, or why the child refused.
func stageInChild(args []string) ([]byte, error) {
	if len(args) < 4 || !filepath.IsAbs(args[0]) || !filepath.IsAbs(args[1]) || !filepath.IsAbs(args[2]) {
		return nil, errors.New("staging requires an absolute worktree, private index and object directory, and literal paths")
	}
	worktree, indexPath, objectPath, paths := args[0], args[1], args[2], args[3:]
	missing, err := childPaths(worktree, paths)
	if err != nil {
		return nil, err
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			_ = os.Unsetenv(name)
		}
	}
	gitenv.SetNoConfig()
	if err := git.CheckConfigStrict(worktree); err != nil {
		return nil, err
	}
	// Git looks for a repository's objects to recognise it; its sandbox
	// hides the shared ones, so every command names the private ones.
	ctx := git.WithEnv(context.Background(), "GIT_LITERAL_PATHSPECS=1", "GIT_OBJECT_DIRECTORY="+objectPath)
	if err := checkNoConversion(ctx, worktree); err != nil {
		return nil, err
	}
	dirs, err := childGitDirs(ctx, worktree)
	if err != nil {
		return nil, err
	}
	private, err := checkPrivatePaths(worktree, dirs, indexPath, objectPath)
	if err != nil {
		return nil, err
	}
	ctx = git.WithEnv(ctx, "GIT_INDEX_FILE="+indexPath)
	if err := checkChildIndex(ctx, worktree, missing); err != nil {
		return nil, err
	}
	if err := checkNoFilters(ctx, worktree, paths); err != nil {
		return nil, err
	}
	inputs, err := captureInputs(worktree, private, paths, missing)
	if err != nil {
		return nil, err
	}
	argv := append([]string{"-C", worktree, "add", "--all", "--"}, paths...)
	output, err := git.BytesContext(ctx, nil, argv...)
	if err != nil {
		return nil, err
	}
	// git add hashes nothing for a stat-clean file, so write each captured
	// input's blob too: Install checks every entry's blob in the private
	// objects. The entry names what git add read, which for a file changed
	// after its capture is the newer content.
	for i, path := range paths {
		if slices.Contains(missing, path) {
			continue
		}
		input := filepath.Join(inputs, strconv.Itoa(i))
		if _, err := git.BytesContext(ctx, nil, "-C", worktree, "hash-object", "-w", "--no-filters", "--", input); err != nil {
			return nil, fmt.Errorf("cannot write the blob of %s", path)
		}
	}
	return output, nil
}

// childPaths checks each path is literal, outside Git metadata and a
// regular file or absent from worktree, and returns those absent.
func childPaths(worktree string, paths []string) ([]string, error) {
	var missing []string
	for _, path := range paths {
		if err := childPath(path); err != nil {
			return nil, err
		}
		//nolint:gosec // path is literal and relative, checked above; Lstat only inspects, in the sandbox.
		info, err := os.Lstat(filepath.Join(worktree, path))
		switch {
		case err == nil && !info.Mode().IsRegular():
			return nil, errors.New("staging requires regular files")
		case err == nil:
		case !os.IsNotExist(err):
			return nil, errors.New("staging path cannot be inspected")
		default:
			missing = append(missing, path)
		}
	}
	return missing, nil
}

// childPath refuses a path that is not literal and relative, or that names
// Git metadata.
func childPath(path string) error {
	if path == "" || path == "." || filepath.IsAbs(path) || filepath.Clean(path) != path ||
		path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) || strings.ContainsRune(path, 0) {
		return errors.New("invalid staging path")
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if strings.EqualFold(part, ".git") {
			return errors.New("Git metadata cannot be staged")
		}
	}
	return nil
}

// configUnset reports whether err is git config --get's exit for a key
// that is not set.
func configUnset(err error) bool {
	var exit *exec.ExitError
	return errors.As(err, &exit) && exit.ExitCode() == 1
}

// checkNoConversion refuses a worktree whose core.autocrlf converts line
// endings.
func checkNoConversion(ctx context.Context, worktree string) error {
	conversion, err := git.BytesContext(ctx, nil, "-C", worktree, "config", "--get", "core.autocrlf")
	if err != nil {
		if configUnset(err) {
			return nil
		}
		return errors.New("cannot inspect Git line ending conversion")
	}
	switch strings.ToLower(strings.TrimSpace(string(conversion))) {
	case "false", "no", "off", "0":
		return nil
	}
	return errors.New("Git line ending conversion is unsupported")
}

// childGitDirs is worktree's Git directory and common directory, each
// absolute and physical, after checking its index, if any, is a regular
// file.
func childGitDirs(ctx context.Context, worktree string) ([]string, error) {
	metadata, err := git.BytesContext(ctx, nil, "-C", worktree, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return nil, errors.New("cannot inspect Git metadata")
	}
	dirs := strings.Split(strings.TrimSuffix(string(metadata), "\n"), "\n")
	if len(dirs) != 2 {
		return nil, errors.New("unsupported Git metadata paths")
	}
	for _, dir := range dirs {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || !filepath.IsAbs(dir) || filepath.Clean(dir) != real {
			return nil, errors.New("unsupported Git metadata paths")
		}
	}
	//nolint:gosec // Git's own directory, checked physical above; inspected only.
	if index, err := os.Lstat(filepath.Join(dirs[0], "index")); err == nil && !index.Mode().IsRegular() {
		return nil, errors.New("unsupported Git index")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, errors.New("cannot inspect Git index")
	}
	return dirs, nil
}

// checkPrivatePaths checks the private index and object directory are
// PRIVATE/index, a regular file, and PRIVATE/objects, a directory, with
// PRIVATE physical, outside the worktree and its Git directories dirs, and
// the index not the real one. It returns PRIVATE.
func checkPrivatePaths(worktree string, dirs []string, indexPath, objectPath string) (string, error) {
	private := filepath.Dir(indexPath)
	realPrivate, err := filepath.EvalSymlinks(private)
	if err != nil || realPrivate != filepath.Clean(private) || filepath.Base(indexPath) != "index" ||
		filepath.Dir(objectPath) != private || filepath.Base(objectPath) != "objects" {
		return "", errors.New("unsupported private staging paths")
	}
	for _, root := range append([]string{worktree}, dirs...) {
		rel, err := filepath.Rel(root, private)
		if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return "", errors.New("private staging paths overlap the repository")
		}
	}
	// The broker's private paths, their shape checked above; inspected only.
	privateIndex, indexErr := os.Lstat(indexPath)      //nolint:gosec // see above.
	privateObjects, objectsErr := os.Lstat(objectPath) //nolint:gosec // see above.
	if indexErr != nil || !privateIndex.Mode().IsRegular() || objectsErr != nil || !privateObjects.IsDir() {
		return "", errors.New("unsupported private staging paths")
	}
	//nolint:gosec // Git's own directory, checked physical above; inspected only.
	if realIndex, err := os.Stat(filepath.Join(dirs[0], "index")); err == nil && os.SameFile(privateIndex, realIndex) {
		return "", errors.New("private index aliases the real index")
	}
	return private, nil
}

// checkChildIndex refuses a sparse or split private index, and a missing
// path, a deletion, that is not exactly one tracked file.
func checkChildIndex(ctx context.Context, worktree string, missing []string) error {
	sparse, err := git.BytesContext(ctx, nil, "-C", worktree, "config", "--bool", "--get", "index.sparse")
	if err != nil {
		if !configUnset(err) {
			return errors.New("cannot inspect sparse index configuration")
		}
	} else if strings.TrimSpace(string(sparse)) != "false" {
		return errors.New("sparse index is unsupported")
	}
	shared, err := git.BytesContext(ctx, nil, "-C", worktree, "rev-parse", "--shared-index-path")
	if err != nil || strings.TrimSpace(string(shared)) != "" {
		return errors.New("split index is unsupported")
	}
	for _, path := range missing {
		tracked, err := git.BytesContext(ctx, nil, "-C", worktree, "ls-files", "-z", "--", path)
		if err != nil || string(tracked) != path+"\x00" {
			return errors.New("deletion requires an exact tracked file")
		}
	}
	return nil
}

// checkNoFilters refuses paths any of whose filter or conversion
// attributes is set.
func checkNoFilters(ctx context.Context, worktree string, paths []string) error {
	names := []string{"filter", "text", "eol", "working-tree-encoding", "ident"}
	attributeArgs := append([]string{"-C", worktree, "check-attr", "-z", "--stdin"}, names...)
	attributes, err := git.BytesContext(ctx, []byte(strings.Join(paths, "\x00")+"\x00"), attributeArgs...)
	if err != nil {
		return errors.New("cannot inspect Git filter attributes")
	}
	fields := strings.Split(string(attributes), "\x00")
	if len(fields) != len(paths)*len(names)*3+1 {
		return errors.New("unexpected Git attribute output")
	}
	for i, path := range paths {
		for j, name := range names {
			at := (i*len(names) + j) * 3
			value := fields[at+2]
			if fields[at] != path || fields[at+1] != name || (value != "unspecified" && value != "unset") {
				return errors.New("staging files with Git filters or conversion attributes is unsupported")
			}
		}
	}
	return nil
}

// captureInputs copies each path but those missing from worktree into
// PRIVATE/inputs/I, I its index in paths, and returns that directory.
func captureInputs(worktree, private string, paths, missing []string) (string, error) {
	inputs := filepath.Join(private, "inputs")
	if err := os.Mkdir(inputs, 0o700); err != nil { //nolint:gosec // in the broker's private directory, outside the repository (checked above).
		return "", errors.New("cannot create private staging inputs")
	}
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return "", errors.New("cannot open worktree")
	}
	defer root.Close()
	for i, path := range paths {
		if slices.Contains(missing, path) {
			continue
		}
		if err := captureInput(root, path, filepath.Join(inputs, strconv.Itoa(i))); err != nil {
			return "", err
		}
	}
	return inputs, nil
}

// captureInput copies the regular file path under root to dest, a new
// file, refusing one that changes while it is copied.
func captureInput(root *os.Root, path, dest string) error {
	before, err := root.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return errors.New("staging input is not a regular file")
	}
	source, err := root.Open(path)
	if err != nil {
		return errors.New("cannot open staging input")
	}
	opened, err := source.Stat()
	if err != nil || !os.SameFile(before, opened) {
		source.Close()
		return errors.New("staging input changed")
	}
	//nolint:gosec // a numbered file in the private inputs directory made above.
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		source.Close()
		return errors.New("cannot capture staging input")
	}
	const maxInput = 64 << 20
	size, copyErr := io.Copy(out, io.LimitReader(source, maxInput+1))
	after, statErr := source.Stat()
	sourceErr, destErr := source.Close(), out.Close()
	if copyErr != nil || statErr != nil || sourceErr != nil || destErr != nil || size > maxInput ||
		!os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return errors.New("staging input changed or could not be captured")
	}
	return nil
}
