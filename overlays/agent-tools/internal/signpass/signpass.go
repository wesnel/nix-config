// Package signpass re-signs a branch's unsigned commits with the hardware
// key, squashing fixups first, for any repository and any workflow.
package signpass

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/sshmux"
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
	// When fixups are squashed: the commits that remain. Trees are the files
	// of the branches in the range, which the pass changes only by folding
	// into a branch's commits fixups it does not contain: on a branch below
	// the tip, or on any branch of a fork's later child.
	Squash bool              `json:"squash,omitempty"`
	Kept   []history.Commit  `json:"kept,omitempty"`
	Trees  map[string]string `json:"trees,omitempty"`
	// Stacked are the other branches inside the range, which must all move.
	Stacked []string `json:"stacked,omitempty"`
	// Lands is where each stacked branch belongs once the pass is done: the
	// Lands[b]-th commit after Start (see landings).
	Lands map[string]int `json:"lands,omitempty"`
	// Worktrees are where stacked branches are checked out, by branch. Each is
	// detached for the rebase, since --update-refs skips a branch checked out
	// anywhere, and switched back to its branch afterwards.
	Worktrees map[string]string `json:"worktrees,omitempty"`
	// Dir is the worktree the rebase runs in.
	Dir string `json:"dir"`
	// GitPID is the process ID of the last git the pass started there
	// (marking).
	GitPID int `json:"gitPid,omitempty"`
	// For a fork's later child (see plan): the rebase replays Fork..Branch onto
	// Onto, the signed copy of Fork, autosquashing when Fixups, and Shape is
	// what the unsigned rehearsal of that produced. Start is then Onto.
	Onto   string `json:"onto,omitempty"`
	Fork   string `json:"fork,omitempty"`
	Fixups bool   `json:"fixups,omitempty"`
	// Plan is the rest of a multi-branch pass, which --resume carries on with.
	Plan *plan `json:"plan,omitempty"`

	// The push flags this run of a single-branch pass has, which the command
	// it prints for resuming repeats.
	push, setUpstream bool
	// gains are the stacked branches the unsigned rehearsal found gaining
	// fixups they do not contain (see gaining).
	gains []string
}

// resume is the command that resumes p.
func (p *pending) resume() string {
	if p.Plan != nil {
		return resumeCommand(p.Branch, p.Plan.Push, p.Plan.SetUpstream)
	}
	return resumeCommand(p.Branch, p.push, p.setUpstream)
}

// checkout is another worktree's branch: checked out there, or being rebased
// or bisected there (busy), which --update-refs skips either way.
type checkout struct {
	path, branch string
	busy         bool
}

