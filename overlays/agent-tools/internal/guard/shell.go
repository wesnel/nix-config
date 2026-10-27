package guard

import (
	"fmt"
	"maps"
	"path/filepath"
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
// made for it. fed says that xargs runs it, adding words the guard does
// not see.
type call struct {
	words     []string
	expands   []bool
	redirects []redirection
	env       []string
	fed       bool
}

// visitor judges one call, or a script that a call runs, and returns a
// denial's reason or "".
type visitor struct {
	call      func(c call) string
	script    func(text string) string
	redirects func([]redirection) string
	chdir     func(string, bool, func() string) string
}

// unwrap takes the wrappers, keywords and assignments off a simple command
// and hands the command they run, or the script, to v.
func unwrap(c call, v visitor) string {
	if len(c.redirects) > 0 && v.redirects != nil {
		if reason := v.redirects(c.redirects); reason != "" {
			return reason
		}
	}
	c.redirects = nil
	for len(c.words) > 0 {
		w := c.words[0]
		switch {
		case assignment.MatchString(w): // the value may expand; the name never does
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
		return call{words: args[from:], expands: expands[from:], env: c.env, fed: c.fed || name == "xargs"}
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
	case name == "git":
		if reason := v.call(c); reason != "" {
			return reason
		}
		return unwrapGit(c.env, args, expands, v)
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
		if reason := v.call(call{words: c.words, expands: c.expands, env: env}); reason != "" {
			return reason
		}
		// An exported alias reaches every later git.
		return envAliases(env, v)
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
		case opts && (a == "-C" || a == "--chdir" || strings.HasPrefix(a, "--chdir=") || strings.HasPrefix(a, "-C")):
			value, expanded := "", expands[i]
			switch {
			case strings.HasPrefix(a, "--chdir="):
				value = strings.TrimPrefix(a, "--chdir=")
			case strings.HasPrefix(a, "-C") && len(a) > 2:
				value = a[2:]
			case i+1 < len(args):
				i++
				value, expanded = args[i], expands[i]
			}
			visit := func() string { return unwrapEnv(env, args[i+1:], expands[i+1:], v) }
			if v.chdir != nil {
				return v.chdir(value, expanded, visit)
			}
			return visit()
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
			c := splitArguments(script)
			return unwrapEnv(env, append(c.words, args[i+1:]...), append(c.expands, expands[i+1:]...), v)
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
		if program == "fmt" {
			program = "nix-fmt" // a flake's formatter, not coreutils' fmt
		}
	}
	words := append([]string{program}, args[min(i, len(args)):]...)
	x := append([]bool{expanded}, expands[min(i, len(expands)):]...)
	return v.call(call{words: words, expands: x, env: env})
}

// unwrapGit hands on the command a git command runs for itself, once v
// has judged the git command: a shell alias given for this command alone
// (envAlias), `bisect run`'s, `submodule foreach`'s script and
// `rebase --exec`'s.
func unwrapGit(env, args []string, expands []bool, v visitor) string {
	if reason := envAliases(env, v); reason != "" {
		return reason
	}
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		var entry string // KEY=VALUE, as -c or --config-env gives it
		expanded := false
		switch a := args[i]; {
		case a == "-c" && i+1 < len(args):
			entry, expanded = args[i+1], i+1 < len(expands) && expands[i+1]
		case a == "--config-env" && i+1 < len(args), strings.HasPrefix(a, "--config-env="):
			spec := strings.TrimPrefix(a, "--config-env=")
			if a == "--config-env" {
				spec = args[i+1]
			}
			key, variable, _ := strings.Cut(spec, "=")
			value, ok := envValue(env, variable)
			if !ok && aliasKey(key) {
				return aliasUnknown
			}
			entry = key + "=" + value
		}
		if key, _, _ := strings.Cut(entry, "="); strings.ContainsAny(key, "$`") {
			return aliasUnknown // a key from a variable may be an alias's
		}
		if key, value, ok := strings.Cut(entry, "="); ok {
			if reason := aliasScript(key, value, expanded, v); reason != "" {
				return reason
			}
		}
		if gitValueFlags[args[i]] {
			i++
		}
		i++
	}
	if i >= len(args) {
		return ""
	}
	sub, rest, x := args[i], args[i+1:], expands[i+1:]
	switch {
	case sub == "bisect" && len(rest) > 1 && rest[0] == "run":
		return unwrap(call{words: rest[1:], expands: x[1:], env: env}, v)
	case sub == "submodule":
		for j, w := range rest {
			if w == "foreach" {
				k := j + 1
				for k < len(rest) && strings.HasPrefix(rest[k], "-") {
					k++
				}
				return v.script(strings.Join(rest[k:], " "))
			}
		}
	case sub == "rebase":
		for j, w := range rest {
			switch {
			case (w == "-x" || w == "--exec") && j+1 < len(rest):
				if reason := v.script(rest[j+1]); reason != "" {
					return reason
				}
			case strings.HasPrefix(w, "--exec="):
				if reason := v.script(strings.TrimPrefix(w, "--exec=")); reason != "" {
					return reason
				}
			}
		}
	}
	return ""
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
	out, _, _ := backgroundSegments(text)
	return out
}

// backgroundSegments is segments, with whether each runs in the
// background (it, or the subshell it is in, ends with a lone &) and what
// ends each: "&&", "||", ";", "|", "&", a newline, "(" or ")" where a
// subshell opens or closes, "$" where a command substitution ends, or ""
// at the end of the line.
func backgroundSegments(text string) (out []string, background []bool, ends []string) {
	type frame struct {
		cur      strings.Builder
		quote    byte  // the quote this frame is inside, if any
		backtick bool  // the frame is a backquoted substitution
		parens   int   // subshells open inside a $( ) frame
		sub      bool  // the frame is a substitution
		groups   []int // where each subshell open in the frame starts in out
		closed   int   // where the subshell that just closed started, or -1
		redirect bool  // the preceding unquoted character was < or >
	}
	stack := []*frame{{closed: -1}}
	top := func() *frame { return stack[len(stack)-1] }
	flush := func(end string) {
		f := top()
		out = append(out, f.cur.String())
		background = append(background, false)
		ends = append(ends, end)
		f.cur.Reset()
	}
	open := func(backtick bool) {
		top().cur.WriteString("$_")
		stack = append(stack, &frame{backtick: backtick, sub: true, closed: -1})
	}
	closeSub := func() {
		flush("$")
		stack = stack[:len(stack)-1]
	}
	for i := 0; i < len(text); i++ {
		c, f := text[i], top()
		afterRedirect := f.redirect
		f.redirect = false
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
			flush("(")
			f.groups = append(f.groups, len(out))
		case c == ')':
			f.parens = max(0, f.parens-1)
			flush(")")
			if n := len(f.groups); n > 0 {
				f.closed, f.groups = f.groups[n-1], f.groups[:n-1]
			}
		case c == '|' && afterRedirect:
			f.cur.WriteByte(c)
		case c == '&' && afterRedirect,
			c == '&' && i+1 < len(text) && text[i+1] == '>':
			// A redirection: >&2, 2>&1, <&3, &>file.
			f.cur.WriteByte(c)
		case c == '&' && (i == 0 || text[i-1] != '&' && text[i-1] != '|') && (i+1 == len(text) || text[i+1] != '&'):
			whole := strings.TrimSpace(f.cur.String()) == "" && f.closed >= 0
			flush("&")
			from := len(out) - 1
			if whole {
				from = f.closed
			}
			for j := from; j < len(out); j++ {
				background[j] = true
			}
			f.closed = -1
		case c == ';' || c == '\n' || c == '|' || c == '&':
			end := string(c)
			if (c == '&' || c == '|') && i+1 < len(text) && text[i+1] == c {
				end += end // the second of the pair ends an empty segment
			}
			flush(end)
		default:
			f.cur.WriteByte(c)
			f.redirect = c == '>' || c == '<'
		}
	}
	for len(stack) > 1 {
		closeSub()
	}
	flush("")
	return out, background, ends
}

