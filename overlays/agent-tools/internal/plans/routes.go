package plans

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
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

// WriteRouted logs r in the plan LocateCheckout finds for session and the
// worktree at top, and returns that plan's file, or "" where there is none.
func WriteRouted(session, top string, r Routed, now time.Time) (string, error) {
	c, err := Load()
	if err != nil {
		return "", err
	}
	p, part, ok := c.LocateCheckout(session, top)
	if !ok {
		return "", nil
	}
	if _, err := p.Routed(part, r, now); err != nil {
		return "", err
	}
	return p.File(), nil
}

// Issued is a review's prompt printed for a reviewer whose reply comes
// back through a check: the nonce the reply must carry, and the focus,
// range, tip and patch ID it was printed for, which the check takes from
// here rather than from the reply.
type Issued struct {
	Nonce, Focus, Range, Tip, PatchID string
}

// WriteIssued logs i in the plan LocateCheckout finds for session and the
// worktree at top, and returns that plan's file, or "" where there is none.
func WriteIssued(session, top string, i Issued, now time.Time) (string, error) {
	c, err := Load()
	if err != nil {
		return "", err
	}
	p, part, ok := c.LocateCheckout(session, top)
	if !ok {
		return "", nil
	}
	what := i.Focus
	if what == "" {
		what = "general"
	}
	if _, err := p.Record(part, Event{Kind: issuedKind, Nonce: i.Nonce, Focus: i.Focus, Range: i.Range, Tip: i.Tip,
		PatchID: i.PatchID, Summary: what + " review of " + i.Range}, now); err != nil {
		return "", err
	}
	return p.File(), nil
}

// FindIssued is the prompt issued with nonce in the plan LocateCheckout
// finds for session and the worktree at top, whether a receipt already
// names it, and whether there is one.
func FindIssued(session, top, nonce string) (i Issued, checked, found bool) {
	c, err := Load()
	if err != nil || nonce == "" {
		return Issued{}, false, false
	}
	p, _, ok := c.LocateCheckout(session, top)
	if !ok {
		return Issued{}, false, false
	}
	events, err := p.Events()
	if err != nil {
		return Issued{}, false, false
	}
	for _, e := range events {
		switch {
		case e.Kind == issuedKind && e.Nonce == nonce && !found:
			i, found = Issued{Nonce: e.Nonce, Focus: e.Focus, Range: e.Range, Tip: e.Tip, PatchID: e.PatchID}, true
		case (e.Kind == receiptKind || e.Kind == routeReceiptKind) && e.Nonce == nonce:
			checked = true
		}
	}
	return i, checked, found
}

// RouteRound is the round a review of focus of the range given would be in
// the plan LocateCheckout finds for session and the worktree at top: one
// more than its route receipts of that focus and range.
func RouteRound(session, top, focus, given string) int {
	c, err := Load()
	if err != nil {
		return 1
	}
	p, _, ok := c.LocateCheckout(session, top)
	if !ok {
		return 1
	}
	events, err := p.Events()
	if err != nil {
		return 1
	}
	round := 1
	for _, e := range events {
		if e.Kind == routeReceiptKind && e.Focus == focus && e.Range == given {
			round++
		}
	}
	return round
}

