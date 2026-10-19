package upgrade

import (
	"cmp"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

// goMajor is a module path's major version suffix.
var goMajor = regexp.MustCompile(`/v[0-9]+$`)

// goTarget is what a replace directive points at: a module and its
// version, or a directory, with no version.
type goTarget struct{ path, version string }

// goModFile is what a go.mod or go.work says that pins, as the go command
// reads it: its go and toolchain lines, its required versions and its
// replacements, by module path, each with the paths in the order it lists
// them.
type goModFile struct {
	directives, requires map[string]string
	replaces             map[string]goTarget
	order, replaceOrder  []string
}

// goMod is file's text read as the go command reads it, a go.work by its
// name; an error where it does not parse.
func goMod(file, text string) (goModFile, error) {
	f := goModFile{directives: map[string]string{}, requires: map[string]string{}, replaces: map[string]goTarget{}}
	if text == "" {
		return f, nil
	}
	var replaces []*modfile.Replace
	if path.Base(file) == "go.work" {
		w, err := modfile.ParseWork(file, []byte(text), nil)
		if err != nil {
			return f, err
		}
		if w.Go != nil {
			f.directives["go"] = w.Go.Version
		}
		if w.Toolchain != nil {
			f.directives["toolchain"] = w.Toolchain.Name
		}
		replaces = w.Replace
	} else {
		// Parse, not ParseLax, which leaves out what applies only to the
		// main module: its replace and toolchain lines.
		m, err := modfile.Parse(file, []byte(text), nil)
		if err != nil {
			return f, err
		}
		if m.Go != nil {
			f.directives["go"] = m.Go.Version
		}
		if m.Toolchain != nil {
			f.directives["toolchain"] = m.Toolchain.Name
		}
		for _, r := range m.Require {
			if _, ok := f.requires[r.Mod.Path]; !ok {
				f.order = append(f.order, r.Mod.Path)
			}
			f.requires[r.Mod.Path] = r.Mod.Version
		}
		replaces = m.Replace
	}
	for _, r := range replaces {
		left := strings.TrimSpace(r.Old.Path + " " + r.Old.Version)
		if _, ok := f.replaces[left]; !ok {
			f.replaceOrder = append(f.replaceOrder, left)
		}
		f.replaces[left] = goTarget{r.New.Path, r.New.Version}
	}
	return f, nil
}

// goModulePin is the pin of a module moved from oldPath at oldVersion to
// newPath at newVersion, the two paths one module's but for a major
// version's suffix, or a replacement's two targets; either side's version
// "" where it has none. Two paths of different modules, even in one
// repository or differing only in case, have no one upstream history
// between them: the pin names no repository, and its versions carry their
// paths, so it has moved however the versions compare.
func goModulePin(key, file, oldPath, oldVersion, newPath, newVersion string) Pin {
	p := Pin{Key: key, File: file, Kind: "go-module", Module: cmp.Or(newPath, oldPath), Old: oldVersion, New: newVersion}
	if oldPath != p.Module {
		p.OldModule = oldPath
	}
	if oldPath != "" && newPath != "" && !sameModule(oldPath, newPath) {
		p.Old, p.New = oldPath+"@"+oldVersion, newPath+"@"+newVersion
		return p
	}
	p.Owner, p.Repo = githubOfModule(p.Module)
	p.OldRef, p.OldTag = moduleRef(cmp.Or(oldPath, p.Module), oldVersion)
	p.NewRef, p.NewTag = moduleRef(p.Module, newVersion)
	return p
}

// goModPins are the go and toolchain lines, the requirements and the
// replacements of a go.mod or go.work that differ between old and next. A
// module dropped and one added whose paths differ only by a major
// version's /vN are one moved. A replacement by a directory has no
// version: its directory stands for one. A file that does not parse is
// one unreadable pin.
func goModPins(file, old, next string) []Pin {
	a, errA := goMod(file, old)
	b, errB := goMod(file, next)
	if err := cmp.Or(errB, errA); err != nil {
		return []Pin{{Key: file, File: file, Kind: "go-mod-unreadable", Old: cmp.Or(blobID([]byte(old)), "none"),
			New: cmp.Or(blobID([]byte(next)), "none"), Note: err.Error(), Line: 1}}
	}
	var pins []Pin
	for _, d := range []string{"go", "toolchain"} {
		if a.directives[d] != b.directives[d] {
			pins = append(pins, Pin{Key: file + "#" + d, File: file, Kind: d, Old: a.directives[d], New: b.directives[d]})
		}
	}
	var dropped []string
	for _, m := range a.order {
		if _, ok := b.requires[m]; !ok {
			dropped = append(dropped, m)
		}
	}
	for _, m := range b.order {
		_, had := a.requires[m]
		switch {
		case had && a.requires[m] == b.requires[m]:
		case had:
			pins = append(pins, goModulePin(file+"#"+m, file, m, a.requires[m], m, b.requires[m]))
		default:
			i := slices.IndexFunc(dropped, func(d string) bool {
				return goMajor.ReplaceAllString(d, "") == goMajor.ReplaceAllString(m, "")
			})
			if i < 0 {
				pins = append(pins, goModulePin(file+"#"+m, file, "", "", m, b.requires[m]))
				continue
			}
			pins = append(pins, goModulePin(file+"#"+m, file, dropped[i], a.requires[dropped[i]], m, b.requires[m]))
			dropped = slices.Delete(dropped, i, i+1)
		}
	}
	for _, m := range dropped {
		pins = append(pins, goModulePin(file+"#"+m, file, m, a.requires[m], "", ""))
	}
	seen := map[string]bool{}
	for _, left := range append(slices.Clone(b.replaceOrder), a.replaceOrder...) {
		before, after := a.replaces[left], b.replaces[left]
		if seen[left] || before == after {
			continue
		}
		seen[left] = true
		key := file + "#replace " + left
		version := func(t goTarget) string { return cmp.Or(t.version, t.path) }
		if before.version == "" || after.version == "" {
			// A directory, or a side with no replacement, names no
			// version to read upstream changes between.
			pins = append(pins, Pin{Key: key, File: file, Kind: "go-replace", Old: version(before), New: version(after)})
			continue
		}
		pins = append(pins, goModulePin(key, file, before.path, before.version, after.path, after.version))
	}
	return pins
}

// goSumPins are the module versions a go.sum or go.work.sum checks
// against another hash at TIP than at BASE: the same version with other
// content, which no upstream history explains. A version added or
// dropped is go.mod's pin.
func goSumPins(file, old, next string) []Pin {
	sums := func(text string) map[string]string {
		m := map[string]string{}
		for _, line := range strings.Split(text, "\n") {
			if f := strings.Fields(line); len(f) == 3 {
				m[f[0]+"@"+f[1]] = f[2]
			}
		}
		return m
	}
	a, b := sums(old), sums(next)
	var pins []Pin
	for _, mv := range slices.Sorted(maps.Keys(b)) {
		if h, ok := a[mv]; ok && h != b[mv] {
			module, version, _ := strings.Cut(mv, "@")
			pins = append(pins, Pin{Key: file + "#" + mv, File: file, Kind: "go-sum", Module: module, Old: h, New: b[mv],
				OldRef: version, NewRef: version})
		}
	}
	return pins
}

// pseudo is a Go pseudo-version, whose last part is its commit.
var pseudo = regexp.MustCompile(`[-.]\d{14}-([0-9a-f]{12})(?:\+incompatible)?$`)

// moduleRef is the commit or tag version v of module names, and the tag
// where it is one: a module in a subdirectory of its repository is tagged
// as DIR/vX.
func moduleRef(module, v string) (ref, tag string) {
	if m := pseudo.FindStringSubmatch(v); m != nil {
		return m[1], ""
	}
	v = strings.TrimSuffix(v, "+incompatible")
	if dir := moduleDir(module); dir != "" && v != "" {
		v = dir + "/" + v
	}
	return v, v
}

// moduleDir is the directory of its GitHub repository a module's code
// lives in, but for a major version's /vN, or "" for the repository's
// top.
func moduleDir(module string) string {
	if owner, _ := githubOfModule(module); owner == "" {
		return ""
	}
	parts := strings.Split(goMajor.ReplaceAllString(module, ""), "/")
	return strings.Join(parts[min(3, len(parts)):], "/")
}

// sameModule reports whether module paths a and b are one module's, as
// the go command compares them, but for a major version's trailing /vN.
func sameModule(a, b string) bool {
	return goMajor.ReplaceAllString(a, "") == goMajor.ReplaceAllString(b, "")
}

// githubOfModule is the GitHub repository a module path's code lives in:
// github.com/OWNER/REPO, or the golang.org/x repositories' mirrors there.
func githubOfModule(m string) (string, string) {
	parts := strings.Split(m, "/")
	switch {
	case len(parts) >= 3 && parts[0] == "github.com":
		return parts[1], parts[2]
	case len(parts) >= 3 && parts[0] == "golang.org" && parts[1] == "x":
		return "golang", parts[2]
	}
	return "", ""
}
