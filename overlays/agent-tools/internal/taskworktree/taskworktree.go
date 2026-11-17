// Package taskworktree creates a task worktree, a linked worktree of the
// repository at ROOT/<repo>/<branch>, <repo> being the main checkout's
// directory name, for an agent whose sandbox cannot write the repository's
// .git/worktrees. The branch and base come from the agent, so each is
// checked as git reads it before git acts on it, and git runs as
// internal/git runs it: no hooks, no fsmonitor, and no repository config
// that names a program. The checkout leaves LFS files as pointers.
package taskworktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// Result is the worktree Create made.
type Result struct {
	Branch string `json:"branch"`
	// Base is the full name of the ref the branch was made from.
	Base string `json:"base"`
	// Commit is the commit Base named, which the worktree has checked out.
	Commit   string `json:"commit"`
	Worktree string `json:"worktree"`
	// Recovered is true where the branch was already there, at Commit,
	// with no worktree, as a `git worktree add -b` the sandbox stopped
	// halfway leaves it.
	Recovered bool `json:"recovered"`
}

// repo is a directory in the repository git acts on. Every command names
// it with -C, so the environment's GIT_DIR and the like do not apply.
type repo string

func (r repo) run(args ...string) (string, error) {
	return git.Run(append([]string{"-C", string(r)}, args...)...)
}

// checkout runs a `git worktree add`. git-lfs, which the operator's global
// config may run as a filter, leaves LFS pointers as they are rather than
// download objects outside the sandbox, from a remote or an lfs.url the
// checked-out tree's .lfsconfig names.
func (r repo) checkout(args ...string) (string, error) {
	return git.RunLong([]string{"GIT_LFS_SKIP_SMUDGE=1"}, append([]string{"-C", string(r), "worktree", "add"}, args...)...)
}

// branch is a name git takes for a branch as given: no option, no `@{-N}`
// shorthand, and no `..` or leading `/` that would lead its worktree's
// path out of the repository's directory under the root.
type branch string

// base is a ref and the commit it names.
type base struct {
	ref, commit string
}

// worktree is one entry of `git worktree list`.
type worktree struct {
	path, branch string
	bare         bool
}

// Root is where task worktrees go: "worktrees" in agent-tools' config at
// config (repos.Path), which the home-manager module writes from
// wgn.home.llm.sandbox.worktrees.
func Root(config string) (string, error) {
	var file struct {
		Worktrees string `json:"worktrees"`
	}
	data, err := os.ReadFile(config)
	if err == nil {
		err = json.Unmarshal(data, &file)
	}
	if err != nil || !filepath.IsAbs(file.Worktrees) {
		return "", fmt.Errorf("agent-tools' config %s names no absolute worktrees directory (%v); hand the "+
			"operator this message, since a home-manager switch writes it", config, err)
	}
	return filepath.Clean(file.Worktrees), nil
}

