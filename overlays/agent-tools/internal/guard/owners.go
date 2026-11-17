package guard

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

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
// Shell edits are only those shellEditPaths names: a write through a script
// it cannot read passes.
func checkOwner(in input) string {
	paths, reason := ownerEditPaths(in, false)
	if reason != "" {
		return reason
	}
	if len(paths) == 0 {
		return ""
	}
	// A sub-agent's edits fail closed: without plans it owns no worktree.
	c, err := plans.Load()
	owners := map[string]string{}
	if err == nil {
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
	if in.ToolName == "Bash" || in.ToolName == "exec_command" {
		// Creating isolation may start in the main checkout. Its existing
		// owner was checked above; redirections and other calls still count.
		paths, reason = ownerEditPaths(in, true)
		if reason != "" {
			return reason
		}
	}
	for _, p := range paths {
		if reason := checkAssociatedWorktree(p, in, c, err, owners); reason != "" {
			return reason
		}
	}
	return ""
}

func ownerEditPaths(in input, creatingWorktree bool) (paths []string, reason string) {
	switch in.ToolName {
	case "Bash", "exec_command":
		var args struct {
			Command string `json:"command"`
		}
		json.Unmarshal(in.ToolInput, &args)
		paths, reason = shellEditPaths(args.Command, in.Cwd, 0, creatingWorktree)
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch":
		// An input that does not decode is denied before the fence is
		// asked.
		changes, _ := fileChanges(in.ToolName, in.ToolInput)
		for _, c := range changes {
			if c.path != "" {
				paths = append(paths, physical(rawPath(c.path, in.Cwd)))
			}
		}
	}
	return paths, reason
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

func checkAssociatedWorktree(p string, in input, c plans.Config, err error, owners map[string]string) string {
	top, g := worktreeOf(p)
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
		return "This edits the repository's main checkout. Create a task worktree at an absolute path outside this checkout (`git worktree add --no-track /ABSOLUTE/PATH -b BRANCH origin/HEAD`)."
	}
	if reason := checkTaskBranch(g); reason != "" {
		return reason
	}
	if in.SessionID == "" || association == nil || !slices.Contains(association.Matched, "session") {
		return fmt.Sprintf("This session is not associated with %s in its newest plan. Work in your own task worktree and record its session and worktree with `agent-plan new`; for an intentional continuation, associate the session with that plan before editing.", top)
	}
	return ""
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

// subagentEdits returns why agent may not edit one of paths, or "": it
// edits only the worktrees owners gives it, the scratch roots, the plans
// directory and devices such as /dev/null. A path in a worktree owners
// names is the owner's alone, even in scratch.
func subagentEdits(agent string, paths []string, owners map[string]string, plansDir string) string {
	var mine []string
	for wt, a := range owners {
		if a == agent {
			mine = append(mine, wt)
		}
	}
	sort.Strings(mine)
	free := scratchRoots()
	if plansDir != "" {
		free = append(free, plansDir)
	}
	for _, p := range paths {
		if within(p, "/dev") {
			continue
		}
		top, _ := worktreeOf(p)
		if owner, named := owners[top]; top != "" && named {
			if owner == agent {
				continue
			}
		} else if slices.ContainsFunc(free, func(root string) bool { return within(p, root) }) {
			continue
		}
		if len(mine) == 0 {
			return fmt.Sprintf("Sub-agent %s owns no worktree, so it edits only scratch ($TMPDIR, /tmp) and the plans directory, and %s is outside them. "+
				"Have whoever coordinates give it its worktree with `agent-plan own PLAN --worktree WORKTREE --agent %s` before it writes there, "+
				"or put the change in your report; reads are not limited.", agent, p, agent)
		}
		return fmt.Sprintf("Sub-agent %s edits only the worktree it owns, %s (OWNERS in its plan), scratch ($TMPDIR, /tmp) and the plans directory, and %s is outside them. "+
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

// shellEditPaths are the absolute paths of the files a command line
// writes, moves or removes, as changedFiles names them, and the directory
// a git command that writes the worktree runs in (gitWrites), following cd
// as command does. Each file is named as fencePath has it, once the
// variables leadingAssignments and loopVariables give are in place: a
// command is judged once for each word of a loop it names.
func shellEditPaths(text, dir string, depth int, creatingWorktree bool) ([]string, string) {
	if depth > maxDepth {
		return nil, ""
	}
	known := leadingAssignments(text)
	loops := loopVariables(text, known)
	var out []string
	w := cwds{here: []string{dir}}
	end := ""
	v := visitor{
		script: func(text string) string {
			paths, reason := editPathsInDirs(text, w.here, depth+1, creatingWorktree)
			if reason != "" {
				return reason
			}
			out = append(out, paths...)
			return ""
		},
		call: func(c call) string {
			if len(c.words) == 0 || c.expands[0] && c.named().expands[0] {
				return ""
			}
			named := c.named()
			if named.words[0] == "cd" || named.words[0] == "pushd" {
				// Redirections before cd open their files where the
				// command starts, before it changes directory.
				out = append(out, fencedPaths(redirections(c.words), w.here)...)
				w.cd(named, end)
				return ""
			}
			paths, reason := callEditPaths(c, named, w.here, creatingWorktree)
			if reason != "" {
				return reason
			}
			out = append(out, paths...)
			return ""
		},
	}
	texts, _, ends, _ := backgroundSegments(text)
	for i, segment := range texts {
		words, expands := split(segment)
		if len(words) == 0 {
			continue
		}
		end = ends[i]
		variants := expansions(words, expands, known, loops, i)
		w.begin(call{words: variants[0], expands: expands})
		if reason := unwrapEach(variants, expands, v); reason != "" {
			return nil, reason
		}
		w.next(end)
	}
	return out, ""
}

// unwrapEach hands v each of a segment's expansions in turn, the words of
// one command as expansions gives them, and returns the first denial's
// reason, or "".
func unwrapEach(variants [][]string, expands []bool, v visitor) string {
	for _, words := range variants {
		if reason := unwrap(call{words: words, expands: expands}, v); reason != "" {
			return reason
		}
	}
	return ""
}

func editPathsInDirs(text string, dirs []string, depth int, creatingWorktree bool) (out []string, reason string) {
	for _, cwd := range dirs {
		paths, reason := shellEditPaths(text, cwd, depth, creatingWorktree)
		if reason != "" {
			return nil, reason
		}
		out = append(out, paths...)
	}
	return out, ""
}

func fencedPaths(targets, dirs []string) (out []string) {
	for _, cwd := range dirs {
		for _, t := range targets {
			if p := fencePath(t, cwd); filepath.IsAbs(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

func callEditPaths(c, named call, dirs []string, creatingWorktree bool) (out []string, reason string) {
	for _, cwd := range dirs {
		targets := changedFiles(base(named.words[0]), named.words[1:], redirections(c.words))
		out = append(out, fencedPaths(targets, []string{cwd})...)
		paths, reason := gitWritePaths(named, cwd, creatingWorktree)
		if reason != "" {
			return nil, reason
		}
		out = append(out, paths...)
	}
	return out, ""
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

// assignedValues maps a variable's name to the value a command line has
// given it, as leadingAssignments finds them.
type assignedValues map[string]string

// leadingAssignments are the variables a command line gives a value before
// it runs anything, which hold wherever the line uses them: the segments
// that open the line hold only NAME=VALUE words, outside any subshell,
// pipeline or background job, and end with ;, && or a newline, so every
// one has run before the first command. A value counts where
// readableValue can tell it and it holds nothing the shell would expand,
// split or read as syntax where the variable is used. A name
// that appears anywhere else on the line other than as $NAME or ${NAME},
// where something may bind it again (a second assignment, for NAME in,
// read NAME, unset NAME, ${NAME:=x}), is left out, and so stays the shell's
// to decide; so is one the line names after a $ the shell leaves as it is
// (quotedNames), since split, which takes quotes off, no longer tells that
// $NAME from one the shell expands. So is one of shellSet, which the shell
// binds without the line naming it. A line that names IFS, which decides
// where the shell splits an unquoted $NAME, or runs code the guard does
// not read in this shell (runsUnread), has none.
func leadingAssignments(text string) assignedValues {
	known := assignedValues{}
	if bindings(text, "IFS") > 0 || runsUnread(text) {
		return known
	}
	assigned := map[string]int{}
	quoted := quotedNames(text)
	words, expands := leadingAssignmentWords(text)
	for j, w := range words {
		name, value, _ := strings.Cut(w, "=")
		assigned[name]++
		if value, ok := readableValue(text, value, expands[j], quoted); ok {
			known[name] = value
		} else {
			delete(known, name)
		}
	}
	for name := range known {
		if bindings(text, name) != 1 || assigned[name] != 1 || quoted[name] || shellSet[name] {
			delete(known, name)
		}
	}
	return known
}

// shellSet are the variables bash or zsh binds itself, as cd does PWD and
// OLDPWD, read REPLY and getopts OPTARG, or through another name tied to
// it, as zsh's path is to PATH. Their values are the shell's to decide.
var shellSet = set("PWD", "OLDPWD", "REPLY", "OPTARG", "OPTIND", "_", "RANDOM", "SRANDOM", "SECONDS",
	"EPOCHSECONDS", "EPOCHREALTIME", "LINENO", "PPID", "UID", "EUID", "GID", "EGID", "SHLVL", "HOSTNAME",
	"PIPESTATUS", "BASH_REMATCH", "BASH_COMMAND", "MATCH", "MBEGIN", "MEND", "match", "mbegin", "mend",
	"BASHPID", "GROUPS", "FUNCNAME", "HISTCMD", "status", "pipestatus", "argv",
	"PATH", "path", "CDPATH", "cdpath", "FPATH", "fpath", "MANPATH", "manpath", "MAILPATH", "mailpath",
	"MODULE_PATH", "module_path", "IFS")

// binders are the commands that may bind a variable the guard does not see
// bound: by code it does not read (., source, eval), or by a name attached
// to an option or built at run time (read -raD).
var binders = set(".", "source", "eval", "read", "mapfile", "readarray", "declare", "typeset", "local",
	"export", "unset", "getopts")

// nameOptions are the commands that bind a variable named by an option's
// value, which may be attached to it (printf -vD), by that option.
var nameOptions = map[string]string{"printf": "-v", "print": "-v", "wait": "-p"}

// runsUnread reports whether any word of a command line, in any position,
// is one of binders, or one after a command of nameOptions starts with its
// option: a blunt check, so that a line which may bind a variable out of
// the guard's sight has no variable followed.
func runsUnread(text string) bool {
	for _, segment := range segments(text) {
		words, _ := split(segment)
		var options []string
		for _, w := range words {
			if binders[w] || slices.ContainsFunc(options, func(o string) bool { return strings.HasPrefix(w, o) }) {
				return true
			}
			if o, ok := nameOptions[w]; ok {
				options = append(options, o)
			}
		}
	}
	return false
}

// readableValue is value, an assignment's on text that the shell expands
// where expanded says, as the shell gives it, with $TMPDIR and $HOME in
// their place (shellPath); ok is false where the guard cannot tell it. It
// cannot where the value names a variable the line binds or names after a
// literal $ (quotedNames), since shellPath reads $TMPDIR and $HOME from the
// guard's own environment, nor where it starts with ~, which split cannot
// tell quoted, and so kept, from expanded.
func readableValue(text, value string, expanded bool, quoted map[string]bool) (string, bool) {
	if strings.HasPrefix(value, "~") {
		return "", false
	}
	for _, m := range variable.FindAllStringSubmatch(value, -1) {
		if name := m[1] + m[2]; bindings(text, name) > 0 || quoted[name] {
			return "", false
		}
	}
	value = shellPath(value, expanded)
	return value, value != "" && !strings.ContainsAny(value, " \t\n$`*?[]~<>|&;(){}'\"\\")
}

// leadingAssignmentWords are the NAME=VALUE words of the segments that open
// a command line, as leadingAssignments takes them, with whether the shell
// expands each. A segment that runs a command after its assignments, which
// then hold for that command alone, ends them, as does one whose word has a
// quote before its = as written ("D"=x), which the shell runs as a command.
func leadingAssignmentWords(text string) (words []string, expands []bool) {
	texts, background, ends, nested := backgroundSegments(text)
	for i, segment := range texts {
		w, x := split(segment)
		if nested[i] || background[i] || !slices.Contains([]string{";", "\n", "&&", ""}, ends[i]) ||
			len(w) == 0 && ends[i] == "&&" || slices.ContainsFunc(w, func(s string) bool { return !assignedAsWritten(s, segment) }) {
			return words, expands
		}
		words, expands = append(words, w...), append(expands, x...)
	}
	return words, expands
}

// assignedAsWritten reports whether word, as split gives it from segment,
// is an assignment there: NAME= stands unquoted at the start of a word of
// segment.
func assignedAsWritten(word, segment string) bool {
	if !assignment.MatchString(word) {
		return false
	}
	name, _, _ := strings.Cut(word, "=")
	return regexp.MustCompile(`(^|[ \t])` + name + `=`).MatchString(segment)
}

var leadingName = regexp.MustCompile(`^\{?([A-Za-z_][A-Za-z0-9_]*)`)

// quotedNames are the names that follow a $ the shell leaves as it is: one
// escaped with a backslash, or inside single quotes.
func quotedNames(text string) map[string]bool {
	out := map[string]bool{}
	quote := byte(0)
	for i := 0; i < len(text); i++ {
		c, literal := text[i], false
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
			literal = c == '$'
		case c == '\\' && i+1 < len(text):
			i++
			literal = text[i] == '$'
		case c == '"' && quote == '"':
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		}
		if m := leadingName.FindStringSubmatch(text[i+1:]); literal && m != nil {
			out[m[1]] = true
		}
	}
	return out
}

var identifier = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// bindings counts the places in text that name appears other than as a
// reference, $NAME or ${NAME}, or as part of a path, next to a / or a .,
// and where it ends a word or is followed by what a binding's name is
// (=, +=, [, a redirection, :, or a closing ) or }): each may bind it.
// Elsewhere, such as s in sed's s#a#b#, no shell syntax binds it.
func bindings(text, name string) int {
	n := 0
	for _, m := range identifier.FindAllStringIndex(text, -1) {
		start, end := m[0], m[1]
		if text[start:end] != name {
			continue
		}
		dollar := start > 0 && text[start-1] == '$'
		braced := start > 1 && text[start-2:start] == "${" && end < len(text) && text[end] == '}'
		path := start > 0 && strings.ContainsRune("/.", rune(text[start-1])) || end < len(text) && strings.ContainsRune("/.", rune(text[end]))
		bound := end == len(text) || strings.ContainsRune(" \t\n=+[;&|()}<>:\"'\\`", rune(text[end]))
		if !dollar && !braced && !path && bound {
			n++
		}
	}
	return n
}

// reference is $NAME or ${NAME} where it ends a word or a path's part, as
// expand takes it: anything else after it, such as zsh's :t modifier or a
// [..] subscript, changes what it gives.
var reference = regexp.MustCompile(`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))(/|$)`)

// expand is words with each reference that known gives a value replaced by
// it, in the words the shell expands, and, where loop names a variable,
// each reference to it in a word's last part replaced by value, where that
// leaves the word nothing more to expand, glob characters included; any
// other word is left as written.
func (known assignedValues) expand(words []string, expands []bool, loop, value string) []string {
	out := slices.Clone(words)
	for i, w := range words {
		if !expands[i] {
			continue
		}
		r := reference.ReplaceAllStringFunc(withLoopValue(w, loop, value), func(ref string) string {
			m := reference.FindStringSubmatch(ref)
			if value, ok := known[m[1]+m[2]]; ok {
				return value + m[3]
			}
			return ref
		})
		if !strings.ContainsAny(r, "$`*?[") {
			out[i] = r
		}
	}
	return out
}

// loopVariable is a for loop's variable as loopVariables takes it: the
// words it runs over, and the segments of the line its loop starts and
// ends at; the segments between them are its body.
type loopVariable struct {
	words    []string
	from, to int
}

// loopName is a word of a loop's list that loopVariables follows: a
// literal file name.
var loopName = regexp.MustCompile(`^[A-Za-z0-9._,+=@%-]+$`)

// loopVariables are the variables of the line's `for NAME in WORD...`
// loops, by the segment of backgroundSegments each loop starts at, where
// every word is a literal file name, never . or .., so that a reference to
// NAME in a path's last part names a file in the same directory whichever
// word it takes; and where that for alone binds NAME, the shell does not
// set it, the line does not name it after a literal $ (quotedNames), no
// leading assignment gives it, and the for is not in a subshell, pipeline
// or background job, nor on a line runsUnread finds.
func loopVariables(text string, known assignedValues) map[string]loopVariable {
	loops := map[string]loopVariable{}
	if runsUnread(text) {
		return loops
	}
	quoted := quotedNames(text)
	texts, background, ends, nested := backgroundSegments(text)
	for i := range texts {
		words, expands := leadingWords(texts[i])
		if len(words) < 4 || words[0] != "for" || words[2] != "in" || slices.Contains(expands[:3], true) ||
			nested[i] || background[i] || ends[i] == "|" || i > 0 && ends[i-1] == "|" {
			continue
		}
		name, list := words[1], words[3:]
		_, assigned := known[name]
		if assigned || shellSet[name] || quoted[name] || bindings(text, name) != 1 || len(list) > 32 ||
			slices.Contains(expands[3:], true) || slices.ContainsFunc(list, notFileName) {
			continue
		}
		loops[name] = loopVariable{words: list, from: i, to: loopEnd(texts, i)}
	}
	return loops
}

// leadingWords are a segment's words, as split gives them, from the first
// that is not a shell keyword.
func leadingWords(segment string) ([]string, []bool) {
	words, expands := split(segment)
	for len(words) > 0 && !expands[0] && keywords[words[0]] {
		words, expands = words[1:], expands[1:]
	}
	return words, expands
}

// loopEnd is the segment of texts that holds the done closing the loop
// that starts at segment from, or len(texts) where none does.
func loopEnd(texts []string, from int) int {
	depth := 0
	for i := from + 1; i < len(texts); i++ {
		words, _ := leadingWords(texts[i])
		switch {
		case len(words) == 0:
		case words[0] == "for" || words[0] == "while" || words[0] == "until" || words[0] == "select":
			depth++
		case words[0] == "done" && depth == 0:
			return i
		case words[0] == "done":
			depth--
		}
	}
	return len(texts)
}

// expansions are words as expand gives them, once for each word of the loop
// variable they name, a segment of its loop's body, or once with no
// loop variable where they name none, or more than one, or change
// directory, which the guard follows once.
func expansions(words []string, expands []bool, known assignedValues, loops map[string]loopVariable, segment int) [][]string {
	names := map[string]bool{}
	for i, w := range words {
		for _, m := range variable.FindAllStringSubmatch(w, -1) {
			if l, ok := loops[m[1]+m[2]]; ok && expands[i] && l.from < segment && segment < l.to {
				names[m[1]+m[2]] = true
			}
		}
	}
	if len(names) != 1 || slices.Contains(words, "cd") || slices.Contains(words, "pushd") {
		return [][]string{known.expand(words, expands, "", "")}
	}
	named := slices.Collect(maps.Keys(names))[0]
	var out [][]string
	for _, value := range loops[named].words {
		out = append(out, known.expand(words, expands, named, value))
	}
	return out
}

// withLoopValue is word with each reference to the variable loop in its
// last part replaced by value, where the part holds nothing else to expand
// and stays a file name of this directory (loopName); otherwise word as
// written. A reference before the last part stays, so expand leaves such a
// word as written.
func withLoopValue(word, loop, value string) string {
	if loop == "" {
		return word
	}
	cut := strings.LastIndex(word, "/") + 1
	last := variable.ReplaceAllStringFunc(word[cut:], func(ref string) string {
		if m := variable.FindStringSubmatch(ref); m[1]+m[2] == loop {
			return value
		}
		return ref
	})
	if notFileName(last) {
		return word
	}
	return word[:cut] + last
}

// notFileName reports whether a word is other than a literal name of a
// file in a directory, as loopName has it, or is . or ...
func notFileName(word string) bool {
	return word == "." || word == ".." || !loopName.MatchString(word)
}

// gitWritePaths names the absolute paths a git command, run in cwd, writes,
// with its -C options applied, when gitWrites names its command and gitRead
// does not pass it as a reading form (stash list, tag -l, apply --check);
// a refusal when a worktree target cannot be resolved.
func gitWritePaths(c call, cwd string, creatingWorktree bool) ([]string, string) {
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
		var paths []string
		if len(positional) > 1 {
			paths = append(paths, fencePath(words[1+positional[1]], dir))
		}
		if !creatingWorktree {
			paths = append(paths, dir)
		}
		return paths, ""
	}
	if len(words) > 1 && words[0] == "worktree" && (words[1] == "remove" || words[1] == "move") {
		return worktreeMutationTargets(words, dir)
	}
	return []string{dir}, ""
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

func worktreeMutationTargets(words []string, dir string) ([]string, string) {
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
	paths := []string{target}
	if want == 2 {
		paths = append(paths, fencePath(args[1], dir))
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
