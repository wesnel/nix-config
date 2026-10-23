package upgrade

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/wesnel/agent-tools/internal/pinscan"
)

// nixVersions are the attributes a Nix file pins a version by, in the
// order a pin's version is taken from the first that moved.
var nixVersions = []string{"version", "rev", "tag", "finalImageTag", "imageDigest", "hash", "sha256", "sha512", "outputHash",
	"vendorHash", "cargoHash", "npmDepsHash"}

// nixUnit is one pinned thing of a Nix file: a derivation, or a fetcher
// set of its own, with the attributes it and the sets in it set, the
// first of each, its GitHub repository where a fetchFromGitHub call in it
// names one, and where each attribute's value starts.
type nixUnit struct {
	key                 string
	attrs               map[string]string
	at                  map[string]int
	owner, repo         string
	githubRev, githubTg string
	// first and last are the lines, from 1, of the unit's set, or of its
	// attributes for the file's own bindings.
	first, last int
}

// lineAt is the line, from 1, of offset at in text.
func lineAt(text string, at int) int { return strings.Count(text[:min(at, len(text))], "\n") + 1 }

// nixUnits are the pinned things of a Nix file's text, by key: each set
// that sets a pin attribute belongs to the nearest set around it, itself
// or up to two out, that sets a pname or a version, a derivation, else to
// itself. A unit's key is its GitHub owner/repo, else its pname, else the
// attribute it is assigned to, else its imageName or name, else its place
// among the units, with `#2` and on for a key an earlier unit has.
func nixUnits(text string) map[string]nixUnit {
	sets, _ := pinscan.NixSets(text)
	has := func(s pinscan.NixSet, names ...string) bool {
		return slices.ContainsFunc(names, func(n string) bool { _, ok := s.Attrs[n]; return ok })
	}
	unitOf := map[int]bool{}
	for i, s := range sets {
		if !has(s, nixVersions...) {
			continue
		}
		u := i
		// The file's own bindings are a unit only for what they pin
		// themselves.
		for j, up := i, 0; j > 0 && up <= 2; j, up = sets[j].Parent, up+1 {
			if has(sets[j], "pname", "version") {
				u = j
				break
			}
		}
		unitOf[u] = true
	}
	inside := func(i, root int) bool {
		for ; i >= 0; i = sets[i].Parent {
			if i == root {
				return true
			}
		}
		return false
	}
	units := map[string]nixUnit{}
	count := map[string]int{}
	for _, root := range slices.Sorted(maps.Keys(unitOf)) {
		u := nixUnit{attrs: map[string]string{}, at: map[string]int{}}
		for i, s := range sets {
			if !inside(i, root) {
				continue
			}
			// A set inside another unit is that unit's.
			if i != root && unitOf[i] {
				continue
			}
			for name, v := range s.Attrs {
				if _, ok := u.attrs[name]; !ok {
					u.attrs[name], u.at[name] = v, s.At[name]
				}
			}
			if strings.HasSuffix(s.Call, "fetchFromGitHub") && s.Attrs["owner"] != "" && s.Attrs["repo"] != "" && u.owner == "" {
				u.owner, u.repo, u.githubRev, u.githubTg = s.Attrs["owner"], s.Attrs["repo"], s.Attrs["rev"], s.Attrs["tag"]
			}
		}
		key := u.attrs["pname"]
		if u.owner != "" {
			key = u.owner + "/" + u.repo
		}
		if root == 0 {
			key = cmp.Or(key, "file")
		}
		key = cmp.Or(key, sets[root].Name, u.attrs["imageName"], u.attrs["name"], fmt.Sprintf("set %d", len(units)+1))
		count[key]++
		if count[key] > 1 {
			key = fmt.Sprintf("%s #%d", key, count[key])
		}
		u.key = key
		u.first, u.last = lineAt(text, sets[root].Start), lineAt(text, sets[root].End)
		if root == 0 {
			u.first, u.last = len(text), 0
			for _, at := range u.at {
				u.first, u.last = min(u.first, lineAt(text, at)), max(u.last, lineAt(text, at))
			}
		}
		units[key] = u
	}
	return units
}

