// Package routes names the focused reviews a change can be routed to, each
// beside the general fresh review every change gets, and finds where a
// change starts.
package routes

import (
	_ "embed"
	"encoding/json"
	"slices"

	"github.com/wesnel/agent-tools/internal/git"
)

//go:embed known.json
var knownJSON []byte

// Known are the routes a focused review exists for, from known.json, which
// the package's Nix passthru.focusPrompts also reads. A route a rule or a
// reviewer names must be one of them, so a change is never routed to a
// review nothing can run; a new focus adds its route there.
var Known = func() []string {
	var known []string
	if err := json.Unmarshal(knownJSON, &known); err != nil || len(known) == 0 {
		panic("routes: known.json holds no routes")
	}
	return known
}()

// IsKnown reports whether route is one of Known.
func IsKnown(route string) bool { return slices.Contains(Known, route) }

// DefaultRef is the default branch of the repository at top, which a
// change is measured from: origin/HEAD, else origin/main or origin/master,
// else main or master; "" where none exists.
func DefaultRef(top string) string {
	for _, ref := range []string{"refs/remotes/origin/HEAD", "refs/remotes/origin/main", "refs/remotes/origin/master",
		"refs/heads/main", "refs/heads/master"} {
		if git.OK("-C", top, "rev-parse", "--verify", "-q", ref+"^{commit}") {
			return ref
		}
	}
	return ""
}

// MergeBase is where the change at tip of the repository at top starts:
// its merge base with DefaultRef, whatever base a caller names, so that a
// base chosen above a commit cannot leave it out. It is "" where there is
// no default branch, or tip shares no history with it.
func MergeBase(top, tip string) string {
	ref := DefaultRef(top)
	if ref == "" {
		return ""
	}
	base, err := git.Run("-C", top, "merge-base", ref, tip)
	if err != nil {
		return ""
	}
	return base
}
