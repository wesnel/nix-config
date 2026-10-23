package guard

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// agentSettings is why no agent writes a clone's agent settings: they name
// the commands agents' tools run in the clone.
const agentSettings = "This is the clone's agent settings, which name the commands agents' tools run in it, and only the operator writes them. " +
	"Put the settings you propose, as the file's whole JSON, in your report for the operator."

// gitLink is why no agent writes the files that tell git which repository
// a worktree belongs to: they choose which agent settings apply in it.
const gitLink = "This file tells git which repository a worktree belongs to (a worktree's .git file, or a git directory's commondir or gitdir), " +
	"which chooses the agent settings that apply there, so only git's worktree commands and the operator change it. " +
	"Use `git worktree add`, `git worktree move` or `git worktree repair`, or put the change in your report for the operator."

// agentSettingsName is the settings' path in a common git directory, where
// package repos reads them.
var agentSettingsName = filepath.Join("info", "agents.json")

// agentSettingsFile reports whether path, absolute, reaches a clone's agent
// settings, info/agents.json in its common git directory: by file identity
// (Reaches), so a case or Unicode variant, a link and a hard link count,
// with the settings of every repository found from path or from one of
// dirs as git finds a common directory, through .git directories, .git
// files and commondir; and, as written or with its links resolved, in any
// case where names fold it, as agents.json in the info directory of
// anything laid out as a git directory, for settings not there yet.
func agentSettingsFile(path string, dirs ...string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	for _, from := range append([]string{path}, dirs...) {
		for _, k := range settingsFiles(from) {
			if Reaches(path, k, false) {
				return true
			}
		}
	}
	for _, p := range []string{filepath.Clean(path), physical(path)} {
		info := filepath.Dir(p)
		if fold(filepath.Base(p)) == "agents.json" && fold(filepath.Base(info)) == "info" && gitDirLayout(filepath.Dir(info)) {
			return true
		}
	}
	return false
}

// holdsAgentSettings reports whether path, absolute, is a directory that
// holds a clone's agent settings that are there, found as
// agentSettingsFile finds them, which a removal or move of path takes
// with it.
func holdsAgentSettings(path string, dirs ...string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	for _, from := range append([]string{path}, dirs...) {
		for _, k := range settingsFiles(from) {
			if _, err := os.Lstat(k); err == nil && Reaches(k, path, true) {
				return true
			}
		}
	}
	return false
}

// settingsFiles are the agent settings of the repositories whose worktree
// or git directory holds from, absolute, or is it, as the file system
// walks it.
func settingsFiles(from string) []string {
	if !filepath.IsAbs(from) {
		return nil
	}
	var out []string
	for d := physical(from); ; d = filepath.Dir(d) {
		if common := commonGitDir(d); common != "" {
			out = append(out, filepath.Join(common, agentSettingsName))
		}
		if d == filepath.Dir(d) {
			return out
		}
	}
}

// gitLinkFile reports whether path, absolute, reaches a file that tells
// git which repository a worktree belongs to, and so which agent settings
// apply there: a worktree's .git file, or a git directory's commondir or
// gitdir. It judges by file identity (Reaches) against those of every
// repository found from path or from one of dirs, as settingsFiles finds
// repositories, each linked worktree's included; and, as written or with
// its links resolved, in any case where names fold it, by name: .git
// where no directory of that name is, and commondir or gitdir in anything
// laid out as a git directory.
func gitLinkFile(path string, dirs ...string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	for _, from := range append([]string{path}, dirs...) {
		for _, k := range linkFiles(from) {
			if Reaches(path, k, false) {
				return true
			}
		}
	}
	for _, p := range []string{filepath.Clean(path), physical(path)} {
		switch base := fold(filepath.Base(p)); {
		case base == ".git":
			if info, err := os.Stat(p); err != nil || !info.IsDir() {
				return true
			}
		case base == "commondir" || base == "gitdir":
			if gitDirLayout(filepath.Dir(p)) {
				return true
			}
		}
	}
	return false
}

// linkFiles are the .git files, commondir and gitdir files there are of
// the repositories whose worktree or git directory holds from, absolute,
// or is it, as the file system walks it. One not there yet is left to
// gitLinkFile's names, so that a new name in a git directory is judged
// by what git reads from it, as gitConfigFile judges it.
func linkFiles(from string) []string {
	if !filepath.IsAbs(from) {
		return nil
	}
	var out []string
	for d := physical(from); ; d = filepath.Dir(d) {
		dotGit := filepath.Join(d, ".git")
		if info, err := os.Stat(dotGit); err == nil && info.Mode().IsRegular() {
			out = append(out, dotGit)
		}
		if gitDir := gitDirOf(d); gitDir != "" {
			admins := []string{gitDir}
			for _, g := range []string{gitDir, commonOf(gitDir)} {
				entries, _ := os.ReadDir(filepath.Join(g, "worktrees"))
				for i, e := range entries {
					if i == 1000 {
						break
					}
					if e.IsDir() {
						admins = append(admins, filepath.Join(g, "worktrees", e.Name()))
					}
				}
			}
			for _, a := range admins {
				for _, f := range []string{filepath.Join(a, "commondir"), filepath.Join(a, "gitdir")} {
					if _, err := os.Lstat(f); err == nil {
						out = append(out, f)
					}
				}
			}
		}
		if d == filepath.Dir(d) {
			return out
		}
	}
}

