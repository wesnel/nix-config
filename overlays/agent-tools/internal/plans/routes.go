package plans

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
	"github.com/wesnel/agent-tools/internal/routes"
)

// The kinds of the events that route a change to focused reviews and
// record them. Each is stored in plan logs, so renaming one means
// rewriting every log that holds it.
const (
	routedKind       = "routed"
	routeReceiptKind = "route-receipt"
	issuedKind       = "review-issued"
	waivedKind       = "waived"
)

// RouteReceiptKind is the kind of the event Receipt logs for a focused
// review.
const RouteReceiptKind = routeReceiptKind

// Routed is what a fresh review found a change needs beyond it: the
// focused reviews, each with the commits that need it.
type Routed struct {
	// Branch is the branch checked out where the review ran.
	Branch string
	// Base is the merge base the routing started from, and Tip the commit
	// it ended at.
	Base, Tip string
	// Routes maps each route to the commits that need it, oldest first.
	Routes map[string][]string
}

// encodeRoutes is m as a routed event holds it: `route:commit,commit`,
// separated by spaces, routes in order.
func encodeRoutes(m map[string][]string) string {
	var out []string
	for _, r := range slices.Sorted(maps.Keys(m)) {
		out = append(out, r+":"+strings.Join(m[r], ","))
	}
	return strings.Join(out, " ")
}

func decodeRoutes(s string) map[string][]string {
	out := map[string][]string{}
	for _, f := range strings.Fields(s) {
		route, commits, _ := strings.Cut(f, ":")
		out[route] = nil
		for _, c := range strings.Split(commits, ",") {
			if c != "" {
				out[route] = append(out[route], c)
			}
		}
	}
	return out
}

// Routed logs r about part, or about the plan itself when part is "".
func (p Plan) Routed(part string, r Routed, now time.Time) (Event, error) {
	return p.Record(part, Event{Kind: routedKind, Branch: r.Branch, Range: r.Base + ".." + r.Tip, Tip: r.Tip,
		Routes: encodeRoutes(r.Routes), Summary: "routes " + strings.Join(slices.Sorted(maps.Keys(r.Routes)), " ")}, now)
}

// WriteRouted logs r in the plan Locate finds for session and the worktree
// at top, and returns that plan's file, or "" where no plan names either.
func WriteRouted(session, top string, r Routed, now time.Time) (string, error) {
	c, err := Load()
	if err != nil {
		return "", err
	}
	p, part, ok := c.Locate(session, top)
	if !ok {
		return "", nil
	}
	if _, err := p.Routed(part, r, now); err != nil {
		return "", err
	}
	return p.File(), nil
}

// Waive logs that the plan's change needs no review of route, and why.
// The waiver stands for the plan's every branch, and the gates and the
// signing pass show it.
func (p Plan) Waive(route, why string, now time.Time) (Event, error) {
	why = strings.TrimSpace(why)
	switch {
	case !routes.IsKnown(route):
		return Event{}, refusal.New("%q is not a review route. Name one of `%s`", route, strings.Join(routes.Known, "`, `"))
	case why == "":
		return Event{}, refusal.New("a waiver needs its reason. Pass it as `--why TEXT`, for the operator to read before signing")
	}
	return p.Record("", Event{Kind: waivedKind, Focus: route, Evidence: why, Summary: route + ": " + why}, now)
}

// Waiver is a route waived, why, and when.
type Waiver struct {
	Route string `json:"route"`
	Why   string `json:"why"`
	Time  string `json:"time"`
}

func waivers(events []Event) []Waiver {
	out := []Waiver{}
	for _, e := range events {
		if e.Kind == waivedKind {
			out = append(out, Waiver{Route: e.Focus, Why: e.Evidence, Time: e.Time})
		}
	}
	return out
}

