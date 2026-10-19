package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pinsOf(t *testing.T, files ...[3]string) []Pin {
	t.Helper()
	repo := newRepo(t)
	var olds, news []string
	for _, f := range files {
		olds = append(olds, f[0]+"="+f[1])
		news = append(news, f[0]+"="+f[2])
	}
	base := commit(t, repo, "base", olds...)
	tip := commit(t, repo, "tip", news...)
	pins, err := Pins(repo, base, tip)
	if err != nil {
		t.Fatal(err)
	}
	return pins
}

func byKeys(pins []Pin) map[string]Pin {
	m := map[string]Pin{}
	for _, p := range pins {
		m[p.Key] = p
	}
	return m
}

// Any change to a flake.nix is one pin of the file, whatever form its
// inputs take, for the reviewer to read its diff: nothing evaluates it.
// The input names on its changed lines are a hint, nothing more.
func TestFlakeNixPin(t *testing.T) {
	old := "{\n  inputs.nixpkgs.url = \"github:NixOS/nixpkgs/nixos-25.05\";\n  outputs = _: {};\n}\n"
	for name, c := range map[string]struct{ next, hint string }{
		"comment decoy": {"{\n  inputs.nixpkgs.url = \"github:evil/nixpkgs/main\";\n  # inputs.nixpkgs.url = \"github:NixOS/nixpkgs/nixos-25.05\";\n  outputs = _: {};\n}\n", "nixpkgs"},
		"one-line set":  {"{\n  inputs.nixpkgs = { url = \"github:evil/nixpkgs/main\"; };\n  outputs = _: {};\n}\n", "nixpkgs"},
		"attr form":     {"{\n  inputs.nixpkgs = {\n    type = \"github\";\n    owner = \"evil\";\n  };\n  outputs = _: {};\n}\n", "nixpkgs"},
		"concat":        {"{\n  inputs.nixpkgs.url = \"github:\" + \"evil/nixpkgs/main\";\n  outputs = _: {};\n}\n", "nixpkgs"},
		"getFlake":      {"{\n  inputs = (builtins.getFlake \"git+ssh://e.example/x\").inputs;\n  outputs = _: {};\n}\n", ""},
		"whitespace":    {old + "\n", ""},
	} {
		pins := pinsOf(t, [3]string{"flake.nix", old, c.next})
		if len(pins) != 1 || pins[0].Key != "flake.nix" || pins[0].Kind != "flake-nix" || !pins[0].Moved() ||
			!strings.Contains(pins[0].Note, c.hint) {
			t.Errorf("%s: %+v", name, pins)
			continue
		}
		g := Gather(t.Context(), pins, Options{Limits: DefaultLimits})
		if g[0].Read() || !strings.Contains(g[0].Why, "read the diff of its inputs") {
			t.Errorf("%s: gathered %+v", name, g)
		}
	}
}

// A flake.lock node whose locked or original object moves in a field
// this does not read (host, submodules, lfs) has moved.
func TestFlakeLockIdentity(t *testing.T) {
	a := strings.Repeat("a", 40)
	lock := func(locked, original string) []byte {
		return []byte(`{"nodes": {"x": {"locked": {"type": "github", "owner": "o", "repo": "x", "rev": "` + a +
			`", "narHash": "sha256-x="` + locked + `}, "original": {"type": "github", "owner": "o", "repo": "x"` + original +
			`}}, "root": {"inputs": {"x": "x"}}}, "root": "root", "version": 7}`)
	}
	base := lock("", "")
	for name, next := range map[string][]byte{
		"host":       lock(`, "host": "evil.example"`, ""),
		"submodules": lock(`, "submodules": true`, ""),
		"original":   lock("", `, "ref": "evil"`),
		"nested":     lock(`, "extra": {"a": 1}`, ""),
	} {
		if pins := flakePins("flake.lock", base, next); len(pins) != 1 || !pins[0].Moved() {
			t.Errorf("%s: %+v", name, pins)
		}
	}
	// The same objects in another order, or spacing, have not moved.
	reordered := []byte(`{"version": 7, "root": "root", "nodes": {"root": {"inputs": {"x": "x"}}, "x": {"original": {"repo": "x",
		"owner": "o", "type": "github"}, "locked": {"narHash": "sha256-x=", "rev": "` + a + `", "repo": "x", "owner": "o", "type": "github"}}}}`)
	if pins := flakePins("flake.lock", base, reordered); len(pins) != 0 {
		t.Errorf("reordered: %+v", pins)
	}
}

