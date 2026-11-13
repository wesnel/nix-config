package redcheck

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

type testSelection struct {
	whole, inline []string
	projected     map[string][]byte
	codeChanged   bool
}

func selectedFiles(top, parent, commit string, globs []string) ([]string, error) {
	if len(globs) == 0 {
		return nil, nil
	}
	paths := make([]string, len(globs))
	for i, glob := range globs {
		paths[i] = ":(glob)" + glob
	}
	return changedFiles(top, parent, commit, paths)
}

func selectTests(top, parent, commit string, opts Options) (*testSelection, error) {
	changed, err := changedFiles(top, parent, commit, nil)
	if err != nil {
		return nil, err
	}
	whole, err := selectedFiles(top, parent, commit, opts.TestFiles)
	if err != nil {
		return nil, err
	}
	inline, err := selectedFiles(top, parent, commit, opts.RustTestFiles)
	if err != nil {
		return nil, err
	}
	selection := &testSelection{whole: whole, projected: map[string][]byte{}}
	for _, path := range changed {
		switch {
		case slices.Contains(whole, path) && slices.Contains(inline, path):
			return nil, refusal.New("%s matches both test-files and rust-test-files; select it in only one", path)
		case slices.Contains(whole, path):
		case slices.Contains(inline, path):
			before, after, err := rustSources(top, parent, commit, path)
			if err != nil {
				return nil, err
			}
			projected, testChanged, codeChanged, err := projectRust(before, after)
			if err != nil {
				return nil, refusal.New("cannot separate Rust tests in %s: %v; select separate test files or report this check as unproven", path, err)
			}
			selection.codeChanged = selection.codeChanged || codeChanged
			if testChanged {
				selection.inline = append(selection.inline, path)
				selection.projected[path] = projected
			}
		default:
			selection.codeChanged = true
		}
	}
	return selection, nil
}

func rustSources(top, parent, commit, path string) ([]byte, []byte, error) {
	if filepath.Ext(path) != ".rs" {
		return nil, nil, refusal.New("rust-test-files selected non-Rust path %s", path)
	}
	var contents [][]byte
	var previousMode string
	for _, revision := range []string{parent, commit} {
		mode, err := git.Run("-C", top, "ls-tree", "--format=%(objectmode)", revision, "--", ":(literal)"+path)
		if err != nil {
			return nil, nil, err
		}
		if mode != "100644" && mode != "100755" || previousMode != "" && previousMode != mode {
			return nil, nil, refusal.New("rust-test-files needs an existing regular file with unchanged mode: %s", path)
		}
		previousMode = mode
		data, err := git.Bytes(nil, "-C", top, "cat-file", "blob", revision+":"+path)
		if err != nil {
			return nil, nil, err
		}
		contents = append(contents, data)
	}
	return contents[0], contents[1], nil
}

func projectRust(before, after []byte) ([]byte, bool, bool, error) {
	old, err := rustTestModule(before)
	if err != nil {
		return nil, false, false, err
	}
	new, err := rustTestModule(after)
	if err != nil {
		return nil, false, false, err
	}
	oldTest, newTest := before[old.start:old.end], after[new.start:new.end]
	if old.start == old.end && len(newTest) > 0 && len(before) > 0 && !bytes.HasSuffix(before, []byte("\n")) {
		return nil, false, false, refusal.New("adding a test module requires the parent to end with a newline")
	}
	production, err := rustProduction(before[:old.start])
	if err != nil {
		return nil, false, false, err
	}
	newProduction, err := rustProduction(after[:new.start])
	if err != nil {
		return nil, false, false, err
	}
	projected := append(bytes.Clone(before[:old.start]), newTest...)
	projected = append(projected, before[old.end:]...)
	return projected, !bytes.Equal(oldTest, newTest), !bytes.Equal(production, newProduction), nil
}

// Classification ignores only trivia after the final production token.
// Projection still retains all parent bytes, including that trivia.
func rustProduction(source []byte) ([]byte, error) {
	tokens, err := rustTokens(source)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, nil
	}
	return source[:tokens[len(tokens)-1].end], nil
}

func (s *testSelection) apply(worktree, commit string) error {
	if len(s.whole) > 0 {
		paths := make([]string, len(s.whole))
		for i, path := range s.whole {
			paths[i] = ":(literal)" + path
		}
		if _, err := git.RunLong(nil, append([]string{"-C", worktree, "restore", "--source", commit, "--staged", "--worktree", "--"}, paths...)...); err != nil {
			return err
		}
	}
	for _, path := range s.inline {
		// Both trees were checked for regular files. os.Root also prevents
		// a tracked ancestor symlink from escaping the throwaway worktree.
		root, err := os.OpenRoot(worktree)
		if err != nil {
			return err
		}
		err = root.WriteFile(filepath.FromSlash(path), s.projected[path], 0o644)
		root.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *testSelection) paths() []string {
	paths := append(slices.Clone(s.whole), s.inline...)
	slices.SortFunc(paths, strings.Compare)
	return paths
}
