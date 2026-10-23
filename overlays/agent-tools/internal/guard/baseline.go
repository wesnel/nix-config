package guard

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

// The rules every session is held to beyond GitHub: what would lose
// the operator's work, read a credential, change their machine or switch the
// guard off. A rule that needs the repository's state asks git, from the
// directory the command runs in.

const (
	sweeping     = "This stages everything in the repository's main checkout, which holds the operator's untracked work in progress. Stage the paths you changed by name (`git add PATH...`), or work in a worktree of your own (`git worktree add`)."
	mainCheckout = "This discards work in the repository's main checkout, which holds the operator's work in progress. Do it in a worktree of your own (`git worktree add`), or ask the operator."
	historyLoss  = "This deletes history git cannot get back, which is the operator's call. Leave it, and put the command in your report for the operator."
	sharedStash  = "Every worktree of a repository shares one stash, so this can discard another worktree's stashed work. Leave the stash as it is, and put the command in your report for the operator."
	sharedConfig = "This setting is shared by every worktree and changes how git runs for the operator too. Put the `git config` command in your report for the operator; a worktree's own setting takes `git config --worktree`."
	secretRead   = "This reads a credential, which stays out of the session. Use the tool that holds it (`gh`, `pr-loop`, `git`), or ask the operator for what you need from it." //nolint:gosec // a refusal message, not a credential
	ngrokWrite   = "ngrok API writes reach production. Use a list or get command, or hand the operator the command."
	systemSwitch = "Activating a system or profile is the operator's step. Build it with `nix build` or `darwin-rebuild build`, and hand the operator the switch command."
	rootCommand  = "Anything needing root is the operator's to run. Put the command in your report for the operator, and say its result is unverified."
	publishing   = "Publishing a package is the operator's step. Build and test it here, and hand the operator the publish command."
	killing      = "The operator's session depends on that process (Emacs, gpg-agent, ssh, nix-daemon or mcp-proxy). Leave it running, and tell the operator if it needs a restart."
	// killPattern and killOthers are why a kill of processes this session
	// did not start is refused: a pattern matches others' processes too.
	killPattern = "A kill by name or pattern can hit processes this session did not start. Kill the PID you started, as `kill PID`, or ask the operator."
	killOthers  = "That process is not one this session started. Kill only the PIDs your own commands started, or ask the operator."
	// killUnseen is why a kill whose PIDs the guard cannot read is refused:
	// `$(pgrep …)`, `$PID` or xargs fills in whatever a pattern matched.
	killUnseen = "The guard cannot see which processes a PID filled in by the shell or xargs names. Kill the PID you started, written as a number, as `kill 12345`, or ask the operator."
	killEmacs  = "Only a test Emacs this session started may be stopped, or its server deleted or restarted, never the operator's daemon, and no Lisp sent to it may name another server. Send it to its own absolute socket under $TMPDIR, /tmp/claude-UID or /tmp/et.*, named by -s alone with no server file, as `emacsclient -s /tmp/et.XXXX/server -e '(kill-emacs)'`."
	destroying = "This would destroy the home directory, the root filesystem, the Nix store or the operator's keys. Name the exact path inside the workspace you mean, or ask the operator."
	guardOff   = "The guard and its hooks are the operator's configuration, and stay on. Put the change you want in your report for the operator."
)

// home is the user's home directory, which ~ and $HOME name.
var home, _ = os.UserHomeDir()

// secretName is an environment variable that likely holds a credential.
var secretName = regexp.MustCompile(`(?i)token|key|secret|password|credential`)

// environ is the guard's own environment, which a session's commands share;
// tests replace it.
var environ = os.Environ

// grepFlags are the grep options an environment search may take, none of
// which reads a pattern from elsewhere or inverts the match.
var grepFlags = set("-E", "-G", "-F", "-n", "-c", "-o", "-q", "-s", "-w", "-x", "-H", "-h",
	"--extended-regexp", "--basic-regexp", "--fixed-strings", "--line-number", "--count", "--only-matching",
	"--quiet", "--silent", "--no-messages", "--word-regexp", "--line-regexp", "--color", "--colour")

// envSearch reports whether text is exactly `env` or `printenv`, with no
// names, piped into one grep, whose one pattern selects no line of the
// guard's environment for a variable whose name looks like a credential's.
// A pattern that selects a credential's line refuses, because the dump
// would print it. The pattern is translated into Go's syntax: grep matches
// each line of a multi-line value on its own, and a basic pattern's ( ) + ?
// and | are literal. Where the two syntaxes could still differ, so that grep
// matches more than Go (a backslash, a brace, a newline, a basic pattern's
// ^ or $ away from its ends, or two pattern syntaxes at once), it refuses.
func envSearch(text string, calls []call) bool {
	segs := segments(text)
	if len(segs) != 2 || len(calls) != 2 || !strings.HasPrefix(text, segs[0]) || text[len(segs[0]):] != "|"+segs[1] {
		return false
	}
	dump, grep := calls[0], calls[1]
	if len(dump.words) != 1 || dump.expands[0] || (dump.words[0] != "env" && dump.words[0] != "printenv") ||
		grep.words[0] != "grep" {
		return false
	}
	fold := false
	syntax := map[byte]bool{} // 'G' basic, 'E' extended, 'F' fixed strings
	flag := func(f byte) {
		switch f {
		case 'i', 'y':
			fold = true
		case 'E', 'F', 'G':
			syntax[f] = true
		}
	}
	var patterns []string
	for i, w := range grep.words[1:] {
		switch {
		case grep.expands[i+1]:
			return false
		case w == "--ignore-case":
			fold = true
		case w == "--extended-regexp":
			flag('E')
		case w == "--fixed-strings":
			flag('F')
		case w == "--basic-regexp":
			flag('G')
		case grepFlags[w] || strings.HasPrefix(w, "--color=") || strings.HasPrefix(w, "--colour="):
			if !strings.HasPrefix(w, "--") {
				flag(w[1])
			}
		case strings.HasPrefix(w, "--"):
			return false
		case strings.HasPrefix(w, "-") && w != "-":
			for _, f := range []byte(w[1:]) {
				if f != 'i' && f != 'y' && !grepFlags["-"+string(f)] {
					return false
				}
				flag(f)
			}
		default:
			patterns = append(patterns, w)
		}
	}
	if len(patterns) != 1 || len(syntax) > 1 || strings.ContainsAny(patterns[0], "\\{\n") {
		return false
	}
	p := patterns[0]
	switch {
	case syntax['F']:
		p = regexp.QuoteMeta(p)
	case !syntax['E']:
		if i := strings.LastIndex(p, "^"); i > 0 {
			return false
		}
		if i := strings.Index(p, "$"); i >= 0 && i != len(p)-1 {
			return false
		}
		var quoted strings.Builder
		for _, c := range p {
			if strings.ContainsRune("()+?|", c) {
				quoted.WriteByte('\\')
			}
			quoted.WriteRune(c)
		}
		p = quoted.String()
	}
	p = "(?m)" + p
	if fold {
		p = "(?i)" + p
	}
	re, err := regexp.Compile(p)
	if err != nil {
		return false
	}
	for _, kv := range environ() {
		if name, _, _ := strings.Cut(kv, "="); secretName.MatchString(name) && re.MatchString(kv) {
			return false
		}
	}
	return true
}

// guardFile reports whether path holds the hooks that run the guard, or
// its settings.
func guardFile(path string) bool {
	if agentToolsConfig(path) {
		return true
	}
	for _, p := range []string{".codex/config.toml", ".codex/hooks.json"} {
		if path == filepath.Join(home, p) {
			return true
		}
	}
	return claudeSettings.MatchString(path)
}

var claudeSettings = regexp.MustCompile(`(^|/)\.claude/settings[^/]*\.json$`)

// expandPath is word as the shell would name a file: with ~ and $HOME
// expanded, and relative to dir, cleaned. Cleaning takes `link/..` away
// by spelling, where the file system climbs from where the link leads:
// physical(rawPath(word, dir)) is the path the file system walks.
func expandPath(word, dir string) string {
	return filepath.Clean(rawPath(word, dir))
}

// rawPath is expandPath's path before it is cleaned.
func rawPath(word, dir string) string {
	for _, h := range []string{"${HOME}", "$HOME"} {
		if word == h || strings.HasPrefix(word, h+"/") {
			word = home + strings.TrimPrefix(word, h)
		}
	}
	if word == "~" || strings.HasPrefix(word, "~/") {
		word = home + word[1:]
	}
	if !filepath.IsAbs(word) && dir != "" {
		word = dir + "/" + word
	}
	return word
}

