package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
)

// gitStageChild is git_stage's Git child, run by gitStage in Codex's
// sandbox with args WORKTREE PRIVATE/index PRIVATE/objects PATH...: it
// stages the literal paths with git add into the private index and object
// directory, after refusing what it cannot stage faithfully (filters,
// conversion, sparse or split indexes, non-regular files). It sees none of
// the repository's objects, so Git writes every blob it stages there
// rather than reusing one; gitstage.Install checks each. It writes
// nothing else; Install decides what of its result reaches the repository.
func gitStageChild(args []string, stdout, stderr io.Writer) int {
	if len(args) < 4 || !filepath.IsAbs(args[0]) || !filepath.IsAbs(args[1]) || !filepath.IsAbs(args[2]) {
		fmt.Fprintln(stderr, "staging requires an absolute worktree, private index and object directory, and literal paths")
		return 1
	}
	worktree, indexPath, objectPath, paths := args[0], args[1], args[2], args[3:]
	var missing []string
	for _, path := range paths {
		if path == "" || path == "." || filepath.IsAbs(path) || filepath.Clean(path) != path ||
			path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) || strings.ContainsRune(path, 0) {
			fmt.Fprintln(stderr, "invalid staging path")
			return 1
		}
		for _, part := range strings.Split(path, string(filepath.Separator)) {
			if strings.EqualFold(part, ".git") {
				fmt.Fprintln(stderr, "Git metadata cannot be staged")
				return 1
			}
		}
		//nolint:gosec // path is literal and relative, checked above; Lstat only inspects, in the sandbox.
		if info, err := os.Lstat(filepath.Join(worktree, path)); err == nil && !info.Mode().IsRegular() {
			fmt.Fprintln(stderr, "staging requires regular files")
			return 1
		} else if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintln(stderr, "staging path cannot be inspected")
				return 1
			}
			missing = append(missing, path)
		}
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			_ = os.Unsetenv(name)
		}
	}
	gitenv.SetNoConfig()
	if err := git.CheckConfigStrict(worktree); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx := git.WithEnv(context.Background(), "GIT_LITERAL_PATHSPECS=1")
	conversion, err := git.BytesContext(ctx, nil, "-C", worktree, "config", "--get", "core.autocrlf")
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			fmt.Fprintln(stderr, "cannot inspect Git line ending conversion")
			return 1
		}
	} else {
		switch strings.ToLower(strings.TrimSpace(string(conversion))) {
		case "false", "no", "off", "0":
		default:
			fmt.Fprintln(stderr, "Git line ending conversion is unsupported")
			return 1
		}
	}
	metadata, err := git.BytesContext(ctx, nil, "-C", worktree, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		fmt.Fprintln(stderr, "cannot inspect Git metadata")
		return 1
	}
	dirs := strings.Split(strings.TrimSuffix(string(metadata), "\n"), "\n")
	if len(dirs) != 2 {
		fmt.Fprintln(stderr, "unsupported Git metadata paths")
		return 1
	}
	for _, dir := range dirs {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || !filepath.IsAbs(dir) || filepath.Clean(dir) != real {
			fmt.Fprintln(stderr, "unsupported Git metadata paths")
			return 1
		}
	}
	objects, err := os.Lstat(filepath.Join(dirs[1], "objects")) //nolint:gosec // Git's own directory, checked physical above; inspected only.
	if err != nil || !objects.IsDir() {
		fmt.Fprintln(stderr, "unsupported Git object directory")
		return 1
	}
	//nolint:gosec // Git's own directory, checked physical above; inspected only.
	if index, err := os.Lstat(filepath.Join(dirs[0], "index")); err == nil && !index.Mode().IsRegular() {
		fmt.Fprintln(stderr, "unsupported Git index")
		return 1
	} else if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(stderr, "cannot inspect Git index")
		return 1
	}
	private := filepath.Dir(indexPath)
	realPrivate, err := filepath.EvalSymlinks(private)
	if err != nil || realPrivate != filepath.Clean(private) || filepath.Base(indexPath) != "index" ||
		filepath.Dir(objectPath) != private || filepath.Base(objectPath) != "objects" {
		fmt.Fprintln(stderr, "unsupported private staging paths")
		return 1
	}
	for _, root := range append([]string{worktree}, dirs...) {
		rel, err := filepath.Rel(root, private)
		if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			fmt.Fprintln(stderr, "private staging paths overlap the repository")
			return 1
		}
	}
	// The broker's private paths, their shape checked above; inspected only.
	privateIndex, indexErr := os.Lstat(indexPath)      //nolint:gosec // see above.
	privateObjects, objectsErr := os.Lstat(objectPath) //nolint:gosec // see above.
	if indexErr != nil || !privateIndex.Mode().IsRegular() || objectsErr != nil || !privateObjects.IsDir() {
		fmt.Fprintln(stderr, "unsupported private staging paths")
		return 1
	}
	//nolint:gosec // Git's own directory, checked physical above; inspected only.
	if realIndex, err := os.Stat(filepath.Join(dirs[0], "index")); err == nil && os.SameFile(privateIndex, realIndex) {
		fmt.Fprintln(stderr, "private index aliases the real index")
		return 1
	}
	ctx = git.WithEnv(ctx, "GIT_INDEX_FILE="+indexPath, "GIT_OBJECT_DIRECTORY="+objectPath)
	sparse, err := git.BytesContext(ctx, nil, "-C", worktree, "config", "--bool", "--get", "index.sparse")
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			fmt.Fprintln(stderr, "cannot inspect sparse index configuration")
			return 1
		}
	} else if strings.TrimSpace(string(sparse)) != "false" {
		fmt.Fprintln(stderr, "sparse index is unsupported")
		return 1
	}
	shared, err := git.BytesContext(ctx, nil, "-C", worktree, "rev-parse", "--shared-index-path")
	if err != nil || strings.TrimSpace(string(shared)) != "" {
		fmt.Fprintln(stderr, "split index is unsupported")
		return 1
	}
	for _, path := range missing {
		tracked, err := git.BytesContext(ctx, nil, "-C", worktree, "ls-files", "-z", "--", path)
		if err != nil || string(tracked) != path+"\x00" {
			fmt.Fprintln(stderr, "deletion requires an exact tracked file")
			return 1
		}
	}
	names := []string{"filter", "text", "eol", "working-tree-encoding", "ident"}
	attributeArgs := append([]string{"-C", worktree, "check-attr", "-z", "--stdin"}, names...)
	attributes, err := git.BytesContext(ctx, []byte(strings.Join(paths, "\x00")+"\x00"), attributeArgs...)
	if err != nil {
		fmt.Fprintln(stderr, "cannot inspect Git filter attributes")
		return 1
	}
	fields := strings.Split(string(attributes), "\x00")
	if len(fields) != len(paths)*len(names)*3+1 {
		fmt.Fprintln(stderr, "unexpected Git attribute output")
		return 1
	}
	for i, path := range paths {
		for j, name := range names {
			at := (i*len(names) + j) * 3
			value := fields[at+2]
			if fields[at] != path || fields[at+1] != name || (value != "unspecified" && value != "unset") {
				fmt.Fprintln(stderr, "staging files with Git filters or conversion attributes is unsupported")
				return 1
			}
		}
	}
	inputs := filepath.Join(private, "inputs")
	if err := os.Mkdir(inputs, 0o700); err != nil { //nolint:gosec // in the broker's private directory, outside the repository (checked above).
		fmt.Fprintln(stderr, "cannot create private staging inputs")
		return 1
	}
	root, err := os.OpenRoot(worktree)
	if err != nil {
		fmt.Fprintln(stderr, "cannot open worktree")
		return 1
	}
	defer root.Close()
	for i, path := range paths {
		if slices.Contains(missing, path) {
			continue
		}
		before, err := root.Lstat(path)
		if err != nil || !before.Mode().IsRegular() {
			fmt.Fprintln(stderr, "staging input is not a regular file")
			return 1
		}
		source, err := root.Open(path)
		if err != nil {
			fmt.Fprintln(stderr, "cannot open staging input")
			return 1
		}
		opened, err := source.Stat()
		if err != nil || !os.SameFile(before, opened) {
			source.Close()
			fmt.Fprintln(stderr, "staging input changed")
			return 1
		}
		//nolint:gosec // a numbered file in the private inputs directory made above.
		dest, err := os.OpenFile(filepath.Join(inputs, strconv.Itoa(i)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			source.Close()
			fmt.Fprintln(stderr, "cannot capture staging input")
			return 1
		}
		const maxInput = 64 << 20
		size, copyErr := io.Copy(dest, io.LimitReader(source, maxInput+1))
		after, statErr := source.Stat()
		sourceErr, destErr := source.Close(), dest.Close()
		if copyErr != nil || statErr != nil || sourceErr != nil || destErr != nil || size > maxInput ||
			!os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
			fmt.Fprintln(stderr, "staging input changed or could not be captured")
			return 1
		}
	}
	argv := append([]string{"-C", worktree, "add", "--all", "--"}, paths...)
	output, err := git.BytesContext(ctx, nil, argv...)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
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
			fmt.Fprintln(stderr, "cannot write the blob of", path)
			return 1
		}
	}
	_, _ = stdout.Write(output)
	return 0
}
