package guard

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

// The shell's side of the guard: it finds every simple command in a command
// line, and the program each one runs once wrappers, shell keywords and
// assignments are taken off. It reads the line as the agent typed it, so a
// command built at run time is beyond it.

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// keywords start a command without being one.
var keywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "do": true,
	"while": true, "until": true, "!": true, "{": true}

// wrapper is a program that runs the command after its own options and
// operands.
type wrapper struct {
	values   map[string]bool // options whose value is the next word
	operands int             // words between the options and the command
}

func set(words ...string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[w] = true
	}
	return m
}

var wrappers = map[string]wrapper{
	"timeout":    {values: set("-s", "--signal", "-k", "--kill-after"), operands: 1},
	"nice":       {values: set("-n", "--adjustment")},
	"nohup":      {},
	"time":       {},
	"builtin":    {},
	"command":    {},
	"exec":       {values: set("-a")},
	"stdbuf":     {values: set("-i", "-o", "-e", "--input", "--output", "--error")},
	"caffeinate": {values: set("-t", "-w")},
	// GNU's -i, -l and -e take their values attached, if at all.
	"xargs": {values: set("-n", "-L", "-I", "-P", "-s", "-d", "-E", "-a", "-J", "-R", "-S",
		"--max-args", "--max-lines", "--max-procs", "--max-chars", "--delimiter", "--eof", "--arg-file", "--process-slot-var")},
	"watch": {values: set("-n", "--interval", "-q", "--equexit")},
	// They run a package's program, which bears its name.
	"npx":  {values: set("-p", "--package", "-c", "--call")},
	"bunx": {values: set("-p", "--package")},
}

// envValues are env's options whose value is the next word.
var envValues = set("-u", "--unset", "-C", "--chdir", "-P", "-S", "--split-string")

// shells run the script after -c (or a combined flag such as -lc).
var shells = set("bash", "sh", "zsh", "fish", "dash", "ksh")

// shellValues are shell options whose value is the next word.
var shellValues = set("-o", "+o", "-O", "+O", "--rcfile", "--init-file")

// nixValues are nix options whose values are the next words, by how many.
var nixValues = map[string]int{"--override-input": 2, "--option": 2, "--arg": 2, "--argstr": 2,
	"--expr": 1, "-f": 1, "--file": 1, "--inputs-from": 1, "-I": 1, "--include": 1, "--profile": 1,
	"--store": 1, "--eval-store": 1, "-p": 1, "--packages": 1}

// options skips the options at the start of args, by the rules most
// programs share: `--` ends them, and a value is the next word only for an
// option in values. It returns where the options end.
func options(args []string, values map[string]bool) int {
	i := 0
	for i < len(args) {
		a := args[i]
		switch {
		case a == "--":
			return i + 1
		case len(a) > 1 && a[0] == '-':
			if values[a] {
				i++
			}
			i++
		default:
			return i
		}
	}
	return i
}

// call is a simple command once wrappers, keywords and assignments are off:
// the program's words, which of them the shell expands, and the assignments
// made for it.
type call struct {
	words   []string
	expands []bool
	env     []string
}

// visitor judges one call, or a script that a call runs, and returns a
// denial's reason or "".
type visitor struct {
	call   func(c call) string
	script func(text string) string
}

