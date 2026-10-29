package signpass

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/sshmux"
)

// plan is a pass over several branches, run as one step per stack. A step
// signs a branch and every branch stacked below it, from wherever that branch
// is checked out, or from a throwaway worktree. When a later step's branch
// shares commits with an earlier step's, which a fork does (a parent with two
// children), the earlier step has already replaced those commits, so the later
// one replays only its own commits, from the commit it shares (its fork
// point, found before anything moved), onto that commit's signed copy, and
// signs them in the same rebase. A remembered parent tip would replay any
// parent commit added after the fork as a duplicate; the fork point cannot.
type plan struct {
	Steps []step `json:"steps"`
	// Next is the step after the one running.
	Next int `json:"next"`
	// Key is the branch whose push state holds the plan's push.
	Key         string   `json:"key"`
	Push        bool     `json:"push,omitempty"`
	SetUpstream bool     `json:"setUpstream,omitempty"`
	Moved       []string `json:"moved,omitempty"`
	// Unmoved are the named branches the pass leaves where they are that it
	// pushes too (unpushed); Held says why it pushes some others not.
	Unmoved []string          `json:"unmoved,omitempty"`
	Held    map[string]string `json:"held,omitempty"`
	Results map[string]any    `json:"results"`
}

// step signs one stack: from the oldest unsigned commit above Base, as a
// single-branch pass does, or, when From is an earlier step, by replaying
// Fork..Branch onto the signed copy of Fork, the Kept-th commit kept in step
// From's rewritten commits. OntoTree and Shape are what the unsigned
// rehearsal of that replay found, which the real one must match.
type step struct {
	Branch string `json:"branch"`
	Base   string `json:"base,omitempty"`
	// Dir is where Branch is checked out, or empty for a throwaway worktree,
	// which is Temp while it exists.
	Dir      string   `json:"dir,omitempty"`
	Temp     string   `json:"temp,omitempty"`
	From     int      `json:"from"`
	Fork     string   `json:"fork,omitempty"`
	Kept     int      `json:"kept,omitempty"`
	OntoTree string   `json:"ontoTree,omitempty"`
	Shape    []string `json:"shape,omitempty"`
	Fixups   bool     `json:"fixups,omitempty"`
	// NewFrom is the parent of the commits the step rewrote, once it has run.
	NewFrom string `json:"newFrom,omitempty"`

	// While planning: the step's commits before and after its rehearsal.
	from, tip, rehearsedFrom, rehearsedTip string
}

