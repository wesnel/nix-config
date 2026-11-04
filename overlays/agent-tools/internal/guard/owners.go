package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

// AuthorizeGitStage rechecks that agent, in session, may still edit
// worktree, as checkOwner judges a git_stage call there.
func AuthorizeGitStage(session, agent, worktree string) error {
	data, err := json.Marshal(map[string]string{"worktree": worktree})
	if err != nil {
		return err
	}
	in := input{ToolName: gitStageTool, ToolInput: data, SessionID: session, AgentID: agent}
	edits, _, reason := editPaths(in)
	if reason == "" {
		conf, confErr := loadPlans()
		reason = checkOwner(in, edits, conf, confErr)
	}
	if reason != "" {
		return fmt.Errorf("Git stage ownership: %s", reason)
	}
	return nil
}

// loadPlans is this host's plans, or nil and why they cannot be loaded.
func loadPlans() (*plans.Config, error) {
	c, err := plans.Load()
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// checkOwner holds file edits, by a file tool or the shell, to the
// worktrees the plans of this host give agents (`agent-plan own`). A
// sub-agent edits only the worktrees it owns, scratch and the plans
// directory; one that owns none edits only scratch and plans, which closes
// the window between its spawn and `agent-plan own`. A worktree a plan
// names is judged by its owner wherever it is, scratch included. The main
// thread edits task worktrees associated with its session by the newest
// matching plan, except those delegated to another agent. Every path is
// compared with its symlinks resolved. The caller is the event's agent_id,
// which Claude Code and Codex set for a sub-agent's call alone.
//
// Shell edits are only those shellEdits names: a write through a script it
// cannot read passes. edits are editPaths(in); conf is this host's plans,
// and nil where confErr says why they cannot be loaded.
func checkOwner(in input, edits []editPath, conf *plans.Config, confErr error) string {
	if len(edits) == 0 {
		return ""
	}
	paths := pathsOf(edits)
	// A sub-agent's edits fail closed: without plans it owns no worktree.
	var c plans.Config
	owners := map[string]string{}
	if conf != nil {
		c = *conf
		named, _ := c.Owners()
		for wt, agent := range named {
			if real := realPath(wt); real != "" {
				wt = real
			}
			owners[wt] = agent
		}
	}
	if in.AgentID != "" {
		return subagentEdits(in.AgentID, paths, owners, c.Dir)
	}
	if reason := checkNamedOwners(paths, owners); reason != "" {
		return reason
	}
	for _, e := range edits {
		if e.worktreeAdd {
			// Creating isolation may start in the main checkout. Its
			// existing owner was checked above; redirections and other
			// calls still count.
			continue
		}
		if reason := checkAssociatedWorktree(e, in, c, confErr, owners); reason != "" {
			return reason
		}
	}
	return ""
}

// editPath is a file or directory a call edits, as an absolute path.
// worktreeAdd marks the directory a git worktree add runs in, which the
// owner fence judges by its owner alone, since creating isolation may start
// in the main checkout. gitRan marks the directory any other git write
// runs in, and alsoIn are the other directories its call is judged in
// after a cd (gitAfterCd), which the fence's refusal names.
type editPath struct {
	path        string
	worktreeAdd bool
	gitRan      bool
	alsoIn      []string
}

// pathsOf is the paths of edits.
func pathsOf(edits []editPath) []string {
	paths := make([]string, len(edits))
	for i, e := range edits {
		paths[i] = e.path
	}
	return paths
}

// editPaths are what the edit checks read of in's call: the files it
// writes, by a file tool or a command line (shellCommand), the runs of
// code the guard cannot read among its commands, and a refusal for a
// command whose writes it cannot place; shell edits are only those
// shellEdits names. Text typed into a running session is read where the
// call is, as check reads it, and is a run of its own in a directory the
// guard cannot know, since the session may be a shell anywhere. A
// git_stage call edits the worktree it names, which must be absolute.
func editPaths(in input) ([]editPath, []unreadRun, string) {
	if line, ok, _ := shellCommand(in.ToolName, in.ToolInput, in.Cwd); ok {
		// An input that does not decode is denied before.
		edits, runs, reason := shellEdits(line.text, line.dir)
		if line.typed && reason == "" {
			runs = append(runs, unreadRun{program: in.ToolName})
		}
		return edits, runs, reason
	}
	switch in.ToolName {
	case gitStageTool:
		var args struct {
			Worktree string `json:"worktree"`
		}
		if json.Unmarshal(in.ToolInput, &args) != nil || !filepath.IsAbs(args.Worktree) {
			return nil, nil, "The Git stage operation needs an absolute worktree path."
		}
		return []editPath{{path: physical(args.Worktree)}}, nil, ""
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch":
		// An input that does not decode is denied before the fence is
		// asked.
		changes, _ := fileChanges(in.ToolName, in.ToolInput)
		var edits []editPath
		for _, c := range changes {
			if c.path != "" {
				edits = append(edits, editPath{path: physical(rawPath(c.path, in.Cwd))})
			}
		}
		return edits, nil, ""
	}
	return nil, nil, ""
}

func checkNamedOwners(paths []string, owners map[string]string) string {
	for _, p := range paths {
		top, _ := worktreeOf(p)
		if gitStalled.Load() {
			return stalledGit
		}
		if owner := owners[top]; top != "" && owner != "" && owner != plans.MainAgent {
			return fmt.Sprintf("The worktree %s belongs to agent %s, which alone edits files there (OWNERS in its plan). "+
				"Send the change to that agent. If it has stopped, the coordinator can hand the worktree back to the main thread with `agent-plan own PLAN --worktree %s --agent main`.",
				top, owner, top)
		}
	}
	return ""
}

func checkAssociatedWorktree(e editPath, in input, c plans.Config, err error, owners map[string]string) string {
	top, g := worktreeOf(e.path)
	if gitStalled.Load() {
		return stalledGit
	}
	if g.dir == "" {
		return ""
	}
	opened, ok := openedRepo(g.dir)
	if !ok {
		return "The guard cannot determine the repository this edit changes. Ask the operator to repair its Git metadata before editing."
	}
	if top == "" && len(opened) != 0 {
		return "This edit is outside a working tree, such as inside Git metadata or a bare repository. Make repository edits in your associated task worktree; report shared metadata changes to the operator."
	}
	if top == "" {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("The guard cannot read the plans configuration: %v. Ask the operator to repair it before editing.", err)
	}
	association, findErr := associatedPlan(c, in.SessionID, top)
	if findErr != nil {
		return fmt.Sprintf("The guard cannot read worktree associations: %v. Ask the operator to repair the plans before editing.", findErr)
	}
	if association == nil && owners[top] == "" && len(opened) > 0 && !slices.ContainsFunc(opened, func(path string) bool {
		return !slices.ContainsFunc(scratchRoots(), func(root string) bool { return within(path, root) })
	}) {
		return ""
	}
	if len(opened) < 3 || resolved(opened[0]) == resolved(opened[1]) {
		return mainCheckoutRefusal(e, opened)
	}
	if reason := checkTaskBranch(g); reason != "" {
		return reason
	}
	if in.SessionID == "" || association == nil || !slices.Contains(association.Matched, "session") {
		return fmt.Sprintf("This session is not associated with %s in its newest plan. Work in your own task worktree and record its session and worktree with `agent-plan new`; for an intentional continuation, associate the session with that plan before editing.", top)
	}
	return ""
}

// mainCheckoutRefusal is why e, an edit of the repository's main checkout
// opened names, is refused: where a cd on the line led its git command
// into a linked worktree of that repository, the form the guard accepts,
// git -C with that worktree's path; else to make a task worktree.
func mainCheckoutRefusal(e editPath, opened []string) string {
	if linked := linkedWorktreeOf(e.alsoIn, opened); linked != "" {
		return fmt.Sprintf("This may run git in the repository's main checkout: the guard cannot tell that the cd before it still holds. "+
			"Run that git command as `git -C %s ...`, without the cd.", shellQuote(linked))
	}
	return "This edits the repository's main checkout. Create a task worktree at an absolute path outside this checkout (`git worktree add --no-track /ABSOLUTE/PATH -b BRANCH origin/HEAD`)."
}

func associatedPlan(c plans.Config, session, top string) (*plans.Found, error) {
	found, err := c.Find(plans.FindOptions{Session: session, Worktree: top})
	if err != nil {
		return nil, err
	}
	for i := range found {
		if slices.Contains(found[i].Matched, "worktree") {
			return &found[i], nil
		}
	}
	return nil, nil
}

func checkTaskBranch(g gitState) string {
	branch, branchOK := worktreeBranch(g)
	defaults, defaultsOK := g.run("for-each-ref", "--format=%(symref)", "refs/remotes/*/HEAD")
	if !branchOK || !defaultsOK {
		return "The guard cannot read this worktree's branch. Ask the operator to repair its Git metadata before editing."
	}
	protected := branch == "" || branch == "main" || branch == "master"
	for _, ref := range strings.Fields(defaults) {
		parts := strings.SplitN(ref, "/", 4)
		if len(parts) == 4 && branch == parts[3] {
			protected = true
		}
	}
	if protected {
		return "This worktree is on a default branch or a detached HEAD. Create a task worktree at an absolute path outside this checkout (`git worktree add --no-track /ABSOLUTE/PATH -b BRANCH HEAD`) before editing."
	}
	return ""
}

// worktreeBranch is the branch checked out where g runs, or "" for a
// detached HEAD. A rebase detaches HEAD until it ends, so for a detached
// HEAD it is the branch a rebase in progress will move, which head-name in
// the worktree's git dir names; a rebase of a detached HEAD writes
// "detached HEAD" there, which names no branch.
func worktreeBranch(g gitState) (string, bool) {
	branch, ok := g.run("branch", "--show-current")
	if !ok || branch != "" {
		return branch, ok
	}
	dir, ok := g.run("rev-parse", "--absolute-git-dir")
	if !ok {
		return "", false
	}
	for _, state := range []string{"rebase-merge", "rebase-apply"} {
		data, err := os.ReadFile(filepath.Join(dir, state, "head-name"))
		if name, named := strings.CutPrefix(strings.TrimSpace(string(data)), "refs/heads/"); err == nil && named && name != "" {
			return name, true
		}
	}
	return "", true
}

// openPlaces names openRoots in a refusal.
const openPlaces = scratchPlaces + " and the plans directory"

// openRoots are the directories every edit fence leaves open: the scratch
// roots, plansDir unless it is "", and devices such as /dev/null.
func openRoots(plansDir string) []string {
	roots := slices.Concat(scratchRoots(), []string{"/dev"})
	if plansDir != "" {
		roots = append(roots, plansDir)
	}
	return roots
}

// isOpen reports whether path is under one of roots.
func isOpen(path string, roots []string) bool {
	return slices.ContainsFunc(roots, func(root string) bool { return within(path, root) })
}

// subagentEdits returns why agent may not edit one of paths, or "": it
// edits only the worktrees owners gives it and openRoots(plansDir). A path
// in a worktree owners names is the owner's alone, even in scratch.
func subagentEdits(agent string, paths []string, owners map[string]string, plansDir string) string {
	var mine []string
	for wt, a := range owners {
		if a == agent {
			mine = append(mine, wt)
		}
	}
	sort.Strings(mine)
	free := openRoots(plansDir)
	for _, p := range paths {
		if within(p, "/dev") {
			continue
		}
		top, _ := worktreeOf(p)
		if owner, named := owners[top]; top != "" && named {
			if owner == agent {
				continue
			}
		} else if isOpen(p, free) {
			continue
		}
		if len(mine) == 0 {
			return fmt.Sprintf("Sub-agent %s owns no worktree, so it edits only "+openPlaces+", and %s is outside them. "+
				"Have whoever coordinates give it its worktree with `agent-plan own PLAN --worktree WORKTREE --agent %s` before it writes there, "+
				"or put the change in your report; reads are not limited.", agent, p, agent)
		}
		return fmt.Sprintf("Sub-agent %s edits only the worktree it owns, %s (OWNERS in its plan), "+openPlaces+", and %s is outside them. "+
			"Make the change in %s, or put it in your report for the operator or whoever coordinates; "+
			"moving the agent to another worktree is `agent-plan own PLAN --worktree WORKTREE --agent %s`, run by whoever coordinates.",
			agent, strings.Join(mine, ", "), p, strings.Join(mine, ", "), agent)
	}
	return ""
}

// within reports whether path, which need not exist yet, is root or under
// it, with the symlinks of both resolved: as written, a path in scratch can
// lead anywhere.
func within(path, root string) bool {
	p, r := resolved(path), realPath(root)
	return p != "" && r != "" && (p == r || strings.HasPrefix(p, strings.TrimSuffix(r, "/")+"/"))
}

// resolved is path with the symlinks of its nearest existing ancestor
// resolved, or "" when none can be.
func resolved(path string) string {
	rest := ""
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(real, rest)
		}
		if filepath.Dir(dir) == dir {
			return ""
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

const subagentStash = "Every worktree shares one stash, so a sub-agent's entry can be taken by, or take, another session's work. " +
	"Set the work aside as a WIP commit on your branch instead (`git -c commit.gpgsign=false commit -m 'WIP: ...'`). " +
	"`git stash list` and `git stash show` still read the stash."

// checkSubagentStash denies a sub-agent's shell command that writes or
// restores a stash entry. It runs before check, whose rules for tagged
// entries would otherwise answer first. The sub-agent is the event's
// agent_id, as for checkOwner.
func checkSubagentStash(in input) string {
	if in.AgentID == "" {
		return ""
	}
	switch in.ToolName {
	case "Bash", "exec_command":
	default:
		return ""
	}
	var args struct {
		Command string `json:"command"`
	}
	json.Unmarshal(in.ToolInput, &args)
	return stashWrites(args.Command, 0)
}

// stashWrites returns subagentStash if a command line runs a `git stash`
// other than list or show, or "".
func stashWrites(text string, depth int) string {
	if depth > maxDepth {
		return ""
	}
	v := visitor{
		script: func(text string) string { return stashWrites(text, depth+1) },
		call:   checkStashWrite,
	}
	for _, segment := range segments(text) {
		if words, expands := split(segment); len(words) > 0 {
			if reason := unwrap(call{words: words, expands: expands}, v); reason != "" {
				return reason
			}
		}
	}
	return ""
}

func checkStashWrite(c call) string {
	if c = c.named(); len(c.words) == 0 || c.expands[0] || base(c.words[0]) != "git" {
		return ""
	}
	words := c.words[1:]
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		if gitValueFlags[words[0]] {
			words = words[1:]
		}
		words = words[min(1, len(words)):]
	}
	if len(words) == 0 || words[0] != "stash" {
		return ""
	}
	if len(words) > 1 && (words[1] == "list" || words[1] == "show") {
		return ""
	}
	return subagentStash
}

// worktreeOf is the top level of the worktree holding path, which need not
// exist yet and may be a directory, with its symlinks resolved, and the git
// state there; "" when none holds it.
func worktreeOf(path string) (string, gitState) {
	if !filepath.IsAbs(path) {
		return "", gitState{}
	}
	dir := path
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		dir = filepath.Dir(path)
	}
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", gitState{}
		}
		dir = parent
	}
	g := gitState{dir: dir}
	top, ok := g.run("rev-parse", "--show-toplevel")
	if !ok {
		return "", g
	}
	if real, err := filepath.EvalSymlinks(top); err == nil {
		top = real
	}
	return top, g
}

