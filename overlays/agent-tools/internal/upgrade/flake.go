package upgrade

import (
	"cmp"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/wesnel/agent-tools/internal/pinscan"
)

// flakeLocked is what this reads of a flake.lock node's locked source:
// its repository, revision and hashes. What it does not read still counts,
// as lockNode's identity.
type flakeLocked struct {
	Type         string `json:"type"`
	Owner        string `json:"owner"`
	Repo         string `json:"repo"`
	Rev          string `json:"rev"`
	NarHash      string `json:"narHash"`
	URL          string `json:"url"`
	Path         string `json:"path"`
	Dir          string `json:"dir"`
	LastModified int64  `json:"lastModified"`
}

// version is what names the source: its revision, else its NAR hash,
// and the directory of it the flake is in, where one is named.
func (l *flakeLocked) version() string {
	v := cmp.Or(l.Rev, l.NarHash)
	if l.Dir != "" {
		v += "?dir=" + l.Dir
	}
	return v
}

// source is where the node comes from, whatever its name and revision:
// what pairs a node dropped with one added under another name.
func (l *flakeLocked) source() string {
	s := ""
	switch {
	case l.Type == "github" && l.Owner != "" && l.Repo != "":
		s = "github:" + strings.ToLower(l.Owner+"/"+l.Repo)
	case l.URL != "":
		s = l.Type + ":" + l.URL
	case l.Path != "":
		s = l.Type + ":" + l.Path
	default:
		return ""
	}
	if l.Dir != "" {
		s += "?dir=" + l.Dir
	}
	return s
}

// time is when its revision was made, or the zero time.
func (l *flakeLocked) time() time.Time {
	if l.LastModified <= 0 {
		return time.Time{}
	}
	return time.Unix(l.LastModified, 0).UTC()
}

// lockNode is a flake.lock node that locks a source: what this reads of
// it, and its identity, its `locked` and `original` objects whole, every
// field of them, so that one this does not read (host, submodules, lfs)
// moving moves the node.
type lockNode struct {
	locked   *flakeLocked
	identity string
}

// flakeLock is what flake.lock says: its nodes, each with what it locks,
// what it was locked from and its inputs, and the root node's name.
type flakeLock struct {
	Nodes map[string]struct {
		Inputs   map[string]json.RawMessage `json:"inputs"`
		Locked   json.RawMessage            `json:"locked"`
		Original json.RawMessage            `json:"original"`
	} `json:"nodes"`
	Root string `json:"root"`
}

// readLock is the flake.lock data holds, or none for no data.
func readLock(data []byte) (flakeLock, error) {
	var l flakeLock
	if len(data) == 0 {
		return l, nil
	}
	return l, json.Unmarshal(data, &l)
}

// canonical is a JSON value with its objects' keys in order and no
// spacing, so that two encodings of one value compare equal.
func canonical(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// node is the node name of l, where it locks a source.
func (l flakeLock) node(name string) *lockNode {
	n, ok := l.Nodes[name]
	if !ok || len(n.Locked) == 0 || string(n.Locked) == "null" {
		return nil
	}
	var locked flakeLocked
	if json.Unmarshal(n.Locked, &locked) != nil {
		return nil
	}
	return &lockNode{locked: &locked, identity: canonical(n.Locked) + "\x00" + canonical(n.Original)}
}

func (l flakeLock) root() string { return cmp.Or(l.Root, "root") }

// flakeDepth bounds how deep input paths and follows are followed.
const flakeDepth = 16

// edge is the node an input of node points at: by name, or, for one that
// follows another, the node the root-relative path it follows resolves
// to; "" where there is none.
func (l flakeLock) edge(node, input string, depth int) string {
	raw, ok := l.Nodes[node].Inputs[input]
	if !ok || depth > flakeDepth {
		return ""
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return name
	}
	var follows []string
	if json.Unmarshal(raw, &follows) != nil {
		return ""
	}
	at := l.root()
	for _, f := range follows {
		if at = l.edge(at, f, depth+1); at == "" {
			return ""
		}
	}
	return at
}

// flakePaths bounds how many input paths one lock is read for.
const flakePaths = 5000

// inputs maps each input path from the root, as `nixpkgs` or
// `hm/nixpkgs`, to the node it resolves to, to any depth up to
// flakeDepth, through follows as through names.
func (l flakeLock) inputs() map[string]string {
	out := map[string]string{}
	var walk func(prefix, node string, depth int)
	walk = func(prefix, node string, depth int) {
		if depth >= flakeDepth {
			return
		}
		for _, input := range slices.Sorted(maps.Keys(l.Nodes[node].Inputs)) {
			if len(out) >= flakePaths {
				return
			}
			target := l.edge(node, input, 0)
			if target == "" {
				continue
			}
			path := strings.TrimPrefix(prefix+"/"+input, "/")
			out[path] = target
			walk(path, target, depth+1)
		}
	}
	walk("", l.root(), 0)
	return out
}

// nodeName is a flake.lock node's name as a pin's key holds it: anything
// but letters, digits and `_.+-` becomes `_`, since the name comes from
// the change and goes into the prompt.
func nodeName(name string) string {
	return strings.Map(func(c rune) rune {
		if c < 0x80 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_.+-", c)) {
			return c
		}
		return '_'
	}, name)
}