// Plan signs several branches in one pass, each from wherever it is checked
// out, and prints what it will do before the first key touch. Every check that
// can refuse runs first, including an unsigned rehearsal of every squash and
// every fork's replay. With push or setUpstream, every branch that moved is
// pushed at the end, as a single-branch pass pushes (pushAll), and so is each
// named branch that did not move when unpushed picks it.
func Plan(base string, names []string, push, setUpstream bool) (any, error) {
	if base != "" && len(names) > 1 {
		return nil, refuse("--base applies to one branch; sign branches with different bases separately")
	}
	before, err := localBranches()
	if err != nil {
		return nil, err
	}
	var named []string
	for _, n := range names {
		if _, ok := before[n]; !ok {
			return nil, refuse("there is no local branch %s", n)
		}
		if !slices.Contains(named, n) {
			named = append(named, n)
		}
	}
	// A branch inside another named branch's stack moves with it.
	var tips []string
	for _, n := range named {
		inside := false
		for _, m := range named {
			if m != n && git.OK("merge-base", "--is-ancestor", before[n], before[m]) && (before[n] != before[m] || m < n) {
				inside = true
			}
		}
		if !inside {
			tips = append(tips, n)
		}
	}
	tips, err = orderTops(base, tips, before)
	if err != nil {
		return nil, err
	}
	all, err := checkouts()
	if err != nil {
		return nil, err
	}
	where := map[string]checkout{}
	for _, c := range all {
		where[c.branch] = c
	}

	pl := &plan{Key: named[0], Push: push, SetUpstream: setUpstream, Results: map[string]any{}}
	var lines, moving, allGains []string
	touches, commits, pushBranches, pushURLs := 0, 0, 0, 0
	for _, t := range tips {
		s := step{Branch: t, From: -1, tip: before[t]}
		b := base
		if b == "" {
			if b, err = DefaultBase(t); err != nil {
				return nil, err
			}
		}
		start, squash, err := RangeStart(b, s.tip)
		if err != nil {
			return nil, err
		}
		if start == "" {
			pl.Results[t] = map[string]string{"branch": t, "signed": "every commit since the base is already signed"}
			lines = append(lines, fmt.Sprintf("%s: already signed", t))
			continue
		}
		if s.from, err = git.Run("rev-parse", start); err != nil {
			return nil, err
		}
		for i, e := range pl.Steps {
			m, err := git.Run("merge-base", s.tip, e.tip)
			if err != nil || git.OK("merge-base", "--is-ancestor", m, e.from) {
				continue
			}
			if s.From < 0 || git.OK("merge-base", "--is-ancestor", s.from, m) {
				s.From, s.from = i, m
			}
		}
		if merges, _ := git.Run("rev-list", "--merges", s.from+".."+s.tip); merges != "" {
			return nil, refuse("%s's commits to sign include merges, which re-signing would flatten; sign them by hand", t)
		}
		if err := lintMessages(t, s.from, s.tip); err != nil {
			return nil, err
		}
		var signs int
		if s.From < 0 {
			s.Base = b
			if signs, err = rehearseStep(&s, squash); err != nil {
				return nil, err
			}
		} else if signs, err = rehearseFork(&s, &pl.Steps[s.From]); err != nil {
			return nil, err
		}
		stacked := stackedIn(before, t, s.from, s.tip)
		if err := inProgress(append([]string{t}, stacked...)); err != nil {
			return nil, err
		}
		if c, ok := where[t]; ok {
			if err := checkClean(c, true); err != nil {
				return nil, err
			}
			s.Dir = c.path
		}
		for _, m := range stacked {
			if c, ok := where[m]; ok {
				if err := checkClean(c, false); err != nil {
					return nil, err
				}
			}
		}
		lands, err := landings(before, stacked, s.from)
		if err != nil {
			return nil, err
		}
		gains, err := gaining(lands, before, s.from, s.rehearsedFrom, s.rehearsedTip)
		if err != nil {
			return nil, err
		}
		// Only a fork's later child may end with other files at its tip.
		if mustTree(s.rehearsedTip) != mustTree(s.tip) {
			gains = append(gains, t)
		}
		allGains = append(allGains, gains...)
		touches += signatures(s.from, s.tip)
		commits += signs
		moving = append(append(moving, t), stacked...)
		lines = append(lines, describe(s, pl.Steps, signs, stacked, gains))
		pl.Steps = append(pl.Steps, s)
	}

	if push || setUpstream {
		var unmoved []string
		for _, n := range named {
			if !slices.Contains(moving, n) {
				unmoved = append(unmoved, n)
			}
		}
		if pl.Unmoved, pl.Held, err = unpushed(unmoved, base, setUpstream, "run the pass again"); err != nil {
			return nil, err
		}
		byRemote, skipped, err := pushTargets(slices.Concat(moving, pl.Unmoved), setUpstream, "run the pass again")
		if err != nil {
			return nil, err
		}
		if err := sshmux.Check("run the pass again"); err != nil {
			return nil, err
		}
		pushBranches, pushURLs = pushShare(byRemote)
		remotes := make([]string, 0, len(byRemote))
		for r := range byRemote {
			remotes = append(remotes, r)
		}
		sort.Strings(remotes)
		for _, r := range remotes {
			var bs []string
			for _, t := range byRemote[r] {
				bs = append(bs, t.branch)
			}
			sort.Strings(bs)
			urls, _ := git.Run("remote", "get-url", "--push", "--all", r)
			n := len(strings.Fields(urls))
			touches += n
			lines = append(lines, fmt.Sprintf("push to %s (%d URL(s)): %s", r, n, strings.Join(bs, ", ")))
		}
		for _, b := range slices.Sorted(maps.Keys(skipped)) {
			lines = append(lines, fmt.Sprintf("%s: not pushed, no upstream", b))
		}
		for _, b := range slices.Sorted(maps.Keys(pl.Held)) {
			lines = append(lines, fmt.Sprintf("%s: %s", b, pl.Held[b]))
		}
	}
	lines = append(lines, fmt.Sprintf("key touches: up to %d", touches))
	if Brief {
		sort.Strings(allGains)
		announceGains(allGains)
		announce(commits, len(moving), pushBranches, pushURLs, touches)
	} else {
		fmt.Fprintf(os.Stderr, "git sign-pass plan:\n  %s\n", strings.Join(lines, "\n  "))
	}
	pl.Results["plan"] = lines
	return runPlan(pl)
}

