package upgrade

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// sourcePin is one entry of niv's or npins' sources: its GitHub
// repository, where it names one, its revision, and its hash.
type sourcePin struct{ owner, repo, rev, hash, url string }

// sourcesPins are the entries of a sources file, read by read, whose
// revision, hash or URL differs between old and next, by name. A file
// that is not JSON is one unreadable pin.
func sourcesPins(file, kind string, old, next []byte, read func([]byte) (map[string]sourcePin, error)) []Pin {
	a, errA := read(old)
	b, errB := read(next)
	if err := cmp.Or(errA, errB); err != nil {
		return []Pin{{Key: file, File: file, Kind: kind + "-unreadable", Old: cmp.Or(blobID(old), "none"),
			New: cmp.Or(blobID(next), "none"), Note: err.Error(), Line: 1}}
	}
	names := map[string]bool{}
	for n := range a {
		names[n] = true
	}
	for n := range b {
		names[n] = true
	}
	var pins []Pin
	for _, name := range slices.Sorted(maps.Keys(names)) {
		sa, inA := a[name]
		sb, inB := b[name]
		if sa == sb {
			continue
		}
		version := func(s sourcePin, in bool) string {
			if !in {
				return ""
			}
			return cmp.Or(s.rev, s.hash, s.url, "none")
		}
		p := Pin{Key: file + "#" + nodeName(name), File: file, Kind: kind, Old: version(sa, inA), New: version(sb, inB),
			OldRef: sa.rev, NewRef: sb.rev, OldHash: sa.hash, NewHash: sb.hash}
		if p.Old == p.New {
			p.Old, p.New = sa.hash+sa.url, sb.hash+sb.url
		}
		if inA && inB && sa.owner != "" && strings.EqualFold(sa.owner, sb.owner) && strings.EqualFold(sa.repo, sb.repo) {
			p.Owner, p.Repo = sb.owner, sb.repo
		}
		pins = append(pins, p)
	}
	return pins
}

// readNiv is niv's nix/sources.json.
func readNiv(data []byte) (map[string]sourcePin, error) {
	out := map[string]sourcePin{}
	if len(data) == 0 {
		return out, nil
	}
	var entries map[string]struct {
		Owner  string `json:"owner"`
		Repo   string `json:"repo"`
		Rev    string `json:"rev"`
		Sha256 string `json:"sha256"`
		URL    string `json:"url"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	for name, e := range entries {
		out[name] = sourcePin{owner: e.Owner, repo: e.Repo, rev: e.Rev, hash: e.Sha256, url: e.URL}
	}
	return out, nil
}

// readNpins is npins' npins/sources.json.
func readNpins(data []byte) (map[string]sourcePin, error) {
	out := map[string]sourcePin{}
	if len(data) == 0 {
		return out, nil
	}
	var file struct {
		Pins map[string]struct {
			Repository struct {
				Type  string `json:"type"`
				Owner string `json:"owner"`
				Repo  string `json:"repo"`
				URL   string `json:"url"`
			} `json:"repository"`
			Revision string `json:"revision"`
			Hash     string `json:"hash"`
			URL      string `json:"url"`
		} `json:"pins"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	for name, e := range file.Pins {
		s := sourcePin{rev: e.Revision, hash: e.Hash, url: cmp.Or(e.URL, e.Repository.URL)}
		if e.Repository.Type == "GitHub" {
			s.owner, s.repo = e.Repository.Owner, e.Repository.Repo
		}
		out[name] = s
	}
	return out, nil
}

func nivPins(file string, old, next []byte) []Pin {
	return sourcesPins(file, "niv", old, next, readNiv)
}

func npinsPins(file string, old, next []byte) []Pin {
	return sourcesPins(file, "npins", old, next, readNpins)
}
