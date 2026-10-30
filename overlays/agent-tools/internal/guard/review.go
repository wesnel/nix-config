package guard

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/plans"
)

// checkPlanReview holds the main thread's file edits, by a file tool or the
// shell, while a plan whose SESSIONS name the session has a design review
// of the plan recorded and not yet answered (plans.PendingPlanReviewOf), so
// its findings shape the work before any of it is written. A command that
// runs code the guard cannot read (unreadRuns) counts as editing the
// directory it runs in. openRoots stay open. A sub-agent is not held: its
// edits are fenced to its own worktree already (checkOwner). paths are
// editPaths(in), runs are unreadRuns of its command; conf is this host's
// plans, and with none nothing is held.
func checkPlanReview(in input, paths []string, runs []unreadRun, conf *plans.Config) string {
	if !reviewHolds(in) || len(paths)+len(runs) == 0 || conf == nil {
		return ""
	}
	p, recorded, pending := conf.PendingPlanReviewOf(in.SessionID)
	if !pending {
		return ""
	}
	held := fmt.Sprintf("The design review %q of the plan %s was recorded at %s and has not been answered", recorded.Summary,
		p.File(), recorded.Time)
	answer := fmt.Sprintf("When its report is in, settle its findings in the plan and log `agent-plan log %s --item %s --kind plan-reviewed`; "+
		"if the review was stopped and will not report, log that event with a summary saying so, and run the review again before building.",
		p.File(), p.Name)
	free := openRoots(conf.Dir)
	for _, path := range paths {
		if !isOpen(path, free) {
			return fmt.Sprintf("%s, so edits outside "+openPlaces+" wait for it, %s among them. %s", held, path, answer)
		}
	}
	for _, r := range runs {
		if !isOpen(r.where, free) {
			where := r.where
			if where == "" {
				where = "a directory the guard cannot work out"
			}
			return fmt.Sprintf("%s, so `%s` waits for it: it runs code the guard cannot read, which could write anywhere, "+
				"in or from %s. Run a probe with its code in "+scratchPlaces+", from there, instead. %s", held, r.program, where, answer)
		}
	}
	return ""
}

// reviewHolds reports whether checkPlanReview may hold in's call: the main
// thread's, in a session.
func reviewHolds(in input) bool {
	return in.SessionID != "" && in.AgentID == ""
}

// unreadRun is a call that runs code the guard cannot read: program, as
// written, run in a directory or from a script file, where. A call is
// held for each.
type unreadRun struct {
	program, where string
}

// interpreterOptions are how an interpreter reads its options before the
// script: inline are those whose value is the code itself, values those
// whose value is the next word. Any other option, such as python's -E or
// perl's -p, gives no code: the run still reads a file.
type interpreterOptions struct {
	inline, values map[string]bool
}

// interpreters run the file they are given, or their standard input,
// unless an inline option gives them the code.
var interpreters = map[string]interpreterOptions{
	"python":    {inline: set("-c"), values: set("-W", "-X")},
	"node":      {inline: set("-e", "--eval", "-p", "--print"), values: set("-r", "--require")},
	"ruby":      {inline: set("-e"), values: set("-I", "-r")},
	"perl":      {inline: set("-e", "-E"), values: set()},
	"php":       {inline: set("-r"), values: set()},
	"lua":       {inline: set("-e"), values: set()},
	"Rscript":   {inline: set("-e"), values: set()},
	"osascript": {inline: set("-e"), values: set()},
	"pwsh":      {inline: set("-c", "-Command"), values: set()},
	"tclsh":     {inline: set(), values: set()},
}

// versionedPython names a python, such as python3 or pypy3, that takes
// python's options.
var versionedPython = regexp.MustCompile(`^(python|pypy)[0-9.]*$`)

// projectRunners run the project's code with their run command.
var projectRunners = set("uv", "poetry", "pipenv")

// cargoValues are cargo's options before its command whose value is the
// next word.
var cargoValues = set("--color", "--config", "-Z", "-C")

// packageManagers run a package's scripts for every command but those in
// packageReads.
var packageManagers = set("npm", "pnpm", "yarn", "bun", "deno")

var packageReads = set("view", "info", "show", "ls", "list", "outdated", "why", "explain", "help", "search", "--version", "-v")

// runners run code named in a file the guard does not read. npx and bunx
// are wrappers: unwrap hands on the program they run.
var runners = set("just", "task")

// sourcing run the script they are given in the shell itself.
var sourcing = set("source", ".")

// editors run the commands an option gives them.
var editors = set("vi", "vim", "nvim")

