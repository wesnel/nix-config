package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/repos"
)

// loopVars mark a review-loop session and its mode, which the operator's
// claude-review-loop alone sets: pr-loop reads PR_LOOP_MODE to gate request,
// reply, draft and push, and the session's settings allow `Bash(pr-loop *)`.
var loopVars = set("PR_LOOP_SESSION", "PR_LOOP_MODE")

// loopStart is why an agent may not start a loop or set its variables.
const loopStart = "Only the operator starts a review loop, with `claude-review-loop --bots|--people`, which sets " +
	"PR_LOOP_SESSION and PR_LOOP_MODE. Run pr-loop without them, and put the loop you propose in the report for the operator."

// namers are the builtins that set or export a variable they are given by
// name, such as `read PR_LOOP_MODE` or `export PR_LOOP_MODE`; printf does
// with `-v NAME`.
var namers = set("read", "mapfile", "readarray", "getopts", "export", "declare", "typeset", "local", "readonly")

// checkLoopStart refuses claude-review-loop, and any assignment of
// PR_LOOP_SESSION or PR_LOOP_MODE the guard reads: before a command, as
// `export` or `env` give it, as `set` or `launchctl setenv` take it, or as
// a namer or `printf -v` names it.
//
// An agent cannot set these in a spelling the guard reads. Any other
// spelling is a compound call, which matches no excluded command, since
// Claude Code matches an exclusion against the text of the call, so it
// runs sandboxed without the token.
func checkLoopStart(c call) string {
	for _, kv := range c.env {
		if name, _, _ := strings.Cut(kv, "="); loopVars[name] {
			return loopStart
		}
	}
	if len(c.words) == 0 {
		return ""
	}
	if !c.expands[0] && base(c.words[0]) == "claude-review-loop" {
		return loopStart
	}
	setter := !c.expands[0] && (base(c.words[0]) == "set" || base(c.words[0]) == "launchctl")
	namer := !c.expands[0] && namers[c.words[0]]
	printf := !c.expands[0] && base(c.words[0]) == "printf"
	for i, w := range c.words {
		if c.expands[i] || i == 0 {
			continue
		}
		name, _, assigned := strings.Cut(w, "=")
		switch {
		case assigned && loopVars[name],
			setter && loopVars[w] && i+1 < len(c.words),
			namer && loopVars[w],
			printf && c.words[i-1] == "-v" && loopVars[w],
			printf && strings.HasPrefix(w, "-v") && loopVars[w[2:]]:
			return loopStart
		}
	}
	return ""
}

// checkLoopSettings refuses a file tool's write naming a loop variable in a
// settings file a session takes its environment from, which would start
// the next session there as a loop.
func checkLoopSettings(tool string, toolInput json.RawMessage, dir string) string {
	for _, c := range fileChanges(tool, toolInput) {
		if !settingsFile(expandPath(c.path, dir)) {
			continue
		}
		for name := range loopVars {
			if strings.Contains(c.text, name) {
				return loopStart
			}
		}
	}
	return ""
}

// settingsFile reports whether a write to path, absolute, reaches a file
// guardFile names, judged as Reaches judges it: as written, through links,
// and by what the file system opens for a name it folds. The Claude Code
// settings it looks for are the home directory's and those of the .claude
// directory in each directory above path, a folded spelling of one
// included. A file with another name, a hard link, could be any of them
// wherever it sits, so it counts as one.
func settingsFile(path string) bool {
	if guardFile(path) {
		return true
	}
	at := physical(path)
	// A .claude directory not made yet: Reaches has no directory to judge
	// by, so the name is judged as APFS would open it.
	if claudeSettings.MatchString(strings.ToLower(at)) ||
		strings.EqualFold(filepath.Base(filepath.Dir(at)), ".claude") && !ascii(filepath.Base(at)) {
		return true
	}
	if info, err := os.Stat(at); err == nil && info.Mode().IsRegular() {
		if st, ok := info.Sys().(*syscall.Stat_t); ok && uint64(st.Nlink) > 1 {
			return true
		}
	}
	roots := []string{home}
	for q := filepath.Dir(at); ; q = filepath.Dir(q) {
		roots = append(roots, q)
		if q == filepath.Dir(q) {
			break
		}
	}
	var targets []string
	for _, root := range roots {
		for _, name := range []string{"settings.json", "settings.local.json"} {
			targets = append(targets, filepath.Join(root, ".claude", name))
		}
	}
	targets = append(targets, filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".codex", "hooks.json"),
		repos.Path())
	for _, t := range targets {
		if Reaches(path, t, false) {
			return true
		}
	}
	config := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(config) {
		config = filepath.Join(home, ".config")
	}
	for _, base := range []string{config, hostpaths.ConfigHome()} {
		if Reaches(path, filepath.Join(base, "agent-tools"), true) {
			return true
		}
	}
	return false
}
