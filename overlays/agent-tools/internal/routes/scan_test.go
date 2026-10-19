package routes

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/refusal"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"},
		args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// write commits files, each "path=content", or "path=" to remove it.
func write(t *testing.T, repo, message string, files ...string) string {
	t.Helper()
	for _, f := range files {
		path, content, _ := strings.Cut(f, "=")
		full := filepath.Join(repo, path)
		if content == "" {
			gitIn(t, repo, "rm", "-q", path)
			continue
		}
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, repo, "add", path)
	}
	gitIn(t, repo, "commit", "-q", "-m", message)
	return gitIn(t, repo, "rev-parse", "HEAD")
}

// scanRepo is a repository whose origin/main holds a server listening on
// a port, and a branch above it whose commits are named by their subjects.
func scanRepo(t *testing.T) (string, map[string]string) {
	t.Helper()
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	write(t, repo, "Add the server", "server.go=package main\n\nfunc serve() { net.Listen(\"tcp\", \":80\") }\n",
		"README.md=hi\n")
	gitIn(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
	gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	gitIn(t, repo, "checkout", "-q", "-b", "work")
	c := map[string]string{}
	c["notes"] = write(t, repo, "Write notes", "README.md=hello\n")
	c["exec"] = write(t, repo, "Run a helper", "run.go=package main\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n")
	c["deps"] = write(t, repo, "Add a dependency", "go.mod=module x\n\nrequire y v1\n")
	c["unlisten"] = write(t, repo, "Stop listening", "server.go=package main\n\nfunc serve() {}\n")
	c["agents"] = write(t, repo, "Allow a tool", ".claude/settings.json={}\n")
	c["docs"] = write(t, repo, "Document secrets", "docs/secret/x.md=x\n")
	c["comment"] = write(t, repo, "Mention exec in prose", "notes.txt=os/exec is not code here\n")
	c["flag"] = write(t, repo, "Take a flag", "main.go=package main\n\nvar v = flag.Bool(\"v\", false, \"\")\n")
	return repo, c
}

func routed(t *testing.T, got []Route) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, r := range got {
		out[r.Route] = r.Commits
	}
	return out
}

// The scan routes each commit by its changed paths and its added and
// removed lines, from where the change starts, and operator rules only add.
func TestScan(t *testing.T) {
	repo, c := scanRepo(t)
	base := MergeBase(repo, "work")
	if base != gitIn(t, repo, "rev-parse", "origin/main") {
		t.Fatalf("merge base %s", base)
	}
	got, err := Scan(repo, base, "work", Builtin)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{c["exec"], c["deps"], c["unlisten"], c["agents"]}
	if r := routed(t, got); strings.Join(r["security"], " ") != strings.Join(want, " ") ||
		strings.Join(r["compat"], " ") != c["flag"] || strings.Join(r["upgrade"], " ") != c["deps"] || len(r) != 3 {
		t.Errorf("routes %+v, want security for %v, compat for %s and upgrade for %s", got, want, c["flag"], c["deps"])
	}
	for _, r := range got {
		if len(r.Why) == 0 {
			t.Errorf("no reason given: %+v", r)
		}
	}

	extra := append(Builtin, Rule{Route: "security", Paths: []string{"docs/secret/**"}, Why: "secrets docs"})
	got, err = Scan(repo, base, "work", extra)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{c["exec"], c["deps"], c["unlisten"], c["agents"], c["docs"]}
	if r := routed(t, got); strings.Join(r["security"], " ") != strings.Join(want, " ") {
		t.Errorf("with an operator rule: %+v, want %v", got, want)
	}
}