// lockedPin is the pin of a flake.lock's source as before at BASE and as
// after at TIP, either nil where it is not there, and whether it moved:
// its revision, else its NAR hash, else, where only a field this does not
// read moved, its locked and original objects' hash.
func lockedPin(key, file string, before, after *lockNode) (Pin, bool) {
	p := Pin{Key: key, File: file, Kind: "flake"}
	if before != nil {
		l := before.locked
		p.Old, p.OldRef, p.OldHash, p.OldTime, p.Type = l.version(), l.Rev, l.NarHash, l.time(), l.Type
	}
	if after != nil {
		l := after.locked
		p.New, p.NewRef, p.NewHash, p.NewTime, p.Type = l.version(), l.Rev, l.NarHash, l.time(), l.Type
	}
	if before != nil && after != nil && p.Old == p.New {
		switch {
		case before.locked.NarHash != after.locked.NarHash:
			p.Old, p.New = before.locked.NarHash, after.locked.NarHash
		case before.identity != after.identity:
			p.Old, p.New = blobID([]byte(before.identity)), blobID([]byte(after.identity))
		}
	}
	if p.Old == p.New {
		return p, false
	}
	switch {
	// Both sides must be the same GitHub repository for its history
	// between them to mean anything.
	case before != nil && after != nil && before.locked.Type != after.locked.Type:
		p.Type = before.locked.Type + " to " + after.locked.Type
	case after != nil && after.locked.Type == "github" && (before == nil ||
		strings.EqualFold(before.locked.Owner, after.locked.Owner) && strings.EqualFold(before.locked.Repo, after.locked.Repo)):
		p.Owner, p.Repo = after.locked.Owner, after.locked.Repo
	}
	return p, true
}

