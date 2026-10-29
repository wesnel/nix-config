package plans

import (
	"slices"
	"strings"
)

// defaultBranches are branch names nearly every plan of a repository
// starts from or commits on, as the main checkout does, so that sharing
// one says nothing about two plans.
var defaultBranches = []string{"main", "master"}

// planFacts are what a plan and its parts name that another plan may
// name too.
type planFacts struct {
	plan  Plan
	repos []string
	// tips are the branches it works on, and bases those it starts from,
	// default branches left out.
	tips, bases []string
	workstream  WorkstreamRef
}

// addBranches adds BRANCHES values, BASE..BRANCH or BRANCH, to f.
func (f *planFacts) addBranches(values []string) {
	for _, v := range values {
		base, tip, ok := strings.Cut(v, "..")
		if !ok {
			base, tip = "", v
		}
		if tip != "" && !slices.Contains(defaultBranches, tip) && !slices.Contains(f.tips, tip) {
			f.tips = append(f.tips, tip)
		}
		if base != "" && !slices.Contains(defaultBranches, base) && !slices.Contains(f.bases, base) {
			f.bases = append(f.bases, base)
		}
	}
}

// factsOf reads p's facts from its files.
func factsOf(p Plan) (planFacts, error) {
	f := planFacts{plan: p}
	files, err := p.files()
	if err != nil {
		return f, err
	}
	for i, file := range files {
		props, err := planHeading(file)
		if err != nil {
			return f, err
		}
		if i == 0 {
			f.workstream = refOf(props["WORKSTREAM"])
		}
		for _, r := range strings.Fields(props["REPOSITORIES"]) {
			if !slices.Contains(f.repos, r) {
				f.repos = append(f.repos, r)
			}
		}
		f.addBranches(strings.Fields(props["BRANCHES"]))
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
	for _, br := range slices.Concat(a.tips, a.bases) {
		if slices.Contains(b.tips, br) && !slices.Contains(out, br) {
			out = append(out, br)
		}
	}
	return out
}
