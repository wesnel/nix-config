package review

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/repos"
	"github.com/wesnel/agent-tools/internal/routes"
)

// routeChange routes the change a general review of r reviewed to the
// focused reviews it needs, into out.Routes: by the content scan of every
// commit from where the change starts, by the built-in rules and the
// operator's, and by the routes the reviewer named. It says on log how to
// run each. Each source only adds.
func routeChange(r *span, out *Output, log io.Writer) {
	rules := slices.Clone(routes.Builtin)
	git.Dir = r.top
	settings, err := repos.Lookup()
	git.Dir = ""
	if err != nil {
		fmt.Fprintf(log, "%s: the operator's route rules are left out: %v\n", name, err)
	} else {
		rules = append(rules, settings.Routes...)
	}
	base := routes.MergeBase(r.top, r.tip)
	if base == "" {
		fmt.Fprintf(log, "%s: this repository has no default branch to measure the change from, so only %s is scanned for "+
			"the reviews it needs; fetch origin's default branch to route the whole of it\n", name, r.given)
		base = r.base
	}
	found := map[string]*routes.Route{}
	add := func(route, why string, commits []string) {
		rt := found[route]
		if rt == nil {
			rt = &routes.Route{Route: route}
			found[route] = rt
		}
		if !slices.Contains(rt.Why, why) {
			rt.Why = append(rt.Why, why)
		}
		for _, c := range commits {
			if !slices.Contains(rt.Commits, c) {
				rt.Commits = append(rt.Commits, c)
			}
		}
	}
	scanned, err := routes.Scan(r.top, base, r.tip, rules)
	if err != nil {
		fmt.Fprintf(log, "%s: the change's commits could not be scanned for the reviews they need: %v\n", name, err)
	}
	for _, rt := range scanned {
		for _, why := range rt.Why {
			add(rt.Route, why, rt.Commits)
		}
	}
	reviewed, _ := git.Run("-C", r.top, "rev-list", "--reverse", "--no-merges", r.base+".."+r.tip)
	for _, p := range out.proposed {
		if !routes.IsKnown(p.Route) {
			fmt.Fprintf(log, "%s: the reviewer named the route %q, which no focused review exists for; it is left out\n", name, p.Route)
			continue
		}
		add(p.Route, "the reviewer: "+strings.TrimSpace(p.Why), touching(r, reviewed, p.Paths))
	}
	all, _ := git.Run("-C", r.top, "rev-list", "--reverse", "--no-merges", base+".."+r.tip)
	order := strings.Fields(all)
	out.Routes = nil
	for _, route := range slices.Sorted(maps.Keys(found)) {
		rt := found[route]
		slices.SortStableFunc(rt.Commits, func(a, b string) int { return slices.Index(order, a) - slices.Index(order, b) })
		out.Routes = append(out.Routes, *rt)
	}
	for _, rt := range out.Routes {
		fmt.Fprintf(log, "%s: this change needs a %s review (%s): run `git fresh-review --focus %s --goal FILE %s` "+
			"as the review-change skill says\n", name, rt.Route, strings.Join(rt.Why, "; "), rt.Route, r.given)
	}
}

// touching are the commits of reviewed that change one of paths, or a
// file below one; all of reviewed where none does, since the reviewer saw
// only the range it was given.
func touching(r *span, reviewed string, paths []string) []string {
	var hits []string
	for _, c := range strings.Fields(reviewed) {
		names, err := git.Run("-C", r.top, "diff-tree", "-r", "--name-only", "--no-commit-id", "--no-renames", "--root", c)
		if err != nil {
			continue
		}
		for _, f := range strings.Split(names, "\n") {
			if slices.ContainsFunc(paths, func(p string) bool {
				p = strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")
				return p != "" && (f == p || strings.HasPrefix(f, p+"/"))
			}) {
				hits = append(hits, c)
				break
			}
		}
	}
	if len(hits) == 0 {
		return strings.Fields(reviewed)
	}
	return hits
}