// operationExits are the git commands, each with its one argument, that
// only step through or leave a rebase or cherry-pick in progress.
// gitWritePaths passes them in any worktree: a stopped rebase detaches
// HEAD, and the way out must not be refused with the edits.
var operationExits = map[string]map[string]bool{
	"rebase":      set("--abort", "--continue", "--skip"),
	"cherry-pick": set("--abort"),
}

// shellEdits reads a command line run in dir in one walk (walkShell): the
// absolute paths of the files it writes, moves or removes, as changedFiles
// names them, and the directory a git command that writes the worktree
// runs in (gitWritePaths), each named as fencePath has it; the runs of
// code the guard cannot read (unreadRunsOf, with its redirections all
// after its words); and a refusal for a command
// whose writes it cannot place. Each git write carries the other
// directories its call is judged in after a cd (gitAfterCd).
func shellEdits(text, dir string) ([]editPath, []unreadRun, string) {
	var edits []editPath
	var runs []unreadRun
	var after gitAfterCd
	reason := walkShell(text, dir, 0, func(c call, cwd string, first bool) string {
		if first {
			after.endCall()
		}
		named := c.named()
		runs = append(runs, unreadRunsOf(c.redirectedLast(), cwd)...)
		if c.expands[0] && named.expands[0] {
			return ""
		}
		// A cd's redirections open their files where it starts, before it
		// changes directory: walkShell hands it on first.
		for _, t := range changedFiles(base(named.words[0]), named.words[1:], redirections(c.words)) {
			if p := fencePath(t, cwd); filepath.IsAbs(p) {
				edits = append(edits, editPath{path: p})
			}
		}
		paths, reason := gitWritePaths(named, cwd)
		after.visit(named, cwd, paths)
		edits = append(edits, paths...)
		return reason
	})
	if reason != "" {
		return nil, nil, reason
	}
	after.endCall()
	for i := range edits {
		if edits[i].gitRan {
			edits[i].alsoIn = after.also[edits[i].path]
		}
	}
	return edits, runs, ""
}

