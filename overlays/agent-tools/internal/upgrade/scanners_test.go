package upgrade

import (
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/routes"
)

// The Nix scanner reads only what it reads, and fails closed: a changed
// line naming a hash, a rev, a URL, a version or a fetcher that no pin
// covers makes a file-level nix-unread pin.
func TestNixFailClosed(t *testing.T) {
	for name, c := range map[string][2]string{
		"comment swallows": {
			"{ src = fetchurl {\n  url = \"https://e.example/foo-1.0.tgz\";\n  # see x = \"\n  hash = \"sha256-OLD\";\n}; }\n",
			"{ src = fetchurl {\n  url = \"https://e.example/foo-2.0.tgz\";\n  # see x = \"\n  hash = \"sha256-NEW\";\n}; }\n"},
		"dollar escape": {
			"{ x = \"$${\"; src = fetchurl { url = \"https://e.example/a\"; hash = \"sha256-OLD\"; }; }\n",
			"{ x = \"$${\"; src = fetchurl { url = \"https://e.example/b\"; hash = \"sha256-NEW\"; }; }\n"},
		"indented string": {
			"{ src = fetchurl { url = \"https://e.example/a\"; hash = ''sha256-OLD''; }; }\n",
			"{ src = fetchurl { url = \"https://e.example/b\"; hash = ''sha256-NEW''; }; }\n"},
		"concatenation": {
			"{ src = fetchurl { url = \"https://e.example/a\"; hash = \"sha256-\" + \"OLD\"; }; }\n",
			"{ src = fetchurl { url = \"https://e.example/b\"; hash = \"sha256-\" + \"NEW\"; }; }\n"},
		"let-bound": {
			"let h = \"sha256-OLD\"; in { src = fetchurl { url = \"https://e.example/a\"; hash = h; }; }\n",
			"let h = \"sha256-NEW\"; in { src = fetchurl { url = \"https://e.example/b\"; hash = h; }; }\n"},
		"fetchTarball": {
			"{ src = builtins.fetchTarball \"https://e.example/a.tgz\"; }\n",
			"{ src = builtins.fetchTarball \"https://e.example/b.tgz\"; }\n"},
	} {
		pins := nixPins("pkgs/x.nix", c[0], c[1])
		if len(pins) == 0 {
			t.Errorf("%s: no pin", name)
			continue
		}
		for _, p := range pins {
			if p.Kind == "nix-unread" && (!p.Moved() || p.Key != "pkgs/x.nix#unread" || !strings.Contains(p.Note, "lines")) {
				t.Errorf("%s: %+v", name, p)
			}
		}
	}
	// A changed line that names none of them, or that a pin covers, is no
	// unread pin.
	drv := func(v string) string {
		return "{ fetchFromGitHub }:\n{\n  pname = \"t\";\n  version = \"" + v + "\";\n  meta.description = \"" + v + "\";\n}\n"
	}
	if pins := nixPins("pkgs/t.nix", drv("1"), drv("2")); len(pins) != 1 || pins[0].Kind != "nix" {
		t.Errorf("a pinned derivation: %+v", pins)
	}
	if pins := nixPins("pkgs/t.nix", "{ a = 1; }\n", "{ a = 2; }\n"); len(pins) != 0 {
		t.Errorf("no pin word: %+v", pins)
	}
	// The words are whole and in any case; a comment, and prose in a
	// string, name none.
	for name, c := range map[string]struct {
		line   string
		unread bool
	}{
		"cargoHash":    {`  cargoHash = lib.fakeHash;`, true},
		"SHA256":       {`  SHA256 = x;`, true},
		"fetchzip":     {`  src = fetchzip { inherit x; };`, true},
		"review":       {`  doReview = x;`, false},
		"prev":         {`  final: prev: { a = 1; }`, false},
		"rev-parse":    {`  cmd = run "git rev-parse HEAD";`, false},
		"comment":      {`  a = 1; # bump the version and hash`, false},
		"prose string": {`  description = "a tool to fetch the latest version";`, false},
		"ref":          {`  ref = "release-1.2";`, true},
		"getFlake":     {`  x = builtins.getFlake "github:o/r/bbbb";`, true},
		"goVersion":    {`  goVersion = "1.22";`, true},
		"srcRev":       {`  srcRev = x;`, true},
		"baseUrl":      {`  baseUrl = x;`, true},
		// A value alone may pin: a commit ID, an SRI or base32 hash, an
		// archive URL, a URL to an archive, an OCI image with a tag or a
		// digest.
		"tarball":       {`  u = "https://x/y/bbbb.tar.gz";`, true},
		"zip":           {`  u = "https://x/y/v1.2.zip";`, true},
		"interpolated":  {`  u = "https://x/y/foo-${v}.tar.xz";`, true},
		"in prose":      {`  d = "get it from https://x/y/foo.tgz now";`, true},
		"image tag":     {`  image = "grafana/grafana:11.2.0";`, true},
		"image digest":  {`  img = "ghcr.io/o/r@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";`, true},
		"host and port": {`  addr = "127.0.0.1:8080";`, false},
		"plain URL":     {`  u = "https://x/y/page.html";`, false},
		"commit ID":     {`  pinned = "0123456789abcdef0123456789abcdef01234567";`, true},
		"SRI":           {`  x = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";`, true},
		"base32":        {`  x = "0m1fl8bnc9y6xhcn3p0afcbz6mbdxbmsacm9pvsd1jmsq6mkn2ch";`, true},
		"archive":       {`  s = "https://e.example/o/r/archive/main.tar.gz";`, true},
		"ref in query":  {`  u = "git+https://x/y?ref=b";`, true},
		"flake ref":     {`    "github:o/r/bbbb"`, true},
		// A name's camelCase or snake_case part pins only in code.
		"sessionUrl":   {`  sessionUrl = false;`, true},
		"tool name":    {`  list = [ "a" "find_version" ];`, false},
		"env string":   {`  env = [ "OLLAMA_API_URL=http://o:1" ];`, false},
		"stateVersion": {`  home.stateVersion = "24.11";`, false},
	} {
		pins := nixPins("x.nix", "{\n}\n", "{\n"+c.line+"\n}\n")
		if got := len(pins) == 1 && pins[0].Kind == "nix-unread"; got != c.unread {
			t.Errorf("%s: %+v, want unread %v", name, pins, c.unread)
		}
	}
	// A line is read as the file has it: inside a string opened on
	// another line it is a string, and prose, but for its ${...}, and
	// after the string closes it is code.
	for name, c := range map[string]struct {
		old, next string
		unread    bool
	}{
		"prose": {"{\n  d = ''\n    Hosts it may reach.\n  '';\n}\n",
			"{\n  d = ''\n    Hosts it may reach, for fetching a version by URL.\n  '';\n}\n", false},
		"apostrophe":   {"{\n  s = ''\n    a\n  '';\n}\n", "{\n  s = ''\n    don't touch the version\n  '';\n}\n", false},
		"quote":        {"{\n  s = ''\n    echo hi\n  '';\n}\n", "{\n  s = ''\n    echo \"it is $1\n  '';\n}\n", false},
		"closed":       {"{\n  s = ''\n    a\n  ''; v = 1;\n}\n", "{\n  s = ''\n    a\n  ''; version = \"2\";\n}\n", true},
		"interpolated": {"{\n  s = ''\n    echo ${a}\n  '';\n}\n", "{\n  s = ''\n    echo ${src.rev}\n  '';\n}\n", true},
	} {
		if pins := nixPins("x.nix", c.old, c.next); (len(pins) > 0) != c.unread {
			t.Errorf("%s: %+v, want a pin %v", name, pins, c.unread)
		}
	}
}

