// Package upgrade finds the dependency pins a change moves, and gathers,
// on the host, what changed upstream between each pin's old and new
// versions, for the upgrade review's prompt. The reviewer reads what is
// gathered and never fetches anything itself.
package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/routes"
)

// Pin is one dependency whose version a change sets, moves or drops.
type Pin struct {
	// Key names the pin across rounds: its file and what it pins there,
	// as `flake.lock#nixpkgs` or `go.mod#golang.org/x/mod`, unique among
	// a change's pins.
	Key string `json:"pin"`
	// Covers are the keys of the other pins that move the same repository
	// between the same versions, which this pin stands for.
	Covers []string `json:"covers,omitempty"`
	File   string   `json:"file"`
	// Kind is what kind of pin it is: flake, flake-nix, go-module, go,
	// toolchain, nix, action or lockfile, among others; a kind this
	// package cannot read upstream changes for says why in Gathered.
	Kind string `json:"kind"`
	// Type is a flake node's locked type, such as github, git or path.
	Type string `json:"type,omitempty"`
	// Owner and Repo are its GitHub repository, where it has one.
	Owner string `json:"owner,omitempty"`
	Repo  string `json:"repo,omitempty"`
	// Old and New are the revision, version or ref at BASE and at TIP;
	// "" on the side where the pin does not exist.
	Old string `json:"old"`
	New string `json:"new"`
	// OldRef and NewRef are what each side names in its repository, a
	// commit or a tag, where known; OldTag and NewTag the release tags.
	OldRef, NewRef string
	OldTag, NewTag string
	// OldHash and NewHash are each side's NAR hash, which a source tree
	// fetched for it must have.
	OldHash, NewHash string
	// OldTime and NewTime are when each side's revision was made, where
	// the lock file says.
	OldTime, NewTime time.Time
	// Module is a Go module's path, and OldModule its path at BASE where
	// that differs, as across a major version's /vN or a replacement's
	// two targets.
	Module, OldModule string
	// Note is why the pin's file could not be read as its tool reads it,
	// where it could not.
	Note string
	// Line is the line of File at TIP that names New, from 1.
	Line int
}

// HashOnly reports whether only the pin's hash moved, its ref the same on
// both sides: there is no upstream history between them to read.
func (p Pin) HashOnly() bool { return p.Moved() && p.OldRef != "" && p.OldRef == p.NewRef }

// Moved reports whether the pin names a version on both sides, and they
// differ: an upgrade or a downgrade, whose upstream changes can be read.
func (p Pin) Moved() bool { return p.Old != "" && p.New != "" && p.Old != p.New }

// DiffOnly reports whether the pin is one only the change's own diff
// explains, with no upstream to gather: a flake.nix changed (flake-nix),
// or changed lines a scanner could not read (nix-unread, actions-unread).
// A reviewer's reading of that diff settles it.
func (p Pin) DiffOnly() bool {
	return p.Kind == "flake-nix" || p.Kind == "nix-unread" || p.Kind == "actions-unread"
}

// Keys are the pin's key and those it Covers.
func (p Pin) Keys() []string { return append([]string{p.Key}, p.Covers...) }