// commonGitDir is the common git directory of the repository d is the
// worktree or git directory of, as git finds it: gitDirOf(d), followed
// through its commondir; "" where d is neither.
func commonGitDir(d string) string {
	gitDir := gitDirOf(d)
	if gitDir == "" {
		return ""
	}
	return commonOf(gitDir)
}

// gitDirOf is the git directory of the repository d is the worktree or
// git directory of, as git finds it: d/.git, a directory or a file naming
// one with `gitdir:`, or d itself; "" where d is neither.
func gitDirOf(d string) string {
	dotGit := filepath.Join(d, ".git")
	switch info, err := os.Stat(dotGit); {
	case err == nil && info.IsDir():
		return dotGit
	case err == nil && info.Mode().IsRegular():
		if named, ok := strings.CutPrefix(firstLine(dotGit), "gitdir: "); ok && named != "" {
			return relativeTo(d, named)
		}
	case gitDirLayout(d):
		return d
	}
	return ""
}

// commonOf is the common directory of the git directory gitDir: where its
// commondir leads, or gitDir itself.
func commonOf(gitDir string) string {
	if common := firstLine(filepath.Join(gitDir, "commondir")); common != "" {
		return relativeTo(gitDir, common)
	}
	return gitDir
}

// gitDirLayout reports whether d is laid out as a git directory: a HEAD
// file, and an objects directory or, for a linked worktree's, a commondir.
func gitDirLayout(d string) bool {
	if info, err := os.Stat(filepath.Join(d, "HEAD")); err != nil || !info.Mode().IsRegular() {
		return false
	}
	if info, err := os.Stat(filepath.Join(d, "objects")); err == nil && info.IsDir() {
		return true
	}
	info, err := os.Stat(filepath.Join(d, "commondir"))
	return err == nil && info.Mode().IsRegular()
}

// firstLine is the first line of the file at path, of at most 4 KiB, with
// surrounding whitespace removed; "" where it cannot be read.
func firstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, _ := io.ReadAll(io.LimitReader(f, 4<<10))
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line)
}

// relativeTo is p, from base where it is relative, cleaned.
func relativeTo(base, p string) string {
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// checkAgentSettingsShell denies a command, run in dir, that writes,
// removes or moves a clone's agent settings or a directory holding them,
// or a file that chooses them (gitLinkFile), by the targets
// gitConfigShellWrite reads a git config write by.
func checkAgentSettingsShell(name string, args, redirected []string, dir string) string {
	if shellWrites(name, args, redirected, dir, func(path string) bool { return agentSettingsFile(path, dir) }) {
		return agentSettings
	}
	if shellWrites(name, args, redirected, dir, func(path string) bool { return gitLinkFile(path, dir) }) {
		return gitLink
	}
	if set("mv", "rm", "unlink", "trash", "rmdir")[name] {
		for _, t := range changedFiles(name, args, redirected) {
			path := expandPath(t, dir)
			if agentSettingsFile(path, dir) || holdsAgentSettings(path, dir) {
				return agentSettings
			}
			if gitLinkFile(path, dir) {
				return gitLink
			}
		}
	}
	return ""
}

// checkFileAgentSettings is why a file tool, called in dir, may not write
// path, absolute: a clone's agent settings, or a file that chooses them;
// "" where it may.
func checkFileAgentSettings(path, dir string) string {
	switch {
	case agentSettingsFile(path, dir):
		return agentSettings
	case gitLinkFile(path, dir):
		return gitLink
	}
	return ""
}

// checkGitAgentSettings denies git's command sub, run where -C took it
// (physical, "" where the guard cannot tell), that writes a clone's agent
// settings or a file that chooses them: as a file it outputs
// (gitTargets), or as the file `git config --file` writes. checkout is
// the hook's directory.
func checkGitAgentSettings(sub string, rest []string, expands []bool, physical, checkout string) string {
	_, outputs := gitTargets(sub, rest, expands, physical)
	if sub == "config" && !configRead(rest) {
		for j, a := range rest {
			file, at := "", j
			switch {
			case (a == "-f" || a == "--file") && j+1 < len(rest):
				file, at = rest[j+1], j+1
			case strings.HasPrefix(a, "--file="):
				file = strings.TrimPrefix(a, "--file=")
			default:
				continue
			}
			if p := physicalPath(physical, shellPath(file, at < len(expands) && expands[at])); p != "" {
				outputs = append(outputs, p)
			}
		}
	}
	for _, p := range outputs {
		if agentSettingsFile(p, physical, checkout) {
			return agentSettings
		}
		if gitLinkFile(p, physical, checkout) {
			return gitLink
		}
	}
	return ""
}
