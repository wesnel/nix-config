package guard

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/plans"
)

// checkPlanReview holds the main thread's file edits, by a file tool or the
// shell, while a plan whose SESSIONS name the session has a design review,
// of the plan or of the result, recorded and not yet answered
// (plans.PendingPlanReviewOf), so its findings shape the work before more
// of it is written. A command that runs code the guard cannot read
// (unreadRunsOf) counts as editing the directory it runs in. openRoots stay
// open. A sub-agent is not held: its edits are fenced to its own worktree
// already (checkOwner). edits and runs are editPaths(in); conf is this
// host's plans, and with none nothing is held.
func checkPlanReview(in input, edits []editPath, runs []unreadRun, conf *plans.Config) string {
	paths := pathsOf(edits)
	if !reviewHolds(in) || len(paths)+len(runs) == 0 || conf == nil {
		return ""
	}
	p, recorded, pending := conf.PendingPlanReviewOf(in.SessionID)
	if !pending {
		return ""
	}
	held := fmt.Sprintf("The design review %q in the plan %s was recorded at %s and has not been answered", recorded.Summary,
		p.File(), recorded.Time)
	answer := "When its report is in, settle its findings. " + plans.AnswerHint(p) +
		" If the review was stopped and will not report, log that answer with a summary saying so, and run the review again before going on."
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

// sourcing run the script they are given in the shell itself.
var sourcing = set("source", ".")

// editors run the commands an option gives them.
var editors = set("vi", "vim", "nvim")

// unreadRunsOf are the runs of code the guard cannot read that c, a call
// walkShell gives with its leading redirections off, makes in cwd: one for
// the directory it runs in and one for each file or directory it may take
// the code from. Such a call is a program named by a path or by an
// expansion, a shell or an interpreter given a file or its standard input,
// source, go run or generate, cargo run, uv, poetry or pipenv run, a
// package manager's scripts, just, task, emacsclient --eval, and vim given
// a command. go test, emacs --batch and inline code (bash -c, python3 -c)
// are not: the first two run code the change holds, and the shell's script
// is read in turn.
func unreadRunsOf(c call, cwd string) []unreadRun {
	unread, sources := runsUnread(c)
	if !unread {
		return nil
	}
	out := []unreadRun{{program: c.words[0], where: cwd}}
	for _, s := range sources {
		if p := expandPath(s, cwd); filepath.IsAbs(p) {
			out = append(out, unreadRun{program: c.words[0], where: p})
		}
	}
	return out
}

// runsUnread reports whether c, once unwrap has taken its wrappers off,
// runs code the guard cannot read, as unreadRuns lists, and the files or
// directories it may take that code from: a program's path, the operands
// of a shell, source or an interpreter (any of them may be the script, as
// an option's value may look like one), the file on its standard input,
// what a subcommand of programs that runs code is given where its sources
// say so, and the directories its location options name. Code on standard
// input from the line itself (<<, <<<) is inline, as -c is.
func runsUnread(c call) (bool, []string) {
	if c.expands[0] {
		return true, nil
	}
	name, args := c.words[0], c.words[1:]
	if strings.Contains(name, "/") {
		return true, []string{name}
	}
	if shells[name] || sourcing[name] {
		// unwrap hands a shell's inline script on as a script; a shell
		// that reaches here runs a file or its standard input.
		files, inlineInput := operands(args)
		return len(files) > 0 || !inlineInput, files
	}
	if _, g, ok := programOf(name); ok {
		switch g.kind {
		case recipes:
			return true, nil
		case interpreters:
			for i := 0; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
				if inlineOption(args[i], g.inline) {
					return false, nil
				}
				i += g.values[args[i]]
			}
			files, inlineInput := operands(args)
			return len(files) > 0 || !inlineInput, files
		case subcommands:
			i, at := g.command(args)
			if i >= len(args) || !g.runs[args[i]] {
				return false, nil
			}
			sources := at[dir]
			if g.sources[args[i]] {
				files, _ := operands(args[i+1:])
				sources = append(sources, files...)
			}
			return true, sources
		case scripts:
			return len(args) == 0 || !g.reads[args[0]], nil
		}
	}
	switch {
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
	for i := 0; i < len(args); {
		// A redirection's target, attached or the next word, is no operand.
		op, target, n := redirection(args, i)
		switch {
		case n == 0:
			if a := args[i]; a != "-" && !strings.HasPrefix(a, "-") {
				files = append(files, a)
			}
			i++
			continue
		case strings.HasSuffix(op, "<<") || strings.HasSuffix(op, "<<<"):
			inlineInput = true
		case strings.HasSuffix(op, "<") && target != "":
			files = append(files, target)
		}
		i += n
	}
	return files, inlineInput
}
