// Package signpass re-signs a branch's unsigned commits with the hardware
// key, squashing fixups first, for any repository and any workflow.
package signpass

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// BackupRef holds each branch's unsigned tip while a pass is in progress.
const BackupRef = "refs/sign-pass/backup/"

var refuse = refusal.New

// pending is a signing pass that has started and not finished, so --resume can
// verify and push it.
type pending struct {
	Branch string            `json:"branch"`
	Start  string            `json:"start"` // parent of the oldest commit re-signed
	Shape  []string          `json:"shape"` // tree, author and message per commit, when nothing is squashed
	Before map[string]string `json:"before"`
	// When fixups are squashed: the commits that remain, and each branch's
	// files, which squashing must not change.
	Squash bool              `json:"squash,omitempty"`
	Kept   []history.Commit  `json:"kept,omitempty"`
	Trees  map[string]string `json:"trees,omitempty"`
}

func stateDir() (string, error) {
	common, err := git.Run("rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "sign-pass"), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// rangeStart is the parent of the oldest commit the pass has to rewrite: the
// oldest unsigned commit, or an older one that a fixup folds into. It also
// reports whether there are fixups to squash.
func rangeStart(base string) (string, bool, error) {
	out, err := git.Run("log", "--reverse", "--format=%H%x00%G?%x00%s", base+"..HEAD")
	if err != nil {
		return "", false, err
	}
	type entry struct{ sha, mark, subject string }
	var commits []entry
	for _, line := range strings.Split(out, "\n") {
		if f := strings.SplitN(line, "\x00", 3); len(f) == 3 {
			commits = append(commits, entry{f[0], f[1], f[2]})
		}
	}
	oldest, fixups := len(commits), false
	for i, c := range commits {
		if c.mark == "N" && i < oldest {
			oldest = i
		}
		target := history.FixupTarget(c.subject)
		if target == "" {
			continue
		}
		fixups = true
		found := -1
		for j := i - 1; j >= 0; j-- {
			if history.FixupTarget(commits[j].subject) == "" && commits[j].subject == target {
				found = j
				break
			}
		}
		if found < 0 {
			return "", false, refuse("%q folds into a commit that is not on this branch; retarget it", c.subject)
		}
		if found < oldest {
			oldest = found
		}
	}
	if oldest == len(commits) {
		return "", false, nil
	}
	return commits[oldest].sha + "^", fixups, nil
}

// rehearseSquash squashes start..tip unsigned in a throwaway worktree, and
// checks the commits that remain and the files at the tip, so a conflict or a
// change shows up before any key touch.
func rehearseSquash(start, tip string, kept []history.Commit) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	scratch, err := os.MkdirTemp(dir, "rehearsal-")
	if err != nil {
		return err
	}
	os.Remove(scratch)
	if _, err := git.Run("worktree", "add", "--detach", scratch, tip); err != nil {
		return err
	}
	defer func() {
		git.Run("-C", scratch, "rebase", "--abort")
		git.Run("worktree", "remove", "--force", scratch)
	}()
	if _, err := git.Run("-C", scratch, "-c", "commit.gpgsign=false", "-c", "sequence.editor=:",
		"rebase", "-i", "--autosquash", "--force-rebase", start); err != nil {
		if m := history.CouldNotApply.FindStringSubmatch(err.Error()); m != nil {
			return refuse("squashing conflicts at %s %q: retarget that fixup at a later commit", m[1], m[2])
		}
		return refuse("squashing the fixups failed: %v", err)
	}
	head, err := git.Run("-C", scratch, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if err := history.Match(kept, start, head, tip); err != nil {
		return err
	}
	if a, _ := git.Run("rev-parse", head+"^{tree}"); a != mustTree(tip) {
		return refuse("squashing would change the files at %s", tip)
	}
	return nil
}

func mustTree(rev string) string {
	tree, _ := git.Run("rev-parse", rev+"^{tree}")
	return tree
}

func pendingPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pending.json"), nil
}

// fullShape is a commit as re-signing must leave it: files, author and
// message, everything except the signature and the committer's time.
func fullShape(base, tip string) ([]string, error) {
	out, err := git.Run("log", "--reverse", "--format=%T%x00%an <%ae> %at%x00%B%x01", base+".."+tip)
	if err != nil {
		return nil, err
	}
	var shape []string
	for _, c := range strings.Split(out, "\x01") {
		if c = strings.Trim(c, "\n"); c != "" {
			shape = append(shape, c)
		}
	}
	return shape, nil
}

func localBranches() (map[string]string, error) {
	out, err := git.Run("for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads/")
	if err != nil {
		return nil, err
	}
	branches := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, oid, ok := strings.Cut(line, " "); ok {
			branches[name] = oid
		}
	}
	return branches, nil
}

