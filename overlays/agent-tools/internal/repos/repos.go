// Package repos looks up the operator's settings for the clone a checkout
// belongs to, which must not come from the repository: a repository that
// could choose them would choose what runs. They are info/agents.json in
// the clone's common git directory, which every worktree of the clone
// shares and git never tracks, and which the guard refuses every agent's
// write to. The operator writes the file; a clone without it has none.
//
// It also names the user's agent-tools config file.
package repos

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/redcheck"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// Settings are how agents prepare, build and test one clone, as its
// settings file holds them: a JSON object of these keys and no others.
type Settings struct {
	// Setup prepares a fresh checkout, such as generating files the
	// repository does not commit. `pr-loop setup [PATH]` runs it with sh in
	// a worktree, inside a sandboxed session's sandbox; nothing runs it on
	// its own.
	Setup string `json:"setup,omitempty"`
	// Suites are the test suites git red-check runs, each where a commit
	// changes its test files: a JSON object of redcheck.Suite's keys alone,
	// which redcheck.CheckSuites must accept. git delegate never runs them,
	// since they run the reviewed commits' code.
	Suites []redcheck.Suite `json:"suites,omitempty"`
	// Routes are rules that route this repository's changes to focused
	// reviews beside the built-in ones, which they only add to. One that
	// fails routes.Rule.Check makes the lookup refuse.
	Routes []routes.Rule `json:"routes,omitempty"`
	// ThreatNotes are the operator's threat notes for the security review:
	// what must hold, the untrusted inputs and boundaries, and what is
	// accepted and why. They are the notes themselves, or the absolute
	// path, without whitespace, of a file outside the repository that holds
	// them; a file the repository holds makes the lookup refuse. Without
	// them the security checklist alone applies.
	ThreatNotes string `json:"threatNotes,omitempty"`

	file string
}

// File is the settings file s was read from, or would be read from.
func (s *Settings) File() string { return s.file }

// notesFile reports whether threat notes name a file: an absolute path
// with no whitespace. Notes holding any are the notes themselves.
func notesFile(notes string) bool {
	return strings.HasPrefix(notes, "/") && !strings.ContainsAny(notes, " \t\r\n")
}

// Notes are the threat notes s gives, read from their file where they
// name one; "" for none.
func (s *Settings) Notes() (string, error) {
	if !notesFile(s.ThreatNotes) {
		return s.ThreatNotes, nil
	}
	data, err := os.ReadFile(s.ThreatNotes)
	if err != nil {
		return "", refusal.New("the threat notes in %s name %s, which cannot be read (%v); ask the operator to "+
			"fix the path, or to put the notes inline", s.file, s.ThreatNotes, err)
	}
	return string(data), nil
}

// checkNotes refuses threat notes in a file the repository containing
// git.Dir holds: in one of its worktrees or its git directory, followed
// through symbolic links, where a change under review could write them.
func checkNotes(s *Settings) error {
	if !notesFile(s.ThreatNotes) {
		return nil
	}
	file := realPath(s.ThreatNotes, 0)
	var roots []string
	if common, err := git.Run("rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		roots = append(roots, common)
	}
	if list, err := git.Run("worktree", "list", "--porcelain"); err == nil {
		for _, line := range strings.Split(list, "\n") {
			if wt, ok := strings.CutPrefix(line, "worktree "); ok {
				roots = append(roots, wt)
			}
		}
	}
	for _, root := range roots {
		root = realPath(root, 0)
		if rel, err := filepath.Rel(root, file); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return refusal.New("the threat notes in %s are at %s, inside its repository at %s, where a change under "+
				"review could write them; ask the operator to keep them outside the repository, or inline", s.file,
				s.ThreatNotes, root)
		}
	}
	return nil
}

// realPath is path with its symbolic links followed, those of a file that
// does not exist yet included, as far as depth allows.
func realPath(path string, depth int) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	if target, err := os.Readlink(path); err == nil && depth < 40 {
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return realPath(target, depth+1)
	}
	dir := filepath.Dir(path)
	if dir == path {
		return path
	}
	return filepath.Join(realPath(dir, depth+1), filepath.Base(path))
}

// Path is the user's config file: agent-tools/config.json under the XDG
// config directory, or $AGENT_TOOLS_CONFIG where hostpaths allows it.
func Path() string {
	return hostpaths.ConfigFile("AGENT_TOOLS_CONFIG", filepath.Join("agent-tools", "config.json"))
}

