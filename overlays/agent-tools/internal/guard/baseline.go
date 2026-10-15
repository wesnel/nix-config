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

// secretName is an environment variable that likely holds a credential.
var secretName = regexp.MustCompile(`(?i)token|key|secret|password|credential`)

// guardFile reports whether path holds the hooks that run the guard, or
// its settings.
func guardFile(path string) bool {
	if agentToolsConfig(path) {
		return true
	}
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
		if agentToolsState(expandPath(target, dir)) {
			return stateWrite
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
		if (name == "sed" || name == "perl") && !has("--in-place") && !strings.Contains(" "+strings.Join(args, " "), " -i") {
			break
		}
		for _, a := range args {
			if guardFile(expandPath(strings.TrimPrefix(a, "of="), dir)) {
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

const realEnvironment = "NGROK_ENV set to prod or stage points ngrok's tooling at a real environment, which is Wesley's to act on. " +
	"Use a dev or local environment, or put the command in your report for Wesley."

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
	// sleeping is for Codex and ECA, which can only wait in the
	// foreground.
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
	"Run the command itself, as `bash -c '...'`, `python3 script.py` or `cat FILE`, or put what you need in your report for Wesley."

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
	secrets := credentialPaths()
	for _, path := range candidates {
		for _, p := range secrets {
			if strings.HasPrefix(path, p) {
				return secretRead
			}
		}
	}
	return ""
}

const appendOnly = "This is a plan's event log, which is append-only and written by agent-plan alone. Record the event with `agent-plan log PLAN --item ID --kind KIND`, and change the plan itself in its plan file."

const stateWrite = "This is agent-tools' state (usage readings, quota choices and asks), which agent-plan writes itself. " +
	"Run the agent-plan command that records what you mean, such as `agent-plan usage choose`, or ask Wesley to change it."

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
// log or agent-tools' state.
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
		if agentToolsState(expandPath(t, dir)) {
			return stateWrite
		}
	}
	return ""
}

// inPlace reports whether sed's or perl's arguments edit their files in
// place.
func inPlace(name string, args []string) bool {
	for _, a := range args {
		switch {
		case a == "--in-place" || strings.HasPrefix(a, "--in-place="),
			name == "sed" && strings.HasPrefix(a, "-i"),
			name == "perl" && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "i"):
			return true
		}
	}
	return false
}

const shellEdit = "`%s` is a tracked file, and editing it from the shell bypasses the file-editing tools AGENTS.md requires. " +
	"Use the file-editing tools (`Edit`, `Write`, `apply_patch`, ECA's `edit_file`), or write scratch output under `$TMPDIR` or /tmp. " +
	"For a mechanical sweep over Go code, use `gopls rename`, `gofmt -r 'PATTERN -> REPLACEMENT' -w` or `go fix`, " +
	"with the exact command in the commit body."

// checkShellEdit denies a command that writes a tracked file with a
// redirection, tee, ed, sed or perl in place, or an interpreter's
// one-liner, run in dir.
func checkShellEdit(name string, args, redirected []string, dir string) string {
	for _, t := range editTargets(name, args, redirected) {
		if trackedFile(expandPath(t, dir)) {
			return fmt.Sprintf(shellEdit, t)
		}
	}
	return ""
}

// editTargets are the files a command edits, as it names them: its output
// redirections, the files of tee, ed, and sed or perl in place, and those
// an interpreter's one-liner writes.
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
		if agentToolsState(path) {
			return stateWrite
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
// is denied, with %s how the agent's interface takes a command Wesley runs,
// as AGENTS.md's "Commands Wesley runs" says.
const keyTouchText = "This waits for a touch of Wesley's hardware key, which nobody is here to give. " +
	"Hand Wesley the command as %s, and meanwhile work from local refs, such as `git log origin/main`, with commits left unsigned; " +
	"`gh pr view` and `gh api` read GitHub over HTTPS."

var (
	// keyTouch is keyTouchText for Claude Code and Codex, whose prompt runs
	// a line beginning with ! in the session.
	keyTouch = fmt.Sprintf(keyTouchText, "one line beginning with `!`, so its output lands here")
	// keyTouchPlain is keyTouchText for ECA, whose prompt does not.
	keyTouchPlain = fmt.Sprintf(keyTouchText, "one plain line for his terminal")
)

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
var sharedKeys = regexp.MustCompile(`^(core\.(bare|worktree|hookspath|fsmonitor|sshcommand|pager|editor)|include(if)?\..*|filter\..*|diff\..*\.textconv|alias\..*|remote\..*|url\..*|credential(\..*)?)$`)