// defaultBase is where the current branch left the default branch: the
// remote's when there is one, and otherwise the local one, unless that is the
// branch being signed.
func defaultBase(branch string) (string, error) {
	for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master",
		"refs/heads/main", "refs/heads/master"} {
		if ref == "refs/heads/"+branch {
			continue
		}
		if git.OK("rev-parse", "--verify", "-q", ref) {
			return git.Run("merge-base", "HEAD", ref)
		}
	}
	return "", refuse("cannot tell where %s starts; pass --base REF", branch)
}

// Run re-signs every commit from the oldest unsigned one on the current
// branch to its tip, with the hardware key, leaving each commit's files,
// author and message exactly as they were. Branches stacked inside that range
// move with it. With push, every branch that moved and has an upstream is
// pushed over SSH, guarded by the commit its remote-tracking ref names.
func Run(base string, resume, push bool) (any, error) {
	path, err := pendingPath()
	if err != nil {
		return nil, err
	}
	var p pending
	if resume {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, refuse("no signing pass is in progress")
		}
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		if rm, _ := git.Run("rev-parse", "--git-path", "rebase-merge"); exists(rm) {
			return nil, refuse("the rebase is still in progress; touch the key and run `git rebase --continue`")
		}
	} else {
		branch, err := git.Run("rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			return nil, err
		}
		if branch == "HEAD" {
			return nil, refuse("HEAD is detached; switch to the branch to sign")
		}
		if exists(path) {
			return nil, refuse("a signing pass is already in progress; finish it with --resume, or remove %s", path)
		}
		if dirty, _ := git.Run("status", "--porcelain", "--untracked-files=no"); dirty != "" {
			return nil, refuse("this worktree has uncommitted changes; commit them (unsigned) first")
		}
		if base == "" {
			if base, err = defaultBase(branch); err != nil {
				return nil, err
			}
		}
		start, squash, err := rangeStart(base)
		if err != nil {
			return nil, err
		}
		if start == "" {
			return map[string]string{"branch": branch, "signed": "every commit since the base is already signed"}, nil
		}
		if merges, _ := git.Run("rev-list", "--merges", start+"..HEAD"); merges != "" {
			return nil, refuse("the commits to sign include merges, which re-signing would flatten; sign them by hand")
		}
		before, err := localBranches()
		if err != nil {
			return nil, err
		}
		p = pending{Branch: branch, Start: start, Before: before, Squash: squash}
		if squash {
			all, err := history.Between(start, "HEAD")
			if err != nil {
				return nil, err
			}
			for _, c := range all {
				if history.FixupTarget(c.Message) == "" {
					p.Kept = append(p.Kept, c)
				}
			}
			p.Trees = map[string]string{}
			for b, oid := range before {
				if git.OK("merge-base", "--is-ancestor", start, oid) && git.OK("merge-base", "--is-ancestor", oid, "HEAD") {
					p.Trees[b] = mustTree(oid)
				}
			}
			if err := rehearseSquash(start, "refs/heads/"+branch, p.Kept); err != nil {
				return nil, err
			}
		} else if p.Shape, err = fullShape(start, "HEAD"); err != nil {
			return nil, err
		}
		if _, err := git.Run("update-ref", BackupRef+branch, "HEAD"); err != nil {
			return nil, err
		}
		data, _ := json.MarshalIndent(p, "", "  ")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
		count, _ := git.Run("rev-list", "--count", start+"..HEAD")
		fmt.Fprintf(os.Stderr, "signing %s commits on %s, squashing fixups first if any; touch the key for each when it blinks\n", count, branch)
		args := []string{"rebase", "--force-rebase", "--gpg-sign", "--update-refs"}
		if squash {
			args = append(args, "-i", "--autosquash")
		}
		if git.Attached([]string{"GIT_SEQUENCE_EDITOR=:"}, append(args, start, branch)...) != 0 {
			return nil, fmt.Errorf("the rebase stopped, usually at a missed key touch. Run `git rebase --continue` "+
				"(touching the key when it blinks), then `git sign-pass --resume`. The unsigned branch is kept as "+
				"%s%s", BackupRef, branch)
		}
	}

	// A branch below the tip gains whatever fixups above it fold into its
	// commits, which is the point of folding; only the tip must keep its files.
	var gained []string
	if p.Squash {
		if err := history.Match(p.Kept, p.Start, "refs/heads/"+p.Branch, p.Branch); err != nil {
			return nil, err
		}
		for b, tree := range p.Trees {
			switch {
			case mustTree("refs/heads/"+b) == tree:
			case b == p.Branch:
				return nil, refuse("squashing changed the files on %s; its unsigned version is %s%s", b, BackupRef, b)
			default:
				gained = append(gained, b)
			}
		}
	} else {
		shape, err := fullShape(p.Start, "refs/heads/"+p.Branch)
		if err != nil {
			return nil, err
		}
		if strings.Join(shape, "\x02") != strings.Join(p.Shape, "\x02") {
			return nil, refuse("re-signing changed a commit's files, author or message; the unsigned branch is %s%s",
				BackupRef, p.Branch)
		}
	}
	marks, err := git.Run("log", "--format=%h %G?", p.Start+"..refs/heads/"+p.Branch)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(marks, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] != "G" && f[1] != "U" {
			return nil, refuse("commit %s has no good signature", f[0])
		}
	}
	after, err := localBranches()
	if err != nil {
		return nil, err
	}
	moved := map[string]string{}
	for b, oid := range after {
		if old, ok := p.Before[b]; ok && old != oid {
			moved[b] = oid
		}
	}
	signed := len(p.Shape)
	if p.Squash {
		signed = len(p.Kept)
	}
	result := map[string]any{"signed": signed, "squashed": p.Squash, "moved": moved}
	if len(gained) > 0 {
		sort.Strings(gained)
		result["gainedFixups"] = gained
	}

	if push {
		pushed, err := pushMoved(moved)
		if err != nil {
			return nil, err
		}
		result["pushed"] = pushed
	}
	os.Remove(path)
	return result, nil
}

