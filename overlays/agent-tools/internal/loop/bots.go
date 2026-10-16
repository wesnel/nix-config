package loop

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// Bot is a review bot the loop requests, waits for and answers.
type Bot struct {
	Name    string
	Pattern *regexp.Regexp
	Request struct {
		// Reviewer is requested as a pull request reviewer, e.g. "@copilot".
		Reviewer string `json:"reviewer"`
		// Comment is posted as a top-level trigger comment instead, e.g.
		// "/agentic_review". The loop deletes it once superseded.
		Comment string `json:"comment"`
	}
	// Verdicts maps a verdict name to text that appears in the bot's review body.
	Verdicts map[string]string
	// ResolvesOwnThreads: the bot resolves its threads itself when it
	// re-reviews, so the loop leaves fixed ones to it.
	ResolvesOwnThreads bool
	// Repos limits the bot to owner/name globs.
	Repos []string
}

type botConfig struct {
	Login   string `json:"login"`
	Request struct {
		Reviewer string `json:"reviewer"`
		Comment  string `json:"comment"`
	} `json:"request"`
	Verdicts           map[string]string `json:"verdicts"`
	ResolvesOwnThreads bool              `json:"resolvesOwnThreads"`
	Repos              []string          `json:"repos"`
}

// ConfigPath is pr-loop's config: pr-loop/config.json under the XDG config
// directory, or $PR_LOOP_CONFIG where hostpaths allows it.
func ConfigPath() string {
	return hostpaths.ConfigFile("PR_LOOP_CONFIG", filepath.Join("pr-loop", "config.json"))
}

// botsFor returns the configured bots that apply to a repository, by name.
func botsFor(slug string) ([]*Bot, error) {
	p := ConfigPath()
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, refuse("no review bots are configured, which takes %s; ask the operator to configure them", p)
	}
	if err != nil {
		return nil, err
	}
	var config struct {
		Bots map[string]botConfig `json:"bots"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(config.Bots))
	for name := range config.Bots {
		names = append(names, name)
	}
	sort.Strings(names)
	var bots []*Bot
	for _, name := range names {
		c := config.Bots[name]
		pattern, err := regexp.Compile("(?i)" + c.Login)
		if err != nil {
			return nil, err
		}
		if (c.Request.Reviewer == "") == (c.Request.Comment == "") {
			return nil, refuse("bot %s in %s needs exactly one of request.reviewer and request.comment; ask the operator to fix it", name, p)
		}
		if !matchesAny(slug, c.Repos) {
			continue
		}
		bot := &Bot{Name: name, Pattern: pattern, Verdicts: c.Verdicts, ResolvesOwnThreads: c.ResolvesOwnThreads, Repos: c.Repos}
		bot.Request.Reviewer, bot.Request.Comment = c.Request.Reviewer, c.Request.Comment
		bots = append(bots, bot)
	}
	return bots, nil
}

func matchesAny(slug string, globs []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		if ok, _ := path.Match(g, slug); ok {
			return true
		}
	}
	return false
}

func botNamed(bots []*Bot, login string) *Bot {
	if login == "" {
		return nil
	}
	for _, b := range bots {
		if b.Pattern.MatchString(login) {
			return b
		}
	}
	return nil
}
