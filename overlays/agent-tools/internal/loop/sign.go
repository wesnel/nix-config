package loop

import (
	gocontext "context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/sshmux"
)

// Sign squashes the loop's fixups into the PR's own commits, signs every
// commit with the hardware key, and pushes the stack over SSH, one branch per
// push, bottom up. It refuses to push unless every branch ends with exactly
// its original commits and exactly the files the loop left it with.
func Sign(resume, noPush bool) (any, error) {
	state, err := loadState(true)
	if err != nil {
		return nil, err
	}
	repo, err := repository()
	if err != nil {
		return nil, err
	}
	branches := state.Order
	top := branches[len(branches)-1]

	if resume {
		if state.Signing == nil {
			return nil, refuse("no signing pass is in progress; run `pr-loop sign` without --resume to start one")
		}
		if path, _ := git.Run("rev-parse", "--git-path", "rebase-merge"); exists(path) {
			return nil, refuse("the rebase is still in progress; touch the key and run `git rebase --continue`")
		}
	} else {
		if err := signable(state); err != nil {
			return nil, err
		}
		if !noPush {
			if err := sshmux.Check("run `pr-loop sign` again"); err != nil {
				return nil, err
			}
		}
		parent, err := state.parentRef(repo, branches[0])
		if err != nil {
			return nil, err
		}
		upstream, err := git.Run("merge-base", "refs/heads/"+branches[0], parent)
		if err != nil {
			return nil, err
		}
		if merges, _ := git.Run("rev-list", "--merges", upstream+"..refs/heads/"+top); merges != "" {
			return nil, refuse("the stack contains merge commits, which squashing would flatten; sign it by hand")
		}
		signing := &Signing{Upstream: upstream, Trees: map[string]string{}}
		for _, b := range branches {
			if signing.Trees[b], err = git.Run("rev-parse", "refs/heads/"+b+"^{tree}"); err != nil {
				return nil, err
			}
		}
		if err := rehearse(state, signing); err != nil {
			return nil, err
		}
		for _, b := range branches {
			if _, err := git.Run("update-ref", "refs/pr-loop/backup/"+b, "refs/heads/"+b); err != nil {
				return nil, err
			}
		}
		state.Signing = signing
		if err := state.save(); err != nil {
			return nil, err
		}
		fmt.Fprintln(os.Stderr, "squashing and signing; touch the key for each commit when it blinks")
		code := git.Attached(gocontext.Background(), noEditor, "rebase", "-i", "--autosquash", "--force-rebase",
			"--gpg-sign", "--update-refs", upstream, top)
		if code != 0 {
			return nil, fmt.Errorf("the rebase stopped, usually at a missed key touch. Run `git rebase --continue` " +
				"(touching the key when it blinks), then `pr-loop sign --resume`. The unsigned branches are " +
				"kept under refs/pr-loop/backup/")
		}
	}

	signing := state.Signing
	for i, b := range branches {
		base := signing.Upstream
		if i > 0 {
			base = "refs/heads/" + branches[i-1]
		}
		if err := matches(state.Branches[b].Commits, base, "refs/heads/"+b, b); err != nil {
			return nil, err
		}
		if tree, _ := git.Run("rev-parse", "refs/heads/"+b+"^{tree}"); tree != signing.Trees[b] {
			return nil, refuse("%s's files changed while being signed; its unsigned version is refs/pr-loop/backup/%s", b, b)
		}
	}
	marks, err := git.Run("log", "--format=%h %G?", signing.Upstream+"..refs/heads/"+top)
	if err != nil {
		return nil, err
	}
	var unsigned []string
	for _, line := range strings.Split(marks, "\n") {
		if fields := strings.Fields(line); len(fields) == 2 && fields[1] != "G" && fields[1] != "U" {
			unsigned = append(unsigned, line)
		}
	}
	if len(unsigned) > 0 {
		return nil, refuse("commits without a good signature: %s", strings.Join(unsigned, ", "))
	}
	result := map[string]any{"verified": "every branch has its original commits and the same files, and every commit is signed"}
	if noPush {
		record(state, plans.Event{Kind: "loop-signed", Summary: "signed " + strings.Join(branches, " ") + ", not pushed"})
		return result, nil
	}

	// One branch per push, bottom up: GitHub starts a pull request's
	// workflows twice when one push moves it and its base together. The
	// pushes share one SSH login, and each branch pushed is recorded at once,
	// so --resume pushes only the rest. SIGINT or SIGTERM stops the push and
	// returns, so the shared login is closed instead of left to persist.
	retry := "rerun `pr-loop sign --resume`"
	ctx, stop := signal.NotifyContext(gocontext.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mux, err := sshmux.Open(retry)
	if err != nil {
		return nil, err
	}
	defer mux.Close()
	url := fmt.Sprintf("git@github.com:%s.git", state.Repo)
	pushed := map[string]string{}
	for _, b := range branches {
		oid, err := git.Run("rev-parse", "refs/heads/"+b)
		if err != nil {
			return nil, err
		}
		if oid != state.Branches[b].RemoteOid {
			fmt.Fprintf(os.Stderr, "pushing %s over SSH; touch the key if it blinks\n", b)
			if git.Attached(ctx, mux.Env(0), "push", fmt.Sprintf("--force-with-lease=refs/heads/%s:%s", b, state.Branches[b].RemoteOid),
				url, "refs/heads/"+b+":refs/heads/"+b) != 0 {
				return nil, fmt.Errorf("the signed push of %s failed; %s to retry it and push the branches above it", b, retry)
			}
			state.Branches[b].RemoteOid = oid
			if err := recordTracking(repo, b, oid); err != nil {
				return nil, err
			}
			if err := state.save(); err != nil {
				return nil, err
			}
		}
		pushed[b] = oid
	}
	record(state, plans.Event{Kind: "loop-signed", Summary: "signed and pushed " + branchTips(state, pushed)})
	state.Signing = nil
	state.Round++
	if err := snapshotRound(state); err != nil {
		return nil, err
	}
	result["pushed"] = pushed
	return result, state.save()
}

// signable checks the stack can be rewritten in this worktree.
func signable(state *State) error {
	for i := 1; i < len(state.Order); i++ {
		if state.Branches[state.Order[i]].Base != state.Order[i-1] {
			return refuse("signing needs a linear stack; sign each chain separately")
		}
	}
	if dirty, _ := git.Run("status", "--porcelain", "--untracked-files=no"); dirty != "" {
		return refuse("this worktree has uncommitted changes")
	}
	here, err := git.Toplevel()
	if err != nil {
		return err
	}
	here, _ = filepath.EvalSymlinks(here)
	list, err := git.Run("worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	for _, block := range strings.Split(list, "\n\n") {
		var path, branch string
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "worktree "); ok {
				path = v
			}
			if v, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
				branch = v
			}
		}
		resolved, _ := filepath.EvalSymlinks(path)
		if state.Branches[branch] != nil && resolved != here {
			return refuse("%s is checked out in %s; remove that worktree first", branch, path)
		}
	}
	return nil
}