// orderTops puts a fold's carrier before a sibling that needs its target's
// folded copy. Shared folds and folds into a carrier's own tail need no order.
func orderTops(base string, tips []string, before map[string]string) ([]string, error) {
	type fold struct{ commit, target string }
	type top struct {
		name     string
		commits  map[string]bool
		folds    []fold
		rewrites bool
	}
	sort.Strings(tips)
	var tops []top
	for _, name := range tips {
		b := base
		if b == "" {
			var err error
			if b, err = DefaultBase(name); err != nil {
				return nil, err
			}
		}
		commits, err := readRange(b, before[name])
		if err != nil {
			return nil, err
		}
		t := top{name: name, commits: map[string]bool{}}
		for i, c := range commits {
			t.commits[c.sha] = true
			t.rewrites = t.rewrites || c.unsigned
			at, err := foldTarget(commits, i)
			if err != nil {
				return nil, err
			}
			if at >= 0 {
				t.folds = append(t.folds, fold{c.sha, commits[at].sha})
				t.rewrites = true
			}
		}
		tops = append(tops, t)
	}
	predecessors := map[string]map[string]bool{}
	for _, carrier := range tops {
		for _, f := range carrier.folds {
			for _, sibling := range tops {
				if !sibling.commits[f.target] || sibling.commits[f.commit] {
					continue
				}
				if !sibling.rewrites {
					return nil, refuse("%s is already signed but needs %s's fold into %.12s; restack it onto that folded history before signing these tops together", sibling.name, carrier.name, f.target)
				}
				if predecessors[sibling.name] == nil {
					predecessors[sibling.name] = map[string]bool{}
				}
				predecessors[sibling.name][carrier.name] = true
			}
		}
	}
	var ordered []string
	done := map[string]bool{}
	for len(ordered) < len(tops) {
		ready := ""
		for _, t := range tops {
			if done[t.name] {
				continue
			}
			blocked := false
			for p := range predecessors[t.name] {
				blocked = blocked || !done[p]
			}
			if !blocked {
				ready = t.name
				break
			}
		}
		if ready == "" {
			var remaining []string
			for _, t := range tops {
				if !done[t.name] {
					remaining = append(remaining, t.name)
				}
			}
			return nil, refuse("cannot order shared folds on %s; their carriers require each other first. Restack the folds into shared history before signing these tops together", strings.Join(remaining, ", "))
		}
		ordered = append(ordered, ready)
		done[ready] = true
	}
	return ordered, nil
}

// rehearseStep checks a step that shares nothing with an earlier one, as a
// single-branch pass does, and returns how many commits it signs.
func rehearseStep(s *step, squash bool) (int, error) {
	s.rehearsedFrom, s.rehearsedTip = s.from, s.tip
	if !squash {
		n, err := git.Run("rev-list", "--count", s.from+".."+s.tip)
		var count int
		fmt.Sscan(n, &count)
		return count, err
	}
	kept, err := keptCommits(s.from, s.tip)
	if err != nil {
		return 0, err
	}
	s.rehearsedTip, err = rehearseSquash(s.from, s.tip, kept)
	return len(kept), err
}

