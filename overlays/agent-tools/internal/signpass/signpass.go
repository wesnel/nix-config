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
	"slices"
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
	// Stacked are the other branches inside the range, which must all move.
	Stacked []string `json:"stacked,omitempty"`
	// Worktrees are where stacked branches are checked out, by branch. Each is
	// detached for the rebase, since --update-refs skips a branch checked out
	// anywhere, and switched back to its branch afterwards.
	Worktrees map[string]string `json:"worktrees,omitempty"`
}

// checkout is another worktree's branch: checked out there, or being rebased
// or bisected there (busy), which --update-refs skips either way.
type checkout struct {
	path, branch string
	busy         bool
}

// otherCheckouts lists the branches every worktree but this one holds.
func otherCheckouts() ([]checkout, error) {
	here, err := git.Run("rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	here, _ = filepath.EvalSymlinks(here)
	list, err := git.Run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var found []checkout
	for _, block := range strings.Split(list, "\n\n") {
		var c checkout
		detached := false
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "worktree "); ok {
				c.path = v
			}
			if v, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
				c.branch = v
			}
			detached = detached || line == "detached"
		}
		if resolved, _ := filepath.EvalSymlinks(c.path); c.path == "" || resolved == here {
			continue
		}
		if detached {
			c.branch, c.busy = busyBranch(c.path), true
		}
		if c.branch != "" {
			found = append(found, c)
		}
	}
	return found, nil
}

// busyBranch is the branch a detached worktree is rebasing or bisecting, if
// any; git lists such a worktree as detached but still counts the branch as
// checked out there.
func busyBranch(path string) string {
	for _, name := range []string{"rebase-merge/head-name", "rebase-apply/head-name", "BISECT_START"} {
		file, err := git.Run("-C", path, "rev-parse", "--path-format=absolute", "--git-path", name)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		b := strings.TrimPrefix(strings.TrimSpace(string(data)), "refs/heads/")
		if git.OK("-C", path, "rev-parse", "--verify", "-q", "refs/heads/"+b) {
			return b
		}
	}
	return ""
}

// detach takes each stacked branch's worktree off its branch, at the same
// commit, so the rebase can move the branch. The worktrees must be clean:
// switching back may change their files (a lower branch gains the fixups
// folded into it), and a change or an untracked file there could make that
// fail once keys have been touched. Ignored files may stay; reattach checks
// that none is in the way.
func detach(p *pending, checkouts []checkout) error {
	var mine []checkout
	for _, c := range checkouts {
		if !slices.Contains(p.Stacked, c.branch) {
			continue
		}
		if c.busy {
			return refuse("%s is being rebased or bisected in %s; finish that first", c.branch, c.path)
		}
		dirty, err := git.Run("-C", c.path, "status", "--porcelain")
		if err != nil {
			return refuse("%s is checked out in %s, which git cannot read: %v", c.branch, c.path, err)
		}
		if dirty != "" {
			return refuse("%s is checked out in %s, which has uncommitted or untracked files; commit them "+
				"(unsigned), stash them or remove them there, then run the pass again", c.branch, c.path)
		}
		mine = append(mine, c)
	}
	p.Worktrees = map[string]string{}
	for _, c := range mine {
		if _, err := git.Run("-C", c.path, "switch", "-q", "--detach"); err != nil {
			reattach(p)
			return err
		}
		p.Worktrees[c.branch] = c.path
	}
	return nil
}