// gitAfterCd collects, for the git writes of a command line, the other
// directories each is judged in from a directory a cd on the line led to:
// after a command substitution or a subshell, the guard judges a call
// both where the cd led and where the line started, since it cannot tell
// that the cd still holds.
type gitAfterCd struct {
	// reached are the directories a cd on the line led to.
	reached []string
	// ran are the directories the call under way's git writes run in, and
	// cdRan those of them judged from a directory in reached.
	ran, cdRan []string
	// also maps the directory a git write runs in to the others its call
	// is judged in from reached.
	also map[string][]string
}

// visit reads named, a call walkShell gives in cwd, whose git writes are
// edits.
func (g *gitAfterCd) visit(named call, cwd string, edits []editPath) {
	if !named.expands[0] && (named.words[0] == "cd" || named.words[0] == "pushd") {
		if to := cdTarget(named, cwd); to != "" {
			g.reached = addDir(g.reached, to)
		}
	}
	for _, e := range edits {
		if !e.gitRan {
			continue
		}
		g.ran = addDir(g.ran, e.path)
		if slices.Contains(g.reached, cwd) {
			g.cdRan = addDir(g.cdRan, e.path)
		}
	}
}

// endCall files the call under way's directories in also.
func (g *gitAfterCd) endCall() {
	for _, d := range g.ran {
		for _, o := range g.cdRan {
			if o == d {
				continue
			}
			if g.also == nil {
				g.also = map[string][]string{}
			}
			g.also[d] = addDir(g.also[d], o)
		}
	}
	g.ran, g.cdRan = nil, nil
}