// otherCheckouts lists the branches every worktree but this one holds.
func otherCheckouts() ([]checkout, error) {
	here, err := git.Toplevel()
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
		if _, err := git.RunLong(nil, "-C", c.path, "switch", "-q", "--detach"); err != nil {
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
				"keeping what you need, then run `%s`", path, b, head, b, p.resume())
		}
		added, err := git.Bytes(nil, "diff", "-z", "--name-only", "--no-renames", "--diff-filter=A", p.Before[b], "refs/heads/"+b)
		if err != nil {
			return err
		}
		for _, f := range strings.Split(string(added), "\x00") {
			if _, err := os.Lstat(filepath.Join(path, f)); f != "" && err == nil {
				return refuse("switching %s back to %s would overwrite %s there; move it away, then run "+
					"`%s`", path, b, f, p.resume())
			}
		}
		if _, err := git.RunLong(nil, "-C", path, "switch", "-q", b); err != nil {
			return refuse("switching %s back to %s failed (%v); fix that, then run `%s`", path, b, err, p.resume())
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

// RangeStart is the parent of the oldest commit the pass has to rewrite: the
// oldest unsigned commit, or an older one that a fixup!, amend! or squash!
// folds into, or "" when base..tip has nothing to sign. It also reports
// whether there is anything to fold, and refuses a fold whose target is not
// in base..tip. A commit is unsigned when its object has no signature header
// for the repository's object format, the one git reads (gpgsig, or
// gpgsig-sha256 in a SHA-256 repository): no verifier runs, so pr-loop
// next, which only reads, can call it.
func RangeStart(base, tip string) (string, bool, error) {
	commits, err := readRange(base, tip)
	if err != nil {
		return "", false, err
	}
	oldest, fixups := len(commits), false
	for i, c := range commits {
		if c.unsigned && i < oldest {
			oldest = i
		}
		target, err := foldTarget(commits, i)
		if err != nil {
			return "", false, err
		}
		if target >= 0 {
			fixups = true
			if target < oldest {
				oldest = target
			}
		}
	}
	if oldest == len(commits) {
		return "", false, nil
	}
	return commits[oldest].sha + "^", fixups, nil
}

type rangeCommit struct {
	sha, subject string
	unsigned     bool
}

// readRange is base..tip, oldest first, each commit unsigned when its object
// has no signature header for the repository's object format (RangeStart).
func readRange(base, tip string) ([]rangeCommit, error) {
	format, err := objectGit("rev-parse", "--show-object-format")
	if err != nil {
		return nil, err
	}
	header := "\ngpgsig "
	if format == "sha256" {
		header = "\ngpgsig-sha256 "
	}
	out, err := objectGit("log", "--reverse", "--format=%H%x00%s", base+".."+tip)
	if err != nil {
		return nil, err
	}
	var commits []rangeCommit
	for _, line := range strings.Split(out, "\n") {
		if f := strings.SplitN(line, "\x00", 2); len(f) == 2 {
			raw, err := objectGit("cat-file", "commit", f[0])
			if err != nil {
				return nil, err
			}
			headers, _, _ := strings.Cut(raw, "\n\n")
			commits = append(commits, rangeCommit{f[0], f[1], !strings.Contains("\n"+headers, header)})
		}
	}
	return commits, nil
}

func foldTarget(commits []rangeCommit, i int) (int, error) {
	kind, target := history.Fold(commits[i].subject)
	if kind == history.Kept {
		return -1, nil
	}
	for j := i - 1; j >= 0; j-- {
		if k, _ := history.Fold(commits[j].subject); k == history.Kept && commits[j].subject == target {
			return j, nil
		}
	}
	return -1, refuse("%q folds into a commit that is not on this branch; retarget it", commits[i].subject)
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
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // holds checkouts of the repository, in its git directory
		return "", err
	}
	scratch, err := os.MkdirTemp(dir, "rehearsal-")
	if err != nil {
		return "", err
	}
	os.Remove(scratch)
	if _, err := git.RunLong(nil, "worktree", "add", "--detach", scratch, tip); err != nil {
		return "", err
	}
	defer func() {
		git.RunLong(nil, "-C", scratch, "rebase", "--abort")
		git.RunLong(nil, "worktree", "remove", "--force", scratch)
	}()
	if _, err := git.RunLong(history.NoEditor, append([]string{"-C", scratch, "-c", "commit.gpgsign=false",
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // the pass's branches and steps, in the git directory
		return err
	}
	return os.WriteFile(path, data, 0o644) //nolint:gosec // the pass's branches and steps, in the git directory
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

// pushes are the branches a signed pass has still to push and, once the push
// is planned (planPush), each push URL's share of it. Checked are those the
// pass did not move, as unpushed checked them.
type pushes struct {
	Branches    []string               `json:"branches"`
	SetUpstream bool                   `json:"setUpstream,omitempty"`
	URLs        []urlPush              `json:"urls,omitempty"`
	Skipped     map[string]string      `json:"skipped,omitempty"`
	Checked     map[string]checkedPush `json:"checked,omitempty"`
}

// checkedPush is a branch whose signatures unpushed checked: at Tip, to be
// pushed leased on Lease. Its push goes only from there.
type checkedPush struct {
	Tip   string `json:"tip"`
	Lease string `json:"lease"`
}

// urlPush is what one push URL of Remote receives: Refs, one push each, in
// order. Done is set once the URL has them all. NeedsSnapshot is set only
// for new plans; false, including in legacy state, preserves the saved Old.
type urlPush struct {
	Remote        string    `json:"remote"`
	URL           string    `json:"url"`
	Refs          []refPush `json:"refs"`
	Done          bool      `json:"done,omitempty"`
	NeedsSnapshot bool      `json:"needsSnapshot,omitempty"`
}

// refPush sends New to refs/heads/Branch, leased on Old: what the URL held
// when that URL was first observed, or the original remote-tracking
// expectation in legacy state. Empty means the URL must not have the branch.
// With Publish, the remote's branch becomes Branch's upstream once every
// URL has it. Done is set once this URL has it.
type refPush struct {
	Branch  string `json:"branch"`
	New     string `json:"new"`
	Old     string `json:"old"`
	Publish bool   `json:"publish,omitempty"`
	Done    bool   `json:"done,omitempty"`
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
					"`%s --resume %s`, or remove %s", other.Branch, b, self(), quote(other.Branch), path)
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

// DefaultBase is where branch left the default branch: the remote's when there
// is one, and otherwise the local one, unless that is the branch being signed.
func DefaultBase(branch string) (string, error) {
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
// push, every branch that moved and has an upstream is pushed to its own name
// at each of its remote's push URLs in turn, one branch per push, bottom up,
// guarded by the commit its remote-tracking ref names, after checking every
// destination before the first touch; with setUpstream as well, so is every
// branch that moved and has none (see planPush). With nothing to sign, push
// and setUpstream push the branch as it is when unpushed picks it
// (pushUnmoved). When the key refuses a
// signature or an SSH login, on a terminal the pass asks whether to retry
// that step (retryTouch).
//
// The signing state lasts until every commit is verified; a push that fails
// after that leaves only the branches each URL still lacks, which --resume
// pushes.
func Run(base string, resume, push, setUpstream bool) (any, error) {
	branch, err := git.Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	if rm := rebaseMerge(); resume && exists(rm) {
		return nil, stoppedRebase(rm, push, setUpstream)
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
		p.push, p.setUpstream = push, setUpstream
		// A pass whose first signature the key refused rewrote nothing.
		if tip, _ := git.Run("rev-parse", "refs/heads/"+p.Branch); tip == p.Before[p.Branch] {
			if Brief {
				announceTouch()
			}
			if err := rebase(p); err != nil {
				return nil, err
			}
		}
	} else {
		if exists(path) {
			return nil, refuse("a signing pass on %s is already in progress; finish it with --resume, or remove %s", branch, path)
		}
		if dirty, _ := git.Run("status", "--porcelain", "--untracked-files=no"); dirty != "" {
			return nil, refuse("this worktree has uncommitted changes; commit them (unsigned) first")
		}
		var early map[string]string
		if p, early, err = prepare(branch, base); err != nil {
			return nil, err
		}
		if early != nil {
			if !push && !setUpstream {
				return early, nil
			}
			return pushUnmoved(branch, base, early, setUpstream)
		}
		p.push, p.setUpstream = push, setUpstream
		pushBranches, urls := 0, 0
		if push || setUpstream {
			byRemote, _, err := pushTargets(append([]string{branch}, p.Stacked...), setUpstream, "run the pass again")
			if err != nil {
				return nil, err
			}
			if err := sshmux.Check("run the pass again"); err != nil {
				return nil, err
			}
			pushBranches, urls = pushShare(byRemote)
		}
		announceGains(p.gains)
		if Brief {
			commits := len(p.Shape)
			if p.Squash {
				commits = len(p.Kept)
			}
			announce(commits, 1+len(p.Stacked), pushBranches, urls, signatures(p.Start, "refs/heads/"+branch)+urls)
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
		pushed, err := pushAll(p.Branch, pushes{Branches: branches, SetUpstream: setUpstream})
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
func prepare(branch, base string) (p *pending, early map[string]string, err error) {
	tip := "refs/heads/" + branch
	if base == "" {
		if base, err = DefaultBase(branch); err != nil {
			return nil, nil, err
		}
	}
	start, squash, err := RangeStart(base, tip)
	if err != nil {
		return nil, nil, err
	}
	if start == "" {
		return nil, map[string]string{"branch": branch, "signed": "every commit since the base is already signed"}, nil
	}
	if merges, _ := git.Run("rev-list", "--merges", start+".."+tip); merges != "" {
		return nil, nil, refuse("the commits to sign include merges, which re-signing would flatten; sign them by hand")
	}
	if err := lintMessages(branch, start, tip); err != nil {
		return nil, nil, err
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
	if p.Lands, err = landings(before, p.Stacked, start); err != nil {
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
	head, err := rehearseSquash(start, tip, p.Kept)
	if err != nil {
		return nil, nil, err
	}
	p.gains, err = gaining(p.Lands, before, start, start, head)
	return p, nil, err
}

// landings counts, for each stacked branch, the commits from..its tip leaves
// once its fixup!, amend! and squash! commits fold in. A rebase of from..tip
// that keeps those commits leaves the branch on the last of them: for a
// branch whose tip is a fixup, the nearest commit below the tip that remains.
func landings(before map[string]string, stacked []string, from string) (map[string]int, error) {
	lands := map[string]int{}
	for _, b := range stacked {
		kept, err := keptCommits(from, before[b])
		if err != nil {
			return nil, err
		}
		lands[b] = len(kept)
	}
	return lands, nil
}

// landing is the k-th commit of from..tip, oldest first, or from when k is 0.
func landing(from, tip string, k int) (string, error) {
	if k == 0 {
		return git.Run("rev-parse", from)
	}
	return nth(from, tip, k)
}

// gaining lists the branches of lands that a rewrite of from..their tips in
// before, as newFrom..newTip, leaves on a landing that differs from what
// their own commits fold to (gained). What differs is folds the branch does
// not contain: from above it in its own stack, or, for a fork's later child,
// from the earlier child whose rewrite gave the fork point's copy.
func gaining(lands map[string]int, before map[string]string, from, newFrom, newTip string) ([]string, error) {
	var gains []string
	for b, k := range lands {
		at, err := landing(newFrom, newTip, k)
		if err != nil {
			return nil, err
		}
		if g, err := gained(from, before[b], newFrom, at); err != nil {
			return nil, err
		} else if g {
			gains = append(gains, b)
		}
	}
	sort.Strings(gains)
	return gains, nil
}

// gained reports whether newFrom..at, a rewrite of from..old, has other
// files than old, or other commits or messages than from..old folds to,
// which an amend! or squash! it does not contain can give it.
func gained(from, old, newFrom, at string) (bool, error) {
	if mustTree(at) != mustTree(old) {
		return true, nil
	}
	own, err := keptCommits(from, old)
	if err != nil {
		return false, err
	}
	return history.Match(own, newFrom, at, "") != nil, nil
}

// gainsPhrase says which branches gain folds they do not contain.
func gainsPhrase(gains []string) string {
	if len(gains) == 1 {
		return gains[0] + " gains fixups from commits it does not contain"
	}
	return strings.Join(gains, ", ") + " gain fixups from commits they do not contain"
}

// announceGains names, before the first touch, the branches the pass will
// give folds they do not contain, in a brief pass as in a full one.
func announceGains(gains []string) {
	if len(gains) > 0 {
		fmt.Fprintf(os.Stderr, "%s\n", gainsPhrase(gains))
	}
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

// lintMessages refuses unless every message from..tip leaves, once its
// folds are squashed in, passes history.LintMessage.
func lintMessages(branch, from, tip string) error {
	kept, err := keptCommits(from, tip)
	if err != nil {
		return err
	}
	for _, c := range kept {
		if problems := history.LintMessage(c.Message); len(problems) > 0 {
			return refuse("%s's commit %q would be signed with a message where %s. Reword it with an amend! commit, "+
				"`git -c commit.gpgsign=false commit --allow-empty -m 'amend! SUBJECT' -m 'NEW MESSAGE'`, and run the pass again",
				branch, history.Subject(c.Message), strings.Join(problems, ", and "))
		}
	}
	return nil
}

// sign detaches the worktrees the pass moves, records it, and rebases with the
// key, in the current worktree.
func sign(p *pending, path string) error {
	checkouts, err := otherCheckouts()
	if err != nil {
		return err
	}
	if p.Dir, err = git.Toplevel(); err != nil {
		return err
	}
	if err := detach(p, checkouts); err != nil {
		return err
	}
	if err := save(p, path, p.Branch); err != nil {
		reattach(p)
		return err
	}
	return rebase(p)
}

// signatures is how many the rebase of from..tip asks the key for: one per
// commit, fixups included, since git signs every step of an autosquash, not
// only the commit a fold ends in.
func signatures(from, tip string) int {
	n, _ := git.Run("rev-list", "--count", from+".."+tip)
	var count int
	fmt.Sscan(n, &count)
	return count
}

// rebase runs the pass's rebase with the key, from the start. When the key
// refuses a signature, on a terminal it asks whether to retry (retryTouch),
// and does so from where the rebase stopped (readyRetry); otherwise it
// returns an error that says how to carry on.
func rebase(p *pending) error {
	defer marking(p)()
	upstream := []string{p.Start}
	if p.Onto != "" {
		upstream = []string{"--onto", p.Onto, p.Fork}
	}
	if !Brief {
		count, _ := git.Run("rev-list", "--count", upstream[len(upstream)-1]+"..refs/heads/"+p.Branch)
		fmt.Fprintf(os.Stderr, "signing %s commits on %s, squashing fixups first if any; touch the key for each when it blinks\n", count, p.Branch)
	}
	args := []string{"rebase", "--force-rebase", "--gpg-sign", "--update-refs"}
	if p.Fixups {
		args = append(args, "-i", "--autosquash")
	}
	args = append(append(args, upstream...), p.Branch)
	// The first attempt must not be bounded: the operator may take any time
	// to reach the key, and a key waiting on its PIN is theirs to answer.
	ctx := context.Background()
	code, stderr := runGit(ctx, history.NoEditor, p.Branch, args...)
	aborted := false
	retry := &retrier{}
	folded := false
	for code != 0 && refusedByKey.MatchString(stderr) {
		restart, step, err := readyRetry()
		if err != nil {
			break
		}
		// A refusal in a fold undoes the rebase (readyRetry), and signing it
		// all again is for the operator to choose, on a terminal.
		if folded = restart && step != ""; folded && !isTerminal(os.Stdin) {
			aborted = true
			break
		}
		what := "signing " + p.Branch
		if folded {
			what = fmt.Sprintf("signing %s again from the start, up to %s, as a refusal in a fold of fixup!, "+
				"amend! or squash! commits is not retried in place", p.Branch, touches(rangeSignatures(p)))
		}
		rctx, done, again := retry.next(ctx, what, step, stderr)
		if !again {
			aborted = restart
			break
		}
		if args, rctx, err = oneStep(args, rctx, restart); err != nil {
			done()
			return err
		}
		if restart {
			code, stderr = runGit(rctx, history.NoEditor, p.Branch, args...)
		} else {
			code, stderr = runGit(rctx, history.NoEditor, p.Branch, "rebase", "--continue")
		}
		done()
		if code, stderr, err = settle(p, retry, code, stderr); err != nil {
			return err
		}
	}
	if code == 0 {
		return nil
	}
	// Whatever kept git from writing a commit, the `git rebase --continue`
	// this prints makes it again.
	if rm := rebaseMerge(); !aborted && unwritten.MatchString(stderr) && exists(rm) {
		if _, err := putBackPick(rm); err != nil {
			return err
		}
	}
	unlock := retry.locked()
	cont, also := carryOn(p)
	if aborted {
		what := "the key refused the first signature, usually at a missed touch, so nothing is signed yet"
		if folded {
			what = "the key refused a signature in a fold of fixup!, amend! or squash! commits, which the pass " +
				"does not retry in place, so it undid the rebase: nothing on " + p.Branch + " is signed yet"
		}
		return fmt.Errorf("%s. Run `%s`, touching the key when it blinks (up to %s), to sign from the start%s. "+
			"The unsigned branch is kept as %s%s%s", what, p.resume(), touches(rangeSignatures(p)), also, BackupRef,
			p.Branch, unlock)
	}
	return fmt.Errorf("the rebase stopped, usually at a missed key touch. Run `%s` "+
		"(touching the key when it blinks, up to %s), then `%s`%s. The unsigned branch is kept as "+
		"%s%s%s", cont, touches(todoSignatures()), p.resume(), also, BackupRef, p.Branch, unlock)
}

func touches(n int) string {
	if n == 1 {
		return "1 touch"
	}
	return fmt.Sprintf("%d touches", n)
}

// carryOn is how the operator carries on with p's stopped rebase, and what
// --resume does besides finishing it, with the touches that needs.
func carryOn(p *pending) (cont, also string) {
	cont = "git rebase --continue"
	if p.Plan != nil {
		cont = "git -C " + quote(p.Dir) + " rebase --continue"
	}
	var does []string
	if len(p.Worktrees) > 0 {
		does = append(does, "switches the worktrees detached for the pass back to their branches")
	}
	if p.Plan != nil && p.Plan.Next < len(p.Plan.Steps) {
		does = append(does, "signs the plan's other branches after it, up to "+touches(planSignatures(p.Plan))+" more")
	}
	if len(does) > 0 {
		also = "; --resume also " + strings.Join(does, ", and ")
	}
	return cont, also
}

// planSignatures is how many signatures the steps of pl after the running
// one ask for.
func planSignatures(pl *plan) int {
	n := 0
	for _, s := range pl.Steps[pl.Next:] {
		tip := "refs/heads/" + s.Branch
		if s.From >= 0 {
			n += signatures(s.Fork, tip)
			continue
		}
		base := s.Base
		if base == "" {
			base, _ = DefaultBase(s.Branch)
		}
		if start, _, err := RangeStart(base, tip); err == nil && start != "" {
			n += signatures(start, tip)
		}
	}
	return n
}

// rangeSignatures is how many signatures p's rebase asks for from its start.
func rangeSignatures(p *pending) int {
	from := p.Start
	if p.Onto != "" {
		from = p.Fork
	}
	return signatures(from, "refs/heads/"+p.Branch)
}

// todoSignatures is how many signatures the stopped rebase still asks for:
// one per step left that makes a commit.
func todoSignatures() int {
	n := 0
	for _, line := range todoLines(rebaseMerge()) {
		if f := strings.Fields(line); len(f) > 0 &&
			slices.Contains([]string{"pick", "p", "reword", "r", "edit", "e", "fixup", "f", "squash", "s"}, f[0]) {
			n++
		}
	}
	return n
}

func rebaseMerge() string {
	rm, _ := git.Run("rev-parse", "--path-format=absolute", "--git-path", "rebase-merge")
	return rm
}

// stopAfterFirst is a sequence editor that puts a break after the first step.
const stopAfterFirst = `f() { awk 'NR==1{print;print "break";next}1' "$1" >"$1.sign-pass" && mv "$1.sign-pass" "$1"; }; f`

// oneStep makes a retry run only the step it retries: a --continue stops at
// a break oneStep puts after the step git put back, and a restart, made
// interactive, at the break stopAfterFirst puts after the first. A retry
// runs where an interrupt from the terminal cannot reach it (retrier.next),
// so the steps after it must run as the first attempt did (settle).
func oneStep(args []string, ctx context.Context, restart bool) ([]string, context.Context, error) {
	if !restart {
		rm := rebaseMerge()
		return args, ctx, writeTodo(rm, slices.Insert(todoLines(rm), 1, "break"))
	}
	if !slices.Contains(args, "-i") {
		args = slices.Insert(slices.Clone(args), 1, "-i")
	}
	return args, git.WithEnv(ctx, "GIT_SEQUENCE_EDITOR="+stopAfterFirst), nil
}

// settle finishes a retry. One that was killed (retrier.killed) before git
// exited at its break left git mid-step: settle readies the rebase for `git
// rebase --continue` or undoes it (recoverKilled), and returns an error that
// says how to carry on. One that stopped at the break after its step
// carries on without a bound, in the foreground, as the first attempt did;
// one that failed before the break loses the break, so the rebase carries on
// past it later.
func settle(p *pending, r *retrier, code int, stderr string) (int, string, error) {
	if r.killed != "" && code != 0 {
		return code, stderr, recoverKilled(p, r)
	}
	rm := rebaseMerge()
	if !exists(rm) {
		return code, stderr, nil
	}
	if code != 0 {
		return code, stderr, dropBreak(rm)
	}
	code, stderr = runGit(context.Background(), history.NoEditor, p.Branch, "rebase", "--continue")
	return code, stderr, nil
}

// recoverKilled readies a rebase whose retry r killed mid-step. A pick there
// is put back at the head of the todo list, its staged changes dropped, as
// readyRetry does when the key refuses, so the signatures before
// it stand. Anything else is undone: a step of a fold leaves state behind
// that putting it back would fold twice, and while nothing is rewritten a
// --continue would not sign the first commit (readyRetry).
func recoverKilled(p *pending, r *retrier) error {
	what := "the retried signature " + r.killed
	unlock := r.locked()
	cont, also := carryOn(p)
	rm := rebaseMerge()
	if !exists(rm) {
		return fmt.Errorf("%s; run `%s`%s", what, p.resume(), unlock)
	}
	if back, err := putBack(rm); err != nil {
		return err
	} else if back {
		return fmt.Errorf("%s, so the pass stopped it, and git and the programs it started in their process group "+
			"with it, and put that step back; the commits before it stay signed%s. Run `%s` (touching the key when "+
			"it blinks, up to %s), then `%s`%s. The unsigned branch is kept as %s%s", what, unlock, cont,
			touches(todoSignatures()), p.resume(), also, BackupRef, p.Branch)
	}
	if _, err := git.RunLong(nil, "rebase", "--abort"); err != nil {
		return fmt.Errorf("%s, and undoing the rebase failed (%v); run `git rebase --abort`, then `%s`%s",
			what, err, p.resume(), unlock)
	}
	return fmt.Errorf("%s, so the pass stopped it, and git and the programs it started in their process group "+
		"with it, and undid the rebase: nothing on %s is signed yet%s. Run `%s`, touching the key when it blinks "+
		"(up to %s), to sign from the start%s. The unsigned branch is kept as %s%s", what, p.Branch, unlock,
		p.resume(), touches(rangeSignatures(p)), also, BackupRef, p.Branch)
}

// markerFile is the file in a rebase's state (rebase-merge) that marks it as
// a signing pass's, naming the unsigned tip the pass started from
// (marking). Git removes it with the rest of that state when the rebase
// ends or is aborted.
const markerFile = "sign-pass"

// marking records, until the function it returns is called, the process ID
// of each git started for p in p's saved state, and marks the rebase that
// git leaves stopped as p's. The mark is written only once git has ended:
// written while git ends a rebase, it would keep git from removing the
// rebase's state.
func marking(p *pending) func() {
	rm := rebaseMerge()
	path, err := statePath("pending", p.Branch)
	git.Started = func(pid int) {
		if p.GitPID = pid; err == nil {
			writeJSON(path, p)
		}
	}
	git.Ended = func(int) {
		if exists(rm) {
			os.WriteFile(filepath.Join(rm, markerFile), []byte(p.Before[p.Branch]+"\n"), 0o600)
		}
	}
	return func() { git.Started, git.Ended = nil, nil }
}

// stoppedRebase is the refusal --resume makes on finding the rebase rm holds
// in progress. It changes nothing there, since only the pass that started a
// rebase knows its state (recoverKilled): it refuses outright while a git the
// saved pass started still runs, and otherwise says whether the rebase is
// that pass's, by the mark it left (marking), and how to carry on from it.
func stoppedRebase(rm string, push, setUpstream bool) error {
	headName, _ := os.ReadFile(filepath.Join(rm, "head-name"))
	branch, ok := strings.CutPrefix(strings.TrimSpace(string(headName)), "refs/heads/")
	p := &pending{}
	found := false
	if ok {
		path, err := statePath("pending", branch)
		if err != nil {
			return err
		}
		if found, err = readJSON(path, p); err != nil {
			return err
		}
	}
	p.push, p.setUpstream = push, setUpstream
	if found && p.GitPID > 0 && syscall.Kill(p.GitPID, 0) == nil {
		return refuse("a git the pass started may still be running on %s's rebase: pid %d. Wait for it to end, or "+
			"end it, then run `%s`", p.Branch, p.GitPID, p.resume())
	}
	if lock, _ := git.Run("rev-parse", "--path-format=absolute", "--git-path", "index.lock"); exists(lock) {
		return refuse("another git may still be at work on the rebase in progress here: %s exists. Wait for it to "+
			"end; if no git is running there, remove that file. Then run `%s`", lock, resumeCommand(branch, push, setUpstream))
	}
	if !found {
		return refuse("the rebase in progress here is not one a signing pass left stopped; finish it with `git "+
			"rebase --continue` or undo it with `git rebase --abort`, then run `%s`", resumeCommand(branch, push, setUpstream))
	}
	cont, also := carryOn(p)
	abort := strings.Replace(cont, "--continue", "--abort", 1)
	if mark, _ := os.ReadFile(filepath.Join(rm, markerFile)); strings.TrimSpace(string(mark)) != p.Before[p.Branch] {
		return refuse("the rebase in progress on %s is not one this signing pass left stopped: if it is your own, "+
			"finish it with `%s` or undo it with `%s`; if a git the pass started outlived the pass and left it, undo "+
			"it with `%s`. Then run `%s`, which signs from the start if nothing is signed (up to %s)%s", p.Branch,
			cont, abort, abort, p.resume(), touches(rangeSignatures(p)), also)
	}
	done, _ := os.ReadFile(filepath.Join(rm, "done"))
	lines := strings.Split(strings.TrimSpace(string(done)), "\n")
	last, todo := lines[len(lines)-1], todoLines(rm)
	staged := !git.OK("diff", "--cached", "--quiet")
	switch {
	case last == "break" && !staged, last == todo[0] && !staged && !slices.Contains(todo[1:min(2, len(todo))], "break"):
		return refuse("the pass's rebase is still in progress; run `%s` (touching the key when it blinks, up to %s), "+
			"then `%s`%s", cont, touches(todoSignatures()), p.resume(), also)
	case last == todo[0] && !staged:
		return refuse("the pass's rebase is still in progress, with a break the pass put after its next step; run "+
			"`%s` twice, as it stops at that break (touching the key when it blinks, up to %s), then `%s`%s",
			cont, touches(todoSignatures()), p.resume(), also)
	}
	return refuse("the pass's rebase was stopped mid-step, where it cannot carry on; undo it with `%s`, then run "+
		"`%s`, which signs from the start (touching the key when it blinks, up to %s)%s", abort, p.resume(),
		touches(rangeSignatures(p)), also)
}

// putBack readies a rebase whose git was killed mid-step, which leaves that
// step done but not rescheduled, for `git rebase --continue`, and reports
// whether it did: it takes out a retry's break (oneStep) and, where the step
// is a pick outside a fold and something is rewritten already, puts the step
// back at the head of the todo list, unless git has or had already made
// the step's commit, and drops its staged changes, as readyRetry does for a
// step the key refused.
func putBack(rm string) (bool, error) {
	if err := dropBreak(rm); err != nil {
		return false, err
	}
	done, _ := os.ReadFile(filepath.Join(rm, "done"))
	lines := strings.Split(strings.TrimSpace(string(done)), "\n")
	last, todo := lines[len(lines)-1], todoLines(rm)
	onto, _ := os.ReadFile(filepath.Join(rm, "onto"))
	head, _ := git.Run("rev-parse", "HEAD")
	f := strings.Fields(last)
	rescheduled := last == todo[0]
	if rescheduled {
		todo = todo[1:]
	}
	if len(f) == 0 || f[0] != "pick" && f[0] != "p" || inFold(last, todo) || head == strings.TrimSpace(string(onto)) {
		return false, nil
	}
	if !rescheduled && !made(f[1], head) {
		if err := writeTodo(rm, append([]string{last}, todo...)); err != nil {
			return false, err
		}
	}
	_, err := git.RunLong(nil, "reset", "-q", "--hard")
	return err == nil, err
}

// made reports whether head is the commit a pick of commit makes: the same
// message and the same change.
func made(commit, head string) bool {
	msg := func(c string) string { m, _ := git.Run("log", "-1", "--format=%B", c); return m }
	if msg(commit) != msg(head) {
		return false
	}
	a, err := patchID(commit+"^", commit)
	if err != nil {
		return false
	}
	b, err := patchID(head+"^", head)
	return err == nil && a == b
}

// inFold reports whether step, with next the steps after it, is part of a
// fold: a fixup, amend or squash step (fixup -C is an amend), or the pick of
// the commit the steps after it fold into.
func inFold(step string, next []string) bool {
	folds := func(line string) bool {
		f := strings.Fields(line)
		return len(f) > 0 && slices.Contains([]string{"fixup", "f", "squash", "s"}, f[0])
	}
	if folds(step) {
		return true
	}
	f := strings.Fields(step)
	return len(f) > 0 && (f[0] == "pick" || f[0] == "p") && len(next) > 0 && folds(next[0])
}

func todoLines(rm string) []string {
	todo, _ := os.ReadFile(filepath.Join(rm, "git-rebase-todo"))
	return strings.Split(strings.TrimRight(string(todo), "\n"), "\n")
}

func writeTodo(rm string, lines []string) error {
	return os.WriteFile(filepath.Join(rm, "git-rebase-todo"), []byte(strings.Join(lines, "\n")+"\n"), 0o600) //nolint:gosec // rm is the rebase state git names in its own git directory

}

// dropBreak takes out the break a retry put among the first two steps
// (oneStep) when git has not reached it.
func dropBreak(rm string) error {
	lines := todoLines(rm)
	for i := 0; i < len(lines) && i < 2; i++ {
		if strings.TrimSpace(lines[i]) == "break" {
			return writeTodo(rm, slices.Delete(lines, i, i+1))
		}
	}
	return nil
}

// readyRetry readies a rebase stopped by a signature the key refused for a
// retry, and reports whether it did so by aborting the rebase, which it does
// while the rebase has rewritten nothing: `git rebase --continue` does not
// keep --force-rebase, so it would fast-forward over commits it should sign.
// Otherwise it puts the step back (putBackPick). A refusal anywhere in a
// fold (inFold) is undone too, with step naming where: git keeps the fold's
// message so far outside the todo list, so a step put back folds a message
// in twice, and the commit that ends a fold with an edited message git makes
// in a git commit of its own, whose refusal it does not reschedule. It
// refuses any other stop, so it never drops a step that will not be redone.
// Otherwise step names the step to retry, the same however often git stops
// at it, and is empty on a restart.
func readyRetry() (restart bool, step string, err error) {
	rm, err := git.Run("rev-parse", "--path-format=absolute", "--git-path", "rebase-merge")
	if err != nil {
		return false, "", err
	}
	onto, _ := os.ReadFile(filepath.Join(rm, "onto"))
	if head, _ := git.Run("rev-parse", "HEAD"); head != "" && head == strings.TrimSpace(string(onto)) {
		_, err := git.RunLong(nil, "rebase", "--abort")
		return true, "", err
	}
	last, todo, _ := stoppedAt(rm)
	if inFold(last, todo) {
		_, err := git.RunLong(nil, "rebase", "--abort")
		return true, last, err
	}
	back, err := putBackPick(rm)
	if err == nil && !back {
		err = fmt.Errorf("the rebase stopped at a step it could not sign that is not a pick: %q", last)
	}
	return false, last, err
}

// stoppedAt is the step the rebase rm holds stopped at, the todo list after
// it, and whether git put the step back at the head of the todo list.
func stoppedAt(rm string) (step string, next []string, rescheduled bool) {
	done, _ := os.ReadFile(filepath.Join(rm, "done"))
	lines := strings.Split(strings.TrimSpace(string(done)), "\n")
	step, next = lines[len(lines)-1], todoLines(rm)
	if rescheduled = step != "" && step == next[0]; rescheduled {
		next = next[1:]
	}
	return step, next, rescheduled
}

// putBackPick readies the rebase rm holds, stopped at a pick whose commit
// git could not write, for `git rebase --continue` to make that commit
// again, and reports whether it did. Git leaves the pick's changes staged,
// where they would stop --continue: before 2.56 it puts the pick back at
// the head of the todo list itself, and from 2.56 it stops after the pick as
// at a conflict, so putBackPick puts it back. The worktree was clean when
// the pass began, so the staged changes are the pick's alone, and
// putBackPick drops them. A pick is the first step of any fold it is in, so
// no fold has begun.
func putBackPick(rm string) (bool, error) {
	step, next, rescheduled := stoppedAt(rm)
	if f := strings.Fields(step); len(f) == 0 || f[0] != "pick" && f[0] != "p" {
		return false, nil
	}
	if !rescheduled {
		if err := writeTodo(rm, append([]string{step}, next...)); err != nil {
			return false, err
		}
	}
	_, err := git.RunLong(nil, "reset", "-q", "--hard")
	return err == nil, err
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
	result = map[string]any{"signed": signed, "squashed": p.Squash || p.Fixups, "moved": moved}
	if len(gained) > 0 {
		sort.Strings(gained)
		result["gainedFixups"] = gained
	}
	// The commits are signed whatever the plans say: a pass the plans could
	// fail would leave signed branches unpushed over a convenience.
	if err := recordCommitRewrites(p); err != nil {
		fmt.Fprintf(os.Stderr, "signed, but could not record the commit rewrites in the plans, whose associations still name the unsigned commits: %v\n", err)
	}
	if err := os.Remove(path); err != nil {
		return nil, nil, err
	}
	return result, moved, nil
}

// verify refuses unless the pass left its commits as recorded, all signed,
// with every stacked branch moved to where it belongs (Lands). It lists the
// branches below the tip that gained fixups.
func verify(p *pending) ([]string, error) {
	if p.Squash {
		if err := history.Match(p.Kept, p.Start, "refs/heads/"+p.Branch, p.Branch); err != nil {
			return nil, err
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
			"`%s`", strings.Join(stale, ", "), p.Branch, p.resume())
	}
	var misplaced []string
	for _, b := range p.Stacked {
		k, ok := p.Lands[b]
		if !ok {
			continue
		}
		want, err := landing(p.Start, "refs/heads/"+p.Branch, k)
		if err != nil {
			return nil, err
		}
		if oid, _ := git.Run("rev-parse", "-q", "--verify", "refs/heads/"+b); oid != want {
			misplaced = append(misplaced, b+" belongs at "+want)
		}
	}
	if len(misplaced) > 0 {
		return nil, refuse("these branches are not on the signed copy of the last commit they keep: %s; point each there "+
			"(`git branch -f BRANCH COMMIT`, or `git reset --keep COMMIT` where it is checked out), then run `%s`",
			strings.Join(misplaced, ", "), p.resume())
	}
	// A branch below the tip gains whatever folds it does not contain fold
	// into its commits, which is the point of folding; only the tip of a
	// single stack must keep its files. A fork's later child gains, tip
	// and all, what its fork point's copy gained.
	from := p.Start
	if p.Fork != "" {
		from = p.Fork
	}
	var gains []string
	for b, tree := range p.Trees {
		g := mustTree("refs/heads/"+b) != tree
		if !g && b != p.Branch {
			var err error
			if g, err = gained(from, p.Before[b], p.Start, "refs/heads/"+b); err != nil {
				return nil, err
			}
		}
		switch {
		case !g:
		case b == p.Branch && p.Onto == "":
			return nil, refuse("squashing changed the files on %s; its unsigned version is %s%s", b, BackupRef, b)
		default:
			gains = append(gains, b)
		}
	}
	return gains, nil
}

// pushAll pushes todo for the pass on branch, planning it first (planPush)
// unless it is planned, and records it as that pass's push state before the
// first push and after each branch's, so --resume pushes each URL only the
// branches it lacks, leased as planned. The pushes to one URL share one SSH
// login (sshmux), closed when pushAll returns, which SIGINT or SIGTERM
// makes it do instead of ending the process. Once every URL has its
// branches, it moves their remote-tracking refs and sets the upstreams, as a
// push to the remote by name would have. pushed is each branch's
// destination, or why it was not pushed.
func pushAll(branch string, todo pushes) (pushed map[string]string, err error) {
	path, err := statePath("push", branch)
	if err != nil {
		return nil, err
	}
	retry := "run `" + resumeCommand(branch, true, todo.SetUpstream) + "`"
	if len(todo.URLs) == 0 {
		if todo, err = planPush(todo.Branches, todo.SetUpstream, retry); err != nil {
			return nil, err
		}
	}
	if err := writeJSON(path, todo); err != nil {
		return nil, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mux, err := sshmux.Open(retry)
	if err != nil {
		return nil, err
	}
	defer mux.Close()
	for i := range todo.URLs {
		u := &todo.URLs[i]
		if u.NeedsSnapshot && !u.Done {
			// %v, as for a failed push: a refusal wrapped here would be
			// reported without the command that resumes the push.
			if err := u.snapshot(ctx, mux.Env(i)); err != nil {
				return nil, fmt.Errorf("%v%s; the branches are signed, so fix the cause and %s", err, pushedTo(todo.URLs), retry)
			}
			if err := writeJSON(path, todo); err != nil {
				return nil, err
			}
		}
		for j := range u.Refs {
			if u.Done || u.Refs[j].Done {
				continue
			}
			if err := pushRef(ctx, u.Remote, u.URL, u.Refs[j], mux.Env(i)); err != nil {
				var r refusal.Refusal
				if errors.As(err, &r) {
					return nil, refuse("%v%s; fix the cause and %s", err, pushedTo(todo.URLs), retry)
				}
				return nil, fmt.Errorf("%v%s; the branches are signed, so fix the cause and %s", err, pushedTo(todo.URLs), retry)
			}
			u.Refs[j].Done = true
			if err := writeJSON(path, todo); err != nil {
				return nil, err
			}
		}
		u.Done = true
	}
	pushed = maps.Clone(todo.Skipped)
	if pushed == nil {
		pushed = map[string]string{}
	}
	for _, u := range todo.URLs {
		for _, r := range u.Refs {
			if _, err := git.Run("update-ref", "refs/remotes/"+u.Remote+"/"+r.Branch, r.New); err != nil {
				return nil, err
			}
			pushed[r.Branch] = u.Remote + " refs/heads/" + r.Branch
			if !r.Publish {
				continue
			}
			if _, err := git.Run("config", "branch."+r.Branch+".remote", u.Remote); err != nil {
				return nil, err
			}
			if _, err := git.Run("config", "branch."+r.Branch+".merge", "refs/heads/"+r.Branch); err != nil {
				return nil, err
			}
			pushed[r.Branch] += ", now its upstream"
		}
	}
	return pushed, os.Remove(path)
}

// pushedTo says which branches each URL already has, for a push that failed
// partway.
func pushedTo(urls []urlPush) string {
	var done []string
	for _, u := range urls {
		var bs []string
		for _, r := range u.Refs {
			if u.Done || r.Done {
				bs = append(bs, r.Branch)
			}
		}
		if len(bs) > 0 {
			done = append(done, fmt.Sprintf("pushed %s to %s", strings.Join(bs, ", "), u.URL))
		}
	}
	if len(done) == 0 {
		return ""
	}
	return "; " + strings.Join(done, "; ")
}

// planPush decides where branches go (pushTargets) and splits that by push
// URL of each remote, each sending the branches' commits as they are now to
// the branches of the same names. The remote-tracking expectations are
// checked against each URL before its first push, then its actual leases
// are saved. A branch published with setUpstream is leased on nothing,
// so it lands only where the URL has no branch of that name. Each branch is
// pushed on its own, bottom up: GitHub starts a pull request's workflows
// twice when one push moves it and its base together.
func planPush(branches []string, setUpstream bool, retry string) (pushes, error) {
	byRemote, skipped, err := pushTargets(branches, setUpstream, retry)
	if err != nil {
		return pushes{}, err
	}
	todo := pushes{Branches: branches, SetUpstream: setUpstream, Skipped: skipped}
	for _, remote := range slices.Sorted(maps.Keys(byRemote)) {
		var refs []refPush
		for _, t := range byRemote[remote] {
			oid, err := git.Run("rev-parse", "--verify", "-q", "refs/heads/"+t.branch)
			if err != nil {
				return pushes{}, err
			}
			refs = append(refs, refPush{Branch: t.branch, New: oid, Old: t.lease, Publish: t.publish})
		}
		refs = bottomUp(refs)
		urls, err := git.Run("remote", "get-url", "--push", "--all", remote)
		if err != nil {
			return pushes{}, err
		}
		for _, url := range strings.Split(urls, "\n") {
			if strings.HasPrefix(url, "-") {
				return pushes{}, refuse("%s has a push URL beginning with a dash, %q, which git would take for an option; "+
					"fix it with `git remote set-url --push`, then %s", remote, url, retry)
			}
			if url != "" {
				todo.URLs = append(todo.URLs, urlPush{Remote: remote, URL: url, Refs: slices.Clone(refs), NeedsSnapshot: true})
			}
		}
	}
	return todo, nil
}

// snapshot records the URL's exact old refs once, just before its first push.
// A missing marker in older state preserves the leases that state saved.
func (u *urlPush) snapshot(ctx context.Context, env []string) error {
	args := []string{"ls-remote", "--exit-code", "--refs", u.URL}
	wanted := map[string]bool{}
	for _, r := range u.Refs {
		if !r.Done {
			ref := "refs/heads/" + r.Branch
			args = append(args, ref)
			wanted[ref] = true
		}
	}
	if len(wanted) == 0 {
		u.NeedsSnapshot = false
		return nil
	}
	what := "checking branches at " + u.URL
	retry := &retrier{}
	run := ctx
	var out []byte
	for {
		var err error
		out, err = git.BytesContext(git.WithEnv(run, env...), nil, args...)
		if err == nil {
			break
		}
		if run.Err() != nil {
			return fmt.Errorf("%s: %w: %w", what, run.Err(), err)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 2 {
			break
		}
		var done context.CancelFunc
		var again bool
		if run, done, again = retry.next(ctx, what, "", err.Error()); !again {
			return fmt.Errorf("%s failed%s: %w", what, retry.locked(), err)
		}
		defer done()
	}
	observed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("%s returned an unexpected ref advertisement", what)
		}
		oid := fields[0]
		validOID := (len(oid) == 40 || len(oid) == 64) && strings.Trim(oid, "0123456789abcdef") == ""
		if !wanted[fields[1]] || !validOID || observed[fields[1]] != "" {
			return fmt.Errorf("%s returned an unexpected ref advertisement", what)
		}
		observed[fields[1]] = fields[0]
	}
	for _, r := range u.Refs {
		if r.Done {
			continue
		}
		// A URL may already hold New: another URL of the same repository
		// pushed it, or another client did. A published branch must still
		// be absent.
		if old := observed["refs/heads/"+r.Branch]; old != "" && old != r.Old && (r.Publish || old != r.New) {
			return refuse("%s at %s changed from the expected %.12s to %.12s; fetch and review that destination before pushing", r.Branch, u.URL, r.Old, old)
		}
	}
	for i := range u.Refs {
		if !u.Refs[i].Done {
			u.Refs[i].Old = observed["refs/heads/"+u.Refs[i].Branch]
		}
	}
	u.NeedsSnapshot = false
	return nil
}

// bottomUp orders refs so that each comes before every ref whose commit
// descends from its own, keeping their order otherwise.
func bottomUp(refs []refPush) []refPush {
	left, ordered := slices.Clone(refs), make([]refPush, 0, len(refs))
	for len(left) > 0 {
		next := 0
		for i, r := range left {
			if !slices.ContainsFunc(left, func(o refPush) bool {
				return o.New != r.New && git.OK("merge-base", "--is-ancestor", o.New, r.New)
			}) {
				next = i
				break
			}
		}
		ordered = append(ordered, left[next])
		left = slices.Delete(left, next, next+1)
	}
	return ordered
}

// pushName is the remote pushRef configures for each push, with url as its
// only setting; it is matched as a regular expression, so it holds no
// metacharacters. Git updates no remote-tracking ref for a push to a remote
// without a fetch refspec, where from 2.56 a push to a bare URL moves the
// remote-tracking ref of the one configured remote whose push URLs include
// it; pushAll moves those once every URL has the branches.
const pushName = "git-sign-pass"

// pushNameFree refuses when this repository configures pushName, whose
// settings would join the URL pushRef gives it, ending with retry.
func pushNameFree(retry string) error {
	set, _ := git.Run("config", "--name-only", "--get-regexp", `^remote\.`+pushName+`\.`)
	if set == "" {
		return nil
	}
	if retry != "" {
		retry = ", then " + retry
	}
	return refuse("git sign-pass pushes through a remote named %s that it sets up for each push, but this "+
		"repository already configures one (%s); rename it with `git remote rename %s NAME`%s",
		pushName, strings.ReplaceAll(set, "\n", ", "), pushName, retry)
}

// pushRef pushes r to url, over whatever transport the URL uses (for
// GitHub, SSH and the key) with env added, without touching any
// remote-tracking ref (pushName), once gate lets it. When the key refuses,
// it asks whether to retry (retryTouch).
func pushRef(ctx context.Context, remote, url string, r refPush, env []string) error {
	if err := pushNameFree(""); err != nil {
		return err
	}
	if err := gate(ctx, url, r, env); err != nil {
		return err
	}
	args := []string{"-c", "remote." + pushName + ".url=" + url, "push",
		fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", r.Branch, r.Old), pushName, r.New + ":refs/heads/" + r.Branch}
	if Brief {
		args = slices.Insert(args, 3, "--quiet")
	}
	what := fmt.Sprintf("the push of %s to %s", r.Branch, url)
	retry := &retrier{}
	// The first attempt must not be bounded: the operator may take any time
	// to reach the key, and a key waiting on its PIN is theirs to answer.
	run := ctx
	for {
		if !Brief {
			fmt.Fprintf(os.Stderr, "pushing %s to %s; touch the key if it blinks\n", r.Branch, url)
		}
		code, stderr := runGit(run, env, "", args...)
		if code == 0 {
			if Brief {
				fmt.Fprintf(os.Stderr, "pushed %s -> %s %.7s\n", r.Branch, remote, r.New)
			}
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%s was interrupted", what)
		}
		var done context.CancelFunc
		var again bool
		if run, done, again = retry.next(ctx, what, "", stderr); !again {
			return fmt.Errorf("%s failed%s", what, retry.locked())
		}
		defer done()
	}
}

// retryPush pushes what a signed pass on branch has left to push; with
// setUpstream, which the pass ran without, it also publishes the branches
// that pass left unpushed for want of an upstream.
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
		return nil, refuse("no signing pass or push is in progress on %s; run `%s` without --resume to start one", branch, self())
	}
	if Brief {
		announceTouch()
	}
	if setUpstream && !todo.SetUpstream && len(todo.URLs) > 0 && len(todo.Skipped) > 0 {
		more, err := planPush(slices.Sorted(maps.Keys(todo.Skipped)), true, "run `"+resumeCommand(branch, false, true)+"`")
		if err != nil {
			return nil, err
		}
		todo.URLs, todo.Skipped = append(todo.URLs, more.URLs...), more.Skipped
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
// before the first key touch, and planPush again before pushing. Branches
// without an upstream are listed in skipped unless setUpstream; a branch
// tracking another branch is refused either way rather than re-pointed. Each
// refusal ends by saying to retry, how depending on whether keys have been
// touched yet.
func pushTargets(branches []string, setUpstream bool, retry string) (map[string][]target, map[string]string, error) {
	if err := pushNameFree(retry); err != nil {
		return nil, nil, err
	}
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
				"`git branch --set-upstream-to=%s %s`; then %s", b, strings.TrimPrefix(merge, "refs/heads/"), quote(b), quote("REMOTE/"+b), quote(b), retry)
		case remote != "" && merge != "":
			if merge != "refs/heads/"+b {
				fix := fmt.Sprintf("`git branch --unset-upstream %s` and use --set-upstream", quote(b))
				if git.OK("rev-parse", "--verify", "-q", "refs/remotes/"+remote+"/"+b) {
					fix = fmt.Sprintf("`git branch --set-upstream-to=%s %s`", quote(remote+"/"+b), quote(b))
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
					"`git branch --set-upstream-to=%s %s`, then %s", b, t.remote, b, quote(t.remote+"/"+b), quote(b), retry)
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

// Destination is where a pass that pushes sends a branch: Flag is the push
// flag it needs, --push for a branch whose upstream is its own name on Remote
// or --set-upstream for one with none, and Tracking is the commit of the
// remote-tracking ref the push is leased on, "" when there is none.
type Destination struct {
	Flag, Remote, Tracking string
}

// DestinationOf is branch's Destination, decided as pushTargets decides it,
// or the refusal pushTargets makes for branch, ending with retry. It reads
// only this repository's config and refs.
func DestinationOf(branch, retry string) (Destination, error) {
	byRemote, skipped, err := pushTargets([]string{branch}, false, retry)
	if err != nil {
		return Destination{}, err
	}
	flag := "--push"
	if _, ok := skipped[branch]; ok {
		if byRemote, _, err = pushTargets([]string{branch}, true, retry); err != nil {
			return Destination{}, err
		}
		flag = "--set-upstream"
	}
	for remote, targets := range byRemote {
		return Destination{Flag: flag, Remote: remote, Tracking: targets[0].lease}, nil
	}
	return Destination{}, fmt.Errorf("no push destination for %s", branch)
}

// unpushed picks, of branches a pass leaves where they are, those a push
// would publish or move forward: each with commits since base (where it left
// the default branch, when base is empty) whose push target has no
// remote-tracking ref of its name or one behind its tip. Of the rest, one
// without an upstream (unless setUpstream) or whose remote has commits its tip
// lacks is in skipped with the reason; one the remote has at its tip, or with
// no commits since base, is in neither. pushTargets checks the branches with
// commits, refusing as it does for a moved branch. Their push, as every
// push, goes only through gate.
func unpushed(branches []string, base string, setUpstream bool, retry string) (todo []string, skipped map[string]string, err error) {
	var ahead []string
	for _, b := range branches {
		from := base
		if from == "" {
			if from, err = DefaultBase(b); err != nil {
				return nil, nil, err
			}
		}
		if n, _ := git.Run("rev-list", "--count", from+"..refs/heads/"+b); n != "" && n != "0" {
			ahead = append(ahead, b)
		}
	}
	byRemote, skipped, err := pushTargets(ahead, setUpstream, retry)
	if err != nil {
		return nil, nil, err
	}
	for _, targets := range byRemote {
		for _, t := range targets {
			tip, err := git.Run("rev-parse", "refs/heads/"+t.branch)
			if err != nil {
				return nil, nil, err
			}
			switch {
			case t.lease == tip:
			case t.lease == "" || git.OK("merge-base", "--is-ancestor", t.lease, tip):
				todo = append(todo, t.branch)
			default:
				skipped[t.branch] = fmt.Sprintf("not pushed: %s/%s has commits %s lacks", t.remote, t.branch, t.branch)
			}
		}
	}
	sort.Strings(todo)
	return todo, skipped, nil
}

// pushUnmoved is a pass on branch that found nothing to sign, and early its
// result, with push or setUpstream: it pushes branch as pushAll does when
// unpushed picks it, and adds to early, as pushed, where it went or why it
// did not.
func pushUnmoved(branch, base string, early map[string]string, setUpstream bool) (any, error) {
	retry := "run the pass again"
	todo, pushed, err := unpushed([]string{branch}, base, setUpstream, retry)
	if err != nil {
		return nil, err
	}
	if len(todo) > 0 {
		if err := sshmux.Check(retry); err != nil {
			return nil, err
		}
		if Brief {
			announceTouch()
		}
		done, err := pushAll(branch, pushes{Branches: todo, SetUpstream: setUpstream})
		if err != nil {
			return nil, err
		}
		maps.Copy(pushed, done)
	}
	if len(pushed) == 0 {
		return early, nil
	}
	result := map[string]any{"pushed": pushed}
	for k, v := range early {
		result[k] = v
	}
	return result, nil
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