// A path git quotes in a patch's headers, for a space, a quote or a
// control character in it, is matched by its lines as any other is.
func TestScanQuotedPaths(t *testing.T) {
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	base := write(t, repo, "Start", "README.md=hi\n")
	var want []string
	for _, name := range []string{"a b/run.go", "q\"uote.go", "tab\there.go", "ünï.go"} {
		want = append(want, write(t, repo, "Run from "+name, name+"=package main\n\nvar _ = exec.Command\n"))
	}
	// A change of type has two sections in the patch, for one path.
	os.Remove(filepath.Join(repo, "ünï.go"))
	os.Symlink("README.md", filepath.Join(repo, "ünï.go"))
	gitIn(t, repo, "add", "ünï.go")
	gitIn(t, repo, "commit", "-q", "-m", "Link it")
	want = append(want, gitIn(t, repo, "rev-parse", "HEAD"))
	got, err := Scan(repo, base, "HEAD", Builtin)
	if err != nil {
		t.Fatal(err)
	}
	if r := routed(t, got); strings.Join(r["security"], " ") != strings.Join(want, " ") {
		t.Errorf("routes %+v, want security for %v", got, want)
	}
}

func TestRuleCheck(t *testing.T) {
	for name, r := range map[string]Rule{
		"unknown route": {Route: "style", Paths: []string{"x"}, Why: "x"},
		"nothing":       {Route: "security", Why: "x"},
		"bad line":      {Route: "security", Lines: []string{"("}, Why: "x"},
		"no reason":     {Route: "security", Paths: []string{"x"}},
		"bad glob":      {Route: "security", Paths: []string{"[x"}, Why: "x"},
		"removed paths": {Route: "security", Paths: []string{"x"}, Removed: true, Why: "x"},
	} {
		var refused refusal.Refusal
		if err := r.Check(); !errors.As(err, &refused) {
			t.Errorf("%s: %v, want a refusal", name, err)
		}
	}
	for _, r := range Builtin {
		if err := r.Check(); err != nil {
			t.Errorf("built-in %+v: %v", r, err)
		}
	}
}

