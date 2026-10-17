package loop

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
)

// autosquashPrefix starts the name of every scratch worktree Autosquash
// makes, which is how it finds one a killed run left registered.
const autosquashPrefix = "pr-loop-autosquash-"

// SquashedCommit is one commit of a trial autosquash, with the exit status
// of the command run at it, when one was.
type SquashedCommit struct {
	Oid     string `json:"oid"`
	Subject string `json:"subject"`
	Exit    *int   `json:"exit,omitempty"`
}

// AutosquashResult is what a trial autosquash found.
type AutosquashResult struct {
	// Base is the commit the branch was squashed onto.
	Base    string           `json:"base"`
	Commits []SquashedCommit `json:"commits"`
	// SameTree is whether the squashed branch has exactly the files HEAD
	// has; a squash that differs dropped or reordered a change.
	SameTree bool `json:"sameTree"`
	// Copied are the ignored generated files copied in from HEAD's
	// worktree.
	Copied []string `json:"copied"`
}

// OK reports whether the squash kept the files and every command passed.
func (r *AutosquashResult) OK() bool {
	ok := r.SameTree
	for _, c := range r.Commits {
		ok = ok && (c.Exit == nil || *c.Exit == 0)
	}
	return ok
}

// Autosquash squashes HEAD's fixups onto upstream, unsigned, in a scratch
// worktree of its own, compares the result's files with HEAD's, and runs
// command, if any, with sh at each squashed commit, its output going to
// log. upstream is where the branch's own commits start, as the merge base
// with HEAD; by default, in a loop, the branch's parent. Before the
// commands run it copies in the files of HEAD's worktree that git ignores
// and generated, git pathspec globs (`:(glob)` magic), names, such as
// generated code a build needs: HEAD's, so every commit runs with the files
// generated for HEAD. Each commit is checked out afresh, discarding what
// the command before changed in tracked files. The scratch worktree is
// removed whatever happens, and any a killed run left registered is
// removed first.
func Autosquash(upstream string, generated []string, command string, log io.Writer) (*AutosquashResult, error) {
	top, err := git.Toplevel()
	if err != nil {
		return nil, err
	}
	if upstream == "" {
		if upstream, err = loopParent(); err != nil {
			return nil, err
		}
	}
	base, err := git.Run("merge-base", "HEAD", upstream)
	if err != nil {
		return nil, refuse("%s shares no history with HEAD; name the branch this one is based on as UPSTREAM", upstream)
	}
	head, err := git.Run("rev-parse", "HEAD^{tree}")
	if err != nil {
		return nil, err
	}
	clearStale()
	parent, err := os.MkdirTemp("", autosquashPrefix)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(parent)
	scratch := filepath.Join(parent, autosquashPrefix+"worktree")
	if _, err := git.Run("worktree", "add", "--detach", scratch, "HEAD"); err != nil {
		return nil, err
	}
	defer func() {
		git.Run("-C", scratch, "rebase", "--abort")
		git.Run("worktree", "remove", "--force", scratch)
	}()
	// rebase.updateRefs off: a user's rebase.updateRefs=true would move the
	// branches the squash passes onto its commits.
	_, err = git.RunEnv(noEditor, "-C", scratch, "-c", "commit.gpgsign=false", "-c", "rebase.updateRefs=false",
		"rebase", "-i", "--autosquash", "--force-rebase", base)
	if err != nil {
		if m := couldNotApply.FindStringSubmatch(err.Error()); m != nil {
			return nil, refuse("squashing conflicts at %s %q: retarget that fixup at a later commit (or the PR's last one), "+
				"then run `pr-loop autosquash` again", m[1], m[2])
		}
		return nil, refuse("squashing the fixups failed: %v", err)
	}
	result := &AutosquashResult{Base: base, Commits: []SquashedCommit{}, Copied: []string{}}
	squashed, err := git.Run("-C", scratch, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return nil, err
	}
	result.SameTree = squashed == head
	list, err := git.Run("-C", scratch, "log", "--reverse", "--format=%H %s", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(list, "\n") {
		if oid, subject, ok := strings.Cut(line, " "); ok {
			result.Commits = append(result.Commits, SquashedCommit{Oid: oid, Subject: subject})
		}
	}
	if result.Copied, err = copyGenerated(top, scratch, generated); err != nil {
		return nil, err
	}
	if command == "" {
		return result, nil
	}
	for i := range result.Commits {
		c := &result.Commits[i]
		if _, err := git.Run("-C", scratch, "checkout", "-q", "-f", "--detach", c.Oid); err != nil {
			return nil, err
		}
		fmt.Fprintf(log, "pr-loop: at %.12s %s: %s\n", c.Oid, c.Subject, command)
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = scratch
		cmd.Env = gitenv.Without(os.Environ())
		cmd.Stdout, cmd.Stderr = log, log
		exit := 0
		if err := cmd.Run(); err != nil {
			var failed *exec.ExitError
			if !errors.As(err, &failed) {
				return nil, err
			}
			exit = failed.ExitCode()
		}
		fmt.Fprintf(log, "pr-loop: at %.12s: exit %d\n", c.Oid, exit)
		c.Exit = &exit
	}
	return result, nil
}

// loopParent is the parent of the loop's branch checked out here.
func loopParent() (string, error) {
	state, err := loadState(false)
	if err != nil {
		return "", err
	}
	branch, err := currentBranch()
	if err != nil || state == nil || state.Branches[branch] == nil {
		return "", refuse("HEAD is not one of a review loop's branches; name the branch it is based on: `pr-loop autosquash UPSTREAM`")
	}
	return state.parentRef(&Repo{Slug: state.Repo}, branch)
}

// clearStale removes the scratch worktrees a killed run left registered,
// whose directories are gone.
func clearStale() {
	list, err := git.Run("worktree", "list", "--porcelain")
	if err != nil {
		return
	}
	for _, block := range strings.Split(list, "\n\n") {
		path, prunable := "", false
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "worktree "); ok {
				path = v
			}
			prunable = prunable || strings.HasPrefix(line, "prunable")
		}
		if prunable && strings.Contains(path, "/"+autosquashPrefix) {
			git.Run("worktree", "remove", "--force", path)
		}
	}
}

// copyGenerated copies the files under from that git ignores and patterns
// match into the same places under to, and returns their paths.
func copyGenerated(from, to string, patterns []string) ([]string, error) {
	copied := []string{}
	if len(patterns) == 0 {
		return copied, nil
	}
	args := []string{"-C", from, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--"}
	for _, p := range patterns {
		args = append(args, ":(glob)"+p)
	}
	list, err := git.Run(args...)
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Split(list, "\x00") {
		if !filepath.IsLocal(name) {
			continue
		}
		src := filepath.Join(from, name)
		info, err := os.Lstat(src)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(to, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return nil, err
		}
		//nolint:gosec // name is a local path git listed, so dst stays under to
		if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
			return nil, err
		}
		copied = append(copied, name)
	}
	return copied, nil
}