// A flake.lock that is not JSON is one unreadable pin.
func TestFlakeLockUnreadable(t *testing.T) {
	old := []byte(`{"nodes": {"root": {}}, "root": "root", "version": 7}`)
	if pins := flakePins("flake.lock", old, []byte("{")); len(pins) != 1 || pins[0].Kind != "flake-unreadable" || !pins[0].Moved() {
		t.Errorf("%+v", pins)
	}
}

// Input paths are followed to any depth, through follows too.
func TestFlakeDeepInputs(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	mk := func(to string) []byte {
		n := func(name, inputs string) string {
			return `"` + name + `": {"inputs": {` + inputs + `}, "locked": {"type": "github", "owner": "o", "repo": "` + name +
				`", "rev": "` + a + `", "narHash": "sha256-` + name + `="}}`
		}
		return []byte(`{"nodes": {` + n("n1", `"n2": "n2"`) + `, ` + n("n2", `"n3": "n3"`) + `, ` + n("n3", `"nixpkgs": "`+to+`"`) +
			`, "good": {"locked": {"type": "github", "owner": "nixos", "repo": "nixpkgs", "rev": "` + a + `", "narHash": "sha256-g="}}` +
			`, "bad": {"locked": {"type": "github", "owner": "evil", "repo": "nixpkgs", "rev": "` + b + `", "narHash": "sha256-b="}}` +
			`, "root": {"inputs": {"n1": "n1", "x": "bad", "y": "good"}}}, "root": "root", "version": 7}`)
	}
	if got := keys(flakePins("flake.lock", mk("good"), mk("bad"))); got != "flake.lock#input n1/n2/n3/nixpkgs" {
		t.Errorf("depth 4: %s", got)
	}
}

// Each Nix derivation, or fetcher set, is one pin, however close its
// neighbours, and a fetcher on one line is read as one on many.
func TestNixPerDerivation(t *testing.T) {
	two := func(a, b string) string {
		return "{\n  a = fetchFromGitHub {\n    owner = \"oa\";\n    repo = \"ra\";\n    rev = \"" + a + "\";\n    hash = \"sha256-" + a +
			"\";\n  };\n  b = fetchFromGitHub {\n    owner = \"ob\";\n    repo = \"rb\";\n    rev = \"" + b + "\";\n    hash = \"sha256-" + b + "\";\n  };\n}\n"
	}
	pins := byKeys(pinsOf(t, [3]string{"pkgs/two.nix", two("a1", "b1"), two("a2", "b2")}))
	if a, b := pins["pkgs/two.nix#oa/ra"], pins["pkgs/two.nix#ob/rb"]; len(pins) != 2 || a.NewRef != "a2" || b.NewRef != "b2" ||
		a.OldRef != "a1" || b.OldRef != "b1" {
		t.Errorf("two close: %+v", pins)
	}
	one := func(rev, hash string) string {
		return "{ fetchFromGitHub }:\n{\n  src = fetchFromGitHub { owner = \"o\"; repo = \"r\"; rev = \"" + rev + "\"; hash = \"" + hash + "\"; };\n}\n"
	}
	pins = byKeys(pinsOf(t, [3]string{"pkgs/x.nix", one("v1.0", "sha256-A="), one("v2.0", "sha256-B=")}))
	if p := pins["pkgs/x.nix#o/r"]; len(pins) != 1 || !p.Moved() || p.OldRef != "v1.0" || p.NewRef != "v2.0" || p.NewHash != "sha256-B=" {
		t.Errorf("one line: %+v", pins)
	}
	// A derivation's version and its fetcher's hash are one pin.
	drv := func(version, hash, vendor string) string {
		return "{ fetchFromGitHub, buildGoModule }:\nbuildGoModule rec {\n  pname = \"t\";\n  version = \"" + version +
			"\";\n  src = fetchFromGitHub {\n    owner = \"acme\";\n    repo = \"t\";\n    rev = \"v${version}\";\n    hash = \"" + hash +
			"\";\n  };\n  vendorHash = \"" + vendor + "\";\n}\n"
	}
	pins = byKeys(pinsOf(t, [3]string{"pkgs/t.nix", drv("1.0", "sha256-A=", "sha256-V="), drv("1.1", "sha256-B=", "sha256-W=")}))
	if p := pins["pkgs/t.nix#acme/t"]; len(pins) != 1 || p.Old != "1.0" || p.New != "1.1" || p.OldRef != "v1.0" || p.NewRef != "v1.1" {
		t.Errorf("a derivation: %+v", pins)
	}
	// sha512 and outputHash pin too; braces in strings and comments do not
	// open a set.
	url := func(v, h string) string {
		return "{\n  # a { brace\n  src = fetchurl {\n    url = \"https://e.com/y-" + v + ".tgz?x={\";\n    sha512 = \"" + h +
			"\";\n  };\n  out = mkDerivation { pname = \"z\"; outputHash = \"" + h + "\"; };\n}\n"
	}
	pins = byKeys(pinsOf(t, [3]string{"pkgs/y.nix", url("1", "AAA"), url("2", "BBB")}))
	if len(pins) != 2 || !pins["pkgs/y.nix#src"].Moved() || !pins["pkgs/y.nix#z"].Moved() {
		t.Errorf("sha512 and outputHash: %+v", pins)
	}
}