// aliasUnknown is why an alias whose name or value the guard cannot read
// is refused.
const aliasUnknown = "This git alias's name or value comes from a variable or an expansion, so the guard cannot read what it runs. " +
	"Give the alias literally (`-c alias.NAME='!CMD'`), or run the command it stands for."

// aliasKey reports whether a git config key defines an alias.
func aliasKey(key string) bool { return strings.HasPrefix(strings.ToLower(key), "alias.") }

// aliasScript hands an alias entry given for one git command (KEY=VALUE)
// to v as the shell command it runs when its value starts with !. A value
// the shell expands, which could become one, is refused.
func aliasScript(key, value string, expanded bool, v visitor) string {
	if !aliasKey(key) {
		return ""
	}
	if script, ok := strings.CutPrefix(strings.TrimLeft(value, " \t"), "!"); ok {
		return v.script(script)
	}
	if expanded && strings.ContainsAny(value, "$`") {
		return aliasUnknown
	}
	return ""
}

// envValue is the value env assigns to name.
func envValue(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if n, value, _ := strings.Cut(env[i], "="); n == name {
			return value, true
		}
	}
	return "", false
}

// envAliases hands to v each shell alias the assignments in env give git:
// GIT_CONFIG_PARAMETERS's entries, each GIT_CONFIG_KEY_n with its
// GIT_CONFIG_VALUE_n, and the aliases of the file GIT_CONFIG_GLOBAL,
// GIT_CONFIG_SYSTEM or GIT_CONFIG names. An alias key whose value env does
// not give, or gives through an expansion, a key that comes from one, and
// such a file the guard cannot read or that includes another, are refused.
func envAliases(env []string, v visitor) string {
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case name == "GIT_CONFIG_GLOBAL" || name == "GIT_CONFIG_SYSTEM" || name == "GIT_CONFIG":
			if value == "/dev/null" {
				continue
			}
			entries, reason := configFileEntries(shellPath(value, true))
			if reason != "" {
				return reason
			}
			for _, e := range entries {
				if reason := aliasScript(e[0], e[1], false, v); reason != "" {
					return reason
				}
			}
		case name == "GIT_CONFIG_PARAMETERS":
			c := splitArguments(value)
			for j, w := range c.words {
				key, val, _ := strings.Cut(w, "=")
				if strings.ContainsAny(key, "$`") {
					return aliasUnknown
				}
				if reason := aliasScript(key, val, c.expands[j], v); reason != "" {
					return reason
				}
			}
		case strings.HasPrefix(name, "GIT_CONFIG_KEY_") && strings.ContainsAny(value, "$`"):
			return aliasUnknown
		case strings.HasPrefix(name, "GIT_CONFIG_KEY_") && aliasKey(value):
			val, ok := envValue(env, "GIT_CONFIG_VALUE_"+strings.TrimPrefix(name, "GIT_CONFIG_KEY_"))
			if !ok {
				return aliasUnknown
			}
			if reason := aliasScript(value, val, true, v); reason != "" {
				return reason
			}
		}
	}
	return ""
}

