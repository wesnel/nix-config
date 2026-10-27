package plans

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

var revisionPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// CommitIdentity resolves ref in top and uses its GitHub origin as a portable identity.
func CommitIdentity(top, ref string) (Event, error) {
	url, err := git.Run("-C", top, "remote", "get-url", "origin")
	if err != nil {
		return Event{}, err
	}
	repository := gh.Slug(url)
	if !repositoryPattern.MatchString(repository) {
		return Event{}, refusal.New("origin has no portable owner/name identity")
	}
	revision, err := git.Run("-C", top, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return Event{}, err
	}
	if !revisionPattern.MatchString(revision) {
		return Event{}, refusal.New("git returned an invalid full revision")
	}
	subject, err := git.Run("-C", top, "show", "-s", "--format=%s", revision)
	return Event{Repository: repository, Revision: revision, Commit: subject, Worktree: top}, err
}

// CommitOptions selects exact associations; Item is local to Plan.
type CommitOptions struct{ Plan, Repository, Revision, Item string }

// CommitAssociation is a recorded identity and the commit it resolves to now.
type CommitAssociation struct {
	Plan             string `json:"plan"`
	File             string `json:"file"`
	Item             string `json:"item"`
	Repository       string `json:"repository"`
	Revision         string `json:"revision"`
	OriginalRevision string `json:"original_revision"`
	Subject          string `json:"subject"`
}

// CommitResolution follows aliases even when no plan associates the revision.
type CommitResolution struct {
	Repository       string `json:"repository"`
	OriginalRevision string `json:"original_revision"`
	Revision         string `json:"revision"`
}

func (c Config) ResolveCommit(repository, revision string) (CommitResolution, error) {
	if !repositoryPattern.MatchString(repository) || !revisionPattern.MatchString(revision) {
		return CommitResolution{}, refusal.New("resolve-commit needs --repo owner/name and --revision FULL-OID")
	}
	ps, _, err := c.Plans()
	if err != nil {
		return CommitResolution{}, err
	}
	a, err := commitAliases(ps)
	if err != nil {
		return CommitResolution{}, err
	}
	return CommitResolution{Repository: repository, OriginalRevision: revision, Revision: a.resolve(repository, revision)}, nil
}

// Commits reads every host's logs. Legacy subject-only events are unresolved.
func (c Config) Commits(o CommitOptions) ([]CommitAssociation, error) {
	if o.Item != "" && o.Plan == "" {
		return nil, refusal.New("--item requires PLAN")
	}
	if o.Repository != "" && !repositoryPattern.MatchString(o.Repository) {
		return nil, refusal.New("--repo needs owner/name")
	}
	if o.Revision != "" && !revisionPattern.MatchString(o.Revision) {
		return nil, refusal.New("--revision needs a full object ID")
	}
	plans, _, err := c.Plans()
	if err != nil {
		return nil, err
	}
	aliases, err := commitAliases(plans)
	if err != nil {
		return nil, err
	}
	selected := ""
	if o.Plan != "" {
		p, err := c.Open(o.Plan)
		if err != nil {
			return nil, err
		}
		selected = p.Name
	}
	out := []CommitAssociation{}
	seen := map[string]bool{}
	for _, p := range plans {
		if selected != "" && selected != p.Name {
			continue
		}
		events, err := p.Events()
		if err != nil {
			return nil, err
		}
		items, err := p.items()
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if e.Revision == "" || e.OldRevision != "" || o.Repository != "" && e.Repository != o.Repository || o.Item != "" && e.Item != o.Item {
				continue
			}
			if !revisionPattern.MatchString(e.Revision) || !repositoryPattern.MatchString(e.Repository) {
				return nil, fmt.Errorf("invalid commit identity in %s", p.log())
			}
			resolved := aliases.resolve(e.Repository, e.Revision)
			if o.Revision != "" && aliases.resolve(e.Repository, o.Revision) != resolved {
				continue
			}
			it, ok := items[e.Item]
			if !ok {
				return nil, fmt.Errorf("commit association names missing item %s in %s", e.Item, p.File())
			}
			key := p.Name + "\x00" + e.Item + "\x00" + e.Repository + "\x00" + e.Revision
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, CommitAssociation{Plan: p.Name, File: it.File, Item: e.Item, Repository: e.Repository, Revision: resolved, OriginalRevision: e.Revision, Subject: e.Commit})
		}
	}
	return out, nil
}