// loggedRoutes are the routes the log names: its routed events', and every
// value REVIEWS was ever given.
func loggedRoutes(events []Event) []string {
	seen := map[string]bool{}
	for _, e := range events {
		switch {
		case e.Kind == routedKind:
			for r := range decodeRoutes(e.Routes) {
				seen[r] = true
			}
		case e.Kind == propertyKind && e.Property == "REVIEWS":
			for _, r := range strings.Fields(e.Value) {
				seen[r] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// RouteDue is a route a change still owes: the commits that need it and no
// route receipt covers, and the findings of the receipts that cover the
// rest with no finding-settled event, as ROUND.ID.
type RouteDue struct {
	Route     string   `json:"route"`
	Uncovered []string `json:"uncovered"`
	Unsettled []string `json:"unsettled"`
}

// RouteState is what a plan's change owes its review routes, its waivers,
// and its PIPELINE as the log gives it.
type RouteState struct {
	Pipeline string     `json:"pipeline"`
	Due      []RouteDue `json:"due"`
	Waivers  []Waiver   `json:"waivers"`
}

// RouteState is what the change on branch, at tip in the repository at
// top, owes the routes p logs: each route its routed events name for
// commits between the default branch's merge base and tip, or for branch
// where those commits have since been rewritten (not merged, and routed in
// this repository), and each REVIEWS ever named, for every commit there;
// less those waived. A commit is covered by a route receipt of that route,
// in any of this host's plans, whose range, read again now at tip, at its
// branch, at its range's TIP or at the tip it recorded, has the patch ID it
// recorded; only p's receipts made for branch hold findings it owes. A
// plan opted out of the pipeline owes nothing.
func (c Config) RouteState(p Plan, top, branch, tip string) (RouteState, error) {
	events, err := p.Events()
	if err != nil {
		return RouteState{}, err
	}
	s := RouteState{Pipeline: p.pipeline(events), Due: []RouteDue{}, Waivers: waivers(events)}
	if strings.HasPrefix(s.Pipeline, "opted-out") || top == "" {
		return s, nil
	}
	base := routes.MergeBase(top, tip)
	if base == "" {
		// No default branch to measure from: the latest routing of this
		// branch says where its change starts.
		for i := len(events) - 1; i >= 0 && base == ""; i-- {
			if b, _, ok := strings.Cut(events[i].Range, ".."); ok && events[i].Kind == routedKind && events[i].Branch == branch &&
				git.OK("-C", top, "merge-base", "--is-ancestor", b, tip) {
				base = b
			}
		}
	}
	if base == "" {
		return s, nil
	}
	out, err := git.Run("-C", top, "rev-list", "--reverse", "--no-merges", base+".."+tip)
	if err != nil {
		return RouteState{}, err
	}
	commits := strings.Fields(out)
	need := map[string]map[string]bool{}
	add := func(route string, cs []string) {
		if need[route] == nil {
			need[route] = map[string]bool{}
		}
		for _, c := range cs {
			need[route][c] = true
		}
	}
	for _, e := range events {
		switch {
		case e.Kind == routedKind:
			for route, routed := range decodeRoutes(e.Routes) {
				var hits []string
				for _, c := range routed {
					if slices.Contains(commits, c) {
						hits = append(hits, c)
					}
				}
				if len(hits) == 0 && e.Branch == branch && rewritten(top, base, e.Range, routed) {
					hits = commits
				}
				add(route, hits)
			}
		case e.Kind == propertyKind && e.Property == "REVIEWS":
			for _, route := range strings.Fields(e.Value) {
				add(route, commits)
			}
		}
	}
	waived := map[string]bool{}
	for _, w := range s.Waivers {
		waived[w.Route] = true
	}
	for _, route := range slices.Sorted(maps.Keys(need)) {
		if waived[route] || len(need[route]) == 0 {
			continue
		}
		covered, unsettled := c.covered(p, top, route, branch, tip, need[route])
		d := RouteDue{Route: route, Uncovered: []string{}, Unsettled: unsettled}
		for _, commit := range commits {
			if need[route][commit] && !covered[commit] {
				d.Uncovered = append(d.Uncovered, commit)
			}
		}
		if len(d.Uncovered) > 0 || len(d.Unsettled) > 0 {
			s.Due = append(s.Due, d)
		}
	}
	return s, nil
}

// covered is the commits the route receipts of route, in this host's
// plans, cover at tip of the repository at top, those made on another
// branch than branch only within that branch's history, and the findings of the
// latest such receipt of each range in each plan that no later
// finding-settled event settles, as ROUND.ID, of those receipts that are
// p's, made for branch or for no branch, and cover a commit of need.
func (c Config) covered(p Plan, top, route, branch, tip string, need map[string]bool) (map[string]bool, []string) {
	covered := map[string]bool{}
	unsettled := []string{}
	own, err := c.ownPlans()
	if err != nil {
		return covered, unsettled
	}
	resolve := func(rev string) string {
		if rev == "" {
			return ""
		}
		out, err := git.Run("-C", top, "rev-parse", "--verify", "-q", "--end-of-options", rev+"^{commit}")
		if err != nil {
			return ""
		}
		return out
	}
	ids := map[[2]string]string{}
	patchID := func(base, t string) string {
		key := [2]string{base, t}
		if id, ok := ids[key]; ok {
			return id
		}
		id, _ := git.PatchID([]string{"-C", top}, base, t)
		ids[key] = id
		return id
	}
	for _, holder := range own {
		events, err := holder.Events()
		if err != nil {
			continue
		}
		mine := holder.File() == p.File()
		latest := map[string]int{}
		for i, e := range events {
			given, tipName, ok := strings.Cut(e.Range, "..")
			if e.Kind != routeReceiptKind || e.Focus != route || !ok || e.PatchID == "" {
				continue
			}
			base := resolve(given)
			if base == "" {
				continue
			}
			own := resolve("refs/heads/" + e.Branch)
			var tips []string
			for _, t := range []string{tip, own, resolve(tipName), resolve(e.Tip)} {
				if t != "" && !slices.Contains(tips, t) {
					tips = append(tips, t)
				}
			}
			for _, t := range tips {
				if patchID(base, t) != e.PatchID {
					continue
				}
				// A receipt made on another branch counts only for
				// that branch's own commits, as a stack's parent's
				// does for its child: never for a range it was
				// pointed at elsewhere.
				if e.Branch != "" && e.Branch != branch &&
					(own == "" || !git.OK("-C", top, "merge-base", "--is-ancestor", t, own)) {
					continue
				}
				out, err := git.Run("-C", top, "rev-list", base+".."+t)
				if err != nil {
					continue
				}
				needed := false
				for _, commit := range strings.Fields(out) {
					covered[commit] = true
					needed = needed || need[commit]
				}
				if mine && needed && (e.Branch == branch || e.Branch == "") {
					latest[e.Range] = i
				}
			}
		}
		for _, given := range slices.Sorted(maps.Keys(latest)) {
			at := latest[given]
			round := 0
			for _, e := range events[:at+1] {
				if e.Kind == routeReceiptKind && e.Focus == route && e.Range == given {
					round++
				}
			}
			for _, id := range strings.Fields(events[at].Findings) {
				review := fmt.Sprintf("%d.%s", round, id)
				if !slices.ContainsFunc(events[at+1:], func(e Event) bool { return e.Kind == "finding-settled" && e.Review == review }) {
					unsettled = append(unsettled, review)
				}
			}
		}
	}
	sort.Strings(unsettled)
	return covered, unsettled
}

// rewritten reports whether the commits a routed event of the range given
// names were rewritten away, rather than made elsewhere or merged: its
// base is a commit of the repository at top, and none of them is an
// ancestor of base, where the change now starts.
func rewritten(top, base, given string, commits []string) bool {
	from, _, _ := strings.Cut(given, "..")
	if from == "" || !git.OK("-C", top, "cat-file", "-e", from+"^{commit}") {
		return false
	}
	return !slices.ContainsFunc(commits, func(c string) bool {
		return git.OK("-C", top, "merge-base", "--is-ancestor", c, base)
	})
}