// unwrap takes the wrappers, keywords and assignments off a simple command
// and hands the command they run, or the script, to v.
func unwrap(c call, v visitor) string {
	for len(c.words) > 0 {
		w := c.words[0]
		switch {
		case !c.expands[0] && assignment.MatchString(w):
			c.env = append(c.env, w)
			c.words, c.expands = c.words[1:], c.expands[1:]
			continue
		case !c.expands[0] && keywords[w]:
			c.words, c.expands = c.words[1:], c.expands[1:]
			continue
		case !c.expands[0] && w == "function" && len(c.words) > 1:
			c.words, c.expands = c.words[2:], c.expands[2:]
			continue
		}
		break
	}
	if len(c.words) == 0 || c.expands[0] {
		return v.call(c)
	}
	name := base(c.words[0])
	args, expands := c.words[1:], c.expands[1:]
	rest := func(from int) call {
		from = min(from, len(args))
		return call{words: args[from:], expands: expands[from:], env: c.env}
	}
	if w, ok := wrappers[name]; ok {
		i := options(args, w.values) + w.operands
		switch {
		case name == "command" && (contains(args[:min(i, len(args))], "-v") || contains(args[:min(i, len(args))], "-V")):
			return "" // prints where the command is
		case name == "watch":
			// watch runs its words, joined, with sh -c.
			return v.script(strings.Join(args[min(i, len(args)):], " "))
		}
		return unwrap(rest(i), v)
	}
	switch {
	case name == "env":
		return unwrapEnv(c.env, args, expands, v)
	case name == "direnv" && len(args) > 0 && args[0] == "exec":
		return unwrap(rest(2), v)
	case name == "nwrap":
		return unwrapNwrap(c, v)
	case shells[name]:
		if script, ok := shellScript(args, name == "fish"); ok {
			return v.script(script)
		}
		return v.call(c)
	case name == "eval":
		return v.script(strings.Join(args, " "))
	case name == "pr-loop" && len(args) > 0 && args[0] == "autosquash":
		// autosquash runs its --exec value with sh -c.
		for i, a := range args {
			for _, flag := range []string{"--exec", "-exec"} {
				script, given := "", false
				switch {
				case a == flag && i+1 < len(args):
					script, given = args[i+1], true
				case strings.HasPrefix(a, flag+"="):
					script, given = strings.TrimPrefix(a, flag+"="), true
				}
				if given {
					if reason := v.script(script); reason != "" {
						return reason
					}
				}
			}
		}
	case name == "nix-shell":
		for i, a := range args {
			if (a == "--run" || a == "--command") && i+1 < len(args) {
				return v.script(args[i+1])
			}
		}
	case name == "nix" && len(args) > 0 && (args[0] == "shell" || args[0] == "develop"):
		for i, a := range args {
			if a == "-c" || a == "--command" {
				return unwrap(rest(i+1), v)
			}
		}
	case name == "nix" && len(args) > 0 && args[0] == "run":
		return unwrapNixRun(c.env, args[1:], expands[1:], v)
	case name == "find":
		for i := 0; i < len(args); i++ {
			if args[i] != "-exec" && args[i] != "-execdir" && args[i] != "-ok" && args[i] != "-okdir" {
				continue
			}
			end := i + 1
			for end < len(args) && args[end] != ";" && args[end] != "+" {
				end++
			}
			if reason := unwrap(call{words: args[i+1 : end], expands: expands[i+1 : end], env: c.env}, v); reason != "" {
				return reason
			}
			i = end
		}
	case name == "export" || name == "declare" || name == "typeset" || name == "local" || name == "readonly":
		env := c.env
		for _, a := range args {
			if assignment.MatchString(a) {
				env = append(env, a)
			}
		}
		return v.call(call{words: c.words, expands: c.expands, env: env})
	}
	return v.call(c)
}

// nwrapValues are the options of ngrok's scripts/nwrap whose value is the
// next word, or follows = in the same word; nwrapFlags take none. An
// option nwrap gains goes in one of them, or the guard refuses it.
var (
	nwrapValues = set("-b", "--branch", "-B", "--create-branch", "-s", "--session")
	nwrapFlags  = set("-a", "--attach", "--no-autocommit", "--no-copy-files", "--no-direnv-exec")
)

// nwrapOwn are nwrap's commands that run nothing inside it.
var nwrapOwn = set("list", "-l", "--list", "prune", "-h", "--help")

const nwrapUnknown = "`nwrap` was given `%s`, an option the guard does not know, which could take a value the guard " +
	"would then judge as the command nwrap runs. Use only nwrap's options %s, `attach`, and `--` before the command, " +
	"or run the command without nwrap."

