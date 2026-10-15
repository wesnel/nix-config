package plans

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// MainAgent is the owner that names a session's main thread, which
// harnesses give no agent ID.
const MainAgent = "main"

var agentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// Owners maps each worktree a plan of this host gives an owner to the agent
// that owns it. A plan records them in its top heading's OWNERS property as
// WORKTREE=AGENT pairs; a newer plan's owner wins.
func (c Config) Owners() (map[string]string, error) {
	own, err := c.ownPlans()
	if err != nil {
		return nil, err
	}
	owners := map[string]string{}
	for _, p := range own {
		items, err := p.items()
		if err != nil {
			continue
		}
		for _, pair := range strings.Fields(items[p.Name].Props["OWNERS"]) {
			if wt, agent, ok := strings.Cut(pair, "="); ok {
				if _, taken := owners[wt]; !taken {
					owners[wt] = agent
				}
			}
		}
	}
	return owners, nil
}

// SetOwner records agent, a harness's agent ID or MainAgent, as the owner
// of worktree in the plan's OWNERS, replacing its earlier owner there, and
// returns the plan's owners.
func (p Plan) SetOwner(worktree, agent string) (map[string]string, error) {
	if err := p.checkOwn(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(worktree) {
		return nil, refusal.New("%q is not an absolute path. Pass the worktree's top level, as `git rev-parse --show-toplevel` prints it", worktree)
	}
	if !agentID.MatchString(agent) {
		return nil, refusal.New("%q is not an agent ID. Pass the ID the harness gave the sub-agent, or `%s` for the main thread", agent, MainAgent)
	}
	worktree = filepath.Clean(worktree)
	if real, err := filepath.EvalSymlinks(worktree); err == nil {
		worktree = real
	}
	var owners map[string]string
	err := p.editPlanProperty(p.file(""), "OWNERS", func(values []string) []string {
		owners = map[string]string{}
		pair := worktree + "=" + agent
		var pairs []string
		for _, v := range values {
			wt, a, ok := strings.Cut(v, "=")
			switch {
			case !ok:
				continue
			case wt == worktree:
				v, a, pair = pair, agent, ""
			}
			pairs = append(pairs, v)
			owners[wt] = a
		}
		if pair != "" {
			pairs = append(pairs, pair)
			owners[worktree] = agent
		}
		return pairs
	})
	return owners, err
}
