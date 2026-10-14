package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/plans"
)

// The rules every session is held to beyond GitHub: what would lose
// Wesley's work, read a credential, change his machine or switch the guard
// off. A rule that needs the repository's state asks git, from the
// directory the command runs in.

const (
	sweeping     = "This stages everything in the repository's main checkout, which holds Wesley's untracked work in progress. Stage the paths you changed by name (`git add PATH...`), or work in a worktree of your own (`git worktree add`)."
	mainCheckout = "This discards work in the repository's main checkout, which holds Wesley's work in progress. Do it in a worktree of your own (`git worktree add`), or ask Wesley."
	historyLoss  = "This deletes history git cannot get back, which is Wesley's call. Leave it, and put the command in your report for Wesley."
	sharedStash  = "Every worktree of a repository shares one stash, so this can discard another worktree's stashed work. Leave the stash as it is, and put the command in your report for Wesley."
	sharedConfig = "This setting is shared by every worktree and changes how git runs for Wesley too. Put the `git config` command in your report for Wesley; a worktree's own setting takes `git config --worktree`."
	secretRead   = "This reads a credential, which stays out of the session. Use the tool that holds it (`gh`, `pr-loop`, `git`), or ask Wesley for what you need from it."
	ngrokWrite   = "ngrok API writes reach production. Use a list or get command, or hand Wesley the command."
	systemSwitch = "Activating a system or profile is Wesley's step. Build it with `nix build` or `darwin-rebuild build`, and hand Wesley the switch command."
	rootCommand  = "Anything needing root is Wesley's to run. Put the command in your report for Wesley, and say its result is unverified."
	publishing   = "Publishing a package is Wesley's step. Build and test it here, and hand Wesley the publish command."
	killing      = "That process is one Wesley's session depends on (Emacs, gpg-agent, ssh, nix-daemon or mcp-proxy). Leave it running, and tell Wesley if it needs a restart."
	destroying   = "This would destroy the home directory, the root filesystem, the Nix store or Wesley's keys. Name the exact path inside the workspace you mean, or ask Wesley."
	guardOff     = "The guard and its hooks are Wesley's configuration, and stay on. Put the change you want in your report for Wesley."
	connector    = "Writing to Slack, Gmail, Linear, GitHub or Notion acts as Wesley. Confirm it with Wesley first, or put the draft in your report."
)

// home is the user's home directory, which ~ and $HOME name.
var home, _ = os.UserHomeDir()

// secretPaths are credentials, relative to home. Claude Code's Read is
// denied the same files by permissions.deny in the claude home-manager
// module; keep the two alike.
var secretPaths = []string{".ssh/id_", ".gnupg/private-keys", ".codex/auth.json", ".claude/.credentials.json",
	".local/state/mcp-proxy/token", "Library/Application Support/mcp-proxy", ".config/mcp-proxy",
	".config/sops/age", ".config/ngrok"}

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

// guardFile reports whether path holds the hooks that run the guard.
func guardFile(path string) bool {
	for _, p := range []string{".codex/config.toml", ".codex/hooks.json", ".config/eca/config.json"} {
		if path == filepath.Join(home, p) {
			return true
		}
	}
	return claudeSettings.MatchString(path)
}

var claudeSettings = regexp.MustCompile(`(^|/)\.claude/settings[^/]*\.json$`)

// expandPath is word as the shell would name a file: with ~ and $HOME
// expanded, and relative to dir.
func expandPath(word, dir string) string {
	for _, h := range []string{"${HOME}", "$HOME"} {
		if word == h || strings.HasPrefix(word, h+"/") {
			word = home + strings.TrimPrefix(word, h)
		}
	}
	if word == "~" || strings.HasPrefix(word, "~/") {
		word = home + word[1:]
	}
	if !filepath.IsAbs(word) && dir != "" {
		word = filepath.Join(dir, word)
	}
	return filepath.Clean(word)
}

