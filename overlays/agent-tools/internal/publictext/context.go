package publictext

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/repos"
)

// harnessRepos are the GitHub repositories, as owner/name, that the
// harness itself lives in: "publicText.harnessRepos" in the user's
// agent-tools config, which the Nix module writes. Never the repository's
// own say, so a product repository that mentions the harness's tools is
// still checked in full.
func harnessRepos() []string {
	data, err := os.ReadFile(repos.Path())
	if err != nil {
		return nil
	}
	var config struct {
		PublicText struct {
			HarnessRepos []string `json:"harnessRepos"`
		} `json:"publicText"`
	}
	if json.Unmarshal(data, &config) != nil {
		return nil
	}
	return config.PublicText.HarnessRepos
}

// isHarness reports whether a remote of the repository at top fetches from
// one of the harness's repositories.
func isHarness(top string) bool {
	listed := harnessRepos()
	if len(listed) == 0 {
		return false
	}
	out, err := git.Run("-C", top, "remote", "-v")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 || f[2] != "(fetch)" {
			continue
		}
		slug := gh.Slug(f[1])
		for _, r := range listed {
			if slug != "" && strings.EqualFold(slug, r) {
				return true
			}
		}
	}
	return false
}

// In is what this process knows of the harness behind a text written in
// dir, or in git's own directory where dir is "": whether the repository
// is the harness's own, the session, its plan's IDs and the local
// directories a reader cannot open. Threads are the caller's to add.
func In(dir string) Context {
	at := []string{}
	if dir != "" {
		at = []string{"-C", dir}
	}
	ctx := Context{}
	top, err := git.Run(append(at, "rev-parse", "--show-toplevel")...)
	if err == nil && top != "" {
		ctx.Harness = isHarness(top)
	}
	if s := plans.EnvSession(); s != "" {
		ctx.Sessions = append(ctx.Sessions, s)
	}
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 {
		ctx.Paths = append(ctx.Paths, home)
	}
	// TMPDIR as /tmp alone would hold every path under /tmp.
	if tmp := strings.TrimRight(os.Getenv("TMPDIR"), "/"); tmp != "" && tmp != "/tmp" {
		ctx.Paths = append(ctx.Paths, tmp)
	}
	if c, err := plans.Load(); err == nil {
		ctx.Paths = append(ctx.Paths, c.Dir)
		if p, _, ok := c.Locate(plans.EnvSession(), top); ok {
			ctx.PlanIDs, _ = p.IDs()
		}
	}
	for i, p := range ctx.Paths {
		ctx.Paths[i] = filepath.Clean(p)
	}
	return ctx
}