// unreadRuns are the calls of command, run in cwd, that run code the guard
// cannot read, each with the directory it runs in and the files and
// directories it may take the code from, as walkShell gives them: a
// program named by a path or by an expansion, a shell or an interpreter
// given a file or its standard input, source, go run or generate, cargo
// run, uv, poetry or pipenv run, a package manager's scripts, just, task,
// emacsclient --eval, and vim given a command. go test, emacs --batch and
// inline code (bash -c, python3 -c) are not: the first two run code the
// change holds, and the shell's script is read in turn.
func unreadRuns(command, cwd string) []unreadRun {
	var out []unreadRun
	walkShell(command, cwd, 0, func(c call, cwd string) {
		unread, sources := runsUnread(c)
		if !unread {
			return
		}
		out = append(out, unreadRun{program: c.words[0], where: cwd})
		for _, s := range sources {
			if p := expandPath(s, cwd); filepath.IsAbs(p) {
				out = append(out, unreadRun{program: c.words[0], where: p})
			}
		}
	})
	return out
}

// runsUnread reports whether c, once unwrap has taken its wrappers off,
// runs code the guard cannot read, as unreadRuns lists, and the files or
// directories it may take that code from: a program's path, the operands
// of a shell, source or an interpreter (any of them may be the script, as
// an option's value may look like one), the file on its standard input,
// what go, uv, poetry or pipenv run is given, and go's -C directory. Code
// on standard input from the line itself (<<, <<<) is inline, as -c is.
func runsUnread(c call) (bool, []string) {
	if c.expands[0] {
		return true, nil
	}
	name, args := c.words[0], c.words[1:]
	if strings.Contains(name, "/") {
		return true, []string{name}
	}
	interp, isInterpreter := interpreters[name]
	if !isInterpreter && versionedPython.MatchString(name) {
		interp, isInterpreter = interpreters["python"], true
	}
	switch {
	case shells[name], sourcing[name]:
		// unwrap hands a shell's inline script on as a script; a shell
		// that reaches here runs a file or its standard input.
		files, inlineInput := operands(args)
		return len(files) > 0 || !inlineInput, files
	case runners[name]:
		return true, nil
	case isInterpreter:
		for i := 0; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
			if inlineOption(args[i], interp.inline) {
				return false, nil
			}
			if interp.values[args[i]] {
				i++
			}
		}
		files, inlineInput := operands(args)
		return len(files) > 0 || !inlineInput, files
	case name == "go":
		var dirs []string
		i := 0
		for i < len(args) && strings.HasPrefix(args[i], "-") {
			if args[i] == "-C" && i+1 < len(args) {
				dirs = append(dirs, args[i+1])
				i++
			}
			i++
		}
		switch {
		case i < len(args) && args[i] == "run":
			files, _ := operands(args[i+1:])
			return true, append(dirs, files...)
		case i < len(args) && args[i] == "generate":
			// It runs the commands its //go:generate lines name.
			return true, dirs
		}
		return false, nil
	case name == "cargo":
		i := 0
		for i < len(args) && (strings.HasPrefix(args[i], "-") || strings.HasPrefix(args[i], "+")) {
			if cargoValues[args[i]] {
				i++
			}
			i++
		}
		return i < len(args) && (args[i] == "run" || args[i] == "r"), nil
	case projectRunners[name]:
		if i := options(args, nil); i < len(args) && args[i] == "run" {
			files, _ := operands(args[i+1:])
			return true, files
		}
		return false, nil
	case packageManagers[name]:
		return len(args) == 0 || !packageReads[args[0]], nil
	case name == "emacsclient" || versionedEmacsclient.MatchString(name):
		return slices.ContainsFunc(args, func(a string) bool { return a == "--eval" || a == "-e" }), nil
	case editors[name]:
		return slices.ContainsFunc(args, func(a string) bool {
			return a == "-c" || a == "-s" || a == "-S" || a == "--cmd" || strings.HasPrefix(a, "+")
		}), nil
	}
	return false, nil
}

// inlineOption reports whether option, one word, is one of inline, with
// its value attached after = (--eval=code), or bundles one of inline's
// letters with others, as perl's -ne or node's -pe.
func inlineOption(option string, inline map[string]bool) bool {
	if name, _, _ := strings.Cut(option, "="); inline[name] {
		return true
	}
	if strings.HasPrefix(option, "--") {
		return false
	}
	for _, letter := range option[1:] {
		if inline["-"+string(letter)] {
			return true
		}
	}
	return false
}

// operands are the files args names that a program may read code from:
// its words that are neither options, - nor redirections, and the file a <
// reads standard input from. inlineInput reports that its standard input
// comes from the line itself, by << or <<<.
func operands(args []string) (files []string, inlineInput bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		op := redirectionWord.FindString(a)
		switch {
		case op == "":
			if a != "-" && !strings.HasPrefix(a, "-") {
				files = append(files, a)
			}
			continue
		case strings.HasSuffix(op, "<<") || strings.HasSuffix(op, "<<<"):
			inlineInput = true
		case strings.HasSuffix(op, "<") && !strings.Contains(op, "&"):
			if target := a[len(op):]; target != "" {
				files = append(files, target)
			} else if i+1 < len(args) {
				files = append(files, args[i+1])
			}
		}
		// The target, attached or the next word, is no operand.
		if len(a) == len(op) && !strings.Contains(op, "&") {
			i++
		}
	}
	return files, inlineInput
}