// fencePath is word, a file a command run in cwd names, as the owner fence
// judges it: with $PWD as cwd, and $TMPDIR and $HOME in their place
// (shellPath), cut at its first part still holding a variable or a
// command substitution, which the shell decides, so that the directory
// before it is judged: MAIN/$x.go as MAIN, and $(cmd)/a.go, which split
// gives as $_/a.go, as cwd, where $(pwd) and $(git rev-parse
// --show-toplevel) lead. Where a .. follows that part, the word is judged
// whole, cleaned, as if the variable held one name: MAIN/$x/../../b as
// b. A word that starts with a variable ($f.go, $out) is judged as cwd,
// where the variable most often leads, so the guard never passes it
// unjudged.
func fencePath(word, cwd string) string {
	for _, p := range []string{"$PWD", "${PWD}"} {
		if (word == p || strings.HasPrefix(word, p+"/")) && cwd != "" {
			word = cwd + strings.TrimPrefix(word, p)
			break
		}
	}
	word = shellPath(word, true)
	parts := strings.Split(word, "/")
	i := slices.IndexFunc(parts, func(p string) bool { return strings.ContainsAny(p, "$`") })
	switch {
	case i < 0, slices.Contains(parts[i+1:], ".."):
		return physical(rawPath(word, cwd))
	case i == 1 && parts[0] == "":
		return "/"
	}
	return physical(rawPath(strings.Join(parts[:i], "/"), cwd))
}

