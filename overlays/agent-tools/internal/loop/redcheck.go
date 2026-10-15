package loop

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// RedCheck is how `git red-check` tests a repository: the test command, an
// optional build command, and the git pathspec globs that pick out test files.
type RedCheck struct {
	Repo      string   `json:"repo,omitempty"`
	Test      string   `json:"test"`
	Build     string   `json:"build,omitempty"`
	TestFiles []string `json:"testFiles"`
}

// RedCheckConfig looks up the red-check settings for the repository
// containing the current directory, by the GitHub repositories its remotes
// fetch from. They come from the "redCheck" map in the user's config.json,
// keyed by owner/name, and never from the repository, which must not be able
// to choose what runs. The result is empty when none are configured.
func RedCheckConfig() (*RedCheck, error) {
	data, err := os.ReadFile(configPath())
	if errors.Is(err, fs.ErrNotExist) {
		return &RedCheck{}, nil
	}
	if err != nil {
		return nil, err
	}
	var config struct {
		RedCheck map[string]RedCheck `json:"redCheck"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	remotes, err := githubRemotes()
	if err != nil {
		return nil, err
	}
	found := map[string]RedCheck{}
	for _, slug := range remotes {
		for repo, settings := range config.RedCheck {
			if strings.EqualFold(repo, slug) {
				settings.Repo = repo
				found[repo] = settings
			}
		}
	}
	switch len(found) {
	case 0:
		return &RedCheck{}, nil
	case 1:
		for _, s := range found {
			return &s, nil
		}
	}
	repos := []string{}
	for repo := range found {
		repos = append(repos, repo)
	}
	sort.Strings(repos)
	return nil, refuse("this checkout's remotes match the red-check settings of %v, and only one may apply; "+
		"pass `--test` and `--test-files`, or ask Wesley to keep one", repos)
}