// checkSession checks a call against the rules for every session that are
// not about GitHub. dir is where it runs, "" when the guard cannot tell.
func checkSession(c call, dir string) string {
	words := c.words
	name, args := base(words[0]), words[1:]
	if versionedEmacsclient.MatchString(name) {
		name = "emacsclient"
	}
	for _, w := range words {
		if reason := checkSecretPath(w, dir); reason != "" {
			return reason
		}
	}
	redirected := redirections(words)
	for _, target := range redirected {
		if guardFile(expandPath(target, dir)) {
			return guardOff
		}
	}
	for _, target := range changedFiles(name, args, redirected) {
		if reason := checkSecretWrite(target, dir); reason != "" {
			return reason
		}
	}
	if reason := checkPlanLogWrite(name, args, redirected, dir); reason != "" {
		return reason
	}
	if reason := checkShellEdit(name, args, redirected, dir); reason != "" {
		return reason
	}
	if gitConfigShellWrite(name, args, redirected, dir) {
		return gitConfigShell
	}
	has := func(flags ...string) bool {
		for _, a := range args {
			for _, f := range flags {
				if a == f || strings.HasPrefix(a, f+"=") {
					return true
				}
			}
		}
		return false
	}
	positional := func() []string {
		var out []string
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				out = append(out, a)
			}
		}
		return out
	}
	first := func() string {
		if p := positional(); len(p) > 0 {
			return p[0]
		}
		return ""
	}
	if versionedEmacs.MatchString(name) {
		name = "emacs"
	}
	switch name {
	case "sudo", "doas", "su", "pkexec", "run0":
		return rootCommand
	case "osascript":
		if strings.Contains(strings.ToLower(strings.Join(args, " ")), "with administrator privileges") {
			return rootCommand
		}
	case "env", "printenv":
		names := positional()
		if len(names) == 0 {
			return secretRead
		}
		for _, n := range names {
			if name == "printenv" && secretName.MatchString(n) {
				return secretRead
			}
		}
	case "export", "declare", "typeset":
		if len(args) == 0 || has("-p") {
			return secretRead
		}
	case "set":
		if len(args) == 0 {
			return secretRead
		}
	case "security":
		switch first() {
		case "dump-keychain", "export":
			return secretRead
		case "find-generic-password", "find-internet-password":
			for _, a := range args {
				if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "wg") {
					return secretRead
				}
			}
		}
	case "git-credential-osxkeychain":
		return secretRead
	case "sops":
		if has("-d", "--decrypt") || first() == "decrypt" || first() == "exec-env" || first() == "exec-file" {
			return secretRead
		}
	case "op":
		p := positional()
		if len(p) > 0 && (p[0] == "read" || p[0] == "inject" || (p[0] == "item" && len(p) > 1 && p[1] == "get")) {
			return secretRead
		}
	case "pass":
		switch p := positional(); {
		case len(p) == 0:
		case p[0] == "ls" || p[0] == "list" || p[0] == "find" || p[0] == "search" || p[0] == "help" || p[0] == "version":
		default:
			return secretRead
		}
	case "ngrok":
		if p := positional(); len(p) > 0 && p[0] == "api" {
			for _, w := range p[1:] {
				if ngrokWrites[w] {
					return ngrokWrite
				}
			}
		}
	case "darwin-rebuild", "nixos-rebuild", "home-manager", "nh":
		for _, a := range args {
			if switchVerbs[name][a] {
				return systemSwitch
			}
		}
	case "activate", "activate-user":
		if strings.Contains(words[0], "/") {
			return systemSwitch
		}
	case "nix":
		p := positional()
		switch {
		case len(p) > 1 && p[0] == "profile" && set("install", "add", "remove", "upgrade", "rollback", "wipe-history")[p[1]]:
			return systemSwitch
		case len(p) > 0 && p[0] == "copy":
			for i, a := range args {
				to := strings.TrimPrefix(a, "--to=")
				if a == "--to" && i+1 < len(args) {
					to = args[i+1]
				} else if to == a {
					continue
				}
				if strings.Contains(to, "://") && !strings.HasPrefix(to, "file://") {
					return systemSwitch
				}
			}
		}
	case "nix-env":
		for _, a := range args {
			if set("--install", "--uninstall", "--upgrade", "--set", "--rollback", "--switch-generation", "--delete-generations")[a] ||
				nixEnvWrite.MatchString(a) {
				return systemSwitch
			}
		}
	case "nix-collect-garbage":
		if has("-d", "--delete-old", "--delete-older-than") {
			return systemSwitch
		}
	case "cachix":
		if first() == "push" {
			return systemSwitch
		}
	case "npm", "pnpm", "yarn", "bun":
		for _, p := range positional() {
			if p == "publish" {
				return publishing
			}
		}
	case "cargo", "vsce":
		if first() == "publish" {
			return publishing
		}
	case "twine":
		if first() == "upload" {
			return publishing
		}
	case "gem":
		if first() == "push" {
			return publishing
		}
	case "docker", "podman":
		for _, p := range positional() {
			if p == "push" {
				return publishing
			}
		}
		if has("--push") {
			return publishing
		}
	case "goreleaser":
		if !set("check", "build", "healthcheck", "init", "jsonschema", "completion", "help", "man")[first()] &&
			!has("-h", "--help", "-v", "--version") {
			return publishing
		}
	case "pkill", "killall":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				return killPattern
			}
		}
	case "kill":
		return checkKill(args)
	case "gpgconf":
		if has("--kill") {
			return killing
		}
	case "ssh", "scp", "sftp":
		for i, a := range args {
			if name == "ssh" && ((a == "-O" && i+1 < len(args) && (args[i+1] == "exit" || args[i+1] == "stop")) || a == "-Oexit" || a == "-Ostop") {
				return killing
			}
		}
		if sshTouch(name, args, dir) {
			return keyTouch
		}
	case "launchctl":
		switch first() {
		case "bootout", "unload", "remove":
			return killing
		case "kickstart":
			if has("-k") {
				return killing
			}
		}
	case "emacsclient":
		if emacsServerCall(args) {
			return killEmacs
		}
	case "emacs", "Emacs":
		// A batch Emacs with none of the operator's init is how elisp is
		// checked: a sandboxed command reaches no Emacs daemon. Anything
		// else starts an Emacs that runs the operator's init, or a daemon.
		quick := has("-Q", "--quick")
		batch := has("--batch", "-batch", "--script", "-script")
		daemon := slices.ContainsFunc(args, emacsDaemonFlag)
		if !quick || !batch || daemon {
			return "Only a batch Emacs with none of the operator's init checks elisp here: no daemon is reachable from " +
				"the sandbox. Run `emacs -Q --batch --init-directory=DIR ...`, DIR from `mktemp -d`, as the emacsclient " +
				"skill says."
		}
		// A batch Emacs's own server is the default one, the operator's.
		if emacsBatchServerCall(emacsBatchLisp(args, dir)) {
			return emacsBatchServer
		}
	case "rm", "trash", "chmod", "chown", "chgrp", "mv":
		recursive := name == "trash" || name == "mv"
		for _, a := range args {
			recursive = recursive || a == "--recursive" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a, "rR"))
		}
		// Only paths named literally, or by ~ or $HOME: the guard does not
		// resolve a relative path, which would put every build under /nix.
		for _, a := range positional() {
			if protectedTree(expandPath(a, ""), recursive) {
				return destroying
			}
		}
		if name != "chmod" && name != "chown" && name != "chgrp" {
			for _, a := range positional() {
				if guardFile(expandPath(a, dir)) {
					return guardOff
				}
			}
		}
	case "find":
		if has("-delete") || strings.Contains(strings.Join(args, " "), "-exec rm") {
			for _, a := range args {
				if strings.HasPrefix(a, "-") {
					break
				}
				if protectedTree(expandPath(a, ""), true) {
					return destroying
				}
			}
		}
	case "tee", "cp", "ln", "install", "truncate", "dd", "sed", "perl":
		if (name == "sed" || name == "perl") && !inPlace(name, args) {
			break
		}
		for _, t := range writeTargets(name, args) {
			if guardFile(expandPath(t, dir)) {
				return guardOff
			}
		}
	case "claude":
		if has("--dangerously-skip-permissions", "--allow-dangerously-skip-permissions") ||
			strings.Contains(strings.Join(args, " "), "bypassPermissions") {
			return guardOff
		}
	case "codex":
		for _, a := range args {
			if strings.HasPrefix(a, "--dangerously-bypass") {
				return guardOff
			}
		}
	}
	if (name == "rm" || name == "unlink" || name == "find" && has("-delete")) && trashOnPath() {
		return useTrash
	}
	return ""
}

// inPlace reports whether sed's, perl's or awk's arguments edit their files
// in place: for sed and perl, --in-place, or an i in a cluster of short
// options (-i, -i.bak, -Ei, -ni, -pi), read up to the first option whose
// value ends the cluster; for awk, gawk's inplace extension (-i inplace,
// --include=inplace).
func inPlace(name string, args []string) bool {
	values := map[string]string{"sed": "efl", "perl": "eEMmIxCdDF"}[name]
	for i, a := range args {
		switch name {
		case "sed", "perl":
			if a == "--in-place" || strings.HasPrefix(a, "--in-place=") {
				return true
			}
			if !strings.HasPrefix(a, "-") || strings.HasPrefix(a, "--") {
				continue
			}
			for _, c := range a[1:] {
				if c == 'i' {
					return true
				}
				if strings.ContainsRune(values, c) {
					break
				}
			}
		case "awk", "gawk":
			var ext string
			switch {
			case a == "-i" || a == "--include":
				if i+1 < len(args) {
					ext = args[i+1]
				}
			case strings.HasPrefix(a, "--include="):
				ext = strings.TrimPrefix(a, "--include=")
			case strings.HasPrefix(a, "-i"):
				ext = strings.TrimPrefix(a, "-i")
			}
			if strings.TrimSuffix(ext, ".awk") == "inplace" {
				return true
			}
		}
	}
	return false
}

// writeTargets are the files one of tee, cp, ln, install, truncate, dd,
// sed -i or perl -i writes. cp, ln and install write only their
// destination, and dd only of=, so a guard file they read from passes.
// A destination that may be a directory is judged with each source's name
// inside it as well.
func writeTargets(name string, args []string) []string {
	switch name {
	case "dd":
		var out []string
		for _, a := range args {
			if strings.HasPrefix(a, "of=") {
				out = append(out, strings.TrimPrefix(a, "of="))
			}
		}
		return out
	case "cp", "ln", "install":
		// Their short options that take a value, besides -t.
		values := map[string]string{"cp": "S", "ln": "S", "install": "gmoS"}[name]
		var target string
		var paths []string
		options := true
		for i := 0; i < len(args); i++ {
			a := args[i]
			next := func() string {
				if i+1 < len(args) {
					i++
					return args[i]
				}
				return ""
			}
			switch {
			case !options || a == "-" || !strings.HasPrefix(a, "-"):
				paths = append(paths, a)
			case a == "--":
				options = false
			case a == "--target-directory":
				target = next()
			case strings.HasPrefix(a, "--target-directory="):
				target = strings.TrimPrefix(a, "--target-directory=")
			case a == "--suffix" || a == "--mode" || a == "--owner" || a == "--group":
				next()
			case strings.HasPrefix(a, "--"):
			default:
				// A cluster of short options, which -t DIR or another
				// option's value ends, attached or as the next word.
				for j := 1; j < len(a); j++ {
					c := a[j]
					if c != 't' && !strings.ContainsRune(values, rune(c)) {
						continue
					}
					v := a[j+1:]
					if v == "" {
						v = next()
					}
					if c == 't' {
						target = v
					}
					break
				}
			}
		}
		sources := paths
		var out []string
		if target == "" {
			if len(paths) == 0 {
				return nil
			}
			target, sources = paths[len(paths)-1], paths[:len(paths)-1]
			out = append(out, target)
		}
		for _, s := range sources {
			out = append(out, filepath.Join(target, filepath.Base(s)))
		}
		return out
	}
	return args
}

const realEnvironment = "NGROK_ENV set to prod or stage points ngrok's tooling at a real environment, which is the operator's to act on. " +
	"Use a dev or local environment, or put the command in your report for the operator."

// realNgrokEnv matches a value of NGROK_ENV that names a real environment.
var realNgrokEnv = regexp.MustCompile(`(?i)^(prod|production|stage|staging)$`)

// checkNgrokEnv denies a call that sets NGROK_ENV to a real environment:
// by an assignment the shell, env or export makes, or by arguments that
// pass it on, as `make X NGROK_ENV=prod`, `docker run -e NGROK_ENV=prod`,
// fish's `set -gx NGROK_ENV prod` and `launchctl setenv NGROK_ENV prod` do.
func checkNgrokEnv(c call) string {
	for _, kv := range c.env {
		if name, value, _ := strings.Cut(kv, "="); name == "NGROK_ENV" && realNgrokEnv.MatchString(value) {
			return realEnvironment
		}
	}
	for i, w := range c.words {
		if c.expands[i] {
			continue
		}
		value, ok := strings.CutPrefix(w, "NGROK_ENV=")
		if !ok && w == "NGROK_ENV" && i+1 < len(c.words) && !c.expands[i+1] {
			value, ok = c.words[i+1], true
		}
		if ok && realNgrokEnv.MatchString(value) {
			return realEnvironment
		}
	}
	return ""
}

const (
	// sleeping is for Codex, which can only wait in the foreground.
	sleeping = "A sleep of 30 seconds or more in the foreground holds the session while nothing happens. " +
		"Wait with a command that polls for the condition and exits when it holds, such as `until CHECK; do sleep 5; done`."
	// sleepingClaude is sleeping for Claude Code, which runs a command in
	// the background and tells the session when it ends.
	sleepingClaude = "A sleep of 30 seconds or more in the foreground holds the session while nothing happens. " +
		"Run the wait with `run_in_background` and a command that exits when the condition holds, such as `until CHECK; do sleep 5; done`, or watch for it with Monitor."
)

