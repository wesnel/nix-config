package upgrade

import (
	"cmp"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

var (
	// actionUses is a line that runs an action at a ref, with the tag a
	// comment after it names.
	actionUses = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*["']?([^\s"'@]+)@([^\s"'#]+)["']?\s*(?:#\s*(\S+))?`)
	// yamlKey is a line that opens a mapping key, with its indentation.
	yamlKey = regexp.MustCompile(`^( *)([A-Za-z0-9_.-]+):\s*(?:#.*)?$`)
)

// actionUse is one place a workflow or composite action runs an action:
// its group, `JOB NAME` for a workflow's, `NAME` for a composite
// action's, the action, the ref and the tag its comment names, its line,
// and which of its group's uses it is, from 1.
type actionUse struct {
	group, name, ref, tag string
	line, nth             int
}

// actionUsesOf are the actions text runs, in order.
func actionUsesOf(text string) []actionUse {
	var out []actionUse
	nth := map[string]int{}
	job, jobIndent, inJobs := "", -1, false
	for i, line := range strings.Split(text, "\n") {
		if m := yamlKey.FindStringSubmatch(line); m != nil {
			indent := len(m[1])
			switch {
			case indent == 0:
				inJobs, job, jobIndent = m[2] == "jobs", "", -1
			case inJobs && (jobIndent < 0 || indent == jobIndent):
				job, jobIndent = m[2], indent
			}
		}
		u := actionUses.FindStringSubmatch(line)
		if u == nil {
			continue
		}
		group := strings.TrimSpace(job + " " + u[1])
		nth[group]++
		out = append(out, actionUse{group: group, name: u[1], ref: u[2], tag: u[3], line: i + 1, nth: nth[group]})
	}
	return out
}

// pairUses pairs the uses at BASE, a, with those at TIP, b: a use with
// one of its group at the same ref first, so that a use inserted or
// dropped moves no other, then one of the same action at the same ref,
// as across a job renamed, and those pairs have not moved; then the rest
// of each group in order. A use with no pair is paired with the zero
// actionUse. The pairs that moved come in group order.
func pairUses(a, b []actionUse) (unmoved, moved [][2]actionUse) {
	usedA, usedB := make([]bool, len(a)), make([]bool, len(b))
	for _, same := range []func(x, y actionUse) bool{
		func(x, y actionUse) bool { return x.group == y.group && x.ref == y.ref },
		func(x, y actionUse) bool { return x.name == y.name && x.ref == y.ref },
	} {
		for i, x := range a {
			for j, y := range b {
				if !usedA[i] && !usedB[j] && same(x, y) {
					usedA[i], usedB[j] = true, true
					unmoved = append(unmoved, [2]actionUse{x, y})
				}
			}
		}
	}
	restA, restB := map[string][]actionUse{}, map[string][]actionUse{}
	for i, x := range a {
		if !usedA[i] {
			restA[x.group] = append(restA[x.group], x)
		}
	}
	for j, y := range b {
		if !usedB[j] {
			restB[y.group] = append(restB[y.group], y)
		}
	}
	groups := slices.Sorted(maps.Keys(restA))
	for g := range restB {
		if _, ok := restA[g]; !ok {
			groups = append(groups, g)
		}
	}
	slices.Sort(groups)
	for _, g := range groups {
		for i := range max(len(restA[g]), len(restB[g])) {
			var pair [2]actionUse
			if i < len(restA[g]) {
				pair[0] = restA[g][i]
			}
			if i < len(restB[g]) {
				pair[1] = restB[g][i]
			}
			moved = append(moved, pair)
		}
	}
	return unmoved, moved
}

// actionsUnread is what a line of a workflow names that may run an action:
// `uses`, `docker://`, or what reads as an action's OWNER/REPO@REF, as a
// folded scalar's next line holds it.
var actionsUnread = regexp.MustCompile(`uses|docker://|[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@`)

// actionPins are the actions a workflow or a composite action runs at
// another ref at TIP than at BASE, newly, or no longer, each occurrence a
// pin of its own, as pairUses pairs them, keyed by its group (`JOB
// NAME`), with `#N` where it is the group's Nth use at TIP (at BASE, for
// one dropped) and N is more than 1, or the next N no other pin's key
// has. A pair that has not moved is read, however its lines changed.
//
// The scanner reads only the block form `uses: NAME@REF`, so it fails
// closed: a changed line actionsUnread matches that is no pin's line is
// one more pin, `actions-unread`, of the file.
func actionPins(file, old, next string) []Pin {
	unmoved, moved := pairUses(actionUsesOf(old), actionUsesOf(next))
	var pins []Pin
	read := map[string]map[int]bool{"BASE": {}, "TIP": {}}
	for _, pair := range unmoved {
		read["BASE"][pair[0].line], read["TIP"][pair[1].line] = true, true
	}
	taken := map[string]bool{}
	for _, pair := range moved {
		ua, ub := pair[0], pair[1]
		read["BASE"][ua.line], read["TIP"][ub.line] = true, true
		group := cmp.Or(ub.group, ua.group)
		n, key := cmp.Or(ub.nth, ua.nth), group
		if n > 1 {
			key = fmt.Sprintf("%s #%d", group, n)
		}
		for taken[key] {
			n++
			key = fmt.Sprintf("%s #%d", group, n)
		}
		taken[key] = true
		name := cmp.Or(ub.name, ua.name)
		p := Pin{Key: file + "#" + key, File: file, Kind: "action", Old: ua.ref, New: ub.ref, OldRef: ua.ref, NewRef: ub.ref,
			Line: ub.line}
		if parts := strings.Split(name, "/"); len(parts) >= 2 && !strings.HasPrefix(name, ".") && !strings.Contains(name, ":") {
			p.Owner, p.Repo = parts[0], parts[1]
		}
		p.OldTag, p.NewTag = cmp.Or(ua.tag, tagOf(ua.ref)), cmp.Or(ub.tag, tagOf(ub.ref))
		pins = append(pins, p)
	}
	removed, added := changedLines(old, next)
	var unread []string
	for _, side := range []struct {
		lines []lineText
		name  string
	}{{removed, "BASE"}, {added, "TIP"}} {
		for _, l := range side.lines {
			if actionsUnread.MatchString(l.text) && !read[side.name][l.line] {
				unread = append(unread, fmt.Sprintf("%d at %s", l.line, side.name))
			}
		}
	}
	if len(unread) > 0 {
		line := 1
		if len(added) > 0 {
			line = added[0].line
		}
		pins = append(pins, Pin{Key: file + "#unread", File: file, Kind: "actions-unread", Old: cmp.Or(blobID([]byte(old)), "none"),
			New: cmp.Or(blobID([]byte(next)), "none"), Note: "(lines " + strings.Join(unread, ", ") + ")", Line: line})
	}
	return pins
}