// unwrapNwrap hands on the command ngrok's scripts/nwrap runs, in a
// worktree it mounts at the repository's own path, so the command acts on
// the paths it names as it would without nwrap. With no command nwrap
// starts the user's shell, handed on as a bare `sh`. An option it does not
// know is refused.
func unwrapNwrap(c call, v visitor) string {
	args, expands := c.words[1:], c.expands[1:]
	i := 0
options:
	for i < len(args) {
		a := args[i]
		name, _, _ := strings.Cut(a, "=")
		switch {
		case nwrapOwn[a]:
			return v.call(c)
		case a == "--":
			i++
			break options
		case nwrapValues[a]:
			i += 2
		case a == "attach", nwrapFlags[a], strings.HasPrefix(a, "--") && nwrapValues[name]:
			i++
		case strings.HasPrefix(a, "-"):
			var known []string
			for _, o := range slices.Sorted(maps.Keys(nwrapValues)) {
				known = append(known, "`"+o+" VALUE`")
			}
			for _, o := range slices.Sorted(maps.Keys(nwrapFlags)) {
				known = append(known, "`"+o+"`")
			}
			return fmt.Sprintf(nwrapUnknown, a, strings.Join(known, ", "))
		default:
			break options
		}
	}
	if i >= len(args) {
		return v.call(call{words: []string{"sh"}, expands: []bool{false}, env: c.env})
	}
	return unwrap(call{words: args[i:], expands: expands[i:], env: c.env}, v)
}

// unwrapEnv reads env's options and assignments, and hands on the command
// they run, or env itself when it runs none; `-S` splits its value into
// words as a shell would.
func unwrapEnv(env, args []string, expands []bool, v visitor) string {
	opts := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case opts && (a == "--" || a == "-"):
			opts = opts && a != "--"
		case opts && (a == "-S" || a == "--split-string" || strings.HasPrefix(a, "--split-string=") || strings.HasPrefix(a, "-S")):
			script := ""
			switch {
			case strings.HasPrefix(a, "--split-string="):
				script = strings.TrimPrefix(a, "--split-string=")
			case strings.HasPrefix(a, "-S") && len(a) > 2:
				script = a[2:]
			case i+1 < len(args):
				i++
				script = args[i]
			}
			words, x := split(script)
			return unwrapEnv(env, append(words, args[i+1:]...), append(x, expands[i+1:]...), v)
		case opts && len(a) > 1 && a[0] == '-':
			if envValues[a] {
				i++
			}
		case !expands[i] && assignment.MatchString(a):
			opts = false
			env = append(env, a)
		default:
			return unwrap(call{words: args[i:], expands: expands[i:], env: env}, v)
		}
	}
	return v.call(call{words: []string{"env"}, expands: []bool{false}, env: env})
}

// unwrapNixRun reads `nix run INSTALLABLE [--] ARGS`, whose program is named
// by the installable's attribute: nixpkgs#gh runs gh. Another installable
// names no program the guard can know, which it treats as one the shell
// expands.
func unwrapNixRun(env, args []string, expands []bool, v visitor) string {
	i := 0
	for i < len(args) && len(args[i]) > 1 && args[i][0] == '-' && args[i] != "--" {
		i += 1 + nixValues[args[i]]
	}
	if i >= len(args) {
		return ""
	}
	at := i
	i++
	if i < len(args) && args[i] == "--" {
		i++
	}
	program, expanded := "", true
	if _, attribute, ok := strings.Cut(args[at], "#"); ok && attribute != "" {
		program, expanded = attribute[strings.LastIndex(attribute, ".")+1:], expands[at]
	}
	words := append([]string{program}, args[min(i, len(args)):]...)
	x := append([]bool{expanded}, expands[min(i, len(expands)):]...)
	return v.call(call{words: words, expands: x, env: env})
}

// shellScript is the script a shell's arguments run with -c, or a flag
// combining it, such as -lc, and fish's --command and --init-command; ok is
// false when they run a file or nothing.
func shellScript(args []string, fish bool) (string, bool) {
	command := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			if command && i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		case strings.HasPrefix(a, "--command=") || strings.HasPrefix(a, "--init-command="):
			_, script, _ := strings.Cut(a, "=")
			return script, true
		case a == "--command" || a == "--init-command" || (fish && a == "-C"):
			if i+1 < len(args) {
				return args[i+1], true
			}
		case shellValues[a]:
			i++
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			command = command || (a[0] == '-' && strings.Contains(a[1:], "c"))
		default:
			return a, command
		}
	}
	return "", false
}