// longSleep reports whether a call is a sleep of 30 seconds or more, by
// the arguments GNU sleep adds up: numbers with an optional s, m, h or d,
// and infinity. One the shell expands, or sleep would refuse, counts as
// shorter.
func longSleep(c call) bool {
	if len(c.words) < 2 || base(c.words[0]) != "sleep" {
		return false
	}
	units := map[string]float64{"": 1, "s": 1, "m": 60, "h": 3600, "d": 86400}
	total := 0.0
	for i, a := range c.words[1:] {
		if c.expands[i+1] {
			return false
		}
		if strings.EqualFold(a, "infinity") || strings.EqualFold(a, "inf") {
			return true
		}
		number := strings.TrimRight(a, "smhd")
		n, err := strconv.ParseFloat(number, 64)
		unit, known := units[a[len(number):]]
		if err != nil || !known {
			return false
		}
		total += n * unit
	}
	return total >= 30
}

const readsTerminal = "This reads its input from the terminal, which Codex keeps open for write_stdin, and text typed there reaches no hook. " +
	"Run the command itself, as `bash -c '...'`, `python3 script.py` or `cat FILE`, or put what you need in your report for the operator."

// terminalReader reports whether a command line is one command that reads
// its input from the terminal: a shell or REPL with nothing to run, or a
// program that reads standard input, with none redirected or piped in.
func terminalReader(text string) bool {
	var calls []call
	for _, segment := range segments(text) {
		if words, expands := split(segment); len(words) > 0 {
			calls = append(calls, call{words: words, expands: expands})
		}
	}
	if len(calls) != 1 {
		return false
	}
	for _, w := range calls[0].words {
		if strings.HasPrefix(w, "<") {
			return false
		}
	}
	// xargs is a wrapper unwrap looks through, and reads the terminal
	// whatever it runs.
	if c := calls[0]; !c.expands[0] && base(c.words[0]) == "xargs" {
		return !contains(c.words, "-a") && !contains(c.words, "--arg-file")
	}
	var found *call
	unwrap(calls[0], visitor{
		script: func(string) string { return "" },
		call:   func(c call) string { found = &c; return "" },
	})
	if found == nil || len(found.words) == 0 || found.expands[0] {
		return false
	}
	name, args := base(found.words[0]), found.words[1:]
	var positional []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
		}
	}
	has := func(flags ...string) bool {
		for _, a := range args {
			if set(flags...)[strings.SplitN(a, "=", 2)[0]] {
				return true
			}
		}
		return false
	}
	switch {
	case shells[name], name == "ruby", name == "perl", name == "lua":
		return len(positional) == 0
	case strings.HasPrefix(name, "python"), name == "node":
		return has("-i", "--interactive") || len(positional) == 0
	case name == "irb", name == "ghci":
		return true
	case name == "nix":
		return len(positional) > 0 && positional[0] == "repl"
	case name == "sqlite3":
		return len(positional) <= 1
	case name == "psql", name == "mysql":
		return !has("-c", "--command", "-f", "--file", "-e", "--execute")
	case name == "cat":
		for _, p := range positional {
			if p != "-" {
				return false
			}
		}
		return true
	case name == "tee":
		return true
	case name == "dd":
		for _, a := range args {
			if strings.HasPrefix(a, "if=") {
				return false
			}
		}
		return true
	}
	return false
}

// nixEnvWrite is nix-env's short options that change a profile.
var nixEnvWrite = regexp.MustCompile(`^-[ieuG]`)

// ngrokWrites are the verbs of the ngrok MCP filter's writes.
var ngrokWrites = set("create", "update", "delete", "remove", "start", "stop", "restart", "set", "put", "patch", "post",
	"add", "replace", "rotate", "enable", "disable", "revoke", "write", "manage", "submit", "send", "run", "exec",
	"execute", "invoke", "import", "upload", "attach", "detach")

// switchVerbs are the words that make each system tool activate something.
var switchVerbs = map[string]map[string]bool{
	"darwin-rebuild": set("switch", "activate", "rollback", "--rollback", "--switch-generation"),
	"nixos-rebuild":  set("switch", "boot", "test", "--rollback"),
	"home-manager":   set("switch", "activate", "remove-generations", "expire-generations", "uninstall"),
	"nh":             set("switch", "boot", "test"),
}

// protectedProcess reports whether a process name or pattern names a
// process the operator's session depends on.
func protectedProcess(name string) bool {
	name = strings.ToLower(name)
	for _, p := range []string{"emacs", "gpg-agent", "ssh", "nix-daemon", "mcp-proxy"} {
		if strings.Contains(name, p) {
			return true
		}
	}
	return false
}

// parentOf is pid's parent, by ps, or 0 where ps cannot say; tests replace
// it.
var parentOf = func(pid int) int {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
}

// commandOf is pid's command name, by ps, or ""; tests replace it.
var commandOf = func(pid int) string {
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}

// harness is the session's harness process: the nearest ancestor of the
// guard, which the harness runs as a hook, whose command is claude, codex
// or node; 0 where there is none.
var harness = func() int {
	for pid, n := os.Getppid(), 0; pid > 1 && n < 64; pid, n = parentOf(pid), n+1 {
		switch commandOf(pid) {
		case "claude", "codex", "node":
			return pid
		}
	}
	return 0
}

// startedHere reports whether pid descends from the session's harness: a
// process one of this session's commands started.
func startedHere(pid int) bool {
	h := harness()
	if h == 0 {
		return false
	}
	for p, n := parentOf(pid), 0; p > 1 && n < 64; p, n = parentOf(p), n+1 {
		if p == h {
			return true
		}
	}
	return false
}

// emacsDaemonFlag reports whether an Emacs argument starts a daemon, in
// any spelling Emacs takes: one dash or two, -daemon, -bg-daemon or
// -fg-daemon, with or without =NAME, and a long option cut short, as
// Emacs matches one by its prefix. A prefix too short for Emacs to take
// counts too.
func emacsDaemonFlag(a string) bool {
	if !strings.HasPrefix(a, "-") {
		return false
	}
	name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
	for _, d := range []struct {
		flag string
		min  int
	}{{"daemon", 2}, {"bg-daemon", 4}, {"fg-daemon", 4}} {
		if len(name) >= d.min && strings.HasPrefix(d.flag, name) {
			return true
		}
	}
	return false
}

// emacsBatchServer is why a batch Emacs may not run Lisp that names a
// server, or starts one under the default name: with no server of its
// own, it acts on the operator's.
const emacsBatchServer = "A batch Emacs that starts, deletes or evaluates in a server under the default name reaches the operator's daemon. Set `server-name` to a test socket's absolute path, as `(setq server-name \"/tmp/et.XXXX/server\")`, in the same Lisp, or leave the server forms out of the check."

// emacsBatchServerCall reports whether a batch Emacs's Lisp reaches the
// operator's server. It reads the Lisp's code, not its comments or
// strings, so a form named in a message or a comment is no call. Where
// every mention of server-name sets it to a test socket's absolute path,
// as a string, the Lisp acts on that server, and passes unless it names
// another; otherwise a server-start or server-mode, or a
// server-force-delete with no name, acts on the default one, theirs.
func emacsBatchServerCall(text string) bool {
	text = lispCode(text)
	named := emacsServerNameSet.FindAllStringSubmatch(text, -1)
	testNamed := len(named) > 0 && len(named) == len(emacsServerNameWord.FindAllString(text, -1))
	for _, m := range named {
		testNamed = testNamed && testSocket(m[1])
	}
	if testNamed {
		return lispNamesServer(emacsServerNameSet.ReplaceAllString(text, ""), false)
	}
	return lispNamesServer(text, true) || emacsServerStart.MatchString(text)
}

// lispCode is Emacs Lisp text with its comments blanked, and each string's
// contents blanked but a server name's: the string just after server-name
// or server-force-delete, which the server check reads.
func lispCode(text string) string {
	b := []byte(text)
	blank := func(from, to int) {
		for k := from; k < to; k++ {
			if b[k] != '\n' {
				b[k] = ' '
			}
		}
	}
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case '?':
			// A character literal: ?" or ?\" is no string.
			if i+1 < len(b) && b[i+1] == '\\' {
				i++
			}
			i++
		case ';':
			end := i
			for end < len(b) && b[end] != '\n' {
				end++
			}
			blank(i, end)
			i = end
		case '"':
			end := i + 1
			for end < len(b) && b[end] != '"' {
				if b[end] == '\\' {
					end++
				}
				end++
			}
			before := strings.TrimRight(string(b[:i]), " \t\n")
			if !strings.HasSuffix(before, "server-name") && !strings.HasSuffix(before, "server-force-delete") {
				blank(i+1, min(end, len(b)))
			}
			i = end
		}
	}
	return string(b)
}

var (
	emacsServerNameWord = regexp.MustCompile(`server-name\b`)
	emacsServerNameSet  = regexp.MustCompile(`server-name\s+"([^"\\]*)"`)
	emacsServerStart    = regexp.MustCompile(`\bserver-(start|mode)\b`)
)

// emacsBatchLisp is the Lisp an Emacs call runs that the guard can read:
// its arguments, and each file it loads that is there to read, -l, --load
// or --script, whose contents count up to a megabyte.
func emacsBatchLisp(args []string, dir string) string {
	var b strings.Builder
	b.WriteString(strings.Join(args, " "))
	for i, a := range args {
		file := ""
		switch {
		case (a == "-l" || a == "--load" || a == "--script" || a == "-script") && i+1 < len(args):
			file = args[i+1]
		case strings.HasPrefix(a, "--load=") || strings.HasPrefix(a, "--script="):
			_, file, _ = strings.Cut(a, "=")
		}
		if file == "" {
			continue
		}
		if f, err := os.Open(expandPath(file, dir)); err == nil {
			data, _ := io.ReadAll(io.LimitReader(f, 1<<20))
			f.Close()
			b.WriteString("\n")
			b.Write(data)
		}
	}
	return b.String()
}

// versionedEmacs is an Emacs binary named with its version, as emacs-30.2
// or emacs30.
var versionedEmacs = regexp.MustCompile(`^[Ee]macs-?[0-9][0-9.]*$`)

// emacsclientOptions are emacsclient's long options, each with whether it
// takes a value, and emacsclientFlags and emacsclientValued its short
// ones, without and with a value: emacsclient 30's getopt_long table and
// optstring.
var (
	emacsclientOptions = map[string]bool{"no-wait": false, "quiet": false, "suppress-output": false, "eval": false,
		"help": false, "version": false, "tty": false, "nw": false, "no-window-system": false, "create-frame": false,
		"reuse-frame": false, "alternate-editor": true, "frame-parameters": true, "timeout": true, "socket-name": true,
		"server-file": true, "display": true, "parent-id": true, "tramp": true}
	emacsclientFlags, emacsclientValued = "nqueHVtc", "aFwsfdT"
)