// The built-in compat rules route a commit that adds or removes a line
// defining what outlives it, and not one that only resembles it.
func TestCompatRules(t *testing.T) {
	rules, err := compile(Builtin)
	if err != nil {
		t.Fatal(err)
	}
	const plans = "overlays/agent-tools/internal/plans/log.go"
	for _, tc := range []struct {
		path, line string
		want       bool
	}{
		// Go flags.
		{"cmd/x/main.go", `	verbose := flags.Bool("verbose", false, "")`, true},
		{"cmd/x/main.go", `	timeout := flags.Duration("timeout", time.Minute, "")`, true},
		{"cmd/x/main.go", `	flag.StringVar(&name, "name", "", "the name")`, true},
		{"cmd/x/main.go", `	n := flag.Int("n", 1, "")`, true},
		{"cmd/x/main.go", `	fs.Var(&list, "path", "")`, true},
		{"cmd/x/main.go", `	cmdFlags.TextVar(&level, "level", slog.LevelInfo, "")`, true},
		{"cmd/x/main.go", `	flags.Parse(args)`, false},
		{"cmd/x/main.go", `	flag.Parse()`, false},
		{"cmd/x/main.go", `	slog.String("key", value)`, false},
		{"cmd/x/main.go", `	return b.String()`, false},
		{"cmd/x/main.go", `	flagName.String("x")`, false},
		{"README.md", `flags.Bool("verbose", false, "")`, false},
		// JSON tags.
		{"internal/x/x.go", "	Name string `json:\"name,omitempty\"`", true},
		{"internal/x/x.go", "	Name string `yaml:\"name\" json:\"-\"`", true},
		{"internal/x/x.go", "	Name string `jsonschema:\"required\"`", false},
		{"internal/x/x.go", "	Name string `yaml:\"name\"`", false},
		{"internal/x/x.go", `	data, err := json.Marshal(v)`, false},
		{"docs/x.md", "Name string `json:\"name\"`", false},
		// Nix options.
		{"modules/x/default.nix", `    enable = mkEnableOption "x";`, true},
		{"modules/x/default.nix", `    port = lib.mkOption {`, true},
		{"modules/x/default.nix", `    package = mkPackageOption pkgs "x" {};`, true},
		{"modules/x/default.nix", `  imports = [(mkRenamedOptionModule ["a"] ["b"])];`, true},
		{"modules/x/default.nix", `    port = lib.mkOptionDefault 80;`, false},
		{"modules/x/default.nix", `    config = mkIf cfg.enable {`, false},
		{"docs/options.md", "Each `mkOption` takes a type.", false},
		// Emacs customisations.
		{"lisp/x.el", `(defcustom x-width 80`, true},
		{"lisp/x.el", `  (defcustom x-width 80`, true},
		{"lisp/x.el", `(define-obsolete-variable-alias 'x-old 'x-new "1.2")`, true},
		{"lisp/x.el", `(defvar x-width 80)`, false},
		{"lisp/x.el", `(defcustom-x x-width)`, false},
		{"lisp/x.el", `(setopt x-width 80)`, false},
		{"README.md", `(defcustom x-width 80`, false},
		// Agent-tools' plan files.
		{plans, `	Kinds = []string{"planned", "plan-reviewed"}`, true},
		{plans, `		"reviewed", "finding-settled", "asked"}`, true},
		{plans, `	routedKind = "routed"`, true},
		{plans, `		block := []string{"*** " + title, ":PROPERTIES:", ":FOUND_IN: " + o.FoundIn, ":END:"}`, true},
		{plans, `var trackedProperties = []string{"WORKTREES", "BRANCHES"}`, true},
		{plans, `	return refusal.New("%s is not a plan", name)`, false},
		{plans, `	kind := e.Kind`, false},
		{plans, `	if e.Kind == receiptKind {`, false},
		{plans, `	return slices.Contains(trackedProperties, name)`, false},
		{plans, `	for _, e := range events {`, false},
		{"internal/guard/x.go", `		block := []string{":PROPERTIES:", ":ID: " + id, ":END:"}`, false},
		{"internal/guard/x.go", `	routedKind = "routed"`, false},
	} {
		// A JSON tag routes only where a line holding it is removed,
		// which a rename also does; every other rule either way.
		onlyRemoved := strings.Contains(tc.line, "json:\"")
		for _, sign := range []string{"-", "+"} {
			want := tc.want && (sign == "-" || !onlyRemoved)
			got := false
			for _, r := range rules {
				if r.Route == "compat" && matches(map[string][]string{tc.path: {sign + tc.line}}, r) {
					got = true
				}
			}
			if got != want {
				t.Errorf("%s: %s%s: routed to compat %v, want %v", tc.path, sign, tc.line, got, want)
			}
		}
	}
}