// Every Nix change that has a pin routes to the upgrade review: the
// routing reads what the scanner reads.
func TestNixRouted(t *testing.T) {
	for name, c := range map[string][2]string{
		"fetchGit ref": {"{\n  src = builtins.fetchGit {\n    url = \"https://x/y\";\n    ref = \"release-1\";\n  };\n}\n",
			"{\n  src = builtins.fetchGit {\n    url = \"https://x/y\";\n    ref = \"release-2\";\n  };\n}\n"},
		"getFlake": {"{\n  f = builtins.getFlake \"github:o/r/aaaa\";\n}\n", "{\n  f = builtins.getFlake \"github:o/r/bbbb\";\n}\n"},
		"fetchTarball": {"import (fetchTarball \"https://github.com/NixOS/nixpkgs/archive/aaaa.tar.gz\") { }\n",
			"import (fetchTarball \"https://github.com/NixOS/nixpkgs/archive/bbbb.tar.gz\") { }\n"},
		"split fetchTarball": {"{\n  src = fetchTarball\n    \"https://github.com/o/r/archive/aaaa.tar.gz\";\n}\n",
			"{\n  src = fetchTarball\n    \"https://github.com/o/r/archive/bbbb.tar.gz\";\n}\n"},
		"tarball url": {"{\n  s = builtins.fetchTarball {\n    url = \"https://x/archive/aaaa.tar.gz\";\n  };\n}\n",
			"{\n  s = builtins.fetchTarball {\n    url = \"https://x/archive/bbbb.tar.gz\";\n  };\n}\n"},
		"goVersion": {"{\n  goVersion = \"1.22\";\n}\n", "{\n  goVersion = \"1.23\";\n}\n"},
		"srcRev":    {"{\n  srcRev = \"aaaa\";\n}\n", "{\n  srcRev = \"bbbb\";\n}\n"},
		"commit ID": {"{\n  pinned = \"0123456789abcdef0123456789abcdef01234567\";\n}\n",
			"{\n  pinned = \"1123456789abcdef0123456789abcdef01234567\";\n}\n"},
		"version": {"{\n  version = \"1\";\n}\n", "{\n  version = \"2\";\n}\n"},
		// A binding a pin reads moves the pin, whatever its name.
		"channel binding": {"let\n  release = \"24.05\";\n  pkgs = import (fetchTarball \"https://github.com/NixOS/nixpkgs/archive/nixos-${release}.tar.gz\") {};\n" +
			"  hm = fetchTarball \"https://github.com/nix-community/home-manager/archive/release-${release}.tar.gz\";\nin pkgs.mkShell {}\n",
			"let\n  release = \"24.11\";\n  pkgs = import (fetchTarball \"https://github.com/NixOS/nixpkgs/archive/nixos-${release}.tar.gz\") {};\n" +
				"  hm = fetchTarball \"https://github.com/nix-community/home-manager/archive/release-${release}.tar.gz\";\nin pkgs.mkShell {}\n"},
		"branch binding": {"let\n  branch = \"release-1.2\";\nin builtins.fetchGit {\n  url = \"https://example.com/y\";\n  ref = branch;\n}\n",
			"let\n  branch = \"release-1.3\";\nin builtins.fetchGit {\n  url = \"https://example.com/y\";\n  ref = branch;\n}\n"},
		// A line whose text stays but which moves into or out of a comment
		// or a string, or into another set, has changed.
		"comment toggle": {"{\n  x = fetchFromGitHub {\n    owner = \"o\";\n    repo = \"r\";\n    /*\n    rev = \"1111111111111111111111111111111111111111\";\n" +
			"    hash = \"sha256-NEWNEWNEWNEWNEWNEWNEWNEWNEWNEW=\";\n    */\n    rev = \"0000000000000000000000000000000000000000\";\n" +
			"    hash = \"sha256-OLDOLDOLDOLDOLDOLDOLDOLDOLDOLD=\";\n  };\n}\n",
			"{\n  x = fetchFromGitHub {\n    owner = \"o\";\n    repo = \"r\";\n    rev = \"1111111111111111111111111111111111111111\";\n" +
				"    hash = \"sha256-NEWNEWNEWNEWNEWNEWNEWNEWNEWNEW=\";\n    /*\n    */\n    /*\n    rev = \"0000000000000000000000000000000000000000\";\n" +
				"    hash = \"sha256-OLDOLDOLDOLDOLDOLDOLDOLDOLDOLD=\";\n    */\n  };\n}\n"},
		"string toggle": {"{\n  doc = ''\n  pkgs = import (fetchTarball \"https://example.com/n/archive/NEW.tar.gz\") {};\n  '';\n" +
			"  pkgs = import (fetchTarball \"https://example.com/n/archive/OLD.tar.gz\") {};\n}\n",
			"{\n  pkgs = import (fetchTarball \"https://example.com/n/archive/NEW.tar.gz\") {};\n  doc = ''\n" +
				"  pkgs = import (fetchTarball \"https://example.com/n/archive/OLD.tar.gz\") {};\n  '';\n}\n"},
		"url swap": {"{\n  a = builtins.fetchurl {\n    url = \"https://example.com/a-1.0.tar.gz\";\n  };\n  b = builtins.fetchurl {\n" +
			"    url = \"https://example.com/a-2.0.tar.gz\";\n  };\n}\n",
			"{\n  a = builtins.fetchurl {\n    url = \"https://example.com/a-2.0.tar.gz\";\n  };\n  b = builtins.fetchurl {\n" +
				"    url = \"https://example.com/a-1.0.tar.gz\";\n  };\n}\n"},
	} {
		repo := newRepo(t)
		base := commit(t, repo, "base", "x.nix="+c[0])
		tip := commit(t, repo, "tip", "x.nix="+c[1])
		pins, err := Pins(repo, base, tip)
		if err != nil || len(pins) == 0 {
			t.Errorf("%s: no pin (%v)", name, err)
			continue
		}
		rs, err := routes.Scan(repo, base, tip, routes.Builtin)
		if err != nil || !slices.ContainsFunc(rs, func(r routes.Route) bool { return r.Route == "upgrade" }) {
			t.Errorf("%s: routes %+v (%v), pins %+v", name, rs, err, pins)
		}
	}
}