// emacsclientSockets reads an emacsclient call's options as its
// getopt_long does, abbreviations, clustered short options and values in
// the next word included, and returns every socket it names, and whether
// it names a server file. ok is false where it cannot read an option as
// emacsclient would.
func emacsclientSockets(args []string) (sockets []string, serverFile, ok bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, bool) {
			if i+1 >= len(args) {
				return "", false
			}
			i++
			return args[i], true
		}
		switch {
		case a == "--":
			return sockets, serverFile, true
		case a == "-nw":
		case strings.HasPrefix(a, "--"):
			name, value, hasValue := strings.Cut(a[2:], "=")
			var matches []string
			for o := range emacsclientOptions {
				if o == name {
					matches = []string{o}
					break
				}
				if strings.HasPrefix(o, name) {
					matches = append(matches, o)
				}
			}
			if len(matches) != 1 {
				return nil, false, false
			}
			o := matches[0]
			if !emacsclientOptions[o] {
				continue
			}
			if !hasValue {
				if value, hasValue = next(); !hasValue {
					return nil, false, false
				}
			}
			switch o {
			case "socket-name":
				sockets = append(sockets, value)
			case "server-file":
				serverFile = true
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for j := 1; j < len(a); j++ {
				c := a[j]
				if strings.IndexByte(emacsclientFlags, c) >= 0 {
					continue
				}
				if strings.IndexByte(emacsclientValued, c) < 0 {
					return nil, false, false
				}
				value := a[j+1:]
				if value == "" {
					var has bool
					if value, has = next(); !has {
						return nil, false, false
					}
				}
				switch c {
				case 's':
					sockets = append(sockets, value)
				case 'f':
					serverFile = true
				}
				break
			}
		}
	}
	return sockets, serverFile, true
}

// testEmacsSocket reports whether an emacsclient call reaches only a test
// Emacs: every socket it names is absolute and, symlinks resolved and,
// on darwin, case folded, under $TMPDIR, /tmp/claude-UID or /tmp/et.*,
// where a test Emacs keeps its own, and not the operator's daemon's; and
// it names no server file, which reaches a daemon over TCP instead.
func testEmacsSocket(args []string) bool {
	sockets, serverFile, ok := emacsclientSockets(args)
	if !ok || serverFile || len(sockets) == 0 {
		return false
	}
	for _, s := range sockets {
		if !testSocket(s) {
			return false
		}
	}
	return true
}

// versionedEmacsclient is an emacsclient binary named with its version, as
// emacsclient-30.2.
var versionedEmacsclient = regexp.MustCompile(`^emacsclient-?[0-9][0-9.]*$`)

// emacsServerCall reports whether an emacsclient call stops Emacs, deletes
// or starts a server, or sends Lisp that names one, other than a test
// Emacs's own. Any such form sent anywhere but a test socket reaches the
// operator's daemon. One that names a server makes even a test Emacs reach
// theirs: server-eval-at by evaluating there, and the server variables by
// moving the socket the rest acts on.
//
// A lifecycle form sent to a test socket naming no other server acts on
// that Emacs's own server alone: `(server-mode -1)` there removed the
// test socket and left the operator's as it was.
func emacsServerCall(args []string) bool {
	text := strings.Join(args, " ")
	if !emacsServerForm.MatchString(text) {
		return false
	}
	return !testEmacsSocket(args) || lispNamesServer(text, false)
}

// lispNamesServer reports whether Lisp text names a server other than the
// running Emacs's own: server-eval-at, a server variable, or a
// server-force-delete whose name is not a test socket's absolute path,
// as a string. bare says whether a server-force-delete with no name
// counts, as in a batch Emacs, whose own server is the operator's
// default one.
func lispNamesServer(text string, bare bool) bool {
	if emacsServerNamed.MatchString(text) {
		return true
	}
	for _, m := range emacsForceDelete.FindAllStringSubmatch(text, -1) {
		if m[1] == "" {
			if bare {
				return true
			}
			continue
		}
		if name, err := strconv.Unquote(m[1]); err != nil || !testSocket(name) {
			return true
		}
	}
	return false
}

var (
	// emacsServerForm is Lisp that stops or restarts Emacs, or deletes,
	// starts, stops or points at a server.
	emacsServerForm = regexp.MustCompile(`(kill|restart)-emacs|server-(force-delete|start|stop|shutdown|kill|mode|process|eval-at|name|socket-dir|auth-dir|use-tcp|host|port)\b`)
	// emacsServerNamed is Lisp that names a server, whatever it does.
	emacsServerNamed = regexp.MustCompile(`server-(eval-at|name|socket-dir|auth-dir|use-tcp|host|port)\b`)
	// emacsForceDelete is a server-force-delete, with the name it is given,
	// if any.
	emacsForceDelete = regexp.MustCompile(`\(\s*server-force-delete(?:\s+([^\s)][^)]*?))?\s*\)`)
)

// testSocket reports whether socket is a test Emacs's, as testEmacsSocket
// says.
func testSocket(socket string) bool {
	if !filepath.IsAbs(socket) || socket != filepath.Clean(socket) {
		return false
	}
	fold := func(p string) string {
		if runtime.GOOS == "darwin" {
			return strings.ToLower(p)
		}
		return p
	}
	socket = fold(resolvedPath(socket))
	below := func(dir string) bool { return strings.HasPrefix(socket, fold(resolvedPath(dir))+"/") }
	uid := strconv.Itoa(os.Getuid())
	tmp := filepath.Clean(os.TempDir())
	for _, own := range []string{filepath.Join(tmp, "emacs"+uid), "/run/user/" + uid + "/emacs"} {
		if socket == fold(resolvedPath(own)) || below(own) {
			return false
		}
	}
	if below(tmp) || below("/tmp/claude-"+uid) {
		return true
	}
	rel, ok := strings.CutPrefix(socket, fold(resolvedPath("/tmp"))+"/et.")
	return ok && strings.Contains(rel, "/") && !strings.HasPrefix(rel, "/")
}

// resolvedPath is the clean absolute path with the symlinks of its deepest
// existing directory, or of itself, resolved.
func resolvedPath(path string) string {
	rest := ""
	for p := path; ; p = filepath.Dir(p) {
		if real, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(real, rest)
		}
		if p == filepath.Dir(p) {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
	}
}

// checkKill denies a kill of every process, a process group, or a process
// this session did not start, which it looks up with ps; and a kill of
// PIDs it cannot read, any but a literal number or a %job, or none at all
// as xargs runs it.
func checkKill(args []string) string {
	// The signal, if any, comes first; a negative number after it is a
	// process group.
	i := 0
	switch {
	case len(args) == 0:
		return killUnseen
	case args[0] == "-l" || args[0] == "-L":
		return ""
	case args[0] == "-s" || args[0] == "-n":
		i = 2
	case strings.HasPrefix(args[0], "-") && args[0] != "--":
		i = 1
	}
	if i < len(args) && args[i] == "--" {
		i++
	}
	pids := args[min(i, len(args)):]
	if len(pids) == 0 {
		return killUnseen
	}
	for _, p := range pids {
		if strings.HasPrefix(p, "%") {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return killUnseen
		}
		if n <= 0 {
			return killing
		}
		if !startedHere(n) {
			if protectedProcess(commandOf(n)) {
				return killing
			}
			return killOthers
		}
	}
	return ""
}

// protectedTree reports whether path is the home directory, the root, or
// in the Nix store or the operator's keys; a glob counts as its directory.
// The home directory and the root count only for a recursive operation.
func protectedTree(path string, recursive bool) bool {
	for strings.HasSuffix(path, "/*") || path != "/" && strings.HasSuffix(path, "*") {
		path = filepath.Dir(path)
	}
	inside := func(root string) bool {
		return path == root || strings.HasPrefix(path, root+"/")
	}
	switch {
	case inside("/nix"), inside(filepath.Join(home, ".ssh")), inside(filepath.Join(home, ".gnupg")):
		return true
	case path == "/" || path == home:
		return recursive
	}
	return false
}

// checkSecretPath denies a word that names a credential file: one under a
// credentialPaths entry, or a .env file at any depth. Each path is judged
// as written and as the file system walks it (physical), so a link to a
// credential is one and `link/..` climbs from where the link leads; and a
// credential is matched by file identity too (credentialUnder).
func checkSecretPath(word, dir string) string {
	return secretPath(word, dir, false)
}

// checkSecretWrite is checkSecretPath for a path a command or tool creates
// or writes: a redirection's target, a cp, mv or ln destination, a file
// tool's path. There a name outside ASCII in a credential's own directory
// is refused too, since the volume may fold it to the credential's
// (Reaches).
func checkSecretWrite(word, dir string) string {
	return secretPath(word, dir, true)
}

// secretPath is checkSecretPath, or with creating checkSecretWrite.
func secretPath(word, dir string, creating bool) string {
	var candidates []string
	for _, w := range []string{word, afterEquals(word)} {
		// @FILE is a file's contents to curl (-d @FILE) and gh (-F k=@FILE).
		for _, v := range []string{w, strings.TrimPrefix(w, "@")} {
			if v != "" {
				candidates = append(candidates, expandPath(v, dir), physical(rawPath(v, dir)))
			}
		}
	}
	for _, path := range candidates {
		if b := fold(filepath.Base(path)); b == ".env" || strings.HasPrefix(b, ".env.") {
			return secretRead
		}
	}
	secrets := credentialPaths()
	for _, path := range candidates {
		for _, p := range secrets {
			if credentialUnder(path, p, creating) {
				return secretRead
			}
		}
	}
	return ""
}

// afterEquals is what follows a word's first =, as in --body-file=PATH, or
// "" when it has none.
func afterEquals(word string) string {
	_, value, _ := strings.Cut(word, "=")
	return value
}

// fold is s as the file system compares names: without regard to case on
// macOS, whose volumes are case-insensitive unless made otherwise.
func fold(s string) string {
	if runtime.GOOS == "darwin" {
		return strings.ToLower(s)
	}
	return s
}

// credentialUnder reports whether path, absolute and clean, is under the
// credential prefix p: by spelling, and by what the file system would
// open (Reaches). A p ending in / is a directory, which it matches as
// well; a p holding a glob matches path or any of its parents, by
// spelling alone. A file ending in .pub is a public key, never under a
// prefix of a name, as `~/.ssh/id_` is. creating is reaches's: path is to
// be created or written.
func credentialUnder(path, p string, creating bool) bool {
	folded, foldedP := fold(path), fold(p)
	if strings.ContainsAny(p, "*?[") {
		foldedP = strings.TrimSuffix(foldedP, "/")
		for q := folded; ; q = filepath.Dir(q) {
			if ok, _ := filepath.Match(foldedP, q); ok {
				return true
			}
			if q == filepath.Dir(q) {
				return false
			}
		}
	}
	if !strings.HasSuffix(folded, ".pub") && strings.HasPrefix(folded, foldedP) || folded+"/" == foldedP {
		return true
	}
	if !filepath.IsAbs(path) {
		return false
	}
	switch _, err := os.Stat(p); {
	case strings.HasSuffix(p, "/"):
		return reaches(path, p, true, creating)
	case err == nil:
		return reaches(path, p, true, creating)
	default:
		return reachesPrefix(path, filepath.Dir(p), filepath.Base(p), creating)
	}
}

const appendOnly = "This is a plan's event log, which is append-only and written by agent-plan alone. Record the event with `agent-plan log PLAN --item ID --kind KIND`, and change the plan itself with agent-plan's other commands, as `agent-plan --help` lists them."