// The built-in upgrade rules route a commit that changes a lock file, a
// version pin in a Nix file or the version of a workflow's action, and
// not one that only resembles one; security keeps every lock file too.
func TestUpgradeRules(t *testing.T) {
	rules, err := compile(Builtin)
	if err != nil {
		t.Fatal(err)
	}
	routesOf := func(path, line string) map[string]bool {
		got := map[string]bool{}
		for _, r := range rules {
			if matches(map[string][]string{path: {line}}, r) {
				got[r.Route] = true
			}
		}
		return got
	}
	for _, tc := range []struct {
		path, line string
		want       bool
	}{
		{"flake.lock", `        "rev": "abc",`, true},
		{"sub/go.mod", `	golang.org/x/mod v0.21.0`, true},
		{"go.sum", `golang.org/x/mod v0.21.0 h1:x=`, true},
		{"web/package-lock.json", `"version": "1.2.3",`, true},
		{"Cargo.lock", `version = "1.0.0"`, true},
		{"overlays/x/default.nix", `  version = "1.2.3";`, true},
		{"overlays/x/default.nix", `    rev = "v${version}";`, true},
		{"overlays/x/default.nix", `    hash = "sha256-AAAA";`, true},
		{"overlays/x/default.nix", `  vendorHash = null;`, true},
		{"overlays/x/default.nix", `  npmDepsHash = "sha256-B";`, true},
		{"overlays/x/default.nix", `  finalImageTag = "1.2";`, true},
		{"overlays/x/default.nix", `    imageDigest = "sha256:abc";`, true},
		{"overlays/x/default.nix", `    tag = "v1";`, true},
		// A line's pattern cannot tell a string from code: routing too
		// often costs a review, and missing a pin on one line costs more.
		{"overlays/x/default.nix", `  description = "the version = 2 of it";`, true},
		{"overlays/x/default.nix", `  description = "the version 2 of it";`, false},
		{"overlays/x/default.nix", `  versionCheck = true;`, false},
		{"overlays/x/default.nix", `  inherit version;`, false},
		{"README.md", `  version = "1.2.3";`, false},
		{".github/workflows/ci.yml", `      - uses: actions/checkout@v4`, true},
		{".github/workflows/ci.yml", `        uses: actions/setup-go@0aaccfd150d50ccaeb58ebd88d36e91967a5f35b # v5.0.2`, true},
		{".github/workflows/ci.yml", `      - run: make`, false},
		// Every form that may name an action routes, a local one too.
		{".github/workflows/ci.yml", `      - uses: ./local-action`, true},
		{".github/workflows/ci.yml", `      - uses: docker://alpine:3.18`, true},
		{".github/workflows/ci.yml", `      - "uses": actions/checkout@main`, true},
		{".github/workflows/ci.yml", `          actions/checkout@main`, true},
		{".github/workflows/ci.yml", `    steps: [{uses: evil/checkout@main}]`, true},
		{"docs/ci.md", `      - uses: actions/checkout@v4`, false},
		{"flake.nix", `    nixpkgs.url = "github:nixos/nixpkgs/nixos-25.11";`, true},
		{"sub/flake.nix", `      url = "github:o/r";`, true},
		{"flake.nix", `    inputs.nixpkgs.follows = "nixpkgs";`, true},
		{"flake.nix", `    owner = "evil";`, true},
		{"flake.nix", `    ref = "nixos-26.05";`, true},
		{"modules/x.nix", `    url = "https://example.com";`, false},
		{"pkgs/x.nix", `  src = fetchFromGitHub { owner = "o"; repo = "r"; rev = "v2.0"; hash = "sha256-B="; };`, true},
		{"pkgs/y.nix", `    sha512 = "BBB";`, true},
		{"pkgs/y.nix", `    outputHash = "sha256-B=";`, true},
		{"pkgs/y.nix", `    myversion = "1";`, false},
		{"nix/sources.json", `    "rev": "abc",`, true},
		{"npins/sources.json", `    "revision": "abc",`, true},
		{".github/actions/x/action.yml", `    uses: actions/checkout@v5`, true},
		{"action.yml", `    uses: actions/checkout@v5`, true},
		{"go.work", `go 1.26`, true},
		{"go.work.sum", `example.com/m v1.0.0 h1:CCCC=`, true},
	} {
		for _, sign := range []string{"-", "+"} {
			if got := routesOf(tc.path, sign+tc.line)["upgrade"]; got != tc.want {
				t.Errorf("%s: %s%s: routed to upgrade %v, want %v", tc.path, sign, tc.line, got, tc.want)
			}
		}
	}
	for _, f := range LockFiles {
		name := "x/" + strings.TrimPrefix(f, "**/")
		if got := routesOf(name, "+x"); !got["security"] || !got["upgrade"] {
			t.Errorf("%s routes to %v, want security and upgrade", name, got)
		}
	}
}