// go.mod and go.work are read as the go command reads them: comments,
// tabs and a parenthesis against the keyword included.
func TestGoModParsed(t *testing.T) {
	for name, c := range map[string][2]string{
		"require( comment": {"module m\n\ngo 1.22\n\nrequire( // deps\n\tgolang.org/x/mod v0.20.0\n)\n",
			"module m\n\ngo 1.22\n\nrequire( // deps\n\tgolang.org/x/mod v0.21.0\n)\n"},
		"require tab (": {"module m\n\ngo 1.22\n\nrequire\t(\n\tgolang.org/x/mod v0.20.0\n)\n",
			"module m\n\ngo 1.22\n\nrequire\t(\n\tgolang.org/x/mod v0.21.0\n)\n"},
		"toolchain comment": {"module m\n\ngo 1.22\ntoolchain go1.23.0 // pinned\n", "module m\n\ngo 1.22\ntoolchain go1.24.0 // pinned\n"},
		"go comment":        {"module m\n\ngo 1.22 // min\n", "module m\n\ngo 1.24 // min\n"},
	} {
		if pins := goModPins("go.mod", c[0], c[1]); len(pins) != 1 || !pins[0].Moved() {
			t.Errorf("%s: %+v", name, pins)
		}
	}
	if pins := goModPins("go.mod", "module m\n", "module m\nrequire (\n"); len(pins) != 1 || pins[0].Kind != "go-mod-unreadable" {
		t.Errorf("unparsable: %+v", pins)
	}
	// A replacement moved to another module at the same version has moved,
	// and names a repository only where both sides are one module, but
	// for a major version's /vN.
	for name, c := range map[string][2]string{
		"another fork":      {"github.com/fork1/c v1.0.0", "github.com/fork2/c v1.0.0"},
		"another directory": {"github.com/a/b/compat v1.2.0", "github.com/a/b/legacy v1.2.0"},
		"another case":      {"github.com/fork/c v1.0.0", "github.com/Fork/c v1.0.0"},
	} {
		retarget := goModPins("go.mod", "module m\nrequire a.b/c v1.0.0\nreplace a.b/c => "+c[0]+"\n",
			"module m\nrequire a.b/c v1.0.0\nreplace a.b/c => "+c[1]+"\n")
		if len(retarget) != 1 || !retarget[0].Moved() || retarget[0].Owner != "" {
			t.Errorf("%s: %+v", name, retarget)
		}
	}
	// A module in a subdirectory of its repository is tagged under it.
	sub := goModPins("go.mod", "module m\nrequire github.com/a/b/sub v1.0.0\n", "module m\nrequire github.com/a/b/sub v1.1.0\n")
	if len(sub) != 1 || sub[0].Owner != "a" || sub[0].Repo != "b" || sub[0].OldTag != "sub/v1.0.0" || sub[0].NewRef != "sub/v1.1.0" {
		t.Errorf("subdirectory: %+v", sub)
	}
	same := goModPins("go.mod", "module m\nreplace a.b/c => github.com/fork/c v1.0.0\n", "module m\nreplace a.b/c => github.com/fork/c/v2 v2.0.0\n")
	if len(same) != 1 || !same[0].Moved() || same[0].Owner != "fork" {
		t.Errorf("one module: %+v", same)
	}
	if pins := goModPins("go.work", "go 1.25\n\nuse ./a\n\nreplace x.example/m => x.example/m v1.0.0\n",
		"go 1.26\n\nuse ./a\n\nreplace x.example/m => x.example/m v1.1.0\n"); len(pins) != 2 {
		t.Errorf("go.work: %+v", pins)
	}
}