const planWrite = "This is a plan file, which agents change through agent-plan alone, so that every change keeps the structure `agent-plan status` checks. " +
	"Use the command for the change: `agent-plan step` or `part` to add an entry, `edit` for its title or text, `after` for its AFTER, " +
	"`section` for Goal or Verification, `set` for a `* Plan` property, `ask` and `decide` for questions, `followup`, `reference` or `postpone`, " +
	"and `agent-plan new` (or `new --parent PLAN --part P1`) for a new plan file; " +
	"a change none of them makes goes in your report for the operator."

// planLog reports whether path is a plan's log, or the lock file that
// records its state, in the plans directory agent-plan's config names. A
// relative path counts, since where it is cannot be told.
func planLog(path string) bool {
	return plans.IsLog(filepath.Base(path)) && inPlans(path)
}

// planFile reports whether path is a plan's file or a part's in the plans
// directory, as planLog does for a log.
func planFile(path string) bool {
	return plans.IsPlan(filepath.Base(path)) && inPlans(path)
}

// inPlans reports whether path is in the plans directory agent-plan's
// config names, or is relative.
func inPlans(path string) bool {
	if !filepath.IsAbs(path) {
		return true
	}
	parent := filepath.Dir(path)
	real, err := filepath.EvalSymlinks(parent)
	for _, d := range plansDirs() {
		if parent == d || err == nil && real == d {
			return true
		}
	}
	return false
}

// plansDirs are the plans directory agent-plan's config names, as named
// and as its symlinks resolve.
func plansDirs() []string {
	dir := filepath.Join(home, "git", "notes", "agents")
	if c, err := plans.Load(); err == nil {
		dir = c.Dir
	}
	out := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != out[0] {
		out = append(out, real)
	}
	return out
}

// nearPlans reports, for path, absolute, whether it is the plans directory
// or inside it, and whether it holds it, as named or as its symlinks
// resolve.
func nearPlans(path string) (inside, holds bool) {
	names := []string{filepath.Clean(path)}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		names = append(names, real)
	} else if parent, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		names = append(names, filepath.Join(parent, filepath.Base(path)))
	}
	for _, d := range plansDirs() {
		for _, p := range names {
			inside = inside || p == d || strings.HasPrefix(p, d+"/")
			holds = holds || p == "/" || strings.HasPrefix(d, p+"/")
		}
	}
	return inside, holds
}

// checkCopyIntoPlans denies a copy, move, install or link into the plans
// directory of a file under a plan's or a log's name, of a directory's
// contents, or of a source word the shell expands (a glob, braces, a
// variable or a command's output), any of which may hold either; and a
// tar or unzip extracting there, or a ditto copying there. changedFiles
// names only the directory.
func checkCopyIntoPlans(name string, args []string, dir string) string {
	var unpackInto []string
	switch name {
	case "tar", "gtar", "bsdtar":
		if tarExtracts(args) {
			if unpackInto = optionValues(args, "-C", "--directory"); len(unpackInto) == 0 {
				unpackInto = []string{"."}
			}
		}
	case "unzip":
		if !slices.ContainsFunc(args, func(a string) bool { return a == "-l" || a == "-t" || a == "-p" || a == "-v" || a == "-Z" }) {
			if unpackInto = optionValues(args, "-d", ""); len(unpackInto) == 0 {
				unpackInto = []string{"."}
			}
		}
	case "ditto":
		// Its last operand is the destination, whether it copies or
		// extracts (-x).
		var positional []string
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				positional = append(positional, a)
			}
		}
		if len(positional) > 1 {
			unpackInto = positional[len(positional)-1:]
		}
	}
	for _, d := range unpackInto {
		if inside, _ := nearPlans(expandPath(d, dir)); inside {
			return planWrite
		}
	}
	if !set("cp", "mv", "install", "ln", "rsync")[name] {
		return ""
	}
	if !set("cp", "mv", "install", "ln", "rsync")[name] {
		return ""
	}
	var positional []string
	recursive := name == "rsync"
	for _, a := range args {
		switch {
		case a == "-r" || a == "-R" || a == "-a" || a == "--recursive" || a == "--archive" ||
			len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsAny(a[1:], "rRa"):
			recursive = true
		case !strings.HasPrefix(a, "-"):
			positional = append(positional, a)
		}
	}
	dests, sources := targetDirectories(args), positional
	if len(dests) == 0 && len(positional) > 1 {
		dests, sources = positional[len(positional)-1:], positional[:len(positional)-1]
	}
	for _, d := range dests {
		if inside, _ := nearPlans(expandPath(d, dir)); !inside {
			continue
		}
		for _, s := range sources {
			base := filepath.Base(s)
			if plans.IsLog(base) {
				return appendOnly
			}
			if plans.IsPlan(base) || recursive || strings.HasSuffix(s, "/") || base == "." || strings.ContainsAny(s, "*?[{$`") {
				return planWrite
			}
			if info, err := os.Stat(expandPath(s, dir)); err == nil && info.IsDir() {
				return planWrite
			}
		}
	}
	return ""
}

// checkPlanCheckout denies a git checkout or restore of a path that is a
// plan's file or log, a glob in the plans directory, or a directory that is
// or holds it, all of which git writes from history.
func checkPlanCheckout(sub string, rest []string, dir string) string {
	if sub != "checkout" && sub != "restore" || dir == "" {
		return ""
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case set("-s", "--source", "-b", "-B", "--orphan", "--conflict", "--pathspec-from-file")[a]:
			i++
			continue
		case a == "--" || strings.HasPrefix(a, "-"):
			continue
		}
		path := expandPath(a, dir)
		inside, holds := nearPlans(path)
		switch {
		case planLog(path):
			return appendOnly
		case planFile(path), holds, isPlansDir(path), inside && strings.ContainsAny(filepath.Base(path), "*?["):
			return planWrite
		}
	}
	return ""
}

// isPlansDir reports whether path is the plans directory itself.
func isPlansDir(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	for _, d := range plansDirs() {
		if filepath.Clean(path) == d || err == nil && real == d {
			return true
		}
	}
	return false
}

// checkPlanLogWrite denies a command that writes, moves or removes a plan's
// log or file. It reads a plan file's writes as changedFiles,
// checkCopyIntoPlans and, for git, checkPlanCheckout name them, and so
// misses these: emacsclient evaluating Lisp that saves a buffer, an editor
// run as a filter (ex, vim -c), patch given a plan file inside its diff,
// and a path the shell builds from variables.
func checkPlanLogWrite(name string, args, redirected []string, dir string) string {
	if reason := checkCopyIntoPlans(name, args, dir); reason != "" {
		return reason
	}
	for _, t := range changedFiles(name, args, redirected) {
		if planLog(expandPath(t, dir)) {
			return appendOnly
		}
		if planFile(expandPath(t, dir)) {
			return planWrite
		}
	}
	return ""
}

// changedFiles are the files a command writes, moves or removes, as it
// names them: those editTargets names, the operands of the programs that
// create, copy, move, remove or change the mode of files, and the
// directory a program writes into, "." for the one it runs in, where it
// unpacks, patches, formats, generates or builds there. checkPlanLogWrite
// and checkOwner read a command through it.
func changedFiles(name string, args, redirected []string) []string {
	targets := editTargets(name, args, redirected)
	var positional []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
		}
	}
	// The directory given by one of these options, or "." without one.
	dirOr := func(short, long string) []string {
		if dirs := optionValues(args, short, long); len(dirs) > 0 {
			return dirs
		}
		return []string{"."}
	}
	switch name {
	case "mv", "rm", "unlink", "trash", "truncate", "shred", "touch", "mkdir", "rmdir":
		targets = append(targets, positional...)
	case "chmod", "chown", "chgrp":
		if len(positional) > 1 {
			targets = append(targets, positional[1:]...)
		}
	case "cp", "install", "ln", "rsync":
		var dirs []string
		if name != "rsync" { // rsync's -t keeps times
			dirs = targetDirectories(args)
		}
		switch {
		case len(dirs) > 0:
			targets = append(targets, dirs...)
		case name == "install" && contains(args, "-d"):
			targets = append(targets, positional...)
		case name == "ln" && len(positional) == 1:
			targets = append(targets, ".") // the link, named as its target, where ln runs
		case len(positional) > 0:
			targets = append(targets, positional[len(positional)-1])
		}
	case "dd":
		for _, a := range args {
			if of, ok := strings.CutPrefix(a, "of="); ok {
				targets = append(targets, of)
			}
		}
	case "tar", "gtar", "bsdtar":
		if tarExtracts(args) {
			targets = append(targets, dirOr("-C", "--directory")...)
		}
	case "unzip":
		if !slices.ContainsFunc(args, func(a string) bool { return a == "-l" || a == "-t" || a == "-p" || a == "-v" || a == "-Z" }) {
			targets = append(targets, dirOr("-d", "")...)
		}
	case "patch":
		if !contains(args, "--dry-run") {
			targets = append(targets, optionValues(args, "-o", "--output")...)
			targets = append(targets, dirOr("-d", "--directory")...)
		}
	case "gofmt", "goimports":
		if contains(args, "-w") {
			targets = append(targets, positional...)
		}
	case "go":
		targets = append(targets, goWrites(args)...)
	case "nix", "nix-build":
		targets = append(targets, nixWrites(name, args)...)
	case "make", "gmake":
		if !slices.ContainsFunc(args, func(a string) bool {
			return set("-n", "--dry-run", "--just-print", "--recon", "-q", "--question")[a]
		}) {
			targets = append(targets, dirOr("-C", "--directory")...)
		}
	case "treefmt", "alejandra", "nixfmt", "nixpkgs-fmt", "black", "ruff", "rustfmt", "cargo", "prettier",
		"shfmt", "sops", "gofumpt", "nix-fmt":
		targets = append(targets, formatWrites(name, args, positional)...)
	}
	return targets
}

// targetDirectories are the directories cp, install or ln are given to
// copy or link into: `-t DIR`, `-t` ending a group of short flags
// (`-rt DIR`) or starting its value (`-tDIR`, `-rtDIR`), and
// `--target-directory`.
func targetDirectories(args []string) []string {
	var dirs []string
	for i, a := range args {
		switch {
		case a == "--target-directory" && i+1 < len(args):
			dirs = append(dirs, args[i+1])
		case strings.HasPrefix(a, "--target-directory="):
			dirs = append(dirs, strings.TrimPrefix(a, "--target-directory="))
		case len(a) > 1 && a[0] == '-' && a[1] != '-':
			if _, value, ok := strings.Cut(a[1:], "t"); ok {
				switch {
				case value != "":
					dirs = append(dirs, value)
				case i+1 < len(args):
					dirs = append(dirs, args[i+1])
				}
			}
		}
	}
	return dirs
}

// formatWrites are the files a formatter rewrites: those it is given, or
// "." for the tree it runs in; none when it only checks or prints, or, for
// the formatters that write only when told to, unless told to.
func formatWrites(name string, args, positional []string) []string {
	has := func(flags ...string) bool {
		return slices.ContainsFunc(args, func(a string) bool { return contains(flags, a) })
	}
	switch {
	case has("--check", "--diff", "-l", "--list-different", "--help", "--version"),
		name == "alejandra" && has("-c"),
		name == "prettier" && !has("-w", "--write"),
		name == "shfmt" && !has("-w", "--write"),
		name == "gofumpt" && !has("-w"),
		name == "sops" && !has("-i", "--in-place"),
		name == "cargo" && (len(positional) == 0 || positional[0] != "fmt"),
		name == "ruff" && (len(positional) == 0 || positional[0] != "format") && !has("--fix"):
		return nil
	}
	if (name == "cargo" || name == "ruff") && len(positional) > 0 {
		positional = positional[1:] // the subcommand
	}
	if len(positional) == 0 {
		return []string{"."}
	}
	return positional
}