// rehearseFork replays a fork's later child onto the rehearsed copy of its
// fork point, unsigned, and checks that the child gains exactly what that
// copy gained over the fork point: nothing, unless a fixup above the fork
// point folded into a commit below it.
func rehearseFork(s *step, e *step) (int, error) {
	if _, _, err := RangeStart(s.from, s.tip); err != nil {
		return 0, err
	}
	kept, err := keptCommits(e.from, s.from)
	if err != nil {
		return 0, err
	}
	s.Fork, s.Kept = s.from, len(kept)
	onto, err := nth(e.rehearsedFrom, e.rehearsedTip, s.Kept)
	if err != nil {
		return 0, err
	}
	own, err := keptCommits(s.Fork, s.tip)
	if err != nil {
		return 0, err
	}
	all, _ := git.Run("rev-list", "--count", s.Fork+".."+s.tip)
	s.Fixups = all != fmt.Sprint(len(own))
	what := fmt.Sprintf("replaying %s onto the signed copy of its fork point %.12s", s.Branch, s.Fork)
	args := []string{"--force-rebase", "--onto", onto, s.Fork}
	if s.Fixups {
		args = append([]string{"-i", "--autosquash"}, args...)
	}
	head, err := rehearse(what, "restack it by hand", s.tip, args...)
	if err != nil {
		return 0, err
	}
	if err := history.Match(own, onto, head, s.Branch); err != nil {
		return 0, err
	}
	if gained, err := sameChange(s.Fork, onto, s.tip, head); err != nil {
		return 0, err
	} else if !gained {
		return 0, refuse("%s would change %s by more than its fork point's signed copy gained; restack it by hand",
			what, s.Branch)
	}
	s.OntoTree = mustTree(onto)
	if s.Shape, err = fullShape(onto, head); err != nil {
		return 0, err
	}
	s.rehearsedFrom, s.rehearsedTip = onto, head
	return len(own), nil
}

// sameChange reports whether newTip differs from oldTip exactly as onto
// differs from fork.
func sameChange(fork, onto, oldTip, newTip string) (bool, error) {
	if mustTree(onto) == mustTree(fork) {
		return mustTree(newTip) == mustTree(oldTip), nil
	}
	a, err := patchID(fork, onto)
	if err != nil {
		return false, err
	}
	b, err := patchID(oldTip, newTip)
	return a == b, err
}

func patchID(from, to string) (string, error) {
	diff, err := git.Bytes(nil, "diff", "--binary", "--no-ext-diff", "--no-textconv", from, to)
	if err != nil {
		return "", err
	}
	out, err := git.Bytes(diff, "patch-id", "--stable")
	if err != nil {
		return "", err
	}
	id, _, _ := strings.Cut(string(out), " ")
	return id, nil
}

// nth is the k-th commit of from..tip, oldest first.
func nth(from, tip string, k int) (string, error) {
	out, err := git.Run("rev-list", "--reverse", from+".."+tip)
	if err != nil {
		return "", err
	}
	commits := strings.Fields(out)
	if k < 1 || k > len(commits) {
		return "", fmt.Errorf("%s..%s has no commit %d", from, tip, k)
	}
	return commits[k-1], nil
}

// checkClean refuses a worktree the plan will move a branch in: it must not be
// mid-rebase or bisect, and must have no changes; the worktree a step runs in
// may keep untracked files, as a single-branch pass allows.
func checkClean(c checkout, own bool) error {
	if c.busy {
		return refuse("%s is being rebased or bisected in %s; finish that first", c.branch, c.path)
	}
	args := []string{"-C", c.path, "status", "--porcelain"}
	if own {
		args = append(args, "--untracked-files=no")
	}
	dirty, err := git.Run(args...)
	if err != nil {
		return refuse("%s is checked out in %s, which git cannot read: %v", c.branch, c.path, err)
	}
	if dirty == "" {
		return nil
	}
	if own {
		return refuse("%s is checked out in %s, which has uncommitted changes; commit them (unsigned) first", c.branch, c.path)
	}
	return refuse("%s is checked out in %s, which has uncommitted or untracked files; commit them "+
		"(unsigned), stash them or remove them there, then run the pass again", c.branch, c.path)
}

func describe(s step, steps []step, signs int, stacked, gains []string) string {
	where := "in " + s.Dir
	if s.Dir == "" {
		where = "in a throwaway worktree"
	}
	what := fmt.Sprintf("sign %d commit(s) after %.12s", signs, s.from)
	if s.From >= 0 {
		what = fmt.Sprintf("replay its %d commit(s) from fork point %.12s onto that commit's signed copy in %s's "+
			"stack, signing them", signs, s.Fork, steps[s.From].Branch)
	}
	line := fmt.Sprintf("%s: %s, %s", s.Branch, what, where)
	if len(stacked) > 0 {
		line += "; moves " + strings.Join(stacked, ", ")
	}
	if len(gains) > 0 {
		line += "; " + gainsPhrase(gains)
	}
	return line
}