// pushMoved pushes each moved branch that has an upstream, atomically per
// remote, over whatever transport the remote uses (for GitHub, SSH and the
// key), each guarded by its remote-tracking ref.
func pushMoved(moved map[string]string) (map[string]string, error) {
	type target struct{ branch, merge, lease string }
	byRemote := map[string][]target{}
	pushed := map[string]string{}
	for b := range moved {
		remote, _ := git.Run("config", "--get", "branch."+b+".remote")
		merge, _ := git.Run("config", "--get", "branch."+b+".merge")
		if remote == "" || merge == "" {
			pushed[b] = "not pushed: no upstream"
			continue
		}
		lease, _ := git.Run("rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+strings.TrimPrefix(merge, "refs/heads/"))
		byRemote[remote] = append(byRemote[remote], target{b, merge, lease})
	}
	for remote, targets := range byRemote {
		args := []string{"push", "--atomic"}
		for _, t := range targets {
			args = append(args, fmt.Sprintf("--force-with-lease=%s:%s", t.merge, t.lease))
		}
		args = append(args, remote)
		for _, t := range targets {
			args = append(args, "refs/heads/"+t.branch+":"+t.merge)
		}
		fmt.Fprintf(os.Stderr, "pushing to %s; touch the key if it blinks\n", remote)
		if git.Attached(nil, args...) != 0 {
			return nil, fmt.Errorf("the push to %s failed; the branches are signed, so push them again by hand", remote)
		}
		for _, t := range targets {
			pushed[t.branch] = remote + " " + t.merge
		}
	}
	return pushed, nil
}