// optionValues are the values an option takes in args: `-t DIR`, `-tDIR`,
// `--long DIR` and `--long=DIR`; long may be "".
func optionValues(args []string, short, long string) []string {
	var values []string
	for i, a := range args {
		switch {
		case (a == short || long != "" && a == long) && i+1 < len(args):
			values = append(values, args[i+1])
		case long != "" && strings.HasPrefix(a, long+"="):
			values = append(values, strings.TrimPrefix(a, long+"="))
		case len(short) == 2 && strings.HasPrefix(a, short) && len(a) > 2 && !strings.HasPrefix(a, "--"):
			values = append(values, a[2:])
		}
	}
	return values
}

// tarExtracts reports whether tar's arguments extract: --extract or --get,
// x in the first word's bundled letters (`tar xzf`), or x in a group of
// short flags.
func tarExtracts(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--extract" || a == "--get":
			return true
		case strings.HasPrefix(a, "--"):
		case strings.HasPrefix(a, "-") || i == 0:
			if strings.ContainsRune(strings.TrimPrefix(a, "-"), 'x') {
				return true
			}
		}
	}
	return false
}

// goWrites are the files and directories a go command writes: its -o
// output, its test profiles, and "." for the commands that rewrite source
// or go.mod in the package it runs in.
func goWrites(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	var out []string
	output := false
	for i, a := range args[1:] {
		name, value, attached := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || !set("o", "coverprofile", "cpuprofile", "memprofile", "blockprofile",
			"mutexprofile", "trace", "outputdir")[name] {
			continue
		}
		if !attached && i+2 < len(args) {
			value = args[i+2]
		}
		if value != "" {
			out = append(out, value)
			output = output || name == "o"
		}
	}
	switch args[0] {
	case "generate", "fmt", "fix", "get", "work":
		out = append(out, ".")
	case "mod":
		if len(args) > 1 && (args[1] == "tidy" || args[1] == "edit" || args[1] == "vendor" || args[1] == "init") {
			out = append(out, ".")
		}
	case "build":
		if !output {
			out = append(out, ".")
		}
	case "test":
		if contains(args, "-c") && !output {
			out = append(out, ".")
		}
	case "install":
		out = append(out, goDir("GOBIN", "bin"))
	case "clean":
		if slices.ContainsFunc(args, func(a string) bool { return strings.HasSuffix(a, "cache") }) {
			out = append(out, goDir("GOCACHE", "cache"), goDir("GOMODCACHE", "pkg/mod"))
		} else {
			out = append(out, ".")
		}
	}
	return out
}

// goDir is the directory the environment variable names, or the one under
// GOPATH (or ~/go) that the go command uses in its place: where go install
// and go clean write.
func goDir(variable, under string) string {
	if dir := os.Getenv(variable); filepath.IsAbs(dir) {
		return dir
	}
	gopath := os.Getenv("GOPATH")
	if !filepath.IsAbs(gopath) {
		gopath = filepath.Join(home, "go")
	}
	return filepath.Join(gopath, under)
}

// nixWrites are the links a nix build leaves: ./result, or its --out-link,
// unless --no-link or --no-out-link; and "." for nix flake update and lock,
// which write flake.lock.
func nixWrites(name string, args []string) []string {
	build := name == "nix-build" || len(args) > 0 && args[0] == "build"
	if len(args) > 1 && args[0] == "flake" && (args[1] == "update" || args[1] == "lock") ||
		len(args) > 0 && args[0] == "fmt" {
		return []string{"."}
	}
	if !build || contains(args, "--no-link") || contains(args, "--no-out-link") {
		return nil
	}
	if links := optionValues(args, "-o", "--out-link"); len(links) > 0 {
		return links
	}
	return []string{"result"}
}

const shellEdit = "`%s` is a tracked file, and editing it from the shell bypasses the file-editing tools AGENTS.md requires. " +
	"Use the file-editing tools (`Edit`, `Write`, `apply_patch`), or write scratch output under `$TMPDIR` or /tmp. " +
	"For a mechanical sweep over Go code, use `gopls rename`, `gofmt -r 'PATTERN -> REPLACEMENT' -w` or `go fix`, " +
	"with the exact command in the commit body."

const shellEditInPlace = "`%s` exists, and rewriting an existing file from the shell, in place or with `>`, `>>` or tee, " +
	"bypasses the file-editing tools AGENTS.md requires, scratch and untracked files included. " +
	"Use the file-editing tools (`Edit`, `Write`, `apply_patch`), " +
	"or write the command's output to a new file. For a mechanical sweep over Go code, use `gopls rename`, " +
	"`gofmt -r 'PATTERN -> REPLACEMENT' -w` or `go fix`, with the exact command in the commit body."

// gitConfigShell is why the shell may not write git config: only `git
// config`, which the guard reads, changes it.
const gitConfigShell = "Git config changes go through `git config`, whose keys and values the guard reads; a shell write to a git config file hides them. Run `git config KEY VALUE`, or put the change in your report for the operator."

// gitConfigShellWrite reports whether a command, run in dir, writes a git
// config file, as gitConfigFile names one: by a redirection, tee, cp, mv,
// ln, install, rsync, truncate or dd onto it, or sed, perl or awk in
// place. A source's name inside a destination counts only where the
// destination is a directory, so copying a git config out passes.
func gitConfigShellWrite(name string, args, redirected []string, dir string) bool {
	targets := editTargets(name, args, redirected)
	if set("tee", "cp", "mv", "ln", "install", "rsync", "truncate", "dd")[name] || inPlace(name, args) {
		written := writeTargets(name, args)
		for _, t := range written {
			if len(written) > 0 && t != written[0] && filepath.Dir(t) == written[0] {
				if info, err := os.Stat(expandPath(written[0], dir)); err != nil || !info.IsDir() {
					continue
				}
			}
			targets = append(targets, t)
		}
	}
	for _, t := range targets {
		if !strings.HasPrefix(t, "&") && !strings.HasPrefix(t, "-") && gitConfigFile(expandPath(t, dir)) {
			return true
		}
	}
	return false
}

// checkShellEdit denies a command, run in dir, that writes an existing file
// (editTargets) wherever it is. Output written to a new file is capturing,
// not editing.
func checkShellEdit(name string, args, redirected []string, dir string) string {
	for _, t := range editTargets(name, args, redirected) {
		if path := expandPath(t, dir); trackedFile(path) {
			return fmt.Sprintf(shellEdit, t)
		} else if existingFile(path) {
			return fmt.Sprintf(shellEditInPlace, t)
		}
	}
	return ""
}

// existingFile reports whether path is absolute and names a regular file.
func existingFile(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// editTargets are the files a command edits, as it names them: its output
// redirections, the files of tee, ed, and sed, perl or awk in place, and
// those an interpreter's one-liner writes.
func editTargets(name string, args, redirected []string) []string {
	var targets []string
	for _, t := range redirected {
		if !strings.HasPrefix(t, "&") {
			targets = append(targets, t)
		}
	}
	if name == "tee" || name == "ed" || inPlace(name, args) {
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				targets = append(targets, a)
			}
		}
	}
	return append(targets, oneLinerWrites(name, args)...)
}

var (
	interpreter = regexp.MustCompile(`^(python[0-9.]*|node|ruby)$`)
	// openForWriting is Python's or Ruby's open of a literal path with a
	// literal mode, which writes when the mode has w, a, x or +.
	openForWriting = regexp.MustCompile(`\bopen\(\s*['"]([^'"]+)['"]\s*,\s*(?:mode\s*=\s*)?['"]([^'"]*)['"]`)
	// writeCall is a call that writes the literal path it is given.
	writeCall = regexp.MustCompile(`\bPath\(\s*['"]([^'"]+)['"]\s*\)\s*\.\s*write_(?:text|bytes)\(|` +
		`\b(?:writeFileSync|writeFile|appendFileSync|appendFile|createWriteStream)\(\s*['"` + "`" + `]([^'"` + "`" + `]+)['"` + "`" + `]|` +
		`\bFile\.(?:write|binwrite)\(\s*['"]([^'"]+)['"]`)
)

// oneLinerWrites are the files a Python, Node or Ruby one-liner (-c, -e,
// --eval) writes, as it names them in string literals: a narrow reading of
// the usual calls, not of every way code can write.
func oneLinerWrites(name string, args []string) []string {
	if !interpreter.MatchString(name) {
		return nil
	}
	flags := set("-e", "--eval", "-p", "--print")
	if strings.HasPrefix(name, "python") {
		flags = set("-c")
	}
	var targets []string
	for i, a := range args {
		code, ok := "", false
		if flag, value, attached := strings.Cut(a, "="); attached && flags[flag] && strings.HasPrefix(flag, "--") {
			code, ok = value, true
		} else if flags[a] && i+1 < len(args) {
			code, ok = args[i+1], true
		}
		if !ok {
			continue
		}
		for _, m := range openForWriting.FindAllStringSubmatch(code, -1) {
			if strings.ContainsAny(m[2], "wax+") {
				targets = append(targets, m[1])
			}
		}
		for _, m := range writeCall.FindAllStringSubmatch(code, -1) {
			targets = append(targets, m[1]+m[2]+m[3])
		}
	}
	return targets
}

// trackedFile reports whether path is a file git tracks, outside the
// scratch roots, that the repository's ignore rules do not name.
func trackedFile(path string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	for _, root := range scratchRoots() {
		for _, r := range []string{root, realPath(root)} {
			if r != "" && (real == r || strings.HasPrefix(real, strings.TrimSuffix(r, "/")+"/")) {
				return false
			}
		}
	}
	g, name := gitState{dir: filepath.Dir(real)}, filepath.Base(real)
	if _, ok := g.run("ls-files", "--error-unmatch", "--", name); !ok {
		return false
	}
	_, ignored := g.run("check-ignore", "-q", "--no-index", "--", name)
	return !ignored
}

// realPath is path with its symlinks resolved, or "" when it cannot be.
func realPath(path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return real
}

// trashOnPath reports whether trash is on the hook's PATH.
var trashOnPath = func() bool {
	_, err := exec.LookPath("trash")
	return err == nil
}

const useTrash = "`trash` is on PATH here, and moves files to the Trash, where they can be got back. " +
	"Use `trash PATH...` in place of rm, unlink or find -delete (`find ... -print0 | xargs -0 trash`)."

// scratchRoots are the directories a shell command may write files in,
// tracked or not: the session's temporary directories.
var scratchRoots = func() []string {
	roots := []string{"/tmp", "/private/tmp"}
	if dir := os.Getenv("TMPDIR"); dir != "" {
		roots = append(roots, dir)
	}
	return roots
}

var redirect = regexp.MustCompile(`^[0-9&]?>>?\|?`)