// gitWritePaths names the absolute paths a git command, run in cwd, writes,
// with its -C options applied, when gitWrites names its command and gitRead
// does not pass it as a reading form (stash list, tag -l, apply --check),
// the directory a worktree add runs in tagged, as is the one any other
// command but git worktree's runs in; a refusal when a worktree target
// cannot be resolved.
func gitWritePaths(c call, cwd string) ([]editPath, string) {
	if base(c.words[0]) != "git" {
		return nil, ""
	}
	words, dir, relocated := gitMutationLocation(c, cwd)
	if len(words) == 0 || !gitWrites[words[0]] || gitRead(words[0], words[1:]) || !filepath.IsAbs(dir) {
		return nil, ""
	}
	if relocated {
		return nil, "The guard requires an explicit working directory for Git mutations. Use `git -C /ABSOLUTE/TASK/WORKTREE` without repository-location flags or GIT_* path overrides."
	}
	// checkSigning still judges --continue and --skip, which replay commits.
	if len(words) == 2 && operationExits[words[0]][words[1]] {
		return nil, ""
	}
	if len(words) > 1 && words[0] == "worktree" && words[1] == "add" {
		positional := gitPositionals(words[0], words[1:])
		var paths []editPath
		if len(positional) > 1 {
			paths = append(paths, editPath{path: fencePath(words[1+positional[1]], dir)})
		}
		return append(paths, editPath{path: dir, worktreeAdd: true}), ""
	}
	if len(words) > 1 && words[0] == "worktree" && (words[1] == "remove" || words[1] == "move") {
		return worktreeMutationTargets(words, dir)
	}
	return []editPath{{path: dir, gitRan: true}}, ""
}

