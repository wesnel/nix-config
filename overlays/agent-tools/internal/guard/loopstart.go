package guard

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/repos"
)

// loopVars mark a review-loop session and its mode, which the operator's
// review-loop alone sets: pr-loop reads PR_LOOP_MODE to gate request,
// reply, draft and push.
var loopVars = set("PR_LOOP_SESSION", "PR_LOOP_MODE")

// loopStart is why an agent may not start a loop or set its variables.
const loopStart = "Only the operator starts a review loop, with `review-loop --harness claude|codex --bots|--people`, which sets " +
	"PR_LOOP_SESSION and PR_LOOP_MODE. Run pr-loop without them, and put the loop you propose in the report for the operator."

// namers are the builtins that set or export a variable they are given by
// name, such as `read PR_LOOP_MODE` or `export PR_LOOP_MODE`; printf does
// with `-v NAME`.
var namers = set("read", "mapfile", "readarray", "getopts", "export", "declare", "typeset", "local", "readonly")

// checkLoopStart refuses review-loop and claude-review-loop, a harness
// started with the loop's variables (harnessLoopStart), and any assignment
// of PR_LOOP_SESSION or PR_LOOP_MODE the guard reads: before a command, as
// `export` or `env` give it, as `set` or `launchctl setenv` take it, or as
// a namer or `printf -v` names it. dir is where c runs.
//
// An agent cannot set these in a spelling the guard reads. Any other
// spelling is a compound call, which matches no excluded command, since
// Claude Code matches an exclusion against the text of the call, so it
// runs sandboxed without the token.
func checkLoopStart(c call, dir string) string {
	for _, kv := range c.env {
		if name, _, _ := strings.Cut(kv, "="); loopVars[name] {
			return loopStart
		}
	}
	if len(c.words) == 0 {
		return ""
	}
	if !c.expands[0] && (base(c.words[0]) == "review-loop" || base(c.words[0]) == "claude-review-loop") {
		return loopStart
	}
	if !c.expands[0] && harnessLoopStart(c, dir) {
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

// harnessLoopStart reports whether c starts Claude Code or Codex with a
// loop's variables, which would make it a loop session: in Claude Code's
// --settings, as text or a file, or in Codex's config overrides (-c,
// --config) or the profile -p or --profile names, which Codex reads from
// $CODEX_HOME/NAME.config.toml. It reads the options before `--`, after
// which every word is a prompt. A value the shell expands, or a file it
// cannot read, counts as naming them; a file that does not exist names
// nothing.
func harnessLoopStart(c call, dir string) bool {
	harness := base(c.words[0])
	if harness != "claude" && harness != "codex" {
		return false
	}
	for i := 1; i < len(c.words) && c.words[i] != "--"; i++ {
		option, value, joined := harnessOption(harness, c.words[i])
		if option == "" {
			continue
		}
		expands := c.expands[i]
		if !joined {
			if i+1 == len(c.words) {
				return false
			}
			i++
			value, expands = c.words[i], c.expands[i]
		}
		if expands {
			if value = shellPath(value, true); strings.ContainsAny(value, "$`") {
				return true
			}
		}
		var named bool
		switch {
		case option == "--config":
			named = namesLoopVar(value)
		case option == "--settings" && strings.HasPrefix(strings.TrimSpace(value), "{"):
			named = namesLoopVar(value)
		case option == "--settings":
			named = fileNamesLoopVar(expandPath(value, dir))
		case option == "--profile":
			codexHome, known := codexHome(c.env, dir)
			named = !known || fileNamesLoopVar(filepath.Join(codexHome, value+".config.toml"))
		}
		if named {
			return true
		}
	}
	return false
}

// harnessOption names the option word w gives harness, "--settings",
// "--config" or "--profile" by its long name, or "" for any other word,
// with its value when w carries it (`--config=V`, `-cV`).
func harnessOption(harness, w string) (option, value string, joined bool) {
	long := map[string][]string{"claude": {"--settings"}, "codex": {"--config", "--profile"}}[harness]
	for _, o := range long {
		if w == o {
			return o, "", false
		}
		if v, ok := strings.CutPrefix(w, o+"="); ok {
			return o, v, true
		}
	}
	if harness != "codex" || len(w) < 2 || w[0] != '-' || (w[1] != 'c' && w[1] != 'p') {
		return "", "", false
	}
	option = map[byte]string{'c': "--config", 'p': "--profile"}[w[1]]
	if len(w) == 2 {
		return option, "", false
	}
	return option, w[2:], true
}

// codexHome is the directory Codex reads its config and profiles from:
// $CODEX_HOME as the call sets it, or as the guard's environment has it,
// else ~/.codex. known is false when the call sets it to what the shell
// expands.
func codexHome(env []string, dir string) (path string, known bool) {
	value, set := os.LookupEnv("CODEX_HOME")
	for _, kv := range env {
		if name, v, _ := strings.Cut(kv, "="); name == "CODEX_HOME" {
			value, set = shellPath(v, true), true
		}
	}
	switch {
	case !set || value == "":
		return filepath.Join(home, ".codex"), true
	case strings.ContainsAny(value, "$`"):
		return "", false
	}
	return expandPath(value, dir), true
}

// namesLoopVar reports whether text names a loop variable.
func namesLoopVar(text string) bool {
	for name := range loopVars {
		if strings.Contains(text, name) {
			return true
		}
	}
	return false
}

// fileNamesLoopVar reports whether the file at path names a loop variable,
// or exists and cannot be read.
func fileNamesLoopVar(path string) bool {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return err != nil || namesLoopVar(string(data))
}

// checkLoopSettings refuses a file tool's write naming a loop variable in a
// settings file a session takes its environment from, which would start
// the next session there as a loop.
func checkLoopSettings(tool string, toolInput json.RawMessage, dir string) string {
	changes, err := fileChanges(tool, toolInput)
	if err != nil {
		return unreadable
	}
	for _, c := range changes {
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