type aliases map[string]map[string]string

func commitAliases(plans []Plan) (aliases, error) {
	a := aliases{}
	for _, p := range plans {
		events, err := p.Events()
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if e.Kind != "commit-rewritten" {
				continue
			}
			if !repositoryPattern.MatchString(e.Repository) || !revisionPattern.MatchString(e.Revision) || !revisionPattern.MatchString(e.OldRevision) {
				return nil, fmt.Errorf("invalid rewrite identity in %s", p.log())
			}
			if err := a.add(e.Repository, e.OldRevision, e.Revision); err != nil {
				return nil, err
			}
		}
	}
	return a, nil
}

func (a aliases) add(repository, old, revision string) error {
	if a[repository] == nil {
		a[repository] = map[string]string{}
	}
	if previous := a[repository][old]; previous != "" && previous != revision {
		return refusal.New("conflicting rewrites of %s in %s", old, repository)
	}
	a[repository][old] = revision
	seen := map[string]bool{}
	for at := old; at != ""; at = a[repository][at] {
		if seen[at] {
			return refusal.New("cyclic commit rewrites in %s", repository)
		}
		seen[at] = true
	}
	return nil
}

func (a aliases) resolve(repository, revision string) string {
	for a[repository][revision] != "" {
		revision = a[repository][revision]
	}
	return revision
}

// RecordRewrites appends aliases to this host's located plan, once each.
// Readable plans of every host share the aliases; mirrored logs are never edited.
func (c Config) RecordRewrites(session, top, repository string, rewrites map[string]string, now time.Time) error {
	if !repositoryPattern.MatchString(repository) {
		return refusal.New("rewrites need a portable owner/name repository")
	}
	all, err := c.Commits(CommitOptions{Repository: repository})
	if err != nil {
		return err
	}
	ps, _, err := c.Plans()
	if err != nil {
		return err
	}
	a, err := commitAliases(ps)
	if err != nil {
		return err
	}
	affected := false
	for _, association := range all {
		for at := association.OriginalRevision; at != ""; at = a[repository][at] {
			if rewrites[at] != "" {
				affected = true
				break
			}
		}
	}
	if !affected {
		return nil
	}
	var old []string
	for from, to := range rewrites {
		if !revisionPattern.MatchString(from) || !revisionPattern.MatchString(to) {
			return refusal.New("rewrites need full object IDs")
		}
		if from == to {
			continue
		}
		if err := a.add(repository, from, to); err != nil {
			return err
		}
		old = append(old, from)
	}
	if len(old) == 0 {
		return nil
	}
	sort.Strings(old)
	p, part, ok := c.Locate(session, top)
	if !ok {
		var err error
		p, part, _, err = c.unplanned(session, top, now)
		if err != nil {
			return err
		}
	}
	return p.withLock(func(l *lockedPlan) error {
		events, err := p.Events()
		if err != nil {
			return err
		}
		kept := map[string]string{}
		for _, e := range events {
			if e.Kind == "commit-rewritten" && e.Repository == repository {
				kept[e.OldRevision] = e.Revision
			}
		}
		item := part
		if item == "" {
			item = p.Name
		}
		for _, from := range old {
			if kept[from] == rewrites[from] {
				continue
			}
			if _, err := l.log(Event{Kind: "commit-rewritten", Item: item, Repository: repository, OldRevision: from, Revision: rewrites[from], Worktree: top}, now); err != nil {
				return err
			}
		}
		return nil
	})
}
