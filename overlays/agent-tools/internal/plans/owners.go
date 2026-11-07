package plans

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

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
	pairs, err := c.ownerPairs()
	if err != nil {
		return nil, err
	}
	owners := map[string]string{}
	for _, pair := range pairs {
		if _, taken := owners[pair.worktree]; !taken {
			owners[pair.worktree] = pair.agent
		}
	}
	return owners, nil
}

// ownerPair is one WORKTREE=AGENT pair of a plan's OWNERS, as written.
type ownerPair struct{ worktree, agent string }

// ownerPairs are the OWNERS pairs of this host's plans, newest plan first.
func (c Config) ownerPairs() ([]ownerPair, error) {
	own, err := c.ownPlans()
	if err != nil {
		return nil, err
	}
	var pairs []ownerPair
	for _, p := range own {
		items, err := p.items()
		if err != nil {
			continue
		}
		for _, pair := range strings.Fields(items[p.Name].Props["OWNERS"]) {
			if wt, agent, ok := strings.Cut(pair, "="); ok {
				pairs = append(pairs, ownerPair{wt, agent})
			}
		}
	}
	return pairs, nil
}

// SessionWorktrees are the worktrees the session, HARNESS:ID, may treat as
// its own: those in WORKTREES (not OWNERS) of this host's plan files,
// parts' included, that name the session in SESSIONS, newest plan first and
// each once, kept where
//   - the newest plan file naming the worktree, as Find with Worktree
//     decides and the guard asks it, names the session; a worktree Find
//     cannot match by that spelling is left out;
//   - the owner of the newest OWNERS pair naming the same file, by any
//     spelling, is MainAgent or there is none; a path that does not exist
//     is matched by its cleaned spelling.
//
// Each is cleaned, its symlinks resolved where it exists. It reads the
// plan files alone: it takes no lock and runs no git, so it never waits on
// the plans' remote or a sync. A main checkout a plan names is among them.
func (c Config) SessionWorktrees(session string) ([]string, error) {
	if harness, id, ok := strings.Cut(session, ":"); !ok || harness == "" || id == "" {
		return nil, refusal.New("worktrees needs the session as HARNESS:ID, not %q. Pass `--session HARNESS:ID`, such as `--session claude:$CLAUDE_CODE_SESSION_ID`", session)
	}
	found, err := c.Find(FindOptions{Session: session})
	if err != nil {
		return nil, err
	}
	pairs, err := c.ownerPairs()
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, f := range found {
		props, err := planHeading(f.File)
		if err != nil {
			continue
		}
		for _, v := range strings.Fields(props["WORKTREES"]) {
			wt := resolved(v)
			if slices.Contains(out, wt) {
				continue
			}
			named, err := c.Find(FindOptions{Session: session, Worktree: wt})
			if err != nil {
				return nil, err
			}
			newest := slices.IndexFunc(named, func(f Found) bool { return slices.Contains(f.Matched, "worktree") })
			if newest < 0 || !slices.Contains(named[newest].Matched, "session") {
				continue
			}
			owner := slices.IndexFunc(pairs, func(p ownerPair) bool { return sameFile(p.worktree, wt) })
			if owner >= 0 && pairs[owner].agent != MainAgent {
				continue
			}
			out = append(out, wt)
		}
	}
	return out, nil
}

// sameFile reports whether a and b name one file, by any spelling: by
// file identity where both exist, else by their cleaned spellings.
func sameFile(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	if errA == nil && errB == nil {
		return os.SameFile(ai, bi)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}

// resolved is path cleaned, with its symlinks resolved where it exists.
func resolved(path string) string {
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// SetOwner records agent, a harness's agent ID or MainAgent, as the owner
// of worktree in the plan's OWNERS, replacing its earlier owner there, logs
// OWNERS as tracked properties are, and returns the plan's owners.
func (p Plan) SetOwner(worktree, agent string, now time.Time) (map[string]string, error) {
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
	err := p.withLock(func(l *lockedPlan) error {
		return l.setPlanProperty("", "OWNERS", func(values []string) []string {
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
		}, "", now)
	})
	return owners, err
}