// A workflow's action is a pin per occurrence: its job, its name, and
// that name's place among the job's uses of it.
func TestActionOccurrences(t *testing.T) {
	old := "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n  b:\n    steps:\n      - uses: actions/checkout@v4\n"
	next := "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n  b:\n    steps:\n      - uses: actions/checkout@0123456789abcdef0123456789abcdef01234567\n"
	pins := actionPins(".github/workflows/ci.yml", old, next)
	if len(pins) != 1 || pins[0].Key != ".github/workflows/ci.yml#b actions/checkout" || pins[0].Line != 7 {
		t.Errorf("pins %+v", pins)
	}
	// A step inserted before another moves no other action; the second
	// use of a name in a job is its own pin.
	inserted := "jobs:\n  a:\n    steps:\n      - uses: actions/setup-go@v5\n      - uses: actions/checkout@v4\n      - uses: actions/checkout@v3\n"
	pins = actionPins("ci.yml", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n      - uses: actions/checkout@v3\n", inserted)
	if len(pins) != 1 || pins[0].Key != "ci.yml#a actions/setup-go" || pins[0].Old != "" {
		t.Errorf("an inserted step: %+v", pins)
	}
	pins = actionPins("ci.yml", inserted, strings.Replace(inserted, "checkout@v3", "checkout@v5", 1))
	if len(pins) != 1 || pins[0].Key != "ci.yml#a actions/checkout #2" || pins[0].Old != "v3" {
		t.Errorf("the second use: %+v", pins)
	}
	// A use inserted before another of the same action at another ref is
	// added, the other unmoved.
	pins = actionPins("ci.yml", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n",
		"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v3\n      - uses: actions/checkout@v4\n")
	if len(pins) != 1 || pins[0].Old != "" || pins[0].New != "v3" || pins[0].Line != 4 {
		t.Errorf("an inserted use: %+v", pins)
	}
	// A use whose line changes but not its ref, or whose job is renamed,
	// has not moved, and its line is read.
	use := "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n"
	for name, next := range map[string]string{
		"named":     "jobs:\n  a:\n    steps:\n      - name: Check out\n        uses: actions/checkout@v4\n",
		"commented": "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4 # v4.2.2\n",
		"quoted":    "jobs:\n  a:\n    steps:\n      - uses: \"actions/checkout@v4\"\n",
		"renamed":   "jobs:\n  b:\n    steps:\n      - uses: actions/checkout@v4\n",
	} {
		if pins := actionPins("ci.yml", use, next); len(pins) != 0 {
			t.Errorf("%s: %+v", name, pins)
		}
	}
	// Every pin has a key of its own.
	pins = actionPins("ci.yml", "jobs:\n  a:\n    steps:\n      - uses: o/r@p1\n      - uses: o/r@p2\n      - uses: o/r@q\n",
		"jobs:\n  a:\n    steps:\n      - uses: o/r@q\n      - uses: o/r@s\n")
	if keys := byKeys(pins); len(pins) != 2 || len(keys) != 2 {
		t.Errorf("colliding keys: %+v", pins)
	}
	// A composite action's steps are read too.
	composite := byKeys(pinsOf(t, [3]string{".github/actions/x/action.yml", "runs:\n  steps:\n    - uses: actions/setup-go@v4\n",
		"runs:\n  steps:\n    - uses: actions/setup-go@v5\n"}))
	if p := composite[".github/actions/x/action.yml#actions/setup-go"]; !p.Moved() {
		t.Errorf("composite: %+v", composite)
	}
}

// A set the scanner keys by its place is paired by what it pins: one
// dropped before another moves neither. A unit moved to another file has
// moved, not been dropped and added.
func TestNixUnitsPaired(t *testing.T) {
	image := func(name, digest string) string {
		return "(pkgs.dockerTools.pullImage { imageName = \"" + name + "\"; imageDigest = \"" + digest + "\"; })\n"
	}
	both := "[\n" + image("a", "sha256:1") + image("b", "sha256:2") + "]\n"
	pins := nixPins("x.nix", both, "[\n"+image("b", "sha256:2")+"]\n")
	if len(pins) != 1 || pins[0].Old != "sha256:1" || pins[0].New != "" {
		t.Errorf("a set dropped: %+v", pins)
	}
	drv := func(v string) string {
		return "{\n  foo = mkDerivation {\n    pname = \"foo\";\n    version = \"" + v + "\";\n  };\n}\n"
	}
	if pins := pinsOf(t, [3]string{"a.nix", drv("1"), "{\n}\n"}, [3]string{"b.nix", "{\n}\n", drv("1")}); len(pins) != 0 {
		t.Errorf("moved whole: %+v", pins)
	}
	pins = pinsOf(t, [3]string{"a.nix", drv("1"), "{\n}\n"}, [3]string{"b.nix", "{\n}\n", drv("2")})
	if len(pins) != 1 || pins[0].Old != "1" || pins[0].New != "2" || pins[0].File != "b.nix" {
		t.Errorf("moved and bumped: %+v", pins)
	}
}

// Pins of one repository between the same two versions, as one nixpkgs
// bump seen through many input paths, are one pin naming every key.
func TestMergedPins(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	lock := func(to string) string {
		return `{"nodes": {"nixpkgs": ` + flakeNode("nixos", a, "") + `, "nixpkgs_2": ` + flakeNode("nixos", b, "") +
			`, "hm": {"inputs": {"nixpkgs": "` + to + `"}, "locked": {"type": "github", "owner": "nix-community", "repo": "home-manager", ` +
			`"rev": "` + a + `", "narHash": "sha256-h="}}, "sops": {"inputs": {"nixpkgs": "` + to + `"}, "locked": {"type": "github", ` +
			`"owner": "mic92", "repo": "sops-nix", "rev": "` + a + `", "narHash": "sha256-s="}}, ` +
			`"root": {"inputs": {"nixpkgs": "` + to + `", "hm": "hm", "sops": "sops"}}}, "root": "root", "version": 7}`
	}
	pins := pinsOf(t, [3]string{"flake.lock", lock("nixpkgs"), lock("nixpkgs_2")})
	var moved []Pin
	for _, p := range pins {
		if p.Owner == "nixos" && p.Moved() {
			moved = append(moved, p)
		}
	}
	if len(moved) != 1 || moved[0].Key != "flake.lock#input hm/nixpkgs" ||
		strings.Join(moved[0].Covers, " ") != "flake.lock#input nixpkgs flake.lock#input sops/nixpkgs" {
		t.Errorf("merged: %+v", moved)
	}
	// A merged pin's ID is its keys', whichever leads; a pin alone keeps
	// its own.
	ab := Pin{Key: "x#b", Covers: []string{"x#a"}, Old: "1", New: "2"}
	ba := Pin{Key: "x#a", Covers: []string{"x#b"}, Old: "1", New: "2"}
	sum := sha256.Sum256([]byte("x#a\x001\x002"))
	if ab.ID() != ba.ID() || (Pin{Key: "x#a", Old: "1", New: "2"}).ID() != hex.EncodeToString(sum[:])[:12] || ab.ID() == (Pin{Key: "x#a", Old: "1", New: "2"}).ID() {
		t.Errorf("IDs %s %s", ab.ID(), ba.ID())
	}
}

// niv's and npins' sources, go.work, and a go.sum hash that moved are
// pins.
func TestOtherPinFiles(t *testing.T) {
	niv := func(rev string) string {
		return `{"nixpkgs": {"owner": "NixOS", "repo": "nixpkgs", "rev": "` + rev + `", "sha256": "x` + rev + `", "type": "tarball"}}`
	}
	npins := func(rev string) string {
		return `{"pins": {"hm": {"type": "Git", "repository": {"type": "GitHub", "owner": "nix-community", "repo": "home-manager"}, ` +
			`"branch": "master", "revision": "` + rev + `", "hash": "h` + rev + `"}}, "version": 5}`
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	pins := byKeys(pinsOf(t,
		[3]string{"nix/sources.json", niv(a), niv(b)},
		[3]string{"npins/sources.json", npins(a), npins(b)},
		[3]string{"go.work", "go 1.25\n\nuse ./a\n", "go 1.26\n\nuse ./a\n"},
		[3]string{"go.sum", "example.com/m v1.0.0 h1:AAAA=\nexample.com/m v1.0.0/go.mod h1:BBBB=\n",
			"example.com/m v1.0.0 h1:CCCC=\nexample.com/m v1.0.0/go.mod h1:BBBB=\n"}))
	if p := pins["nix/sources.json#nixpkgs"]; !p.Moved() || p.Owner != "NixOS" || p.OldRef != a || p.NewRef != b {
		t.Errorf("niv: %+v", pins)
	}
	if p := pins["npins/sources.json#hm"]; !p.Moved() || p.Owner != "nix-community" || p.Repo != "home-manager" || p.NewRef != b {
		t.Errorf("npins: %+v", pins)
	}
	if p := pins["go.work#go"]; !p.Moved() || p.New != "1.26" {
		t.Errorf("go.work: %+v", pins)
	}
	if p := pins["go.sum#example.com/m@v1.0.0"]; !p.HashOnly() || p.Old != "h1:AAAA=" || p.New != "h1:CCCC=" {
		t.Errorf("go.sum: %+v", pins)
	}
	if _, ok := pins["go.sum#example.com/m@v1.0.0/go.mod"]; ok {
		t.Error("an unchanged go.sum line is a pin")
	}
}

// Pins whose keys collide are told apart by an index.
func TestUniqueKeys(t *testing.T) {
	two := "{\n  a = {\n    pname = \"same\";\n    version = \"1\";\n  };\n  b = {\n    pname = \"same\";\n    version = \"1\";\n  };\n}\n"
	pins := pinsOf(t, [3]string{"x.nix", two, strings.ReplaceAll(two, `"1"`, `"2"`)})
	if got := keys(pins); got != "x.nix#same x.nix#same #2" {
		t.Errorf("keys %s", got)
	}
}

// A clone's replace refs and grafts are never followed, and a shallow
// clone is refused.
func TestCloneReplaceGrafts(t *testing.T) {
	clones, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(clones, "o", "r")
	os.MkdirAll(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	a := commit(t, repo, "v1", "f=1")
	b := commit(t, repo, "Add a backdoor to the auth check", "f=2")
	fake := gitIn(t, repo, "commit-tree", gitIn(t, repo, "rev-parse", b+"^{tree}"), "-p", a, "-m", "Fix a typo in the README")
	gitIn(t, repo, "replace", b, fake)
	got, err := cloneLog(context.Background(), clones, Pin{Owner: "o", Repo: "r", OldRef: a, NewRef: b})
	if err != nil || !strings.Contains(got.Body, "backdoor") || strings.Contains(got.Body, "typo") {
		t.Errorf("replace: %q %v", got.Body, err)
	}
	gitIn(t, repo, "replace", "-d", b)
	c := commit(t, repo, "Remove signature checks", "f=3")
	d := commit(t, repo, "v2", "f=4")
	os.WriteFile(filepath.Join(repo, ".git", "info", "grafts"), []byte(d+" "+a+"\n"), 0o644)
	t.Setenv("GIT_GRAFT_FILE", filepath.Join(repo, ".git", "info", "grafts"))
	got, err = cloneLog(context.Background(), clones, Pin{Owner: "o", Repo: "r", OldRef: a, NewRef: d})
	if err != nil || !strings.Contains(got.Body, "Remove signature checks") {
		t.Errorf("grafts hide %s: %q %v", c[:7], got.Body, err)
	}
	os.WriteFile(filepath.Join(repo, ".git", "shallow"), []byte(a+"\n"), 0o644)
	if _, err := cloneLog(context.Background(), clones, Pin{Owner: "o", Repo: "r", OldRef: a, NewRef: d}); err == nil ||
		!strings.Contains(err.Error(), "shallow") {
		t.Errorf("a shallow clone: %v", err)
	}
}