func base(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

func contains(words []string, w string) bool {
	for _, x := range words {
		if x == w {
			return true
		}
	}
	return false
}

// segments splits a command line into its simple commands: at ;, &&, ||, |,
// &, newlines and parentheses, outside quotes. A command substitution, in or
// out of double quotes, is a command of its own, and stands in the command
// around it as `$_`, a word the shell expands.
func segments(text string) []string {
	out, _ := backgroundSegments(text)
	return out
}

// backgroundSegments is segments, with whether each runs in the
// background: it, or the subshell it is in, ends with a lone &.
func backgroundSegments(text string) ([]string, []bool) {
	type frame struct {
		cur      strings.Builder
		quote    byte  // the quote this frame is inside, if any
		backtick bool  // the frame is a backquoted substitution
		parens   int   // subshells open inside a $( ) frame
		sub      bool  // the frame is a substitution
		groups   []int // where each subshell open in the frame starts in out
		closed   int   // where the subshell that just closed started, or -1
	}
	var out []string
	var background []bool
	stack := []*frame{{closed: -1}}
	top := func() *frame { return stack[len(stack)-1] }
	flush := func() {
		f := top()
		out = append(out, f.cur.String())
		background = append(background, false)
		f.cur.Reset()
	}
	open := func(backtick bool) {
		top().cur.WriteString("$_")
		stack = append(stack, &frame{backtick: backtick, sub: true, closed: -1})
	}
	closeSub := func() {
		flush()
		stack = stack[:len(stack)-1]
	}
	for i := 0; i < len(text); i++ {
		c, f := text[i], top()
		if c != ' ' && c != '\t' && c != '&' {
			f.closed = -1
		}
		switch {
		case f.quote == '\'':
			if c == '\'' {
				f.quote = 0
			}
			f.cur.WriteByte(c)
		case c == '\\' && i+1 < len(text):
			f.cur.WriteByte(c)
			i++
			f.cur.WriteByte(text[i])
		case c == '$' && i+1 < len(text) && text[i+1] == '(':
			i++
			open(false)
		case c == '`' && f.backtick:
			closeSub()
		case c == '`':
			open(true)
		case f.quote == '"':
			if c == '"' {
				f.quote = 0
			}
			f.cur.WriteByte(c)
		case c == '\'' || c == '"':
			f.quote = c
			f.cur.WriteByte(c)
		case c == ')' && f.sub && !f.backtick && f.parens == 0:
			closeSub()
		case c == '(':
			f.parens++
			flush()
			f.groups = append(f.groups, len(out))
		case c == ')':
			f.parens = max(0, f.parens-1)
			flush()
			if n := len(f.groups); n > 0 {
				f.closed, f.groups = f.groups[n-1], f.groups[:n-1]
			}
		case c == '&' && i > 0 && (text[i-1] == '>' || text[i-1] == '<'),
			c == '&' && i+1 < len(text) && text[i+1] == '>':
			// A redirection: >&2, 2>&1, <&3, &>file.
			f.cur.WriteByte(c)
		case c == '&' && (i == 0 || text[i-1] != '&' && text[i-1] != '|') && (i+1 == len(text) || text[i+1] != '&'):
			whole := strings.TrimSpace(f.cur.String()) == "" && f.closed >= 0
			flush()
			from := len(out) - 1
			if whole {
				from = f.closed
			}
			for j := from; j < len(out); j++ {
				background[j] = true
			}
			f.closed = -1
		case c == ';' || c == '\n' || c == '|' || c == '&':
			flush()
		default:
			f.cur.WriteByte(c)
		}
	}
	for len(stack) > 1 {
		closeSub()
	}
	flush()
	return out, background
}

// split splits one segment into words the way a shell would, removing quotes,
// and reports for each word whether the shell expands part of it: a $ or a
// backquote outside single quotes and not escaped.
func split(segment string) (words []string, expands []bool) {
	var cur strings.Builder
	in, quote, expand := false, byte(0), false
	end := func() {
		words, expands = append(words, cur.String()), append(expands, expand)
		cur.Reset()
		in, expand = false, false
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(segment) {
				i++
				cur.WriteByte(segment[i])
			} else {
				expand = expand || quote == '"' && (c == '$' || c == '`')
				cur.WriteByte(c)
			}
		case c == '\'' || c == '"':
			quote, in = c, true
		case c == '\\' && i+1 < len(segment):
			i++
			cur.WriteByte(segment[i])
			in = true
		case c == ' ' || c == '\t':
			if in {
				end()
			}
		case c == '#' && !in:
			return words, expands
		default:
			expand = expand || c == '$' || c == '`'
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		end()
	}
	return words, expands
}