// flakePins are the nodes of a flake.lock added, removed or moved between
// old and next: by name, and a node dropped and one added under another
// name from the same source, as nixpkgs_2 renamed nixpkgs_3, as one moved.
// An input path that resolves to another node at TIP than at BASE, which
// locks another source, is a pin of its own, `input PATH`: what the
// configuration uses moved though no node may have. A lock that is not
// JSON is one unreadable pin.
func flakePins(file string, old, next []byte) []Pin {
	a, errA := readLock(old)
	b, errB := readLock(next)
	if err := cmp.Or(errA, errB); err != nil {
		return []Pin{{Key: file, File: file, Kind: "flake-unreadable", Old: cmp.Or(blobID(old), "none"),
			New: cmp.Or(blobID(next), "none"), Note: err.Error(), Line: 1}}
	}
	type pair struct{ before, after string }
	var pairs []pair
	var dropped []string
	for _, n := range slices.Sorted(maps.Keys(a.Nodes)) {
		if a.node(n) == nil {
			continue
		}
		if b.node(n) != nil {
			pairs = append(pairs, pair{n, n})
		} else {
			dropped = append(dropped, n)
		}
	}
	for _, n := range slices.Sorted(maps.Keys(b.Nodes)) {
		if b.node(n) == nil || a.node(n) != nil {
			continue
		}
		i := slices.IndexFunc(dropped, func(d string) bool {
			s := a.node(d).locked.source()
			return s != "" && s == b.node(n).locked.source()
		})
		if i >= 0 {
			pairs = append(pairs, pair{dropped[i], n})
			dropped = slices.Delete(dropped, i, i+1)
		} else {
			pairs = append(pairs, pair{"", n})
		}
	}
	for _, d := range dropped {
		pairs = append(pairs, pair{d, ""})
	}
	var pins []Pin
	for _, pr := range pairs {
		key := file + "#" + nodeName(cmp.Or(pr.after, pr.before))
		if pr.before != "" && pr.after != "" && pr.before != pr.after {
			key = file + "#" + nodeName(pr.before) + " to " + nodeName(pr.after)
		}
		if p, moved := lockedPin(key, file, a.node(pr.before), b.node(pr.after)); moved {
			pins = append(pins, p)
		}
	}
	// An input whose node kept its name is that node's pin.
	inputsA, inputsB := a.inputs(), b.inputs()
	for _, path := range slices.Sorted(maps.Keys(inputsB)) {
		from, to := inputsA[path], inputsB[path]
		if from == "" || from == to || a.node(from) == nil || b.node(to) == nil {
			continue
		}
		var parts []string
		for _, part := range strings.Split(path, "/") {
			parts = append(parts, nodeName(part))
		}
		key := file + "#input " + strings.Join(parts, "/")
		if p, moved := lockedPin(key, file, a.node(from), b.node(to)); moved {
			pins = append(pins, p)
		}
	}
	slices.SortFunc(pins, func(x, y Pin) int { return strings.Compare(x.Key, y.Key) })
	return pins
}

// flakeNixName is a name a flake.nix line may give an input, as
// `inputs.NAME`, `NAME.url`, `NAME.follows` or `NAME = {`.
var flakeNixName = regexp.MustCompile(`(?:\binputs\.([A-Za-z0-9_-]+)|\b([A-Za-z0-9_-]+)\.(?:url|follows|inputs|flake)\b|^\s*([A-Za-z0-9_-]+)\s*=\s*\{)`)

// flakeNixPin is a change to a flake.nix, as one pin of the file: what
// its inputs are is what nix evaluates them to, which this does not do,
// so its diff is the reviewer's to read. Its Note names, best effort, the
// input names its changed lines give, for the reviewer to start from,
// trusted for nothing else.
func flakeNixPin(file, old, next string) []Pin {
	removed, added := changedLines(old, next)
	names := map[string]bool{}
	for _, l := range append(removed, added...) {
		for _, m := range flakeNixName.FindAllStringSubmatch(l.text, -1) {
			if n := cmp.Or(m[1], m[2], m[3]); n != "" && n != "inputs" && n != "outputs" {
				names[nodeName(n)] = true
			}
		}
	}
	line := 1
	if len(added) > 0 {
		line = added[0].line
	}
	note := "no input name on its changed lines"
	if len(names) > 0 {
		note = "input names on its changed lines: " + strings.Join(slices.Sorted(maps.Keys(names)), ", ")
	}
	return []Pin{{Key: file, File: file, Kind: "flake-nix", Old: cmp.Or(blobID([]byte(old)), "none"),
		New: cmp.Or(blobID([]byte(next)), "none"), Note: note, Line: line}}
}

// lineText is a line of a file and its number, from 1.
type lineText struct {
	line int
	text string
}

// changedLines are pinscan.ChangedLines.
func changedLines(old, next string) (removed, added []lineText) {
	return lineTexts(pinscan.ChangedLines(old, next))
}

// nixChangedLines are pinscan.NixChangedLines.
func nixChangedLines(old, next string) (removed, added []lineText) {
	return lineTexts(pinscan.NixChangedLines(old, next))
}

func lineTexts(removed, added []pinscan.Line) ([]lineText, []lineText) {
	conv := func(ls []pinscan.Line) []lineText {
		var out []lineText
		for _, l := range ls {
			out = append(out, lineText{l.N, l.Text})
		}
		return out
	}
	return conv(removed), conv(added)
}