// rehearse squashes the stack, unsigned, in a throwaway worktree and checks
// the result, so a conflict or a stray commit shows up before any key touch.
func rehearse(state *State, signing *Signing) error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // holds checkouts of the repository, in its git directory
		return err
	}
	scratch, err := os.MkdirTemp(dir, "rehearsal-")
	if err != nil {
		return err
	}
	os.Remove(scratch)
	top := state.Order[len(state.Order)-1]
	if _, err := git.Run("worktree", "add", "--detach", scratch, "refs/heads/"+top); err != nil {
		return err
	}
	defer func() {
		git.Run("-C", scratch, "rebase", "--abort")
		git.Run("worktree", "remove", "--force", scratch)
	}()
	// rebase.updateRefs off: a user's rebase.updateRefs=true would move every
	// branch of the stack not checked out elsewhere onto these unsigned
	// commits, so the backups and the signed rebase would start from them.
	_, err = git.RunEnv(noEditor, "-C", scratch, "-c", "commit.gpgsign=false", "-c", "rebase.updateRefs=false",
		"rebase", "-i", "--autosquash", "--force-rebase", signing.Upstream)
	if err != nil {
		if m := couldNotApply.FindStringSubmatch(err.Error()); m != nil {
			return refuse("squashing conflicts at %s %q: retarget that fixup at a later commit (or the PR's last one) and push again", m[1], m[2])
		}
		return refuse("squashing the fixups failed: %v", err)
	}
	// The rehearsal leaves the branches alone, so each branch's end is found
	// by counting its original commits.
	list, err := git.Run("-C", scratch, "rev-list", "--reverse", signing.Upstream+"..HEAD")
	if err != nil {
		return err
	}
	squashed := strings.Fields(list)
	at := 0
	for _, b := range state.Order {
		want := state.Branches[b].Commits
		if at+len(want) > len(squashed) {
			return refuse("squashing leaves %s with fewer commits than it began with", b)
		}
		base := signing.Upstream
		if at > 0 {
			base = squashed[at-1]
		}
		end := squashed[at+len(want)-1]
		if err := matches(want, base, end, b); err != nil {
			return err
		}
		if tree, _ := git.Run("rev-parse", end+"^{tree}"); tree != signing.Trees[b] {
			return refuse("squashing changes %s's files; a fixup is probably in the wrong branch", b)
		}
		at += len(want)
	}
	if at != len(squashed) {
		return refuse("squashing leaves %d commits that belong to no branch", len(squashed)-at)
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
