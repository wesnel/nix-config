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
	// Dir is the worktree the rebase runs in.
	Dir string `json:"dir"`
	// For a fork's later child (see plan): the rebase replays Fork..Branch onto
	// Onto, the signed copy of Fork, autosquashing when Fixups, and Shape is
	// what the unsigned rehearsal of that produced. Start is then Onto.
	Onto   string `json:"onto,omitempty"`
	Fork   string `json:"fork,omitempty"`
	Fixups bool   `json:"fixups,omitempty"`
	// Plan is the rest of a multi-branch pass, which --resume carries on with.
	Plan *plan `json:"plan,omitempty"`
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
	all, err := checkouts()
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(c checkout) bool {
		resolved, _ := filepath.EvalSymlinks(c.path)
		return resolved == here
	}), nil
}

// checkouts lists the branches every worktree holds.
func checkouts() ([]checkout, error) {
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
		if c.path == "" {
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
// oldest unsigned commit, or an older one that a fixup!, amend! or squash!
// folds into. It also reports whether there is anything to fold.
func rangeStart(base, tip string) (string, bool, error) {
	out, err := git.Run("log", "--reverse", "--format=%H%x00%G?%x00%s", base+".."+tip)
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
		kind, target := history.Fold(c.subject)
		if kind == history.Kept {
			continue
		}
		fixups = true
		found := -1
		for j := i - 1; j >= 0; j-- {
			if k, _ := history.Fold(commits[j].subject); k == history.Kept && commits[j].subject == target {
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
// change shows up before any key touch. It returns the squashed tip.
func rehearseSquash(start, tip string, kept []history.Commit) (string, error) {
	head, err := rehearse("squashing", "retarget that fixup at a later commit", tip,
		"-i", "--autosquash", "--force-rebase", start)
	if err != nil {
		return "", err
	}
	if err := history.Match(kept, start, head, tip); err != nil {
		return "", err
	}
	if a, _ := git.Run("rev-parse", head+"^{tree}"); a != mustTree(tip) {
		return "", refuse("squashing would change the files at %s", tip)
	}
	return head, nil
}

// rehearse runs an unsigned rebase of tip in a throwaway worktree and returns
// where it ends; its commits outlive the worktree. what and fix word a
// refusal.
func rehearse(what, fix, tip string, args ...string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	scratch, err := os.MkdirTemp(dir, "rehearsal-")
	if err != nil {
		return "", err
	}
	os.Remove(scratch)
	if _, err := git.Run("worktree", "add", "--detach", scratch, tip); err != nil {
		return "", err
	}
	defer func() {
		git.Run("-C", scratch, "rebase", "--abort")
		git.Run("worktree", "remove", "--force", scratch)
	}()
	if _, err := git.RunEnv(history.NoEditor, append([]string{"-C", scratch, "-c", "commit.gpgsign=false",
		"-c", "rebase.updateRefs=false", "rebase"}, args...)...); err != nil {
		if m := history.CouldNotApply.FindStringSubmatch(err.Error()); m != nil {
			return "", refuse("%s conflicts at %s %q: %s", what, m[1], m[2], fix)
		}
		return "", refuse("%s failed: %v", what, err)
	}
	return git.Run("-C", scratch, "rev-parse", "HEAD")
}

func mustTree(rev string) string {
	tree, _ := git.Run("rev-parse", rev+"^{tree}")
	return tree
}

// save records the pass for --resume and keeps the unsigned tip.
func save(p *pending, path, branch string) error {
	if _, err := git.Run("update-ref", BackupRef+branch, "refs/heads/"+branch); err != nil {
		return err
	}
	return writeJSON(path, p)
}

func writeJSON(path string, v any) error {
	data, _ := json.MarshalIndent(v, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// readJSON reports false when path does not exist.
func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, v)
}

// statePath is where a pass on branch keeps its state of one kind: "pending"
// while signing, from the first touch until every commit is verified, and
// "push" from then until its branches are pushed. Each pass has its own, so a
// pass on one branch never waits on another.
func statePath(kind, branch string) (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, kind, branch+".json"), nil
}

// pushes are the branches a signed pass has still to push.
type pushes struct {
	Branches    []string `json:"branches"`
	SetUpstream bool     `json:"setUpstream,omitempty"`
}

// inProgress refuses when a signing pass that has not finished involves any of
// branches: rebasing them again would leave its --resume nothing to verify.
func inProgress(branches []string) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	root := filepath.Join(dir, "pending")
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		var other pending
		if _, err := readJSON(path, &other); err != nil {
			return err
		}
		for _, b := range append([]string{other.Branch}, other.Stacked...) {
			if slices.Contains(branches, b) {
				return refuse("a signing pass on %s, which also moves %s, is in progress; finish it with "+
					"`git sign-pass --resume %s`, or remove %s", other.Branch, b, other.Branch, path)
			}
		}
		return nil
	})
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

// defaultBase is where branch left the default branch: the remote's when there
// is one, and otherwise the local one, unless that is the branch being signed.
func defaultBase(branch string) (string, error) {
	for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master",
		"refs/heads/main", "refs/heads/master"} {
		if ref == "refs/heads/"+branch {
			continue
		}
		if git.OK("rev-parse", "--verify", "-q", ref) {
			return git.Run("merge-base", "refs/heads/"+branch, ref)
		}
	}
	return "", refuse("cannot tell where %s starts; pass --base REF", branch)
}

// Run re-signs every commit from the oldest unsigned one on the current
// branch to its tip, with the hardware key, leaving each commit's files,
// author and message exactly as they were, except for the messages an amend!
// or squash! rewrites. Branches stacked inside that range move with it, and so
// does any other worktree they are checked out in, which must be clean. With
// push, every branch that moved and has an upstream is pushed over SSH to its
// own name, guarded by the commit its remote-tracking ref names, after
// checking every destination before the first touch; with setUpstream as well,
// so is every branch that moved and has none (see pushMoved).
//
// The signing state lasts until every commit is verified; a push that fails
// after that leaves only the list of branches to push, which --resume retries.
func Run(base string, resume, push, setUpstream bool) (any, error) {
	branch, err := git.Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	if rm, _ := git.Run("rev-parse", "--git-path", "rebase-merge"); resume && exists(rm) {
		return nil, refuse("the rebase is still in progress; touch the key and run `git rebase --continue`")
	}
	if branch == "HEAD" {
		return nil, refuse("HEAD is detached; switch to the branch to sign")
	}
	path, err := statePath("pending", branch)
	if err != nil {
		return nil, err
	}
	p := &pending{}
	if resume {
		found, err := readJSON(path, p)
		if err != nil {
			return nil, err
		}
		if !found {
			return retryPush(branch, setUpstream)
		}
	} else {
		if exists(path) {
			return nil, refuse("a signing pass on %s is already in progress; finish it with --resume, or remove %s", branch, path)
		}
		if dirty, _ := git.Run("status", "--porcelain", "--untracked-files=no"); dirty != "" {
			return nil, refuse("this worktree has uncommitted changes; commit them (unsigned) first")
		}
		var early any
		if p, early, err = prepare(branch, base); err != nil || early != nil {
			return early, err
		}
		if push || setUpstream {
			if _, _, err := pushTargets(append([]string{branch}, p.Stacked...), setUpstream, "run the pass again"); err != nil {
				return nil, err
			}
		}
		if err := sign(p, path); err != nil {
			return nil, err
		}
	}

	result, moved, err := finish(p, path)
	if err != nil {
		return nil, err
	}
	if p.Plan != nil {
		p.Plan.record(&p.Plan.Steps[p.Plan.Next-1], result, moved)
		return runPlan(p.Plan)
	}
	if push || setUpstream {
		branches := make([]string, 0, len(moved))
		for b := range moved {
			branches = append(branches, b)
		}
		sort.Strings(branches)
		pushed, err := pushAll(p.Branch, pushes{branches, setUpstream})
		if err != nil {
			return nil, err
		}
		result["pushed"] = pushed
	}
	return result, nil
}

// Resume finishes the pass on branch from wherever its rebase ran, or retries
// its push.
func Resume(branch string, push, setUpstream bool) (any, error) {
	path, err := statePath("pending", branch)
	if err != nil {
		return nil, err
	}
	var p pending
	found, err := readJSON(path, &p)
	if err != nil {
		return nil, err
	}
	if !found {
		return retryPush(branch, setUpstream)
	}
	if p.Dir != "" {
		git.Dir = p.Dir
	}
	return Run("", true, push, setUpstream)
}

// prepare checks a pass on branch from base and records what it must leave,
// touching nothing; early is the result when there is nothing to sign.
func prepare(branch, base string) (p *pending, early any, err error) {
	tip := "refs/heads/" + branch
	if base == "" {
		if base, err = defaultBase(branch); err != nil {
			return nil, nil, err
		}
	}
	start, squash, err := rangeStart(base, tip)
	if err != nil {
		return nil, nil, err
	}
	if start == "" {
		return nil, map[string]string{"branch": branch, "signed": "every commit since the base is already signed"}, nil
	}
	if merges, _ := git.Run("rev-list", "--merges", start+".."+tip); merges != "" {
		return nil, nil, refuse("the commits to sign include merges, which re-signing would flatten; sign them by hand")
	}
	before, err := localBranches()
	if err != nil {
		return nil, nil, err
	}
	p = &pending{Branch: branch, Start: start, Before: before, Squash: squash, Fixups: squash,
		Stacked: stackedIn(before, branch, start, tip)}
	if err := inProgress(append([]string{branch}, p.Stacked...)); err != nil {
		return nil, nil, err
	}
	if !squash {
		p.Shape, err = fullShape(start, tip)
		return p, nil, err
	}
	if p.Kept, err = keptCommits(start, tip); err != nil {
		return nil, nil, err
	}
	p.Trees = map[string]string{}
	for b, oid := range before {
		if git.OK("merge-base", "--is-ancestor", start, oid) && git.OK("merge-base", "--is-ancestor", oid, tip) {
			p.Trees[b] = mustTree(oid)
		}
	}
	_, err = rehearseSquash(start, tip, p.Kept)
	return p, nil, err
}

// stackedIn lists the branches other than branch whose tips lie in from..tip.
func stackedIn(before map[string]string, branch, from, tip string) []string {
	var stacked []string
	for b, oid := range before {
		if b != branch && git.OK("merge-base", "--is-ancestor", oid, tip) && !git.OK("merge-base", "--is-ancestor", oid, from) {
			stacked = append(stacked, b)
		}
	}
	sort.Strings(stacked)
	return stacked
}

// keptCommits are the commits from..tip leaves once its fixup!, amend! and
// squash! commits fold in, with the messages they leave.
func keptCommits(from, tip string) ([]history.Commit, error) {
	all, err := history.Between(from, tip)
	if err != nil {
		return nil, err
	}
	return history.Squashed(all), nil
}

// sign detaches the worktrees the pass moves, records it, and rebases with the
// key, in the current worktree.
func sign(p *pending, path string) error {
	checkouts, err := otherCheckouts()
	if err != nil {
		return err
	}
	if p.Dir, err = git.Run("rev-parse", "--show-toplevel"); err != nil {
		return err
	}
	if err := detach(p, checkouts); err != nil {
		return err
	}
	if err := save(p, path, p.Branch); err != nil {
		reattach(p)
		return err
	}
	upstream := []string{p.Start}
	if p.Onto != "" {
		upstream = []string{"--onto", p.Onto, p.Fork}
	}
	count, _ := git.Run("rev-list", "--count", upstream[len(upstream)-1]+"..refs/heads/"+p.Branch)
	fmt.Fprintf(os.Stderr, "signing %s commits on %s, squashing fixups first if any; touch the key for each when it blinks\n", count, p.Branch)
	args := []string{"rebase", "--force-rebase", "--gpg-sign", "--update-refs"}
	if p.Fixups {
		args = append(args, "-i", "--autosquash")
	}
	if git.Attached(history.NoEditor, append(append(args, upstream...), p.Branch)...) != 0 {
		detached := ""
		if len(p.Worktrees) > 0 {
			detached = "; --resume also switches the worktrees detached for the pass back to their branches"
		}
		cont, resume := "git rebase --continue", "git sign-pass --resume"
		if p.Plan != nil {
			cont, resume = "git -C "+p.Dir+" rebase --continue", "git sign-pass --resume "+p.Branch
			detached += ", and then carries on with the rest of the branches"
		}
		return fmt.Errorf("the rebase stopped, usually at a missed key touch. Run `%s` "+
			"(touching the key when it blinks), then `%s`%s. The unsigned branch is kept as "+
			"%s%s", cont, resume, detached, BackupRef, p.Branch)
	}
	return nil
}

// finish verifies a pass whose rebase has ended, switches detached worktrees
// back, and clears its signing state. moved is every branch that moved.
func finish(p *pending, path string) (result map[string]any, moved map[string]string, err error) {
	if err := reattach(p); err != nil {
		return nil, nil, err
	}
	gained, err := verify(p)
	if err != nil {
		return nil, nil, err
	}
	after, err := localBranches()
	if err != nil {
		return nil, nil, err
	}
	moved = map[string]string{}
	for b, oid := range after {
		if old, ok := p.Before[b]; ok && old != oid {
			moved[b] = oid
		}
	}
	signed := len(p.Shape)
	if p.Squash {
		signed = len(p.Kept)
	}
	result = map[string]any{"signed": signed, "squashed": p.Squash, "moved": moved}
	if len(gained) > 0 {
		sort.Strings(gained)
		result["gainedFixups"] = gained
	}
	if err := os.Remove(path); err != nil {
		return nil, nil, err
	}
	return result, moved, nil
}

// verify refuses unless the pass left its commits as recorded, all signed,
// with every stacked branch moved. It lists the branches below the tip that
// gained fixups.
func verify(p *pending) ([]string, error) {
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
	return gained, nil
}

// pushAll records branches as still to push, then pushes them, forgetting
// each remote's branches once it has them, so --resume on branch retries only
// what is left.
func pushAll(branch string, todo pushes) (map[string]string, error) {
	path, err := statePath("push", branch)
	if err != nil {
		return nil, err
	}
	if err := writeJSON(path, todo); err != nil {
		return nil, err
	}
	retry := fmt.Sprintf("run `git sign-pass --resume %s`", branch)
	pushed, err := pushMoved(todo.Branches, todo.SetUpstream, retry, func(done []string) error {
		todo.Branches = slices.DeleteFunc(todo.Branches, func(b string) bool { return slices.Contains(done, b) })
		return writeJSON(path, todo)
	})
	if err != nil {
		return nil, err
	}
	return pushed, os.Remove(path)
}

// retryPush pushes what a signed pass on branch has left to push.
func retryPush(branch string, setUpstream bool) (any, error) {
	path, err := statePath("push", branch)
	if err != nil {
		return nil, err
	}
	var todo pushes
	found, err := readJSON(path, &todo)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, refuse("no signing pass or push is in progress on %s; run `git sign-pass` without --resume to start one", branch)
	}
	todo.SetUpstream = todo.SetUpstream || setUpstream
	pushed, err := pushAll(branch, todo)
	if err != nil {
		return nil, err
	}
	return map[string]any{"pushed": pushed}, nil
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
// tracking another branch is refused either way rather than re-pointed. Each
// refusal ends by saying to retry, how depending on whether keys have been
// touched yet.
func pushTargets(branches []string, setUpstream bool, retry string) (map[string][]target, map[string]string, error) {
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
				"`git branch --set-upstream-to=REMOTE/%s %s`; then %s", b, strings.TrimPrefix(merge, "refs/heads/"), b, b, b, retry)
		case remote != "" && merge != "":
			if merge != "refs/heads/"+b {
				fix := fmt.Sprintf("`git branch --unset-upstream %s` and use --set-upstream", b)
				if git.OK("rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+b) {
					fix = fmt.Sprintf("`git branch --set-upstream-to=%s/%s %s`", remote, b, b)
				}
				return nil, nil, refuse("%s tracks %s/%s, a different branch, and --push only pushes a branch to its own "+
					"name; point it at its own with %s, then %s", b, remote, strings.TrimPrefix(merge, "refs/heads/"), fix, retry)
			}
			t.lease, _ = git.Run("rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+b)
		case !setUpstream:
			skipped[b] = "not pushed: no upstream"
			continue
		default:
			var err error
			if t.remote, err = pushRemote(b, retry); err != nil {
				return nil, nil, err
			}
			if git.OK("rev-parse", "--verify", "-q", "refs/remotes/"+t.remote+"/"+b) {
				return nil, nil, refuse("%s has no upstream but %s already has a branch %s; if it is this one, run "+
					"`git branch --set-upstream-to=%s/%s %s`, then %s", b, t.remote, b, t.remote, b, b, retry)
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
// is pushed. done hears which branches each successful push sent.
func pushMoved(branches []string, setUpstream bool, retry string, done func([]string) error) (map[string]string, error) {
	byRemote, pushed, err := pushTargets(branches, setUpstream, retry)
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
			return nil, fmt.Errorf("the push to %s failed; the branches are signed, so fix the cause and %s", remote, retry)
		}
		sent := make([]string, 0, len(targets))
		for _, t := range targets {
			sent = append(sent, t.branch)
		}
		if err := done(sent); err != nil {
			return nil, err
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
func pushRemote(branch, retry string) (string, error) {
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
		"choose with `git config remote.pushDefault REMOTE`, then %s", branch, strings.Join(remotes, ", "), retry)
}