// ID is the pin's keys, sorted, with its two versions, hashed: it changes
// when the pin is moved again, so an unread pin settled in a review record
// stays settled only for the versions it was settled at, and whichever of
// a merged pin's keys leads.
func (p Pin) ID() string {
	keys := p.Keys()
	slices.Sort(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\x00") + "\x00" + p.Old + "\x00" + p.New))
	return hex.EncodeToString(sum[:])[:12]
}

// Pins are the pins base..tip changes in the repository at top, in file
// order, then as each file lists them, each with a key no other has.
func Pins(top, base, tip string) ([]Pin, error) {
	names, err := git.Bytes(nil, "-C", top, "diff", "--name-only", "-z", "--no-renames", base, tip)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(string(names), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	var pins []Pin
	for _, f := range files {
		old, _ := git.Bytes(nil, "-C", top, "cat-file", "blob", base+":"+f)
		next, _ := git.Bytes(nil, "-C", top, "cat-file", "blob", tip+":"+f)
		var found []Pin
		switch name := path.Base(f); {
		case name == "flake.lock":
			found = flakePins(f, old, next)
		case name == "flake.nix":
			found = flakeNixPins(top, base, tip, f, string(old), string(next))
		case name == "go.mod" || name == "go.work":
			found = goModPins(f, string(old), string(next))
		case name == "go.sum" || name == "go.work.sum":
			found = goSumPins(f, string(old), string(next))
		case routes.Match("**/nix/sources.json", f):
			found = nivPins(f, old, next)
		case routes.Match("**/npins/sources.json", f):
			found = npinsPins(f, old, next)
		case slices.ContainsFunc(routes.LockFiles, func(g string) bool { return routes.Match(g, f) }):
			found = lockFilePin(top, base, tip, f)
		case strings.HasSuffix(f, ".nix"):
			found = nixPins(f, string(old), string(next))
		case routes.Match(".github/workflows/**", f) || routes.Match(".github/actions/**", f) ||
			name == "action.yml" || name == "action.yaml":
			found = actionPins(f, string(old), string(next))
		}
		for i := range found {
			if found[i].Line == 0 {
				found[i].Line = lineOf(next, found[i].New)
			}
		}
		pins = append(pins, found...)
	}
	return merged(uniqueKeys(movedAcross(pins))), nil
}

// movedAcross makes a Nix unit dropped from one file and one of its key
// added to another one pin, the added one's, moved from the dropped one's
// version, or none where the version is the same: the unit was moved.
func movedAcross(pins []Pin) []Pin {
	unit := func(p Pin) string { return strings.TrimPrefix(p.Key, p.File+"#") }
	gone := map[int]bool{}
	for i, d := range pins {
		if d.Kind != "nix" || d.Old == "" || d.New != "" {
			continue
		}
		j := slices.IndexFunc(pins, func(p Pin) bool {
			return p.Kind == "nix" && p.Old == "" && p.New != "" && p.File != d.File && unit(p) == unit(d)
		})
		if j < 0 || gone[j] {
			continue
		}
		gone[i] = true
		if pins[j].New == d.Old {
			gone[j] = true
			continue
		}
		pins[j].Old, pins[j].OldRef, pins[j].OldTag, pins[j].OldHash = d.Old, d.OldRef, d.OldTag, d.OldHash
	}
	var out []Pin
	for i, p := range pins {
		if !gone[i] {
			out = append(out, p)
		}
	}
	return out
}

// merged makes the moves of one repository between the same two versions
// one pin, the first, which Covers the keys of the rest: one move is
// gathered once, and read once.
func merged(pins []Pin) []Pin {
	first := map[[5]string]int{}
	var out []Pin
	for _, p := range pins {
		if p.Owner == "" || !p.Moved() {
			out = append(out, p)
			continue
		}
		k := [5]string{strings.ToLower(p.Owner + "/" + p.Repo), p.Old, p.New, p.OldRef, p.NewRef}
		if i, ok := first[k]; ok {
			out[i].Covers = append(out[i].Covers, p.Key)
			continue
		}
		first[k] = len(out)
		out = append(out, p)
	}
	return out
}

// uniqueKeys gives each pin whose key an earlier one has an index, `KEY
// #2` and on, in their order, so that a reply's account of a pin names it
// alone.
func uniqueKeys(pins []Pin) []Pin {
	seen := map[string]int{}
	for i := range pins {
		k := pins[i].Key
		seen[k]++
		if n := seen[k]; n > 1 {
			pins[i].Key = fmt.Sprintf("%s #%d", k, n)
			seen[pins[i].Key]++
		}
	}
	return pins
}

// lineOf is the first line of data holding s, from 1, or 1.
func lineOf(data []byte, s string) int {
	i := strings.Index(string(data), s)
	if s == "" || i < 0 {
		return 1
	}
	return strings.Count(string(data[:i]), "\n") + 1
}

// blobID is a short hash of a file's content, which stands for its
// version where nothing in it can.
func blobID(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

// lockFilePin is a lock file this package reads no versions from, as one
// pin, versioned by its blob on each side; none where TIP drops it.
func lockFilePin(top, base, tip, file string) []Pin {
	old, _ := git.Run("-C", top, "rev-parse", "--verify", "-q", base+":"+file)
	next, err := git.Run("-C", top, "rev-parse", "--verify", "-q", tip+":"+file)
	if err != nil || next == "" {
		return nil
	}
	return []Pin{{Key: file, File: file, Kind: "lockfile", Old: old, New: next, Line: 1}}
}

// commitish is a full or abbreviated commit ID.
var commitish = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// tagOf is ref where it is not a commit ID, else "".
func tagOf(ref string) string {
	if commitish.MatchString(ref) {
		return ""
	}
	return ref
}
