package signpass

import (
	"fmt"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/history"
	"github.com/wesnel/agent-tools/internal/plans"
)

// recordCommitRewrites runs after verification and before pending state is
// cleared. finish only warns when it fails, and it records nothing when the
// commits before and after do not pair up.
func recordCommitRewrites(p *pending) error {
	c, err := plans.Load()
	if err != nil {
		return err
	}
	all, err := c.Commits(plans.CommitOptions{})
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return nil
	}
	identity, err := plans.CommitIdentity(p.Dir, p.Before[p.Branch])
	if err != nil {
		return nil
	}
	from := p.Start
	if p.Fork != "" {
		from = p.Fork
	}
	old, err := git.Run("rev-list", "--reverse", from+".."+p.Before[p.Branch])
	if err != nil {
		return err
	}
	oldIDs := strings.Fields(old)
	new, err := git.Run("rev-list", "--reverse", p.Start+"..refs/heads/"+p.Branch)
	if err != nil {
		return err
	}
	newIDs := strings.Fields(new)
	groups := make([]int, len(oldIDs))
	for i := range groups {
		groups[i] = i
	}
	if p.Fixups {
		commits, err := history.Between(from, p.Before[p.Branch])
		if err != nil {
			return err
		}
		groups = history.FoldGroups(commits)
	}
	if len(oldIDs) != len(groups) {
		return fmt.Errorf("cannot map signing rewrite: original commit count differs")
	}
	rewrites := map[string]string{}
	for i, old := range oldIDs {
		if groups[i] >= len(newIDs) {
			return fmt.Errorf("cannot map signing rewrite: surviving commit count differs")
		}
		rewrites[old] = newIDs[groups[i]]
	}
	return c.RecordRewrites(plans.EnvSession(), p.Dir, identity.Repository, rewrites, time.Now())
}
