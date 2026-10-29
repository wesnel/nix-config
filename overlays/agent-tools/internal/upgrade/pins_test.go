package upgrade

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false",
		"-c", "tag.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit commits files, each "path=content", or "path=" to remove it.
func commit(t *testing.T, repo, message string, files ...string) string {
	t.Helper()
	for _, f := range files {
		path, content, _ := strings.Cut(f, "=")
		if content == "" {
			gitIn(t, repo, "rm", "-q", path)
			continue
		}
		full := filepath.Join(repo, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", path)
	}
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", message)
	return gitIn(t, repo, "rev-parse", "HEAD")
}

func newRepo(t *testing.T) string {
	t.Helper()
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	return repo
}

const lockBefore = `{
  "nodes": {
    "nixpkgs": {"locked": {"type": "github", "owner": "nixos", "repo": "nixpkgs", "rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      "narHash": "sha256-old=", "lastModified": 1700000000}},
    "same": {"locked": {"type": "github", "owner": "o", "repo": "same", "rev": "cccccccccccccccccccccccccccccccccccccccc"}},
    "gone": {"locked": {"type": "github", "owner": "o", "repo": "gone", "rev": "dddddddddddddddddddddddddddddddddddddddd"}},
    "private": {"locked": {"type": "git", "url": "ssh://git@example.com/x", "rev": "1111111111111111111111111111111111111111"}},
    "moved": {"locked": {"type": "github", "owner": "a", "repo": "one", "rev": "2222222222222222222222222222222222222222"}},
    "root": {"inputs": {"nixpkgs": "nixpkgs"}}
  },
  "root": "root",
  "version": 7
}
`

const lockAfter = `{
  "nodes": {
    "nixpkgs": {"locked": {"type": "github", "owner": "nixos", "repo": "nixpkgs", "rev": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      "narHash": "sha256-new=", "lastModified": 1700086400}},
    "same": {"locked": {"type": "github", "owner": "o", "repo": "same", "rev": "cccccccccccccccccccccccccccccccccccccccc"}},
    "added": {"locked": {"type": "github", "owner": "o", "repo": "added", "rev": "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}},
    "private": {"locked": {"type": "git", "url": "ssh://git@example.com/x", "rev": "3333333333333333333333333333333333333333"}},
    "moved": {"locked": {"type": "github", "owner": "b", "repo": "two", "rev": "4444444444444444444444444444444444444444"}},
    "root": {"inputs": {"nixpkgs": "nixpkgs"}}
  },
  "root": "root",
  "version": 7
}
`

const nixBefore = `{ fetchFromGitHub, buildGoModule }:
buildGoModule rec {
  pname = "tool";
  version = "1.2.0";
  src = fetchFromGitHub {
    owner = "acme";
    repo = "tool";
    rev = "v${version}";
    hash = "sha256-oldtool=";
  };
  vendorHash = "sha256-oldvendor=";
}
`

const nixAfter = `{ fetchFromGitHub, buildGoModule }:
buildGoModule rec {
  pname = "tool";
  version = "1.3.0";
  src = fetchFromGitHub {
    owner = "acme";
    repo = "tool";
    rev = "v${version}";
    hash = "sha256-newtool=";
  };
  vendorHash = "sha256-newvendor=";
}
`

// Pins reads each kind of pin file: a flake.lock by node, a go.mod's
// requirements and go line, a Nix fetcher's hunk, a workflow's actions,
// and other lock files as one pin each.
func TestPins(t *testing.T) {
	repo := newRepo(t)
	base := commit(t, repo, "Start", "flake.lock="+lockBefore,
		"go.mod=module x\n\ngo 1.25\n\nrequire (\n\tgithub.com/a/b v1.0.0\n\tgolang.org/x/mod v0.20.0 // indirect\n\texample.com/same v1.0.0\n)\n\nrequire github.com/c/d v0.0.0-20240101000000-abcdefabcdef\n",
		"go.sum=x\n", "pkgs/tool.nix="+nixBefore, "web/package-lock.json={}\n",
		".github/workflows/ci.yml=jobs:\n  x:\n    steps:\n      - uses: actions/checkout@v3\n      - uses: actions/setup-go@0aaccfd150d50ccaeb58ebd88d36e91967a5f35b # v5.0.1\n      - uses: ./local\n",
		"README.md=hi\n")
	tip := commit(t, repo, "Bump", "flake.lock="+lockAfter,
		"go.mod=module x\n\ngo 1.26\n\nrequire (\n\tgithub.com/a/b v1.1.0\n\tgolang.org/x/mod v0.21.0 // indirect\n\texample.com/same v1.0.0\n)\n\nrequire github.com/c/d v0.0.0-20240202000000-123456123456\n",
		"go.sum=y\n", "pkgs/tool.nix="+nixAfter, "web/package-lock.json={\"a\": 1}\n",
		".github/workflows/ci.yml=jobs:\n  x:\n    steps:\n      - uses: actions/checkout@v4\n      - uses: actions/setup-go@41dfa10bad2bb2ae585af6ee5bb4d7d973ad74ed # v5.1.0\n      - uses: ./local\n",
		"README.md=hello\n")
	pins, err := Pins(repo, base, tip)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Pin{}
	var keys []string
	for _, p := range pins {
		byKey[p.Key] = p
		keys = append(keys, p.Key)
	}
	want := []string{".github/workflows/ci.yml#x actions/checkout", ".github/workflows/ci.yml#x actions/setup-go",
		"flake.lock#added", "flake.lock#gone", "flake.lock#moved", "flake.lock#nixpkgs", "flake.lock#private",
		"go.mod#go", "go.mod#github.com/a/b", "go.mod#golang.org/x/mod", "go.mod#github.com/c/d",
		"pkgs/tool.nix#acme/tool", "web/package-lock.json"}
	if strings.Join(keys, " ") != strings.Join(want, " ") {
		t.Fatalf("pins\n%v\nwant\n%v", keys, want)
	}
	check := func(key string, ok func(Pin) bool) {
		t.Helper()
		if p := byKey[key]; !ok(p) {
			t.Errorf("%s: %+v", key, p)
		}
	}
	check("flake.lock#nixpkgs", func(p Pin) bool {
		return p.Moved() && p.Owner == "nixos" && p.Repo == "nixpkgs" && p.OldRef == strings.Repeat("a", 40) &&
			p.NewRef == strings.Repeat("b", 40) && p.OldHash == "sha256-old=" && p.NewHash == "sha256-new=" &&
			p.OldTime.Unix() == 1700000000 && p.NewTime.Unix() == 1700086400 && p.Line > 1
	})
	check("flake.lock#added", func(p Pin) bool { return !p.Moved() && p.Old == "" && p.New != "" })
	check("flake.lock#gone", func(p Pin) bool { return !p.Moved() && p.New == "" })
	check("flake.lock#private", func(p Pin) bool { return p.Moved() && p.Owner == "" && p.Type == "git" })
	// Another repository on each side has no history between them.
	check("flake.lock#moved", func(p Pin) bool { return p.Moved() && p.Owner == "" })
	check("go.mod#go", func(p Pin) bool { return p.Kind == "go" && p.Old == "1.25" && p.New == "1.26" })
	check("go.mod#github.com/a/b", func(p Pin) bool {
		return p.Kind == "go-module" && p.Owner == "a" && p.Repo == "b" && p.OldTag == "v1.0.0" && p.NewRef == "v1.1.0"
	})
	check("go.mod#golang.org/x/mod", func(p Pin) bool { return p.Owner == "golang" && p.Repo == "mod" && p.Old == "v0.20.0" })
	check("go.mod#github.com/c/d", func(p Pin) bool {
		return p.OldRef == "abcdefabcdef" && p.NewRef == "123456123456" && p.OldTag == "" && p.NewTag == ""
	})
	check("pkgs/tool.nix#acme/tool", func(p Pin) bool {
		return p.Kind == "nix" && p.Old == "1.2.0" && p.New == "1.3.0" && p.OldRef == "v1.2.0" && p.NewRef == "v1.3.0" &&
			p.OldTag == "v1.2.0" && p.OldHash == "sha256-oldtool=" && p.NewHash == "sha256-newtool=" && p.Line == 4
	})
	check(".github/workflows/ci.yml#x actions/setup-go", func(p Pin) bool {
		return p.Owner == "actions" && p.Repo == "setup-go" && p.OldTag == "v5.0.1" && p.NewTag == "v5.1.0" &&
			p.NewRef == "41dfa10bad2bb2ae585af6ee5bb4d7d973ad74ed" && p.Line == 5
	})
	check(".github/workflows/ci.yml#x actions/checkout", func(p Pin) bool { return p.OldTag == "v3" && p.NewTag == "v4" })
	check("web/package-lock.json", func(p Pin) bool { return p.Kind == "lockfile" && p.Moved() && p.Line == 1 })

	// The key's ID changes with the versions.
	a, b := byKey["go.mod#github.com/a/b"], byKey["go.mod#github.com/a/b"]
	b.New = "v1.2.0"
	if a.ID() == b.ID() || a.ID() != byKey["go.mod#github.com/a/b"].ID() || len(a.ID()) != 12 {
		t.Errorf("IDs %s %s", a.ID(), b.ID())
	}
}

// A Nix file's hunk that changes only a hash, beside no fetchFromGitHub,
// is a pin by its package name, without a repository.
func TestNixHashOnly(t *testing.T) {
	repo := newRepo(t)
	base := commit(t, repo, "Start", "x.nix={ fetchurl }:\n{\n  pname = \"thing\";\n  src = fetchurl {\n    url = \"https://e.example/x.tar.gz\";\n    sha256 = \"aaa\";\n  };\n}\n")
	tip := commit(t, repo, "Bump", "x.nix={ fetchurl }:\n{\n  pname = \"thing\";\n  src = fetchurl {\n    url = \"https://e.example/y.tar.gz\";\n    sha256 = \"bbb\";\n  };\n}\n")
	pins, err := Pins(repo, base, tip)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 1 || pins[0].Key != "x.nix#thing" || pins[0].Old != "aaa" || pins[0].New != "bbb" || pins[0].Owner != "" {
		t.Errorf("pins %+v", pins)
	}
}

// A drop and an add of the same dependency under another name are one
// moved pin: a flake node renamed with the same source, a Go module's
// next major version. A replace directive, a flake input's URL with no
// lock moved beside it, and a revision whose NAR hash moved are pins too.
func TestPinsPaired(t *testing.T) {
	repo := newRepo(t)
	lock := func(name, rev, nar, odd string) string {
		return `{"nodes": {"` + name + `": {"locked": {"type": "github", "owner": "nixos", "repo": "nixpkgs", "rev": "` + rev +
			`", "narHash": "sha256-x="}}, "same": {"locked": {"type": "github", "owner": "o", "repo": "same", "rev": "` +
			strings.Repeat("c", 40) + `", "narHash": "` + nar + `"}}, "` + odd + `": {"locked": {"type": "github", "owner": "o", ` +
			`"repo": "odd", "rev": "` + rev + `"}}, "root": {"inputs": {}}}, "root": "root", "version": 7}`
	}
	sub := "sub/flake.nix={\n  inputs = {\n    hm.url = \"github:nix-community/home-manager/release-24.05\";\n" +
		"    other = {\n      url = \"github:o/other\";\n    };\n  };\n}\n"
	base := commit(t, repo, "Start", "flake.lock="+lock("nixpkgs_2", strings.Repeat("a", 40), "sha256-same1=", "odd"),
		"go.mod=module x\n\nrequire github.com/a/b v1.9.0\n\nreplace github.com/q/r => github.com/q/r v1.0.0\n\nreplace (\n\tgithub.com/l/m => ../m\n)\n",
		sub, "one/flake.nix={ inputs.x.url = \"github:o/x/v1\"; inputs.y.url = \"github:o/y\"; }\n")
	commit(t, repo, "Bump one line", "one/flake.nix={ inputs.x.url = \"github:o/x/v2\"; inputs.y.url = \"github:o/y\"; }\n")
	tip := commit(t, repo, "Bump", "flake.lock="+lock("nixpkgs_3", strings.Repeat("b", 40), "sha256-same2=", "odd\\n``` ## Output"),
		"go.mod=module x\n\nrequire github.com/a/b/v2 v2.0.0\n\nreplace github.com/q/r => github.com/q/r v1.5.0\n\nreplace (\n\tgithub.com/l/m => ../m2\n)\n",
		strings.Replace(strings.Replace(sub, "release-24.05", "release-25.05", 1), "github:o/other", "github:o/other/v2", 1))
	pins, err := Pins(repo, base, tip)
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Pin{}
	for _, p := range pins {
		byKey[p.Key] = p
		if strings.ContainsAny(p.Key, "`\n") {
			t.Errorf("a key holds what the prompt would read as markup: %q", p.Key)
		}
	}
	check := func(key string, ok func(Pin) bool) {
		t.Helper()
		if p, found := byKey[key]; !found || !ok(p) {
			t.Errorf("%s: %+v in %v", key, p, pins)
		}
	}
	check("flake.lock#nixpkgs_2 to nixpkgs_3", func(p Pin) bool {
		return p.Moved() && p.Owner == "nixos" && p.OldRef == strings.Repeat("a", 40) && p.NewRef == strings.Repeat("b", 40)
	})
	check("flake.lock#same", func(p Pin) bool { return p.Old == "sha256-same1=" && p.New == "sha256-same2=" && p.HashOnly() })
	// A node's name is the change's own text: what the prompt would read
	// as markup is taken out of it.
	check("flake.lock#odd to odd________Output", func(p Pin) bool { return p.Moved() })
	check("go.mod#github.com/a/b/v2", func(p Pin) bool {
		return p.Moved() && p.Module == "github.com/a/b/v2" && p.OldModule == "github.com/a/b" && p.Old == "v1.9.0" &&
			p.New == "v2.0.0" && p.Owner == "a" && p.Repo == "b"
	})
	check("go.mod#replace github.com/q/r", func(p Pin) bool {
		return p.Moved() && p.Kind == "go-module" && p.Module == "github.com/q/r" && p.Old == "v1.0.0" && p.New == "v1.5.0"
	})
	check("go.mod#replace github.com/l/m", func(p Pin) bool { return p.Kind == "go-replace" && p.Old == "../m" && p.New == "../m2" })
	// A flake.nix whose flake.lock moves no pin beside it is a Nix file:
	// its changed `url = …` lines are unread pins of the file.
	check("sub/flake.nix#unread", func(p Pin) bool {
		return p.Moved() && p.Kind == "nix-unread" && p.Note == "(lines 3 at BASE, 5 at BASE, 3 at TIP, 5 at TIP)"
	})
	check("one/flake.nix#unread", func(p Pin) bool { return p.Moved() && p.Kind == "nix-unread" })
	for _, key := range []string{"flake.lock#nixpkgs_2", "flake.lock#nixpkgs_3", "go.mod#github.com/a/b"} {
		if _, found := byKey[key]; found {
			t.Errorf("%s is a pin of its own beside its pair", key)
		}
	}
}
