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
	"github.com/wesnel/agent-tools/internal/refusal"
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
}

// Path is the user's config file: $AGENT_TOOLS_CONFIG, or
// agent-tools/config.json under the XDG config directory.
func Path() string {
	if p := os.Getenv("AGENT_TOOLS_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agent-tools", "config.json")
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

// Lookup returns the settings for the repository containing git.Dir, matched
// by the GitHub repositories its remotes fetch from, without regard to case.
// The result is empty when none are configured. Remotes that match different
// settings are refused, since only one may apply.
func Lookup() (*Settings, error) {
	data, err := os.ReadFile(Path())
	if errors.Is(err, fs.ErrNotExist) {
		return &Settings{}, nil
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
	remotes, err := Remotes()
	if err != nil {
		return nil, err
	}
	var found []Settings
	for _, slug := range remotes {
		for repo, settings := range config.Repos {
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