// reattach switches each detached worktree back to its branch, wherever the
// rebase has moved it. It refuses rather than lose anything: when the
// worktree has left the commit it was detached at, or when the branch's new
// files would land on a file already there.
func reattach(p *pending) error {
	branches := make([]string, 0, len(p.Worktrees))
	for b := range p.Worktrees {
		branches = append(branches, b)
	}
	sort.Strings(branches)
	for _, b := range branches {
		path := p.Worktrees[b]
		if head, _ := git.Run("-C", path, "symbolic-ref", "-q", "HEAD"); head == "refs/heads/"+b {
			continue
		}
		if head, _ := git.Run("-C", path, "rev-parse", "HEAD"); head != p.Before[b] {
			return refuse("%s was detached from %s for the pass and is now at %s; switch it back to %s by hand, "+
				"keeping what you need, then run `git sign-pass --resume`", path, b, head, b)
		}
		added, err := git.Bytes(nil, "diff", "-z", "--name-only", "--no-renames", "--diff-filter=A", p.Before[b], "refs/heads/"+b)
		if err != nil {
			return err
		}
		for _, f := range strings.Split(string(added), "\x00") {
			if _, err := os.Lstat(filepath.Join(path, f)); f != "" && err == nil {
				return refuse("switching %s back to %s would overwrite %s there; move it away, then run "+
					"`git sign-pass --resume`", path, b, f)
			}
		}
		if _, err := git.Run("-C", path, "switch", "-q", b); err != nil {
			return refuse("switching %s back to %s failed (%v); fix that, then run `git sign-pass --resume`", path, b, err)
		}
	}
	return nil
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

// save records the pass for --resume and keeps the unsigned tip.
func save(p *pending, path, branch string) error {
	if _, err := git.Run("update-ref", BackupRef+branch, "HEAD"); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
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
// move with it, and so does any other worktree they are checked out in, which
// must be clean. With push, every branch that moved and has an upstream is
// pushed over SSH to its own name, guarded by the commit its remote-tracking
// ref names, after checking every destination before the first touch; with
// setUpstream as well, so is every branch that moved and has none (see
// pushMoved).
func Run(base string, resume, push, setUpstream bool) (any, error) {
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
		for b, oid := range before {
			if b != branch && git.OK("merge-base", "--is-ancestor", oid, "HEAD") &&
				!git.OK("merge-base", "--is-ancestor", oid, start) {
				p.Stacked = append(p.Stacked, b)
			}
		}
		sort.Strings(p.Stacked)
		if push || setUpstream {
			if _, _, err := pushTargets(append([]string{branch}, p.Stacked...), setUpstream); err != nil {
				return nil, err
			}
		}
		checkouts, err := otherCheckouts()
		if err != nil {
			return nil, err
		}
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
		if err := detach(&p, checkouts); err != nil {
			return nil, err
		}
		if err := save(&p, path, branch); err != nil {
			reattach(&p)
			return nil, err
		}
		count, _ := git.Run("rev-list", "--count", start+"..HEAD")
		fmt.Fprintf(os.Stderr, "signing %s commits on %s, squashing fixups first if any; touch the key for each when it blinks\n", count, branch)
		args := []string{"rebase", "--force-rebase", "--gpg-sign", "--update-refs"}
		if squash {
			args = append(args, "-i", "--autosquash")
		}
		if git.Attached([]string{"GIT_SEQUENCE_EDITOR=:"}, append(args, start, branch)...) != 0 {
			detached := ""
			if len(p.Worktrees) > 0 {
				detached = "; --resume also switches the worktrees detached for the pass back to their branches"
			}
			return nil, fmt.Errorf("the rebase stopped, usually at a missed key touch. Run `git rebase --continue` "+
				"(touching the key when it blinks), then `git sign-pass --resume`%s. The unsigned branch is kept as "+
				"%s%s", detached, BackupRef, branch)
		}
	}

	if err := reattach(&p); err != nil {
		return nil, err
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
	var stale []string
	for _, b := range p.Stacked {
		if oid, _ := git.Run("rev-parse", "-q", "--verify", "refs/heads/"+b); oid == p.Before[b] {
			stale = append(stale, b)
		}
	}
	if len(stale) > 0 {
		return nil, refuse("these branches still point at unsigned commits the pass replaced: %s; point each at its signed copy on %s "+
			"(`git branch -f BRANCH COMMIT`, or `git reset --keep COMMIT` where it is checked out), then run "+
			"`git sign-pass --resume`", strings.Join(stale, ", "), p.Branch)
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

	if push || setUpstream {
		pushed, err := pushMoved(moved, setUpstream)
		if err != nil {
			return nil, err
		}
		result["pushed"] = pushed
	}
	os.Remove(path)
	return result, nil
}

// target is where one branch is pushed: refs/heads/BRANCH on remote, leased
// on lease, which is empty when the remote must not have the branch yet.
type target struct {
	branch, remote, lease string
	publish               bool
}

// pushTargets decides where each branch goes, or refuses: a branch whose
// upstream is another branch (`git switch -c x origin/main` makes one), and
// any branch that would land on its remote's default branch. Run calls it
// before the first key touch, and pushMoved again before pushing. Branches
// without an upstream are listed in skipped unless setUpstream; a branch
// tracking another branch is refused either way rather than re-pointed.
func pushTargets(branches []string, setUpstream bool) (map[string][]target, map[string]string, error) {
	byRemote := map[string][]target{}
	skipped := map[string]string{}
	for _, b := range branches {
		remote, _ := git.Run("config", "--get", "branch."+b+".remote")
		merge, _ := git.Run("config", "--get", "branch."+b+".merge")
		t := target{branch: b, remote: remote}
		switch {
		case remote == "." && merge != "":
			return nil, nil, refuse("%s tracks the local branch %s, so --push has nowhere to send it; drop that with "+
				"`git branch --unset-upstream %s` and use --set-upstream, or set its remote branch with "+
				"`git branch --set-upstream-to=REMOTE/%s %s`", b, strings.TrimPrefix(merge, "refs/heads/"), b, b, b)
		case remote != "" && merge != "":
			if merge != "refs/heads/"+b {
				fix := fmt.Sprintf("`git branch --unset-upstream %s` and use --set-upstream", b)
				if git.OK("rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+b) {
					fix = fmt.Sprintf("`git branch --set-upstream-to=%s/%s %s`", remote, b, b)
				}
				return nil, nil, refuse("%s tracks %s/%s, a different branch, and --push only pushes a branch to its own "+
					"name; point it at its own with %s", b, remote, strings.TrimPrefix(merge, "refs/heads/"), fix)
			}
			t.lease, _ = git.Run("rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+b)
		case !setUpstream:
			skipped[b] = "not pushed: no upstream"
			continue
		default:
			var err error
			if t.remote, err = pushRemote(b); err != nil {
				return nil, nil, err
			}
			if git.OK("rev-parse", "--verify", "-q", "refs/remotes/"+t.remote+"/"+b) {
				return nil, nil, refuse("%s has no upstream but %s already has a branch %s; if it is this one, run "+
					"`git branch --set-upstream-to=%s/%s %s` and then `git sign-pass --resume --push`", b, t.remote, b, t.remote, b, b)
			}
			// An empty lease pushes only if the remote has no such branch,
			// whatever this repository last fetched.
			t.publish = true
		}
		if slices.Contains(defaultBranches(t.remote), b) {
			return nil, nil, refuse("%s is %s's default branch, which git sign-pass never pushes to; sign without "+
				"--push and push it yourself if that is intended", b, t.remote)
		}
		byRemote[t.remote] = append(byRemote[t.remote], t)
	}
	return byRemote, skipped, nil
}

// defaultBranches are the names remote's default branch may have: the one
// refs/remotes/REMOTE/HEAD points at, or main and master when this repository
// has not recorded it (`git remote set-head REMOTE --auto` records it).
func defaultBranches(remote string) []string {
	if head, err := git.Run("symbolic-ref", "-q", "refs/remotes/"+remote+"/HEAD"); err == nil {
		if name, ok := strings.CutPrefix(head, "refs/remotes/"+remote+"/"); ok {
			return []string{name}
		}
	}
	return []string{"main", "master"}
}

// pushMoved pushes each moved branch that has an upstream, atomically per
// remote, over whatever transport the remote uses (for GitHub, SSH and the
// key), to the branch of the same name, guarded by its remote-tracking ref.
// With setUpstream, a moved branch without an upstream goes in the same push
// to its push remote, only if the remote has no branch of that name, and that
// becomes its upstream. Every branch is checked (pushTargets) before anything
// is pushed, so a refusal leaves the pass for --resume.
func pushMoved(moved map[string]string, setUpstream bool) (map[string]string, error) {
	branches := make([]string, 0, len(moved))
	for b := range moved {
		branches = append(branches, b)
	}
	sort.Strings(branches)
	byRemote, pushed, err := pushTargets(branches, setUpstream)
	if err != nil {
		return nil, err
	}
	remotes := make([]string, 0, len(byRemote))
	for remote := range byRemote {
		remotes = append(remotes, remote)
	}
	sort.Strings(remotes)
	for _, remote := range remotes {
		targets := byRemote[remote]
		args := []string{"push", "--atomic"}
		for _, t := range targets {
			args = append(args, fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", t.branch, t.lease))
		}
		args = append(args, remote)
		for _, t := range targets {
			args = append(args, "refs/heads/"+t.branch+":refs/heads/"+t.branch)
		}
		fmt.Fprintf(os.Stderr, "pushing to %s; touch the key if it blinks\n", remote)
		if git.Attached(nil, args...) != 0 {
			return nil, fmt.Errorf("the push to %s failed; the branches are signed, so push them again by hand", remote)
		}
		for _, t := range targets {
			pushed[t.branch] = remote + " refs/heads/" + t.branch
			if !t.publish {
				continue
			}
			if _, err := git.Run("config", "branch."+t.branch+".remote", remote); err != nil {
				return nil, err
			}
			if _, err := git.Run("config", "branch."+t.branch+".merge", "refs/heads/"+t.branch); err != nil {
				return nil, err
			}
			pushed[t.branch] += ", now its upstream"
		}
	}
	return pushed, nil
}

// pushRemote is where a branch without an upstream is published:
// branch.NAME.pushRemote, then remote.pushDefault, then the only remote, then
// origin.
func pushRemote(branch string) (string, error) {
	for _, key := range []string{"branch." + branch + ".pushRemote", "remote.pushDefault"} {
		if remote, _ := git.Run("config", "--get", key); remote != "" {
			return remote, nil
		}
	}
	out, err := git.Run("remote")
	if err != nil {
		return "", err
	}
	remotes := strings.Fields(out)
	switch {
	case len(remotes) == 1:
		return remotes[0], nil
	case slices.Contains(remotes, "origin"):
		return "origin", nil
	case len(remotes) == 0:
		return "", refuse("%s has no upstream and there is no remote to publish it to", branch)
	}
	return "", refuse("%s has no upstream and it is not clear which of the remotes %s to publish it to; "+
		"choose with `git config remote.pushDefault REMOTE`, then run `git sign-pass --resume --set-upstream`",
		branch, strings.Join(remotes, ", "))
}