// A Nix change is routed to upgrade as its scanner reads the file whole:
// a line naming what may pin in code is, prose in a string is not.
func TestScanNixFiles(t *testing.T) {
	for _, tc := range []struct {
		old, next string
		want      bool
	}{
		{"{\n  u = \"https://e.example/a\";\n}\n", "{\n  url = \"https://e.example/b\";\n}\n", true},
		{"{\n  f = builtins.getFlake \"github:o/r/aaaa\";\n}\n", "{\n  f = builtins.getFlake \"github:o/r/bbbb\";\n}\n", true},
		{"{\n  d = ''\n    Hosts it may reach.\n  '';\n}\n", "{\n  d = ''\n    Hosts it may reach, by a version's URL.\n  '';\n}\n", false},
		{"{\n  home.stateVersion = \"24.05\";\n}\n", "{\n  home.stateVersion = \"24.11\";\n}\n", false},
	} {
		repo, _ := filepath.EvalSymlinks(t.TempDir())
		gitIn(t, repo, "init", "-q", "-b", "main")
		base := write(t, repo, "base", "x.nix="+tc.old)
		tip := write(t, repo, "tip", "x.nix="+tc.next)
		rs, err := Scan(repo, base, tip, Builtin)
		got := slices.ContainsFunc(rs, func(r Route) bool { return r.Route == "upgrade" })
		if err != nil || got != tc.want {
			t.Errorf("%q: routes %+v (%v), want upgrade %v", tc.next, rs, err, tc.want)
		}
	}
}

// A commit that adds a JSON field is not routed to compat; one that
// renames or removes a field's tag is.
func TestScanJSONTags(t *testing.T) {
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	gitIn(t, repo, "init", "-q", "-b", "main")
	base := write(t, repo, "Start", "x.go=package x\n\ntype T struct {\n\tA int `json:\"a\"`\n}\n")
	write(t, repo, "Add a field", "x.go=package x\n\ntype T struct {\n\tA int `json:\"a\"`\n\tB int `json:\"b\"`\n}\n")
	renamed := write(t, repo, "Rename a field", "x.go=package x\n\ntype T struct {\n\tA int `json:\"a\"`\n\tB int `json:\"bee\"`\n}\n")
	removed := write(t, repo, "Drop a field", "x.go=package x\n\ntype T struct {\n\tA int `json:\"a\"`\n}\n")
	got, err := Scan(repo, base, "HEAD", Builtin)
	if err != nil {
		t.Fatal(err)
	}
	if r := routed(t, got); strings.Join(r["compat"], " ") != renamed+" "+removed || len(r) != 1 {
		t.Errorf("routes %+v, want compat for %s %s", got, renamed, removed)
	}
}

func TestMatch(t *testing.T) {
	for _, tc := range []struct {
		glob, path string
		want       bool
	}{
		{"**/go.mod", "go.mod", true},
		{"**/go.mod", "a/b/go.mod", true},
		{"**/go.mod", "a/go.mod.x", false},
		{".github/workflows/**", ".github/workflows/ci.yml", true},
		{".github/workflows/**", ".github/x.yml", false},
		{"**/*.go", "a/b.go", true},
		{"**/*.go", "a/b.gox", false},
		{"*.md", "a/b.md", false},
		{"**/.claude/**", "x/.claude/settings.json", true},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
	} {
		if got := Match(tc.glob, tc.path); got != tc.want {
			t.Errorf("Match(%q, %q) = %v", tc.glob, tc.path, got)
		}
	}
}

// A changed file that holds tests beside its code, which red-check cannot
// tell apart, is named for the reviewer; a Go file never is.
func TestSharedFiles(t *testing.T) {
	repo, _ := scanRepo(t)
	base := gitIn(t, repo, "rev-parse", "HEAD")
	write(t, repo, "Add code with its tests", "src/lib.rs=fn f() {}\n#[cfg(test)]\nmod tests {}\n",
		"x.py=def f():\n    \"\"\"\n    >>> f()\n    \"\"\"\n", "plain.py=def g(): pass\n",
		"y_test.go=package main\n", "z.rs=fn h() {}\n")
	got, err := SharedFiles(repo, base, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "src/lib.rs x.py" {
		t.Errorf("shared files %v", got)
	}
}