// linkedWorktreeOf is the one of dirs, the other directories a git
// command was judged in, that is in a linked worktree of the repository
// opened names, by its common directory; "" when none is or several are.
func linkedWorktreeOf(dirs []string, opened []string) string {
	if len(opened) < 2 {
		return ""
	}
	found := ""
	for _, d := range dirs {
		top, g := worktreeOf(d)
		if top == "" {
			continue
		}
		other, ok := openedRepo(g.dir)
		if !ok || len(other) < 3 || resolved(other[0]) == resolved(other[1]) || resolved(other[1]) != resolved(opened[1]) {
			continue
		}
		if found != "" {
			return ""
		}
		found = d
	}
	return found
}

func gitMutationLocation(c call, cwd string) ([]string, string, bool) {
	dir, words := cwd, c.words[1:]
	relocated := false
	for _, kv := range append(slices.Clone(c.env), c.assigned...) {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(gitenv.Location, name) {
			relocated = true
		}
	}
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		flag, _, _ := strings.Cut(words[0], "=")
		if flag == "--git-dir" || flag == "--work-tree" || flag == "--bare" {
			relocated = true
		}
		switch {
		case words[0] == "-C" && len(words) > 1:
			dir = fencePath(words[1], dir)
			words = words[2:]
		case gitValueFlags[words[0]]:
			words = words[min(2, len(words)):]
		default:
			words = words[1:]
		}
	}
	return words, dir, relocated
}

func worktreeMutationTargets(words []string, dir string) ([]editPath, string) {
	args := words[2:]
	for len(args) > 0 && (args[0] == "-f" || args[0] == "--force") {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	want := 1
	if words[1] == "move" {
		want = 2
	}
	if len(args) != want || strings.HasPrefix(args[0], "-") {
		return nil, "The guard cannot determine this worktree command's targets. Use an explicit worktree path and only --force or -- before it."
	}
	target, reason := worktreeTarget(dir, args[0])
	if reason != "" {
		return nil, reason
	}
	paths := []editPath{{path: target}}
	if want == 2 {
		paths = append(paths, editPath{path: fencePath(args[1], dir)})
	}
	return paths, ""
}

// Require an absolute path so Git's suffix and case-matching rules cannot
// select a different worktree from the one whose ownership is checked.
func worktreeTarget(dir, word string) (string, string) {
	refusal := "The guard cannot resolve this worktree target. Use an existing worktree's absolute path from `git worktree list`."
	if !filepath.IsAbs(word) || strings.ContainsAny(word, "$`") {
		return "", refusal
	}
	g := gitState{dir: dir}
	listing, ok := g.run("worktree", "list", "--porcelain", "-z")
	if !ok {
		if gitStalled.Load() {
			return "", stalledGit
		}
		return "", refusal
	}
	path := resolved(fencePath(word, dir))
	for _, field := range strings.Split(listing, "\x00") {
		if target, ok := strings.CutPrefix(field, "worktree "); ok {
			if resolved(target) == path {
				top, _ := worktreeOf(target)
				if top != "" && resolved(top) == path {
					return top, ""
				}
			}
		}
	}
	return "", refusal
}
