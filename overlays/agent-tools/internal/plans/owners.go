package plans

import (
	"os"
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
	data, err := os.ReadFile(p.file(""))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if line == "* Plan" {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, refusal.New("%s has no `* Plan` heading. Restore it as the plan-work skill describes, then set the owner again", p.file(""))
	}
	owners := map[string]string{}
	var pairs []string
	at, end := -1, -1
	for i := start + 1; i < len(lines) && end < 0; i++ {
		switch {
		case lines[i] == ":END:":
			end = i
		case strings.HasPrefix(lines[i], ":OWNERS:"):
			at = i
			for _, pair := range strings.Fields(strings.TrimPrefix(lines[i], ":OWNERS:")) {
				if wt, a, ok := strings.Cut(pair, "="); ok {
					pairs = append(pairs, pair)
					owners[wt] = a
				}
			}
		}
	}
	if end < 0 {
		return nil, refusal.New("%s's `* Plan` heading has no property drawer. Restore it as the plan-work skill describes, then set the owner again", p.file(""))
	}
	pair := worktree + "=" + agent
	if _, had := owners[worktree]; had {
		for i := range pairs {
			if strings.HasPrefix(pairs[i], worktree+"=") {
				pairs[i] = pair
			}
		}
	} else {
		pairs = append(pairs, pair)
	}
	owners[worktree] = agent
	line := ":OWNERS: " + strings.Join(pairs, " ")
	if at >= 0 {
		lines[at] = line
	} else {
		lines = append(lines[:end], append([]string{line}, lines[end:]...)...)
	}
	return owners, os.WriteFile(p.file(""), []byte(strings.Join(lines, "\n")), 0o644)
}