// HostBranches are the branches this host's plans name in BRANCHES, their
// own and their parts', newest plan first, each once.
func HostBranches() []string {
	c, err := Load()
	if err != nil {
		return nil
	}
	own, err := c.ownPlans()
	if err != nil {
		return nil
	}
	var out []string
	for _, p := range own {
		files, err := p.files()
		if err != nil {
			continue
		}
		for _, f := range files {
			props, err := planHeading(f)
			if err != nil {
				continue
			}
			for _, v := range strings.Fields(props["BRANCHES"]) {
				if _, b, ok := strings.Cut(v, ".."); ok {
					v = b
				}
				if v != "" && !slices.Contains(out, v) {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// ReviewsRemoved are the routes REVIEWS lost, in the log of the plan
// LocateCheckout finds for session and the worktree at top, since that
// log's last routed event: a route taken out is a change of what reviews
// the change, which the next routing sends to a security review.
func ReviewsRemoved(session, top string) []string {
	c, err := Load()
	if err != nil {
		return nil
	}
	p, _, ok := c.LocateCheckout(session, top)
	if !ok {
		return nil
	}
	events, err := p.Events()
	if err != nil {
		return nil
	}
	last := map[string][]string{}
	lost := map[string]bool{}
	for _, e := range events {
		switch {
		case e.Kind == routedKind:
			lost = map[string]bool{}
		case e.Kind == propertyKind && e.Property == "REVIEWS":
			now := strings.Fields(e.Value)
			for _, r := range last[e.Item] {
				if !slices.Contains(now, r) {
					lost[r] = true
				}
			}
			last[e.Item] = now
		}
	}
	return slices.Sorted(maps.Keys(lost))
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

// Gate is what the gates make of a branch's routes: why it may not be
// opened, pushed or signed yet, or "", and the waivers and pipeline
// opt-outs that let it pass, for the operator to see.
type Gate struct {
	Refusal string
	Notes   []string
}

// GateFor is what a gate is asked about: Branch at Tip in the repository
// at Top, checked out at Worktree, or "" where it is not.
type GateFor struct {
	Top, Worktree, Branch, Tip string
	// Scanned are the routes the caller's own scan found, each with the
	// commits that need it, owed beside the plans' routes unless a plan
	// waives them, and held up by the unsettled findings of the receipts
	// covering those commits in the plans gating the branch.
	Scanned map[string][]string
	// Loop is a review loop's gate: it may review a route through the
	// relay, but a waiver, and a route it cannot meet, are the
	// operator's.
	Loop bool
}

// RouteGate is the gate for a branch, from the plans of this host that
// name the branch in BRANCHES, and, where it is checked out, those that
// name its worktree and no other branch, or none, less those of other
// repositories and those finished: a route any of them owes, or the
// caller's scan found, with commits no route receipt covers or findings of
// its covering receipts unsettled, refuses. The Stop hook asks once; this
// is where an owed route stops the work.
func (c Config) RouteGate(at GateFor) (Gate, error) {
	top, worktree, branch, tip := at.Top, at.Worktree, at.Branch, at.Tip
	var found []Found
	if branch != "" {
		f, err := c.Find(FindOptions{Branch: branch})
		if err != nil {
			return Gate{}, err
		}
		found = append(found, f...)
	}
	if worktree != "" {
		f, err := c.Find(FindOptions{Worktree: worktree})
		if err != nil {
			return Gate{}, err
		}
		for _, x := range f {
			props, err := planHeading(x.File)
			if err != nil {
				continue
			}
			names := strings.Fields(props["BRANCHES"])
			if len(names) == 0 || slices.ContainsFunc(names, func(v string) bool {
				_, b, ok := strings.Cut(v, "..")
				return v == branch || ok && b == branch
			}) {
				found = append(found, x)
			}
		}
	}
	var g Gate
	var owed, routesOwed []string
	var gating []Plan
	seen, waived, reported := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, f := range found {
		if seen[f.Plan] {
			continue
		}
		seen[f.Plan] = true
		p, err := c.Open(f.Plan)
		if err != nil {
			return Gate{}, err
		}
		props, err := planHeading(p.File())
		if err != nil {
			return Gate{}, err
		}
		events, err := p.Events()
		if err != nil {
			return Gate{}, err
		}
		if otherRepository(top, props["REPOSITORIES"]) || finished(top, props, events) {
			continue
		}
		s, err := c.RouteState(p, top, branch, tip)
		if err != nil {
			return Gate{}, err
		}
		gating = append(gating, p)
		if strings.HasPrefix(s.Pipeline, "opted-out") {
			g.Notes = append(g.Notes, fmt.Sprintf("%s: PIPELINE %s", p.File(), s.Pipeline))
			at.Scanned = nil
		}
		for _, w := range s.Waivers {
			g.Notes = append(g.Notes, fmt.Sprintf("%s: %s review waived: %s", p.File(), w.Route, w.Why))
			waived[w.Route] = true
		}
		for _, d := range s.Due {
			var parts []string
			if len(d.Uncovered) > 0 {
				var short []string
				for _, u := range d.Uncovered {
					short = append(short, u[:min(12, len(u))])
				}
				parts = append(parts, "commits "+strings.Join(short, ", ")+" have no route receipt")
			}
			if len(d.Unsettled) > 0 {
				parts = append(parts, "findings "+strings.Join(d.Unsettled, ", ")+" are not settled")
				reported[p.File()+"\x00"+d.Route] = true
			}
			owed = append(owed, fmt.Sprintf("%s owes a %s review (%s)", p.File(), d.Route, strings.Join(parts, "; ")))
			if !slices.Contains(routesOwed, d.Route) {
				routesOwed = append(routesOwed, d.Route)
			}
		}
	}
	for _, route := range slices.Sorted(maps.Keys(at.Scanned)) {
		need := map[string]bool{}
		for _, commit := range at.Scanned[route] {
			need[commit] = true
		}
		if waived[route] || len(need) == 0 {
			continue
		}
		covered, _ := c.covered(Plan{}, top, route, branch, tip, need)
		var short []string
		for _, commit := range at.Scanned[route] {
			if !covered[commit] {
				short = append(short, commit[:min(12, len(commit))])
			}
		}
		owes := len(short) > 0
		if owes {
			owed = append(owed, fmt.Sprintf("its new commits %s need a %s review and have no route receipt", strings.Join(short, ", "), route))
		}
		// The findings of the receipts covering them count where they
		// would for a route a plan owes: in each plan gating the branch.
		for _, p := range gating {
			if reported[p.File()+"\x00"+route] {
				continue
			}
			if _, unsettled := c.covered(p, top, route, branch, tip, need); len(unsettled) > 0 {
				owed = append(owed, fmt.Sprintf("%s holds the %s review of its new commits, whose findings %s are not settled",
					p.File(), route, strings.Join(unsettled, ", ")))
				owes = true
			}
		}
		if owes && !slices.Contains(routesOwed, route) {
			routesOwed = append(routesOwed, route)
		}
	}
	if len(owed) == 0 {
		return g, nil
	}
	base := strings.TrimPrefix(strings.TrimPrefix(routes.DefaultRef(top), "refs/remotes/"), "refs/heads/")
	if base == "" {
		base = "BASE"
	}
	var runs []string
	for _, r := range routesOwed {
		if at.Loop {
			runs = append(runs, fmt.Sprintf("`git fresh-review --focus %s --print-prompt --goal FILE %s..%s`", r, base, branch))
		} else {
			runs = append(runs, fmt.Sprintf("`git fresh-review --focus %s --goal FILE %s..%s`", r, base, branch))
		}
	}
	if at.Loop {
		g.Refusal = fmt.Sprintf("%s's change is not through its focused reviews: %s. Give a new `reviewer` sub-agent the "+
			"prompt %s prints, whose base must lie below every commit listed, check its reply with `git fresh-review --check "+
			"FILE %s..%s`, and settle its findings with `agent-plan log PLAN --kind finding-settled --review ROUND.ID`; a "+
			"waiver is the operator's alone. Where the loop cannot do that, stop and hand back to the operator, naming the %s "+
			"review %s owes", branch, strings.Join(owed, "; "), strings.Join(runs, " and "), base, branch,
			strings.Join(routesOwed, " and "), branch)
		return g, nil
	}
	g.Refusal = fmt.Sprintf("%s's change is not through its focused reviews: %s. Run %s, whose base must lie below every "+
		"commit listed, settle the findings in the plan named with them as the review-change skill says "+
		"(`agent-plan log PLAN --kind finding-settled --review ROUND.ID`), or, where the change does not need a route, "+
		"`agent-plan waive PLAN --route ROUTE --why TEXT`; then run this again",
		branch, strings.Join(owed, "; "), strings.Join(runs, " and "))
	return g, nil
}

// otherRepository reports whether a plan's REPOSITORIES names only other
// repositories than the one at top, which is known by the GitHub
// owner/name its remotes fetch from. A plan naming none, or a repository
// fetching from GitHub through no remote, is never another's.
func otherRepository(top, repositories string) bool {
	names := strings.Fields(repositories)
	if len(names) == 0 {
		return false
	}
	out, err := git.Run("-C", top, "remote", "-v")
	if err != nil {
		return false
	}
	var slugs []string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 3 && f[2] == "(fetch)" {
			if s := gh.Slug(f[1]); s != "" {
				slugs = append(slugs, s)
			}
		}
	}
	return len(slugs) > 0 && !slices.ContainsFunc(names, func(n string) bool {
		return slices.ContainsFunc(slugs, func(s string) bool { return strings.EqualFold(n, s) })
	})
}

// finished reports whether a plan's work in the repository at top is
// over: its STATUS is halted; a signing pass signed it and nothing was
// committed or pushed in it since; or each branch its BRANCHES names is
// merged into the default branch or deleted. A review routed after the
// signing is no new work: a loop's relayed review logs one in the plan
// naming its branch, whatever that plan's state.
func finished(top string, props map[string]string, events []Event) bool {
	if props["STATUS"] == "halted" {
		return true
	}
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Kind {
		case "signed":
			return true
		case "committed", "pushed":
			return branchesDone(top, props["BRANCHES"], events)
		}
	}
	return branchesDone(top, props["BRANCHES"], events)
}

// branchesDone reports whether each of branches, a plan's BRANCHES, is
// merged into the default branch of the repository at top, or deleted. A
// branch is judged at the commit the plan's events last name for it, where
// they name one, so that a later branch reusing the name does not keep the
// plan open; else at its ref.
func branchesDone(top, branches string, events []Event) bool {
	names := strings.Fields(branches)
	if len(names) == 0 {
		return false
	}
	def := routes.DefaultRef(top)
	for _, v := range names {
		if _, b, ok := strings.Cut(v, ".."); ok {
			v = b
		}
		at := ""
		for i := len(events) - 1; i >= 0 && at == ""; i-- {
			if events[i].Branch == v {
				at = cmp.Or(events[i].Commit, events[i].Tip)
			}
		}
		if at == "" {
			if !git.OK("-C", top, "rev-parse", "--verify", "-q", "refs/heads/"+v) {
				continue
			}
			at = "refs/heads/" + v
		}
		if def == "" || strings.HasPrefix(at, "-") || !git.OK("-C", top, "merge-base", "--is-ancestor", at, def) {
			return false
		}
	}
	return true
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