// runPlan runs the steps from pl.Next on, then the push.
func runPlan(pl *plan) (any, error) {
	for pl.Next < len(pl.Steps) {
		s := &pl.Steps[pl.Next]
		pl.Next++
		if err := s.enter(); err != nil {
			return nil, err
		}
		path, err := statePath("pending", s.Branch)
		if err != nil {
			return nil, err
		}
		var p *pending
		if s.From < 0 {
			var early map[string]string
			if p, early, err = prepare(s.Branch, s.Base); err != nil {
				return nil, err
			}
			if early != nil {
				pl.record(s, map[string]any{"result": early}, nil)
				continue
			}
			s.NewFrom = p.Start
		} else {
			e := pl.Steps[s.From]
			onto, err := nth(e.NewFrom, "refs/heads/"+e.Branch, s.Kept)
			if err != nil {
				return nil, err
			}
			if mustTree(onto) != s.OntoTree {
				return nil, fmt.Errorf("the signed copy of %s's fork point, %s, does not have the files its rehearsal "+
					"had; nothing of %s has been rewritten, so restack it by hand", s.Branch, onto, s.Branch)
			}
			before, err := localBranches()
			if err != nil {
				return nil, err
			}
			tip := "refs/heads/" + s.Branch
			p = &pending{Branch: s.Branch, Start: onto, Onto: onto, Fork: s.Fork, Fixups: s.Fixups, Shape: s.Shape,
				Before: before, Stacked: stackedIn(before, s.Branch, s.Fork, tip), Trees: map[string]string{}}
			if p.Lands, err = landings(before, p.Stacked, s.Fork); err != nil {
				return nil, err
			}
			for _, b := range append([]string{s.Branch}, p.Stacked...) {
				p.Trees[b] = mustTree(before[b])
			}
			s.NewFrom = onto
		}
		p.Plan = pl
		if err := sign(p, path); err != nil {
			return nil, err
		}
		result, moved, err := finish(p, path)
		if err != nil {
			return nil, err
		}
		pl.record(s, result, moved)
	}
	if err := mainWorktree(); err != nil {
		return nil, err
	}
	result := map[string]any{"branches": pl.Results}
	if pl.Push || pl.SetUpstream {
		branches := slices.Sorted(slices.Values(append(pl.Moved, pl.Unmoved...)))
		pushed, err := pushAll(pl.Key, pushes{Branches: slices.Compact(branches), SetUpstream: pl.SetUpstream})
		if err != nil {
			return nil, err
		}
		for b, why := range pl.Held {
			if _, ok := pushed[b]; !ok {
				pushed[b] = why
			}
		}
		result["pushed"] = pushed
	}
	return result, nil
}

// enter makes the step's worktree the one git runs in, adding a throwaway one
// when its branch is checked out nowhere.
func (s *step) enter() error {
	if s.Dir != "" {
		git.Dir = s.Dir
		return nil
	}
	if s.Temp == "" {
		dir, err := stateDir()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, "worktrees"), 0o755); err != nil { //nolint:gosec // holds checkouts of the repository, in its git directory
			return err
		}
		if s.Temp, err = os.MkdirTemp(filepath.Join(dir, "worktrees"), "pass-"); err != nil {
			return err
		}
		os.Remove(s.Temp)
		if _, err := git.RunLong(nil, "worktree", "add", "-q", s.Temp, s.Branch); err != nil {
			return err
		}
	}
	git.Dir = s.Temp
	return nil
}

// record keeps a finished step's result and removes its throwaway worktree.
func (pl *plan) record(s *step, result map[string]any, moved map[string]string) {
	pl.Results[s.Branch] = result
	for b := range moved {
		pl.Moved = append(pl.Moved, b)
	}
	if s.Temp != "" {
		mainWorktree()
		git.RunLong(nil, "worktree", "remove", s.Temp)
		s.Temp = ""
	}
}

// mainWorktree makes the repository's main worktree the one git runs in, so
// nothing runs in a throwaway worktree once it is gone.
func mainWorktree() error {
	list, err := git.Run("worktree", "list", "--porcelain")
	if err != nil {
		return err
	}
	first, _, _ := strings.Cut(list, "\n")
	git.Dir = strings.TrimPrefix(first, "worktree ")
	return nil
}