// Remotes maps each remote of the repository containing git.Dir whose fetch
// URL is on GitHub to its owner/name.
func Remotes() (map[string]string, error) {
	out, err := git.Run("remote", "-v")
	if err != nil {
		return nil, err
	}
	remotes := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "(fetch)" {
			continue
		}
		if slug := gh.Slug(fields[1]); slug != "" {
			remotes[fields[0]] = slug
		}
	}
	return remotes, nil
}

// SettingsFile is the settings file of the clone containing git.Dir, or
// the process's directory where git.Dir is "": info/agents.json in its
// common git directory, as git finds it there with gitenv.Location, which
// GIT_DIR and GIT_COMMON_DIR are among, unset. Without git.Dir, the
// environment must agree with the worktree on its git directory
// (git.Toplevel).
func SettingsFile() (string, error) {
	dir := git.Dir
	if dir == "" {
		top, err := git.Toplevel()
		if err != nil {
			return "", err
		}
		dir = top
	}
	common, err := git.Run("-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "info", "agents.json"), nil
}

// Lookup returns the settings of the clone containing git.Dir, from the
// file SettingsFile names: empty where it does not exist. A file that is
// a symbolic link, not a regular file, writable by anyone but its owner,
// or not a JSON object of Settings' keys alone is refused, as is one whose
// test suites, route rules or threat notes cannot be used.
func Lookup() (*Settings, error) {
	file, err := SettingsFile()
	if err != nil {
		return nil, err
	}
	s := &Settings{file: file}
	data, err := readSettings(file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(s); err != nil {
		if suites := oneSuite(data); suites != "" {
			return nil, refusal.New("the agent settings %s hold test, build or testFiles, the keys of the one test suite "+
				"they held before they held a list; ask the operator to replace those keys with %s, NAME a name for the "+
				"suite", file, suites)
		}
		return nil, refusal.New("the agent settings %s do not read as a JSON object of setup, suites, routes and "+
			"threatNotes (%v); ask the operator to fix them", file, err)
	}
	if len(s.Suites) > 0 {
		if err := redcheck.CheckSuites(s.Suites); err != nil {
			return nil, refusal.New("the test suites in %s: %v; ask the operator to fix them", file, err)
		}
	}
	for _, r := range s.Routes {
		if err := r.Check(); err != nil {
			return nil, refusal.New("the routes in %s: %v", file, err)
		}
	}
	if err := checkNotes(s); err != nil {
		return nil, err
	}
	return s, nil
}

// oneSuite is the "suites" key that holds the one test suite whose keys,
// test, build and testFiles, the settings data hold at its top level, as
// the settings held it before they held a list; "" where they hold none of
// those keys.
func oneSuite(data []byte) string {
	var keys map[string]json.RawMessage
	if json.Unmarshal(data, &keys) != nil {
		return ""
	}
	_, test := keys["test"]
	_, build := keys["build"]
	_, files := keys["testFiles"]
	if !test && !build && !files {
		return ""
	}
	// A value of another type is left out: the operator rewrites it anyway.
	suite := redcheck.Suite{Name: "NAME"}
	_ = json.Unmarshal(keys["test"], &suite.Test)
	_ = json.Unmarshal(keys["build"], &suite.Build)
	_ = json.Unmarshal(keys["testFiles"], &suite.TestFiles)
	// Shell commands as written, not with &, < and > escaped.
	var out strings.Builder
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	_ = enc.Encode([]redcheck.Suite{suite})
	return `"suites": ` + strings.TrimSuffix(out.String(), "\n")
}

// readSettings reads the settings file, opened without following a link,
// refusing one that is not a regular file or that anyone but its owner may
// write. An error wrapping fs.ErrNotExist means there is none.
func readSettings(file string) ([]byte, error) {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, err
	case errors.Is(err, syscall.ELOOP):
		return nil, refusal.New("the agent settings %s are a symbolic link, which may lead anywhere; ask the operator to "+
			"put the file itself there", file)
	case err != nil:
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, refusal.New("the agent settings %s are not a regular file; ask the operator to put the file "+
			"itself there", file)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, refusal.New("the agent settings %s may be written by others than their owner (mode %04o); ask the "+
			"operator to run `chmod go-w %s`", file, info.Mode().Perm(), file)
	}
	return io.ReadAll(io.LimitReader(f, 1<<20))
}
