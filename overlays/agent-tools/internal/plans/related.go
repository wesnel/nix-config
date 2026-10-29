package plans

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Two plans are related on hard evidence, which links them by itself, or
// on softer signs, which Related lists for an agent to judge:
//
//   - hard: a branch both work on or one stacks on, a pull request both
//     name, a part or follow-up of one that the other's text names with
//     the plan (taken over), a commit both logs record on the same branch;
//   - soft: a session or a linked worktree both name, or one's text naming
//     the other with none of its items.
//
// Default branches and the main checkout are no evidence of either kind:
// every plan of a repository may name them, and the commit hook once filed
// other sessions' commits into whichever plan named the main checkout.

// defaultBranches are branch names nearly every plan of a repository
// starts from or commits on, as the main checkout does, so that sharing
// one says nothing about two plans.
var defaultBranches = []string{"main", "master"}

// branchPair is a BRANCHES value: the branch worked on, and the one it
// starts from, if given.
type branchPair struct{ base, tip string }

// planFacts are what a plan and its parts name that another plan may
// name too.
type planFacts struct {
	plan       Plan
	id, title  string
	repos      []string
	branches   []branchPair
	prs        []string
	sessions   []string
	worktrees  []string
	commits    []string
	workstream WorkstreamRef
	related    []string
	lines      []string
	// refs are the IDs of its parts and follow-ups.
	refs map[string]bool
}

// addBranches adds BRANCHES values, BASE..BRANCH or BRANCH, to f, a
// default branch as no branch.
func (f *planFacts) addBranches(values []string) {
	keep := func(b string) string {
		if slices.Contains(defaultBranches, b) {
			return ""
		}
		return b
	}
	for _, v := range values {
		base, tip, ok := strings.Cut(v, "..")
		if !ok {
			base, tip = "", v
		}
		if pair := (branchPair{keep(base), keep(tip)}); pair.tip != "" && !slices.Contains(f.branches, pair) {
			f.branches = append(f.branches, pair)
		}
	}
}

// tips are the branches f works on.
func (f planFacts) tips() []string {
	var out []string
	for _, b := range f.branches {
		if !slices.Contains(out, b.tip) {
			out = append(out, b.tip)
		}
	}
	return out
}

// addAll adds each of values to list that it does not hold yet.
func addAll(list *[]string, values ...string) {
	for _, v := range values {
		if v != "" && !slices.Contains(*list, v) {
			*list = append(*list, v)
		}
	}
}

// itemRef is an item ID a plan's text may name another's part or
// follow-up by: P3, F2 or P1.F2.
var itemRef = regexp.MustCompile(`\b((?:P[0-9]+\.)?[PF][0-9]+)\b`)

// factsOf reads p's facts from its files and its log.
func factsOf(p Plan) (planFacts, error) {
	f := planFacts{plan: p, refs: map[string]bool{}}
	files, err := p.files()
	if err != nil {
		return f, err
	}
	for i, file := range files {
		data, err := readPlan(file)
		if err != nil {
			return f, err
		}
		text := string(data)
		lines := strings.Split(text, "\n")
		f.lines = append(f.lines, lines...)
		var props map[string]string
		for _, h := range parseOrg(text) {
			if h.Level == 1 && sectionTitle(h) == "Plan" {
				props = h.Props
				break
			}
		}
		if i == 0 {
			f.id, f.title = fileID(text), orgTitle(text)
			f.workstream = refOf(props["WORKSTREAM"])
			f.related = relatedIDs(lines)
		}
		addAll(&f.repos, strings.Fields(props["REPOSITORIES"])...)
		f.addBranches(strings.Fields(props["BRANCHES"]))
		addAll(&f.prs, strings.Fields(props["PULL_REQUESTS"])...)
		addAll(&f.sessions, strings.Fields(props["SESSIONS"])...)
		trees := strings.Fields(props["WORKTREES"])
		for _, pair := range strings.Fields(props["OWNERS"]) {
			if wt, _, ok := strings.Cut(pair, "="); ok {
				trees = append(trees, wt)
			}
		}
		for _, wt := range trees {
			if linkedWorktree(wt) {
				addAll(&f.worktrees, filepath.Clean(wt))
			}
		}
	}
	list, err := p.itemList()
	if err != nil {
		return f, err
	}
	for _, it := range list {
		if itemRef.FindString(it.ID) == it.ID {
			f.refs[it.ID] = true
		}
	}
	events, err := p.Events()
	if err != nil {
		return f, err
	}
	for _, e := range events {
		if e.Kind == "committed" && e.Branch != "" && e.Commit != "" && !slices.Contains(defaultBranches, e.Branch) {
			addAll(&f.commits, e.Branch+"\x00"+e.Commit)
		}
	}
	return f, nil
}

// sameRepository reports whether a and b may be in the same repository:
// one names none, or they share one.
func sameRepository(a, b planFacts) bool {
	if len(a.repos) == 0 || len(b.repos) == 0 {
		return true
	}
	for _, r := range a.repos {
		if slices.Contains(b.repos, r) {
			return true
		}
	}
	return false
}

// continued are the branches of b's that a works on too or starts from.
func continued(a, b planFacts) []string {
	if !sameRepository(a, b) {
		return nil
	}
	var out []string
	for _, br := range a.branches {
		for _, name := range []string{br.tip, br.base} {
			if name != "" && slices.Contains(b.tips(), name) {
				addAll(&out, name)
			}
		}
	}
	return out
}

// shortName is p's name without its host, as plans' text names one.
func (f planFacts) shortName() string { return strings.TrimSuffix(f.plan.Name, "."+f.plan.Host) }