// cwds follows where the commands of one command line may run as cd moves
// them: here, the directories the next command may run in ("" for one the
// guard cannot tell), and before, where the line may be instead if a cd
// in the && chain under way fails. A cd holds for certain only over the
// commands joined to it by &&. Past any other end, the shell may be where
// the cd went or, the cd having failed or run in a subshell or pipeline,
// where it was. A cd that is an if's or a while's condition holds over the
// branch it guards, and one that && joins to a { } group over the group.
type cwds struct {
	here, before []string
	// The call under way is the condition of an if, while or until, and a
	// cd.
	condition bool
	// The if, while, until and { } blocks open, each with where the line
	// is if the cd guarding it failed; nil for a block no cd guards.
	blocks []*[]string
}

// begin moves w for the shell keyword a call c starts with, before c is
// judged: it opens or closes a block, or enters an else branch, which runs
// where the guarding cd's failure left the line.
func (w *cwds) begin(c call) {
	w.condition = false
	if len(c.words) == 0 || c.expands[0] {
		return
	}
	switch c.words[0] {
	case "if", "while", "until":
		w.blocks = append(w.blocks, nil)
		cd := len(c.words) > 2 && !c.expands[1] && (c.words[1] == "cd" || c.words[1] == "pushd")
		// until's body runs while the cd fails, and while's runs the cd
		// again from where the body left it, the same place only for an
		// absolute path.
		w.condition = cd && (c.words[0] == "if" ||
			c.words[0] == "while" && strings.HasPrefix(shellPath(c.words[2], c.expands[2]), "/"))
	case "{":
		var outside *[]string
		if len(w.before) > 0 { // a cd's && chain runs into the group
			before := w.before
			outside, w.before = &before, nil
		}
		w.blocks = append(w.blocks, outside)
	case "else", "elif":
		if n := len(w.blocks); n > 0 && w.blocks[n-1] != nil {
			w.here = slices.Clone(*w.blocks[n-1])
		}
	case "fi", "done", "}":
		if n := len(w.blocks); n > 0 {
			if outside := w.blocks[n-1]; outside != nil {
				for _, d := range *outside {
					w.here = addDir(w.here, d)
				}
			}
			w.blocks = w.blocks[:n-1]
		}
	}
}

// cd moves w for a cd or pushd call c that end ends.
func (w *cwds) cd(c call, end string) {
	var to []string
	for _, d := range w.here {
		to = addDir(to, cdTarget(c, d))
	}
	if n := len(w.blocks); w.condition && n > 0 && (end == ";" || end == "\n") {
		outside := w.here
		w.blocks[n-1], w.here = &outside, to
		return
	}
	if end == "&&" {
		for _, d := range w.here {
			w.before = addDir(w.before, d)
		}
		w.here = to
		return
	}
	for _, d := range to {
		w.here = addDir(w.here, d)
	}
}