// nixPins are the pinned things of a Nix file whose pin attributes differ
// between old and next, one pin each: its version, else the first other
// pin attribute that moved, and, for a fetchFromGitHub call, its
// repository and the rev or tag on each side, a "v${version}" naming the
// version.
//
// The scanner reads Nix only as far as it goes, so it fails closed: a
// changed line that names what may pin a version (pinscan.NixPinLines)
// and lies in no unit a pin was made of is one more pin, `nix-unread`, of
// the file.
func nixPins(file, old, next string) []Pin {
	pins, unitsA, unitsB := nixUnitPins(file, old, next)
	removed, added := nixChangedLines(old, next)
	var unread []string
	for _, side := range []struct {
		lines []lineText
		units []nixUnit
		name  string
		pins  map[int]bool
	}{{removed, unitsA, "BASE", pinscan.NixPinLines(old)}, {added, unitsB, "TIP", pinscan.NixPinLines(next)}} {
		for _, l := range side.lines {
			if !side.pins[l.line] || slices.ContainsFunc(side.units, func(u nixUnit) bool {
				return l.line >= u.first && l.line <= u.last
			}) {
				continue
			}
			unread = append(unread, fmt.Sprintf("%d at %s", l.line, side.name))
		}
	}
	if len(unread) > 0 {
		line := 1
		if len(added) > 0 {
			line = added[0].line
		}
		pins = append(pins, Pin{Key: file + "#unread", File: file, Kind: "nix-unread", Old: cmp.Or(blobID([]byte(old)), "none"),
			New: cmp.Or(blobID([]byte(next)), "none"), Note: "(lines " + strings.Join(unread, ", ") + ")", Line: line})
	}
	return pins
}

// nixUnitPins are nixPins of the units the scanner reads, with the units
// a pin was made of on each side. A unit on one side and one on the other
// that pin the same values are one unmoved, whatever their keys, so that
// a unit keyed by its place moves no other when one is added or dropped
// before it; the rest pair by key.
func nixUnitPins(file, old, next string) ([]Pin, []nixUnit, []nixUnit) {
	a, b := nixUnits(old), nixUnits(next)
	same := func(x, y nixUnit) bool {
		return !slices.ContainsFunc(nixVersions, func(attr string) bool { return x.attrs[attr] != y.attrs[attr] })
	}
	for k, ua := range a {
		if ub, ok := b[k]; ok && same(ua, ub) {
			delete(a, k)
			delete(b, k)
		}
	}
	for _, ka := range slices.Sorted(maps.Keys(a)) {
		for _, kb := range slices.Sorted(maps.Keys(b)) {
			if same(a[ka], b[kb]) {
				delete(a, ka)
				delete(b, kb)
				break
			}
		}
	}
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var pins []Pin
	var unitsA, unitsB []nixUnit
	for _, key := range slices.Sorted(maps.Keys(keys)) {
		ua, inA := a[key]
		ub, inB := b[key]
		p := Pin{Key: file + "#" + key, File: file, Kind: "nix"}
		moved := ""
		for _, attr := range nixVersions {
			if ua.attrs[attr] != ub.attrs[attr] {
				moved = attr
				break
			}
		}
		if moved == "" {
			continue
		}
		if inA {
			unitsA = append(unitsA, ua)
		}
		if inB {
			unitsB = append(unitsB, ub)
		}
		pick := func(u nixUnit, in bool) string {
			if !in {
				return ""
			}
			return cmp.Or(u.attrs[moved], "none")
		}
		p.Old, p.New = pick(ua, inA), pick(ub, inB)
		if inB {
			p.Line = strings.Count(next[:ub.at[moved]], "\n") + 1
		}
		if inA && inB && ua.owner != "" && strings.EqualFold(ua.owner, ub.owner) && strings.EqualFold(ua.repo, ub.repo) {
			p.Owner, p.Repo = ub.owner, ub.repo
			ref := func(u nixUnit) string {
				r := strings.ReplaceAll(cmp.Or(u.githubRev, u.githubTg), "${version}", u.attrs["version"])
				if strings.Contains(r, "${") {
					return ""
				}
				return r
			}
			p.OldRef, p.NewRef = ref(ua), ref(ub)
			p.OldTag, p.NewTag = tagOf(p.OldRef), tagOf(p.NewRef)
			p.OldHash, p.NewHash = cmp.Or(ua.attrs["hash"], ua.attrs["sha256"]), cmp.Or(ub.attrs["hash"], ub.attrs["sha256"])
		}
		pins = append(pins, p)
	}
	return pins, unitsA, unitsB
}