// takenOver are the items of b's that a's text names on a line that names
// b too, in order; and whether a's text names b at all.
func takenOver(a, b planFacts) ([]string, bool) {
	name := regexp.MustCompile(`(^|[^A-Za-z0-9-])` + regexp.QuoteMeta(b.shortName()) + `([^A-Za-z0-9-]|$)`)
	var out []string
	named := false
	for _, l := range a.lines {
		if !name.MatchString(l) {
			continue
		}
		named = true
		for _, m := range itemRef.FindAllString(l, -1) {
			if b.refs[m] {
				addAll(&out, m)
			}
		}
	}
	return out, named
}

// shared are the values both lists hold, in a's order.
func shared(a, b []string) []string {
	var out []string
	for _, v := range a {
		if slices.Contains(b, v) {
			addAll(&out, v)
		}
	}
	return out
}

// evidence is why plan a is related to plan b, as sentences seen from a:
// the hard evidence and the softer signs.
func evidence(a, b planFacts) (hard, soft []string) {
	if sameRepository(a, b) {
		for _, br := range shared(a.tips(), b.tips()) {
			hard = append(hard, "Both work on branch "+br+".")
		}
		for _, x := range a.branches {
			if x.base != "" && slices.Contains(b.tips(), x.base) && !slices.Contains(a.tips(), x.base) {
				hard = append(hard, "Stacks on its branch "+x.base+".")
			}
		}
		for _, x := range b.branches {
			if x.base != "" && slices.Contains(a.tips(), x.base) && !slices.Contains(b.tips(), x.base) {
				hard = append(hard, "Its branch "+x.tip+" stacks on this plan's "+x.base+".")
			}
		}
		for _, c := range shared(a.commits, b.commits) {
			branch, subject, _ := strings.Cut(c, "\x00")
			hard = append(hard, "Both log the commit '"+subject+"' on "+branch+".")
		}
	}
	for _, pr := range shared(a.prs, b.prs) {
		hard = append(hard, "Both name pull request "+pr+".")
	}
	mine, namesIt := takenOver(a, b)
	if len(mine) > 0 {
		hard = append(hard, "Takes over its "+strings.Join(mine, ", ")+".")
	}
	theirs, namesThis := takenOver(b, a)
	if len(theirs) > 0 {
		hard = append(hard, "Took over this plan's "+strings.Join(theirs, ", ")+".")
	}
	for _, s := range shared(a.sessions, b.sessions) {
		soft = append(soft, "Both name session "+s+".")
	}
	for _, wt := range shared(a.worktrees, b.worktrees) {
		soft = append(soft, "Both name worktree "+wt+".")
	}
	if namesIt && len(mine) == 0 {
		soft = append(soft, "This plan's text names it.")
	}
	if namesThis && len(theirs) == 0 {
		soft = append(soft, "Its text names this plan.")
	}
	return hard, soft
}

// Relation is another plan related to a plan.
type Relation struct {
	// Plan is the other plan's name, and File its file.
	Plan  string `json:"plan"`
	File  string `json:"file"`
	Title string `json:"title"`
	// Hard is whether the evidence links the plans by itself; Evidence
	// is the hard evidence, or else the softer signs.
	Hard     bool     `json:"hard"`
	Evidence []string `json:"evidence"`
	// Linked is whether the plan's Related links the other already.
	Linked bool `json:"linked"`
	id     string
}

// Related are the plans, any host's, related to the plan plan names: those
// on hard evidence first, then those on softer signs, each newest first.
// It writes nothing. A plan that cannot be read is left out and named in
// the error.
func (c Config) Related(plan string) ([]Relation, error) {
	p, err := c.Open(plan)
	if err != nil {
		return nil, err
	}
	mine, err := factsOf(p)
	if err != nil {
		return nil, err
	}
	ps, _, err := c.Plans()
	if err != nil {
		return nil, err
	}
	var hard, soft []Relation
	var errs []error
	for _, o := range ps {
		if o.Name == p.Name {
			continue
		}
		theirs, err := factsOf(o)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", o.Name, err))
			continue
		}
		h, s := evidence(mine, theirs)
		r := Relation{Plan: o.Name, File: o.file(""), Title: theirs.title, Hard: len(h) > 0, Evidence: h,
			Linked: theirs.id != "" && slices.Contains(mine.related, theirs.id), id: theirs.id}
		switch {
		case r.Hard:
			hard = append(hard, r)
		case len(s) > 0:
			r.Evidence = s
			soft = append(soft, r)
		}
	}
	return append(append([]Relation{}, hard...), soft...), errors.Join(errs...)
}

// LinkEvidence links the plan plan names, this host's, to each plan hard
// evidence relates it to that it does not link yet, through Link, with the
// evidence as the reason, and returns those it linked, Linked. A plan in
// the old form, which has no file-level ID to link to, is left unlinked.
// What gives a plan new evidence calls it: new, the commit hook, and the
// commands that change a plan's branches, pull requests or text.
func (c Config) LinkEvidence(plan string, now time.Time) ([]Relation, error) {
	p, err := c.Open(plan)
	if err != nil {
		return nil, err
	}
	if err := p.checkOwn(); err != nil {
		return nil, err
	}
	rs, err := c.Related(plan)
	errs := []error{err}
	out := []Relation{}
	for _, r := range rs {
		if !r.Hard || r.Linked || r.id == "" {
			continue
		}
		e, err := c.Link(p.file(""), r.File, strings.Join(r.Evidence, " "), now)
		if err != nil {
			errs = append(errs, fmt.Errorf("linking %s: %w", r.Plan, err))
			continue
		}
		if e.Kind != "" {
			r.Linked = true
			out = append(out, r)
		}
	}
	return out, errors.Join(errs...)
}
