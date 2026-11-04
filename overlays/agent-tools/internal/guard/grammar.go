package guard

import (
	"regexp"
	"strings"
)

// The command lines of the programs the guard reads past their name, for
// the code they run (runsUnread) and the files they write (changedFiles),
// one grammar each. A program gains a rule here, as a row, not in either
// reader.

// programKind is how a program runs code the guard cannot read.
type programKind int

const (
	// subcommands run it for the subcommands in runs alone: go run, cargo
	// run, uv run.
	subcommands programKind = iota
	// interpreters run the file they are given, or their standard input,
	// unless an inline option gives them the code.
	interpreters
	// scripts run a package's scripts for every subcommand but those in
	// reads: npm, yarn.
	scripts
	// recipes run code named in a file the guard does not read, whatever
	// they are given: just, task.
	recipes
)

// location is what a location option's value names.
type location int

const (
	// dir is the directory the program works in, and runs code from.
	dir location = iota + 1
)

// grammar is how a program reads its command line.
type grammar struct {
	kind programKind
	// values are the options before the subcommand, or an interpreter's
	// script, whose value is the next words, by how many. Any other
	// option takes none: for an interpreter, such as python's -E or perl's
	// -p, it gives no code, and the run still reads a file.
	values map[string]int
	// toolchain says a +TOOLCHAIN word may come before the subcommand, as
	// cargo's +nightly does.
	toolchain bool
	// inline are the options whose value is the code itself (python -c).
	inline map[string]bool
	// locations are the options of values whose value names where the
	// program works.
	locations map[string]location
	// runs are the subcommands that run code the guard cannot read
	// (subcommands), reads those that run none (scripts), and sources
	// those of runs whose operands may name the code (go run ./cmd/gen).
	runs, reads, sources map[string]bool
	// writes are the subcommands that rewrite the files of the directory
	// the program works in.
	writes map[string]bool
}

// programs are the grammars, by program name; pythons name python's.
var programs = map[string]grammar{
	"python":    {kind: interpreters, inline: set("-c"), values: arity("-W", "-X")},
	"node":      {kind: interpreters, inline: set("-e", "--eval", "-p", "--print"), values: arity("-r", "--require")},
	"ruby":      {kind: interpreters, inline: set("-e"), values: arity("-I", "-r")},
	"perl":      {kind: interpreters, inline: set("-e", "-E")},
	"php":       {kind: interpreters, inline: set("-r")},
	"lua":       {kind: interpreters, inline: set("-e")},
	"Rscript":   {kind: interpreters, inline: set("-e")},
	"osascript": {kind: interpreters, inline: set("-e")},
	"pwsh":      {kind: interpreters, inline: set("-c", "-Command")},
	"tclsh":     {kind: interpreters},
	// go generate runs the commands its //go:generate lines name.
	"go": {kind: subcommands, values: arity("-C"), locations: map[string]location{"-C": dir},
		runs: set("run", "generate"), sources: set("run"), writes: set("generate", "fmt", "fix", "get", "work")},
	"cargo": {kind: subcommands, toolchain: true, values: arity("--color", "--config", "-Z", "-C"),
		runs: set("run", "r"), writes: set("fmt")},
	"uv":     {kind: subcommands, runs: set("run"), sources: set("run")},
	"poetry": {kind: subcommands, runs: set("run"), sources: set("run")},
	"pipenv": {kind: subcommands, runs: set("run"), sources: set("run")},
	"npm":    {kind: scripts, reads: packageReads},
	"pnpm":   {kind: scripts, reads: packageReads},
	"yarn":   {kind: scripts, reads: packageReads},
	"bun":    {kind: scripts, reads: packageReads},
	"deno":   {kind: scripts, reads: packageReads},
	// npx and bunx are wrappers: unwrap hands on the program they run.
	"just": {kind: recipes},
	"task": {kind: recipes},
}

// packageReads are the package managers' subcommands that run no script.
var packageReads = set("view", "info", "show", "ls", "list", "outdated", "why", "explain", "help", "search", "--version", "-v")

// pythons names a python, such as python3 or pypy3, whose grammar is
// python's.
var pythons = regexp.MustCompile(`^(python|pypy)[0-9.]*$`)

// arity is options that each take one word of value.
func arity(options ...string) map[string]int {
	m := map[string]int{}
	for _, o := range options {
		m[o] = 1
	}
	return m
}

// programOf is the program whose grammar name has, as programs names it
// ("python" for python3), and that grammar; ok is false for a program
// with none.
func programOf(name string) (program string, g grammar, ok bool) {
	if g, ok := programs[name]; ok {
		return name, g, true
	}
	if pythons.MatchString(name) {
		return "python", programs["python"], true
	}
	return "", grammar{}, false
}

// command is where g's subcommand starts in args, past the options and
// toolchain before it, and the values of the location options among them
// by kind. `--` ends the options.
func (g grammar) command(args []string) (int, map[location][]string) {
	at := map[location][]string{}
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			return i + 1, at
		case len(a) > 1 && a[0] == '-':
			if kind := g.locations[a]; kind != 0 && i+1 < len(args) {
				at[kind] = append(at[kind], args[i+1])
			}
			i += 1 + g.values[a]
		case g.toolchain && strings.HasPrefix(a, "+"):
			i++
		default:
			return i, at
		}
	}
	return i, at
}