// redirections are the files a command's output redirections write.
func redirections(words []string) []string {
	var out []string
	for i, w := range words {
		m := redirect.FindString(w)
		if m == "" {
			continue
		}
		if target := w[len(m):]; target != "" {
			out = append(out, target)
		} else if i+1 < len(words) {
			out = append(out, words[i+1])
		}
	}
	return out
}

// checkFileTool checks a call to a file-editing tool: Claude Code's Edit,
// Write, MultiEdit and NotebookEdit, and Codex's apply_patch.
func checkFileTool(tool string, toolInput json.RawMessage, dir string) string {
	changes, err := fileChanges(tool, toolInput)
	if err != nil {
		return unreadable
	}
	for _, c := range changes {
		path := expandPath(c.path, dir)
		if reason := checkSecretPath(path, dir); reason != "" {
			return reason
		}
		if reason := checkSecretWrite(path, dir); reason != "" {
			return reason
		}
		if planLog(path) {
			return appendOnly
		}
		if planFile(path) {
			return planWrite
		}
		if reason := gitConfigAlias(path, c.text); reason != "" {
			return reason
		}
		switch {
		case filepath.Base(path) == "config" && filepath.Base(filepath.Dir(path)) == ".git":
			return sharedConfig
		case strings.HasSuffix(path, ".codex/hooks.json") && guardFile(path), agentToolsConfig(path):
			return guardOff
		case guardFile(path):
			existing, _ := os.ReadFile(path)
			for _, text := range []string{c.text, string(existing)} {
				if strings.Contains(text, "hooks") || strings.Contains(text, "disableAllHooks") {
					return guardOff
				}
			}
		}
	}
	return ""
}

// change is a file a file-editing tool writes, as the call names it, and
// the text the call puts in or takes out.
type change struct{ path, text string }

// fileChanges are the files a call to a file-editing tool writes, or an
// error where its input does not decode.
func fileChanges(tool string, toolInput json.RawMessage) ([]change, error) {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Content      string `json:"content"`
		OldString    string `json:"old_string"`
		NewString    string `json:"new_string"`
		NewSource    string `json:"new_source"`
		Command      string `json:"command"`
		Edits        []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	if err := json.Unmarshal(toolInput, &in); err != nil {
		return nil, err
	}
	var changes []change
	if tool == "apply_patch" {
		var path string
		var text strings.Builder
		flush := func() {
			if path != "" {
				changes = append(changes, change{path, text.String()})
			}
			text.Reset()
		}
		for _, line := range strings.Split(in.Command, "\n") {
			for _, header := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: ", "*** Move to: "} {
				if p, ok := strings.CutPrefix(line, header); ok {
					flush()
					path = strings.TrimSpace(p)
					line = ""
				}
			}
			text.WriteString(line + "\n")
		}
		flush()
	} else {
		// Each part on lines of its own, so a rule reading lines reads
		// each as it is.
		text := strings.Join([]string{in.Content, in.OldString, in.NewString, in.NewSource}, "\n")
		for _, e := range in.Edits {
			text += "\n" + e.OldString + "\n" + e.NewString
		}
		changes = append(changes, change{in.FilePath + in.NotebookPath, text})
	}
	return changes, nil
}

// gitState is where a git command acts and what it finds there: its
// directory, and for signs the -c and --config-env options and the
// assignments it runs with.
type gitState struct {
	dir         string
	config, env []string
}

// signs reports whether a commit written by the command would be signed, by
// the configuration it would read; false where the guard cannot tell.
func (g gitState) signs() bool {
	out, ok := g.query("config", "--bool", "commit.gpgsign")
	return ok && out == "true"
}

// query runs git in g.dir with the command's configuration and
// assignments, as the command itself would read them.
func (g gitState) query(args ...string) (string, bool) {
	if g.dir == "" {
		return "", false
	}
	cmd := exec.Command("git", append(append(append([]string{}, g.config...), "-C", g.dir), args...)...)
	cmd.Env = append(gitenv.Without(os.Environ()), g.env...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err == nil
}

// keyTouchText is why a command that waits for a touch of the hardware key
// is denied, with %s how the agent's interface takes a command the operator
// runs, as AGENTS.md's "Commands the operator runs" says.
const keyTouchText = "This waits for a touch of the operator's hardware key, which nobody is here to give. " +
	"Hand the operator the command as %s, and meanwhile work from local refs, such as `git log origin/main`, with commits left unsigned; " +
	"`gh pr view` and `gh api` read GitHub over HTTPS."

// keyTouch is keyTouchText for Claude Code and Codex, whose prompt runs a
// line beginning with ! in the session.
var keyTouch = fmt.Sprintf(keyTouchText, "one line beginning with `!`, so its output lands here")

// touchHosts are the hosts an SSH login to waits for a key touch: the git
// forges. A login elsewhere, such as the devbox's, rides a master
// connection already open.
var touchHosts = set("github.com", "ssh.github.com", "git.sr.ht")

// remoteValueFlags are the options of each git command that contacts a
// remote whose value is the next word, which is therefore not the
// repository.
var remoteValueFlags = map[string]map[string]bool{
	"fetch": set("--depth", "--deepen", "--shallow-since", "--shallow-exclude", "-j", "--jobs", "--upload-pack",
		"-o", "--server-option", "--negotiation-tip", "--refmap", "--recurse-submodules-default", "--filter"),
	"pull": set("--depth", "--deepen", "--shallow-since", "--shallow-exclude", "-j", "--jobs", "--upload-pack",
		"-o", "--server-option", "--negotiation-tip", "-s", "--strategy", "-X", "--strategy-option", "--cleanup"),
	"ls-remote": set("--upload-pack", "--exec", "--sort", "-o", "--server-option"),
	"clone": set("-b", "--branch", "-o", "--origin", "--depth", "--reference", "--reference-if-able", "-c", "--config",
		"-u", "--upload-pack", "--template", "--separate-git-dir", "-j", "--jobs", "--filter", "--shallow-since",
		"--shallow-exclude", "--bundle-uri", "--ref-format", "--server-option", "--revision"),
	"remote":    set(),
	"submodule": set("-b", "--branch", "--name", "--reference", "--depth", "-j", "--jobs", "--filter"),
}

// positionals are args without their options and the values remoteValueFlags
// gives sub.
func positionals(sub string, args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--":
			return append(out, args[i+1:]...)
		case !strings.HasPrefix(a, "-") || a == "-":
			out = append(out, a)
		case remoteValueFlags[sub][a]:
			i++
		}
	}
	return out
}

// sshURL reports whether url is one git reaches over SSH: an ssh:// URL,
// or the scp-like host:path, whose colon comes before any slash.
func sshURL(url string) bool {
	lower := strings.ToLower(url)
	for _, scheme := range []string{"ssh://", "git+ssh://", "ssh+git://"} {
		if strings.HasPrefix(lower, scheme) {
			return true
		}
	}
	if strings.Contains(url, "://") {
		return false
	}
	colon := strings.Index(url, ":")
	return colon > 0 && !strings.Contains(url[:colon], "/")
}

// remoteURL is the URL git fetches repo from, a remote's name or a URL, as
// `git ls-remote --get-url` resolves it with url.<base>.insteadOf; repo
// itself where git cannot say. "" is the default remote.
func (g gitState) remoteURL(repo string) string {
	args := []string{"ls-remote", "--get-url"}
	if repo != "" {
		args = append(args, repo)
	}
	if out, ok := g.query(args...); ok && out != "" {
		return out
	}
	return repo
}

// remoteNames are the remotes name stands for: the members of the group
// remotes.<name>, and name itself.
func (g gitState) remoteNames(name string) []string {
	members, _ := g.query("config", "--get-all", "remotes."+name)
	return append(strings.Fields(members), name)
}

// allRemotes are the repository's remotes.
func (g gitState) allRemotes() []string {
	out, _ := g.query("remote")
	return strings.Fields(out)
}

// submoduleURLs are the URLs of the repository's submodules, in its config
// and in .gitmodules; one relative to the superproject is its default
// remote's.
func (g gitState) submoduleURLs() []string {
	var urls []string
	lists := []string{}
	if out, ok := g.query("config", "--get-regexp", `^submodule\..*\.url$`); ok {
		lists = append(lists, out)
	}
	if top, ok := g.query("rev-parse", "--show-toplevel"); ok {
		if out, ok := g.query("config", "-f", filepath.Join(top, ".gitmodules"), "--get-regexp", `^submodule\..*\.url$`); ok {
			lists = append(lists, out)
		}
	}
	for _, list := range lists {
		for _, line := range strings.Split(list, "\n") {
			if _, url, ok := strings.Cut(line, " "); ok {
				if strings.HasPrefix(url, "./") || strings.HasPrefix(url, "../") {
					url = g.remoteURL("")
				}
				urls = append(urls, url)
			}
		}
	}
	return urls
}

// overSSH reports whether `git sub rest...` contacts a remote, and would
// reach one over SSH: fetch, pull, ls-remote, clone, archive --remote, the
// remote verbs that read the remote, and submodule update and add.
func overSSH(sub string, rest []string, g gitState) bool {
	has := func(flags ...string) bool {
		for _, w := range rest {
			if set(flags...)[strings.SplitN(w, "=", 2)[0]] {
				return true
			}
		}
		return false
	}
	if has("-h", "--help") {
		return false
	}
	p := positionals(sub, rest)
	var repos []string // remotes' names, groups or URLs; "" the default remote
	switch sub {
	case "fetch", "pull":
		switch {
		case has("--all"):
			repos = g.allRemotes()
		case has("--multiple"):
			repos = p
		case len(p) == 0:
			repos = []string{""}
		default:
			repos = p[:1]
		}
	case "ls-remote":
		if has("--get-url") {
			return false
		}
		repos = append(p, "")[:1]
	case "clone":
		repos = p[:min(1, len(p))]
	case "archive":
		for i, w := range rest {
			if r, ok := strings.CutPrefix(w, "--remote="); ok {
				repos = append(repos, r)
			} else if w == "--remote" && i+1 < len(rest) {
				repos = append(repos, rest[i+1])
			}
		}
	case "remote":
		if len(p) == 0 {
			return false
		}
		switch names := p[1:]; p[0] {
		case "update":
			repos = names
			if len(repos) == 0 {
				repos = g.allRemotes()
			}
		case "show":
			if !has("-n") {
				repos = names
			}
		case "prune":
			repos = names
		case "set-head":
			if has("-a", "--auto") {
				repos = names[:min(1, len(names))]
			}
		}
	case "submodule":
		switch {
		case len(p) > 1 && p[0] == "add":
			repos = p[1:2]
		case len(p) > 0 && p[0] == "update":
			repos = g.submoduleURLs()
		}
	}
	for _, repo := range repos {
		names := []string{repo}
		if repo != "" && !sshURL(repo) {
			names = g.remoteNames(repo)
		}
		for _, name := range names {
			if sshURL(g.remoteURL(name)) {
				return true
			}
		}
	}
	return false
}

// sshValueFlags are the options of ssh, scp and sftp whose value is the
// next word, or the rest of the word.
var sshValueFlags = map[string]string{"ssh": "BbcDEeFIiJLlmOopQRSWw", "scp": "cDFiJloPSX", "sftp": "BbcDFiJloPRSsX"}