// checkSession checks a call against the rules for every session that are
// not about GitHub. dir is where it runs, "" when the guard cannot tell.
func checkSession(c call, dir string) string {
	words := c.words
	name, args := base(words[0]), words[1:]
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
		if planLog(expandPath(target, dir)) {
			return appendOnly
		}
	}
	if reason := checkPlanLogWrite(name, args, dir); reason != "" {
		return reason
	}
	if reason := checkShellEdit(name, args, redirected, dir); reason != "" {
		return reason
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
			if !strings.HasPrefix(a, "-") && protectedProcess(a) {
				return killing
			}
		}
	case "kill":
		return checkKill(args)
	case "gpgconf":
		if has("--kill") {
			return killing
		}
	case "ssh":
		for i, a := range args {
			if (a == "-O" && i+1 < len(args) && (args[i+1] == "exit" || args[i+1] == "stop")) || a == "-Oexit" || a == "-Ostop" {
				return killing
			}
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
		if strings.Contains(strings.Join(args, " "), "kill-emacs") {
			return killing
		}
	case "emacs", "Emacs":
		return "A second Emacs, batch or not, runs apart from Wesley's daemon and can leave files and state behind for it. " +
			"Use `emacsclient` as the emacsclient skill says, such as `emacsclient -e '(check-parens)'`."
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

// inPlace reports whether sed's or perl's arguments edit their files in
// place: --in-place, or an i in a cluster of short options (-i, -i.bak,
// -Ei, -ni, -pi), read up to the first option whose value ends the cluster.
func inPlace(name string, args []string) bool {
	values := map[string]string{"sed": "efl", "perl": "eEMmIxCdDF"}[name]
	for _, a := range args {
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
// process Wesley's session depends on.
func protectedProcess(name string) bool {
	name = strings.ToLower(name)
	for _, p := range []string{"emacs", "gpg-agent", "ssh", "nix-daemon", "mcp-proxy"} {
		if strings.Contains(name, p) {
			return true
		}
	}
	return false
}

// checkKill denies a kill of every process, a process group, or a process
// Wesley's session depends on, which it looks up with ps.
func checkKill(args []string) string {
	// The signal, if any, comes first; a negative number after it is a
	// process group.
	i := 0
	switch {
	case len(args) == 0:
		return ""
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
	for _, p := range args[min(i, len(args)):] {
		n, err := strconv.Atoi(p)
		if err != nil {
			continue // a job, or a name the shell expands
		}
		if n <= 0 {
			return killing
		}
		cmd := exec.Command("ps", "-o", "comm=", "-p", p)
		if out, err := cmd.Output(); err == nil && protectedProcess(filepath.Base(strings.TrimSpace(string(out)))) {
			return killing
		}
	}
	return ""
}

// protectedTree reports whether path is the home directory, the root, or
// in the Nix store or Wesley's keys; a glob counts as its directory. The
// home directory and the root count only for a recursive operation.
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

// checkSecretPath denies a word that names a credential file.
func checkSecretPath(word, dir string) string {
	if strings.HasSuffix(word, ".pub") {
		return ""
	}
	candidates := []string{expandPath(word, dir)}
	if _, value, ok := strings.Cut(word, "="); ok {
		candidates = append(candidates, expandPath(value, dir))
	}
	for _, path := range candidates {
		for _, p := range secretPaths {
			if strings.HasPrefix(path, home+"/"+p) {
				return secretRead
			}
		}
	}
	return ""
}

const appendOnly = "This is a plan's event log, which is append-only and written by agent-plan alone. Record the event with `agent-plan log PLAN --item ID --kind KIND`, and change the plan itself in its plan file."

// planLog reports whether path is a plan's log, or the lock file that
// records its state, in the plans directory agent-plan's config names. A
// relative path counts, since where it is cannot be told.
func planLog(path string) bool {
	if !plans.IsLog(filepath.Base(path)) {
		return false
	}
	if !filepath.IsAbs(path) {
		return true
	}
	dir := filepath.Join(home, "git", "notes", "agents")
	if c, err := plans.Load(); err == nil {
		dir = c.Dir
	}
	if filepath.Dir(path) == filepath.Clean(dir) {
		return true
	}
	realDir, err1 := filepath.EvalSymlinks(dir)
	parent, err2 := filepath.EvalSymlinks(filepath.Dir(path))
	return err1 == nil && err2 == nil && parent == realDir
}

// checkPlanLogWrite denies a command that writes, moves or removes a plan's
// log.
func checkPlanLogWrite(name string, args []string, dir string) string {
	var positional, targets []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			positional = append(positional, a)
		}
	}
	switch name {
	case "tee", "mv", "rm", "unlink", "trash", "truncate", "shred":
		targets = positional
	case "cp", "install", "ln":
		if len(positional) > 0 {
			targets = positional[len(positional)-1:]
		}
	case "dd":
		for _, a := range args {
			if of, ok := strings.CutPrefix(a, "of="); ok {
				targets = append(targets, of)
			}
		}
	case "sed", "perl":
		if inPlace(name, args) {
			targets = positional
		}
	}
	for _, t := range targets {
		if planLog(expandPath(t, dir)) {
			return appendOnly
		}
	}
	return ""
}

const shellEdit = "`%s` is a tracked file, and editing it from the shell bypasses the file-editing tools AGENTS.md requires. " +
	"Use the file-editing tools (`Edit`, `Write`, `apply_patch`, ECA's `edit_file`), or write scratch output under `$TMPDIR` or /tmp. " +
	"For a mechanical sweep over Go code, use `gopls rename`, `gofmt -r 'PATTERN -> REPLACEMENT' -w` or `go fix`, " +
	"with the exact command in the commit body."

// checkShellEdit denies a command that writes a tracked file with a
// redirection, tee, ed, or sed or perl in place, run in dir.
func checkShellEdit(name string, args, redirected []string, dir string) string {
	for _, t := range editTargets(name, args, redirected) {
		if trackedFile(expandPath(t, dir)) {
			return fmt.Sprintf(shellEdit, t)
		}
	}
	return ""
}

// editTargets are the files a command edits, as it names them: its output
// redirections, and the files of tee, ed, and sed or perl in place.
func editTargets(name string, args, redirected []string) []string {
	var targets []string
	for _, t := range redirected {
		if !strings.HasPrefix(t, "&") {
			targets = append(targets, t)
		}
	}
	if name == "tee" || name == "ed" || (name == "sed" || name == "perl") && inPlace(name, args) {
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				targets = append(targets, a)
			}
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
// Write, MultiEdit and NotebookEdit, Codex's apply_patch, and ECA's
// write_file, edit_file and move_file.
func checkFileTool(tool string, toolInput json.RawMessage, dir string) string {
	for _, c := range fileChanges(tool, toolInput) {
		path := expandPath(c.path, dir)
		if reason := checkSecretPath(path, dir); reason != "" {
			return reason
		}
		if planLog(path) {
			return appendOnly
		}
		switch {
		case filepath.Base(path) == "config" && filepath.Base(filepath.Dir(path)) == ".git":
			return sharedConfig
		case strings.HasSuffix(path, ".codex/hooks.json") && guardFile(path):
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

// fileChanges are the files a call to a file-editing tool writes.
func fileChanges(tool string, toolInput json.RawMessage) []change {
	var in struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Path         string `json:"path"`
		Source       string `json:"source"`
		Destination  string `json:"destination"`
		Content      string `json:"content"`
		OldString    string `json:"old_string"`
		NewString    string `json:"new_string"`
		OldContent   string `json:"original_content"`
		NewContent   string `json:"new_content"`
		NewSource    string `json:"new_source"`
		Command      string `json:"command"`
		Edits        []struct {
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		} `json:"edits"`
	}
	json.Unmarshal(toolInput, &in)
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
		text := in.Content + in.OldString + in.NewString + in.OldContent + in.NewContent + in.NewSource
		for _, e := range in.Edits {
			text += e.OldString + e.NewString
		}
		changes = append(changes, change{in.FilePath + in.NotebookPath + in.Path, text})
		for _, p := range []string{in.Source, in.Destination} {
			if p != "" {
				changes = append(changes, change{p, ""})
			}
		}
	}
	return changes
}

// connectorTool matches the claude.ai connectors that act as Wesley.
var connectorTool = regexp.MustCompile(`(?i)^mcp__claude_ai_(slack|gmail|linear|github|notion)__`)

// readWord matches a tool name that names a read.
var readWord = regexp.MustCompile(`(?i)(^|[_-])(get|list|search|fetch|read|query|view|find|lookup|describe|retrieve)([_-]|$)`)

// checkConnector asks before a claude.ai connector writes.
func checkConnector(tool string) string {
	if connectorTool.MatchString(tool) && !readWord.MatchString(connectorTool.ReplaceAllString(tool, "")) {
		return connector
	}
	return ""
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
	if g.dir == "" {
		return false
	}
	cmd := exec.Command("git", append(append([]string{}, g.config...), "-C", g.dir, "config", "--bool", "commit.gpgsign")...)
	cmd.Env = append(gitenv.Without(os.Environ()), g.env...)
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
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

const signingOn = "Signing needs Wesley's hardware key, which nobody is here to touch, and this `git %s` would sign. " +
	"Run it as `git -c commit.gpgsign=false %s ...`, without -S or --gpg-sign; Wesley signs everything in one pass at the end."

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
			"Add a worktree of your own without it (`git worktree add -b NEW ../PATH BRANCH`), or ask Wesley."
	case (sub == "switch" || sub == "checkout") && has("--ignore-other-worktrees"):
		return "A branch checked out in another worktree belongs to whoever works there, and checking it out here too moves it under them. " +
			"Work on a branch of your own in a worktree of your own (`git worktree add -b NEW ../PATH BRANCH`), or ask Wesley."
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
			return "Changing branch in the repository's main checkout moves Wesley's working tree under him. " +
				"Work in a worktree of your own (`git worktree add ../PATH BRANCH`), or ask Wesley."
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
	"Exit with `keep`, or commit the work and put the removal in your report for Wesley."

// checkExitWorktree denies Claude Code's ExitWorktree when it would discard
// the worktree at dir and its branch while they hold work found nowhere
// else.
func checkExitWorktree(toolInput json.RawMessage, dir string) string {
	var in struct {
		Action         string `json:"action"`
		DiscardChanges bool   `json:"discard_changes"`
	}
	json.Unmarshal(toolInput, &in)
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
	"restore it with `git stash apply <sha>`, and for the drop, which is Wesley's, put `git stash drop stash@{N}` in your report, " +
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

// sharedKeys are the git config keys whose shared value is Wesley's.
var sharedKeys = regexp.MustCompile(`^(core\.(bare|worktree|hookspath|fsmonitor|sshcommand|pager|editor)|include(if)?\..*|filter\..*|diff\..*\.textconv|remote\..*|url\..*|credential(\..*)?)$`)