// next moves w past the end of a call.
func (w *cwds) next(end string) {
	if end == "&&" {
		return
	}
	for _, d := range w.before {
		w.here = addDir(w.here, d)
	}
	w.before = nil
}

func (w *cwds) inDir(word string, expanded bool, visit func() string) string {
	before := w.here
	w.here = nil
	for _, cwd := range before {
		c := call{words: []string{"cd", word}, expands: []bool{false, expanded}}
		w.here = addDir(w.here, cdTarget(c, cwd))
	}
	defer func() { w.here = before }()
	return visit()
}

// addDir is dirs with d, once.
func addDir(dirs []string, d string) []string {
	if slices.Contains(dirs, d) {
		return dirs
	}
	return append(dirs, d)
}

type redirection struct {
	op, target string
	expands    bool
	literal    bool
}

func (r redirection) file() bool {
	return r.op != "<<" && r.op != "<<-" && r.op != "<<<" &&
		!(strings.HasSuffix(r.op, "&") && (r.target == "-" || digits(r.target)))
}

func (r redirection) output() bool { return r.file() && strings.Contains(r.op, ">") }

func (r redirection) path(cwd string) (string, bool) {
	target := r.target
	if r.expands {
		target = shellPath(target, true)
		if strings.ContainsAny(target, "$`") {
			return "", false
		}
	}
	if r.literal {
		if !filepath.IsAbs(target) && cwd != "" {
			target = filepath.Join(cwd, target)
		}
	} else {
		target = expandPath(target, cwd)
	}
	return target, filepath.IsAbs(target)
}

func digits(s string) bool {
	return s != "" && strings.IndexFunc(s, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// split separates executable arguments from shell redirections before
// removing quotes; quoted or escaped operators remain literal arguments.
func split(segment string) call { return lex(segment, true) }

// splitArguments reads words that another program splits, without giving
// redirection operators the shell's meaning (env -S, git config values).
func splitArguments(segment string) call { return lex(segment, false) }

func lex(segment string, redirects bool) (result call) {
	var cur strings.Builder
	in, quote, expand, quoted := false, byte(0), false, false
	braces := 0
	pending := -1
	end := func() {
		if pending >= 0 {
			result.redirects[pending].target, result.redirects[pending].expands = cur.String(), expand
			result.redirects[pending].literal = quoted && !expand
			pending = -1
		} else {
			result.words, result.expands = append(result.words, cur.String()), append(result.expands, expand)
		}
		cur.Reset()
		in, expand, quoted = false, false, false
	}
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch {
		case quote == 'a':
			if c == '\'' {
				quote = 0
			} else if c == '\\' && i+1 < len(segment) {
				i++
				cur.WriteByte(segment[i])
			} else {
				cur.WriteByte(c)
			}
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
		case c == '$' && i+1 < len(segment) && segment[i+1] == '\'':
			cur.WriteByte(c)
			i++
			quote, in, quoted = 'a', true, true
			expand = true
		case c == '$' && i+1 < len(segment) && segment[i+1] == '{':
			braces++
			cur.WriteByte(c)
			in, expand = true, true
		case c == '}' && braces > 0:
			braces--
			cur.WriteByte(c)
			in = true
		case c == '\'' || c == '"':
			quote, in, quoted = c, true, true
		case c == '\\' && i+1 < len(segment):
			i++
			cur.WriteByte(segment[i])
			in, quoted = true, true
		case redirects && (c == '>' || c == '<' || c == '&' && i+1 < len(segment) && segment[i+1] == '>'):
			if in {
				if c != '&' && pending < 0 && !quoted && digits(cur.String()) {
					cur.Reset()
					in, expand = false, false
				} else {
					end()
				}
			}
			op := string(c)
			if c == '&' {
				i++
				op += ">"
				c = '>'
			}
			if i+1 < len(segment) && segment[i+1] == c {
				i++
				op += string(c)
				if c == '<' && i+1 < len(segment) && (segment[i+1] == '<' || segment[i+1] == '-') {
					i++
					op += string(segment[i])
				}
			} else if i+1 < len(segment) && (segment[i+1] == '&' || c == '>' && segment[i+1] == '|' || c == '<' && segment[i+1] == '>') {
				i++
				op += string(segment[i])
			}
			result.redirects = append(result.redirects, redirection{op: op})
			pending = len(result.redirects) - 1
		case c == ' ' || c == '\t':
			if in {
				end()
			}
		case c == '#' && !in && braces == 0:
			return result
		default:
			expand = expand || c == '$' || c == '`'
			cur.WriteByte(c)
			in = true
		}
	}
	if in {
		end()
	}
	return result
}
