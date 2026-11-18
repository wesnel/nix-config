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

// splitter separates the tests a source file holds from its production
// code: given the file at the parent and at the commit, it returns the
// parent's bytes with only the commit's tests in place of the parent's,
// whether the tests changed, and whether the production code did. It
// refuses a file whose tests it cannot tell apart, rather than guess.
type splitter func(before, after []byte) (projected []byte, testChanged, codeChanged bool, err error)

// inlineTests is how red-check reads the tests out of a source file that
// holds its own: split projects them onto the parent's code, and tests
// returns them alone, empty where the file holds none.
type inlineTests struct {
	split splitter
	tests func(source []byte) ([]byte, error)
}

// inlineLanguages is how red-check reads inline test files, by file
// extension. An extension without an entry is refused; another language
// whose tests live in its source files is one more entry here.
var inlineLanguages = map[string]inlineTests{
	// The trailing top-level #[cfg(test)] module.
	".rs": {split: projectRust, tests: RustTestContent},
}

// InlineTests are the tests the source file at path holds, read as its
// extension says, or an error for an extension red-check cannot read.
func InlineTests(path string, source []byte) ([]byte, error) {
	lang, ok := inlineLanguages[filepath.Ext(path)]
	if !ok {
		return nil, refusal.New("red-check has no way to read the tests of a %q file, %s", filepath.Ext(path), path)
	}
	return lang.tests(source)
}

// testSelection is what a suite tests in one commit: the test files it
// takes whole, the inline test files whose tests changed, each with its
// projection, and for each inline test file the commit changes, whether
// its production code changed.
type testSelection struct {
	whole, inline []string
	projected     map[string][]byte
	inlineCode    map[string]bool
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

// selectTests is what suite tests in commit, whose parent is parent.
func selectTests(top, parent, commit string, suite Suite) (*testSelection, error) {
	whole, err := selectedFiles(top, parent, commit, suite.TestFiles)
	if err != nil {
		return nil, err
	}
	inline, err := selectedFiles(top, parent, commit, suite.InlineTestFiles)
	if err != nil {
		return nil, err
	}
	selection := &testSelection{whole: whole, projected: map[string][]byte{}, inlineCode: map[string]bool{}}
	for _, path := range inline {
		if slices.Contains(whole, path) {
			return nil, refusal.New("suite %q selects %s in both testFiles and inlineTestFiles; select it in only one", suite.Name, path)
		}
		lang, ok := inlineLanguages[filepath.Ext(path)]
		if !ok {
			return nil, refusal.New("suite %q selects %s as an inline test file, but red-check has no way to split a %q file's tests from its code; select its tests as testFiles, or report this check as unproven",
				suite.Name, path, filepath.Ext(path))
		}
		before, after, err := inlineSources(top, parent, commit, path)
		if err != nil {
			return nil, err
		}
		projected, testChanged, codeChanged, err := lang.split(before, after)
		if err != nil {
			return nil, refusal.New("cannot separate the tests in %s: %v; select separate test files or report this check as unproven", path, err)
		}
		selection.inlineCode[path] = codeChanged
		if testChanged {
			selection.inline = append(selection.inline, path)
			selection.projected[path] = projected
		}
	}
	return selection, nil
}

// inlineSources are an inline test file's bytes at parent and at commit,
// both regular files of one mode.
func inlineSources(top, parent, commit, path string) ([]byte, []byte, error) {
	var contents [][]byte
	var previousMode string
	for _, revision := range []string{parent, commit} {
		mode, err := git.Run("-C", top, "ls-tree", "--format=%(objectmode)", revision, "--", ":(literal)"+path)
		if err != nil {
			return nil, nil, err
		}
		if mode != "100644" && mode != "100755" || previousMode != "" && previousMode != mode {
			return nil, nil, refusal.New("an inline test file must be an existing regular file whose mode the commit leaves alone: %s", path)
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
