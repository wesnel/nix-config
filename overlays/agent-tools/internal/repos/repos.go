// Package repos looks up the user's settings for the GitHub repository a
// checkout belongs to. They come from the user's agent-tools/config.json,
// and never from the repository, which must not be able to choose what runs.
package repos

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// Settings are how agents prepare, build and test one repository.
type Settings struct {
	// Repo is the owner/name the settings are keyed by; empty when none apply.
	Repo string `json:"repo,omitempty"`
	// Setup prepares a fresh checkout, such as generating files the
	// repository does not commit.
	Setup string `json:"setup,omitempty"`
	// Test is the test command, run with sh at the top of a checkout.
	Test string `json:"test,omitempty"`
	// Build builds the tests without running them.
	Build string `json:"build,omitempty"`
	// TestFiles are git pathspec globs (`:(glob)` magic) for test files.
	TestFiles []string `json:"testFiles,omitempty"`
	// ModuleDir is the directory of the Go module the review VM downloads
	// modules for, relative to the top of a checkout and inside it: "."
	// for the top. The commands above still run at the top.
	ModuleDir string `json:"moduleDir,omitempty"`
	// Routes are rules that route this repository's changes to focused
	// reviews beside the built-in ones, which they only add to.
	Routes []routes.Rule `json:"routes,omitempty"`
	// ThreatNotes are the operator's threat notes for the security review:
	// the notes themselves, or the absolute path, without whitespace, of a
	// file outside the repository that holds them.
	ThreatNotes string `json:"threatNotes,omitempty"`
}

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
		return "", refusal.New("the threat notes of %s in %s name %s, which cannot be read (%v); ask the operator to "+
			"fix the path, or to put the notes inline", s.Repo, Path(), s.ThreatNotes, err)
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
			return refusal.New("the threat notes of %s in %s are at %s, inside its repository at %s, where a change under "+
				"review could write them; ask the operator to keep them outside the repository, or inline", s.Repo, Path(),
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
	return remotes(nil)
}

// repositoryConfig has git read only the repository's own configuration:
// not the user's, the system's, or what `git -c` passes down, any of which
// could rewrite a remote's URL with url.*.insteadOf.
var repositoryConfig = []string{
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null",
	"GIT_CONFIG_COUNT=0", "GIT_CONFIG_PARAMETERS=",
}

func remotes(env []string) (map[string]string, error) {
	out, err := git.RunEnv(env, "remote", "-v")
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

// Lookup returns the settings for the repository containing git.Dir, matched
// by the GitHub repositories its remotes fetch from, as the repository's own
// configuration names them, without regard to case. The result is empty
// when none are configured. Remotes that match different settings are
// refused, since only one may apply.
func Lookup() (*Settings, error) {
	config, err := load()
	if err != nil || config == nil {
		return &Settings{}, err
	}
	remotes, err := remotes(repositoryConfig)
	if err != nil {
		return nil, err
	}
	var slugs []string
	for _, slug := range remotes {
		slugs = append(slugs, slug)
	}
	s, err := match(config, slugs)
	if err != nil {
		return nil, err
	}
	if err := checkNotes(s); err != nil {
		return nil, err
	}
	return s, nil
}

// For returns the settings for the GitHub repository slug, owner/name,
// matched without regard to case; empty when none are configured.
func For(slug string) (*Settings, error) {
	config, err := load()
	if err != nil || config == nil {
		return &Settings{}, err
	}
	return match(config, []string{slug})
}

// load reads the config's repositories; nil when there is no config.
func load() (map[string]Settings, error) {
	data, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config struct {
		Repos map[string]Settings `json:"repos"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	for repo, s := range config.Repos {
		dir := filepath.ToSlash(filepath.Clean(s.ModuleDir))
		if filepath.IsAbs(s.ModuleDir) || dir == ".." || strings.HasPrefix(dir, "../") {
			return nil, refusal.New("the moduleDir %q of %s in %s is not inside the checkout; "+
				"ask the operator to make it relative to the top of the repository", s.ModuleDir, repo, Path())
		}
		s.ModuleDir = dir
		for _, r := range s.Routes {
			if err := r.Check(); err != nil {
				return nil, refusal.New("%s's routes in %s: %v", repo, Path(), err)
			}
		}
		config.Repos[repo] = s
	}
	return config.Repos, nil
}

func match(config map[string]Settings, slugs []string) (*Settings, error) {
	var found []Settings
	for _, slug := range slugs {
		for repo, settings := range config {
			if !strings.EqualFold(repo, slug) {
				continue
			}
			settings.Repo = repo
			if !contains(found, settings) {
				found = append(found, settings)
			}
		}
	}
	switch len(found) {
	case 0:
		return &Settings{}, nil
	case 1:
		return &found[0], nil
	}
	names := []string{}
	for _, s := range found {
		names = append(names, s.Repo)
	}
	sort.Strings(names)
	return nil, refusal.New("this checkout's remotes match the settings of %v in %s, and only one may apply; "+
		"ask the operator to keep one of them", names, Path())
}

func contains(found []Settings, s Settings) bool {
	for _, f := range found {
		if reflect.DeepEqual(f, s) {
			return true
		}
	}
	return false
}