// sshTouch reports whether ssh, scp or sftp with these arguments logs in to
// one of touchHosts, by the name given or the HostName ssh's config gives
// it.
func sshTouch(name string, args []string, dir string) bool {
	values := sshValueFlags[name]
	var config []string // -F, for ssh -G
	var destinations []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			destinations = append(destinations, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			destinations = append(destinations, a)
			continue
		}
		for j := 1; j < len(a); j++ {
			flag := a[j]
			if name == "ssh" && (flag == 'G' || flag == 'V' || flag == 'Q') {
				return false // prints and exits without connecting
			}
			if !strings.ContainsRune(values, rune(flag)) {
				continue
			}
			value := a[j+1:]
			if value == "" && i+1 < len(args) {
				i++
				value = args[i]
			}
			if flag == 'F' {
				config = []string{"-F", expandPath(value, dir)}
			}
			break
		}
	}
	if name != "scp" && len(destinations) > 1 {
		destinations = destinations[:1] // the rest is ssh's command
	}
	for _, d := range destinations {
		host, remote := sshHost(name, d)
		if !remote {
			continue
		}
		if touchHosts[strings.ToLower(host)] {
			return true
		}
		out, err := exec.Command("ssh", append(append([]string{"-G"}, config...), host)...).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if resolved, ok := strings.CutPrefix(line, "hostname "); ok && touchHosts[strings.ToLower(resolved)] {
				return true
			}
		}
	}
	return false
}

// sshHost is the host a destination of ssh, scp or sftp names, and whether
// it names one: scp's local paths do not.
func sshHost(name, d string) (string, bool) {
	rest, scheme := "", false
	for _, s := range []string{"ssh://", "scp://", "sftp://"} {
		if r, ok := strings.CutPrefix(d, s); ok {
			rest, scheme = r, true
		}
	}
	if scheme {
		rest, _, _ = strings.Cut(rest, "/")
	} else {
		colon := strings.Index(d, ":")
		if name == "scp" && (colon <= 0 || strings.Contains(d[:colon], "/")) {
			return "", false
		}
		rest = d
	}
	if at := strings.LastIndex(rest, "@"); at >= 0 {
		rest = rest[at+1:]
	}
	host, _, _ := strings.Cut(rest, ":")
	return host, host != ""
}

// commitWriters are the git commands that write commits, with the options
// that make each write none.
var commitWriters = map[string]map[string]bool{
	"commit":      set("--dry-run"),
	"rebase":      set("--abort", "--quit", "--edit-todo", "--show-current-patch"),
	"cherry-pick": set("--abort", "--quit", "--no-commit", "-n"),
	"revert":      set("--abort", "--quit", "--no-commit", "-n"),
	"merge":       set("--abort", "--quit", "--no-commit", "--squash", "--ff-only"),
	"am":          set("--abort", "--quit", "--show-current-patch"),
	"pull":        set("--ff-only"),
}

const signingOn = "Signing needs the operator's hardware key, which nobody is here to touch, and this `git %s` would sign. " +
	"Run it as `git -c commit.gpgsign=false %s ...`, without -S or --gpg-sign; the operator signs everything in one pass at the end."

// checkSigning denies a command that would write a signed commit, which
// waits for a key touch: commit.gpgsign is on where it runs, or it asks for
// a signature.
func checkSigning(sub string, rest []string, g gitState) string {
	none, writes := commitWriters[sub]
	if !writes {
		return ""
	}
	asked := false
	for _, w := range rest {
		switch {
		case w == "--no-gpg-sign", none[w], w == "-h", w == "--help":
			return ""
		case w == "--gpg-sign", strings.HasPrefix(w, "--gpg-sign="), strings.HasPrefix(w, "-S"):
			asked = true
		}
	}
	if !asked && !g.signs() {
		return ""
	}
	return fmt.Sprintf(signingOn, sub, sub)
}

func (g gitState) run(args ...string) (string, bool) {
	if g.dir == "" {
		return "", false
	}
	cmd := exec.Command("git", append([]string{"-C", g.dir}, args...)...)
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err == nil
}

// main reports whether dir is a repository's main worktree, or may be:
// a directory the guard cannot tell counts as one.
func (g gitState) main() bool {
	if g.dir == "" {
		return true
	}
	out, ok := g.run("rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if !ok {
		return false
	}
	dirs := strings.Split(out, "\n")
	return len(dirs) == 2 && dirs[0] == dirs[1]
}

// checkGitSession checks a git command against the rules for every
// session that need the repository's state: destroying the main checkout's
// work and losing history.
func checkGitSession(sub string, rest []string, g gitState) string {
	has := func(flags ...string) bool {
		for _, w := range rest {
			for _, f := range flags {
				if w == f || strings.HasPrefix(w, f+"=") {
					return true
				}
			}
		}
		return false
	}
	short := func(letter string) bool {
		for _, w := range rest {
			if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.Contains(w, letter) {
				return true
			}
		}
		return false
	}
	everything := func() bool {
		for _, w := range rest {
			if w == "." || w == "./" || w == ":/" || w == "*" || w == ":(top)" {
				return true
			}
		}
		return false
	}
	first := ""
	if len(rest) > 0 {
		first = rest[0]
	}
	switch {
	case sub == "stash" && (first == "drop" || first == "clear"):
		return sharedStash
	case sub == "stash" && untaggedStash(rest):
		return untagged
	case sub == "worktree" && first == "add" && (has("--force") || short("f")):
		return "--force adds a worktree on a branch another worktree has, or over a path git keeps for one, which can take another session's place. " +
			"Add a worktree of your own without it (`git worktree add -b NEW ../PATH BRANCH`), or ask the operator."
	case (sub == "switch" || sub == "checkout") && has("--ignore-other-worktrees"):
		return "A branch checked out in another worktree belongs to whoever works there, and checking it out here too moves it under them. " +
			"Work on a branch of your own in a worktree of your own (`git worktree add -b NEW ../PATH BRANCH`), or ask the operator."
	case sub == "reset" && has("--hard"),
		sub == "checkout" && (has("-f", "--force") || everything()),
		sub == "restore" && everything() && (!has("--staged", "-S") || has("--worktree", "-W")),
		sub == "switch" && has("-f", "--force", "--discard-changes"),
		sub == "clean" && (short("f") || has("--force")),
		sub == "stash" && has("-u", "--include-untracked", "-a", "--all"):
		if g.main() {
			return mainCheckout
		}
	case sub == "add" && (has("-A", "--all") || everything()):
		if g.main() {
			return sweeping
		}
	case (sub == "switch" || sub == "checkout") && changesBranch(sub, rest, g):
		if g.main() {
			return "Changing branch in the repository's main checkout moves the operator's working tree under them. " +
				"Work in a worktree of your own (`git worktree add ../PATH BRANCH`), or ask the operator."
		}
	case sub == "reflog" && (first == "expire" || first == "delete"),
		sub == "gc" && (has("--prune=now") || has("--prune=all")),
		sub == "prune", sub == "filter-branch", sub == "filter-repo",
		sub == "worktree" && first == "prune":
		return historyLoss
	case sub == "update-ref" && has("-d"):
		for _, w := range rest {
			if strings.HasPrefix(w, "refs/heads/") {
				return historyLoss
			}
		}
	case sub == "branch" && (has("-D") || (has("-d", "--delete") && has("-f", "--force")) || short("D")):
		for _, b := range rest {
			if strings.HasPrefix(b, "-") {
				continue
			}
			if g.unpushed("refs/heads/" + b) {
				return historyLoss
			}
		}
	case sub == "worktree" && first == "remove" && has("-f", "--force"):
		for _, p := range rest[1:] {
			if strings.HasPrefix(p, "-") {
				continue
			}
			if (gitState{dir: expandPath(p, g.dir)}).dirty() {
				return historyLoss
			}
		}
	}
	return ""
}

// unpushed reports whether ref has commits no remote-tracking branch has,
// or may have: a ref git cannot read counts.
func (g gitState) unpushed(ref string) bool {
	out, ok := g.run("rev-list", "--max-count=1", ref, "--not", "--remotes")
	return !ok || out != ""
}

// dirty reports whether the worktree at g.dir has changes or untracked
// files, or may have: a directory git cannot read counts.
func (g gitState) dirty() bool {
	out, ok := g.run("status", "--porcelain")
	return !ok || out != ""
}

const discardWorktree = "Removing this worktree with discard_changes deletes uncommitted files or commits no remote has, which git cannot get back. " +
	"Exit with `keep`, or commit the work and put the removal in your report for the operator."

// checkExitWorktree denies Claude Code's ExitWorktree when it would discard
// the worktree at dir and its branch while they hold work found nowhere
// else.
func checkExitWorktree(toolInput json.RawMessage, dir string) string {
	var in struct {
		Action         string `json:"action"`
		DiscardChanges bool   `json:"discard_changes"`
	}
	if json.Unmarshal(toolInput, &in) != nil {
		return unreadable
	}
	if in.Action != "remove" || !in.DiscardChanges {
		return ""
	}
	if g := (gitState{dir: dir}); g.dirty() || g.unpushed("HEAD") {
		return discardWorktree
	}
	return ""
}

const untagged = "Every worktree shares one stash, so an untagged entry, or a pop or apply of whatever is on top, can take another session's work. " +
	"Use a tagged entry: `git stash push -u -m <unique-tag>`, take its SHA from `git stash list --format='%H %s'`, " +
	"restore it with `git stash apply <sha>`, and for the drop, which is the operator's, put `git stash drop stash@{N}` in your report, " +
	"N found again by the tag in `git stash list`."

// untaggedStash reports whether `git stash` with these arguments makes an
// entry without a message, or pops or applies one without naming it.
func untaggedStash(args []string) bool {
	verb := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		verb, args = args[0], args[1:]
	}
	named := false
	for _, a := range args {
		named = named || !strings.HasPrefix(a, "-")
	}
	switch verb {
	case "", "push":
		for _, a := range args {
			if a == "--" {
				break
			}
			if a == "--message" || strings.HasPrefix(a, "--message=") ||
				strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "m") {
				return false
			}
		}
		return true
	case "save", "pop", "apply":
		return !named
	}
	return false
}

// changesBranch reports whether `git switch` or `git checkout` with these
// arguments moves the worktree to another branch or commit, rather than
// detaching where it is or checking out files.
func changesBranch(sub string, args []string, g gitState) bool {
	var positional []string
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help" || a == "--":
			return false
		case a == "-" || !strings.HasPrefix(a, "-"):
			positional = append(positional, a)
		case set("-c", "-C", "--create", "--force-create", "-b", "-B", "--orphan")[strings.SplitN(a, "=", 2)[0]]:
			return true
		}
	}
	if sub == "switch" {
		return len(positional) > 0
	}
	if len(positional) != 1 {
		return false
	}
	if positional[0] == "-" {
		return true
	}
	_, commit := g.run("rev-parse", "--verify", "-q", positional[0]+"^{commit}")
	return commit
}

// sharedKeys are the git config keys whose shared value is the operator's.
var sharedKeys = regexp.MustCompile(`^(core\.(bare|worktree|hookspath|fsmonitor|sshcommand|pager|editor)|include(if)?\..*|filter\..*|diff\..*\.textconv|remote\..*|url\..*|credential(\..*)?)$`)