// Create makes branch from base and its worktree under root, in the
// repository containing dir, or recovers branch where it already points
// at base with no worktree. root is absolute.
func Create(dir, root, branchName, baseName string) (Result, error) {
	r := repo(dir)
	if err := git.CheckConfig(dir); err != nil {
		return Result{}, err
	}
	b, err := r.parseBranch(branchName)
	if err != nil {
		return Result{}, err
	}
	from, err := r.resolveBase(baseName)
	if err != nil {
		return Result{}, err
	}
	trees, err := r.worktrees()
	if err != nil {
		return Result{}, err
	}
	main := trees[0]
	if main.bare {
		return Result{}, refusal.New("the repository at %s is bare, so it has no main checkout to name its "+
			"task worktrees by; run `git task-worktree` from a repository with a working tree", main.path)
	}
	target := filepath.Join(root, filepath.Base(main.path), filepath.FromSlash(string(b)))
	if within(resolved(target), resolved(main.path)) {
		return Result{}, refusal.New("the worktree %s would be in the repository's main checkout, %s, which "+
			"stays as the operator left it; hand the operator this message, since the main checkout is "+
			"where task worktrees go", target, main.path)
	}
	if link := linkUnder(root, target); link != "" {
		return Result{}, refusal.New("%s is a symbolic link, so the worktree %s, which this command makes "+
			"outside the sandbox, could land outside %s; hand the operator this message, to remove the link",
			link, target, root)
	}
	if _, err := os.Lstat(target); err == nil {
		return Result{}, refusal.New("%s already exists. If it is the worktree of %s, enter it with "+
			"EnterWorktree's `path`; otherwise name another branch", target, b)
	}
	result := Result{Branch: string(b), Base: from.ref, Commit: from.commit, Worktree: target}
	if at, err := r.run("rev-parse", "--verify", "--quiet", "refs/heads/"+string(b)); err == nil {
		for _, w := range trees {
			if w.branch == "refs/heads/"+string(b) {
				return Result{}, refusal.New("branch %s is already checked out in %s. Work there, entering it "+
					"with EnterWorktree's `path`, or name another branch", b, w.path)
			}
		}
		if at != from.commit {
			return Result{}, refusal.New("branch %s already exists, at %s, not at %s (%s). Name another "+
				"branch, or pass the branch as its own base, `git task-worktree %s %s`, to make a worktree "+
				"for it as it is", b, at, from.ref, from.commit, b, b)
		}
		if _, err := r.checkout(target, string(b)); err != nil {
			return Result{}, err
		}
		result.Recovered = true
		return result, nil
	}
	if _, err := r.checkout("--no-track", "-b", string(b), target, from.commit); err != nil {
		return Result{}, err
	}
	return result, nil
}

// parseBranch is name as a branch, where git takes it as one unchanged.
func (r repo) parseBranch(name string) (branch, error) {
	out, err := r.run("check-ref-format", "--branch", name)
	if err != nil || out != name || strings.HasPrefix(name, "-") {
		return "", refusal.New("%q is not a branch name git takes as given. Name the branch as "+
			"`git check-ref-format --branch NAME` prints it back unchanged, such as `topic/short-name`", name)
	}
	return branch(name), nil
}

// resolveBase is the ref name names, as git resolves it, and its commit.
// A commit ID or an expression such as `main~1` names no ref.
func (r repo) resolveBase(name string) (base, error) {
	ref, err := r.run("rev-parse", "--verify", "--quiet", "--symbolic-full-name", "--end-of-options", name)
	if err == nil && strings.HasPrefix(ref, "refs/") {
		if commit, err := r.run("rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}"); err == nil {
			return base{ref: ref, commit: commit}, nil
		}
	}
	return base{}, refusal.New("BASE %q is not a ref of this repository that names a commit. Name a branch, "+
		"remote-tracking branch or tag, such as `origin/main`", name)
}

// worktrees are the repository's worktrees, the main one first.
func (r repo) worktrees() ([]worktree, error) {
	out, err := r.run("worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var trees []worktree
	for _, field := range strings.Split(out, "\x00") {
		switch key, value, _ := strings.Cut(field, " "); key {
		case "worktree":
			trees = append(trees, worktree{path: value})
		case "branch":
			if len(trees) > 0 {
				trees[len(trees)-1].branch = value
			}
		case "bare":
			if len(trees) > 0 {
				trees[len(trees)-1].bare = true
			}
		}
	}
	if len(trees) == 0 {
		return nil, errors.New("git worktree list named no worktree")
	}
	return trees, nil
}

// resolved is path with the symbolic links in its longest existing prefix
// resolved, so it compares with a path git prints.
func resolved(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

// linkUnder is the first existing symbolic link on the way from root down
// to path, root itself left out, or "". A sandboxed agent can write under
// root, so a link there would aim a write made outside the sandbox. A link
// made between this check and git's write still gets through.
func linkUnder(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	at := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		at = filepath.Join(at, part)
		info, err := os.Lstat(at)
		if err != nil {
			return ""
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return at
		}
	}
	return ""
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}