// The workflow scanner reads only the block form, and fails closed: a
// changed line naming uses or docker:// that no pin covers makes a
// file-level actions-unread pin.
func TestActionsFailClosed(t *testing.T) {
	for name, c := range map[string][2]string{
		"flow":       {"jobs:\n  a:\n    steps: [{uses: actions/checkout@v4}]\n", "jobs:\n  a:\n    steps: [{uses: evil/checkout@main}]\n"},
		"flow item":  {"jobs:\n  a:\n    steps:\n      - {uses: actions/checkout@v4}\n", "jobs:\n  a:\n    steps:\n      - {uses: actions/checkout@main}\n"},
		"quoted key": {"jobs:\n  a:\n    steps:\n      - \"uses\": actions/checkout@v4\n", "jobs:\n  a:\n    steps:\n      - \"uses\": actions/checkout@main\n"},
		"folded":     {"jobs:\n  a:\n    steps:\n      - uses: >-\n          actions/checkout@v4\n", "jobs:\n  a:\n    steps:\n      - uses: >-\n          actions/checkout@main\n"},
		"docker":     {"jobs:\n  a:\n    steps:\n      - uses: docker://alpine:3.18\n", "jobs:\n  a:\n    steps:\n      - uses: docker://evil/alpine:latest\n"},
		"reusable":   {"jobs:\n  a:\n    uses: o/r/.github/workflows/x.yml@v1\n", "jobs:\n  a:\n    uses: o/r/.github/workflows/x.yml@main\n"},
	} {
		pins := actionPins(".github/workflows/ci.yml", c[0], c[1])
		if len(pins) == 0 {
			t.Errorf("%s: no pin", name)
		}
		for _, p := range pins {
			if p.Kind == "actions-unread" && (!p.Moved() || !strings.Contains(p.Note, "lines")) {
				t.Errorf("%s: %+v", name, p)
			}
		}
	}
	// A pin's own line is read.
	pins := actionPins("ci.yml", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n",
		"jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v5\n")
	if len(pins) != 1 || pins[0].Kind != "action" {
		t.Errorf("block form: %+v", pins)
	}
}
