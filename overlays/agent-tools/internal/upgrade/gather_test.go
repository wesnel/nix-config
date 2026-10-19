package upgrade

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gh"
)

// fakeNix stands in for `nix flake prefetch --json -- github:O/R/REF`: it
// serves $FAKE_STORE/O_R_REF, whose NAR hash is in the file beside it
// named O_R_REF.hash, and logs each call.
const fakeNix = `#!/bin/sh
echo "nix $*" >> "$FAKE_LOG"
[ "$1 $2 $3 $4" = "flake prefetch --json --" ] || { echo "fake nix: unexpected $*" >&2; exit 64; }
name=$(printf '%s' "${5#github:}" | tr / _)
dir="$FAKE_STORE/$name"
[ -d "$dir" ] || { echo "error: unable to download $5" >&2; exit 1; }
printf '{"hash":"%s","storePath":"%s"}\n' "$(cat "$dir.hash")" "$dir"
`

// fakeGo stands in for `go mod download -json -- M@V...`: it serves
// $FAKE_MODS/M@V, with each / of the path as _, and logs each call with
// the environment the download runs in; and for `go clean -modcache`,
// which it logs with the module cache it cleans.
const fakeGo = `#!/bin/sh
if [ "$*" = "clean -modcache" ]; then echo "clean GOMODCACHE=$GOMODCACHE" >> "$FAKE_LOG.clean"; exit 0; fi
echo "go $* GOPROXY=$GOPROXY GOVCS=$GOVCS GOPRIVATE=$GOPRIVATE GIT_CONFIG_GLOBAL=$GIT_CONFIG_GLOBAL" >> "$FAKE_LOG"
[ "$1 $2 $3 $4" = "mod download -json --" ] || { echo "fake go: unexpected $*" >&2; exit 64; }
shift 4
for mv in "$@"; do
  m=${mv%%@*} v=${mv#*@}
  dir="$FAKE_MODS/$(printf '%s' "$mv" | tr / _)"
  if [ -d "$dir" ]; then printf '{"Path":"%s","Version":"%s","Dir":"%s"}\n' "$m" "$v" "$dir"
  else printf '{"Path":"%s","Version":"%s","Error":"%s: not found"}\n' "$m" "$v" "$mv"; fi
done
`

type world struct {
	o           Options
	log, store  string
	mods        string
	mu          sync.Mutex
	requests    []string
	releases    string
	compareBody string
	// private are the repositories, as OWNER/REPO, GitHub says are
	// private, and looked the repositories asked about, which requests
	// leaves out.
	private map[string]bool
	looked  []string
}

// calls are the GitHub requests made, and the fakes' calls.
func (w *world) calls(t *testing.T) (github []string, programs string) {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	data, _ := os.ReadFile(w.log)
	return append([]string{}, w.requests...), string(data)
}

// setWorld puts fake nix and go first on PATH, serves GitHub's API from a
// test server, and gives Options a cache and clones of the test's own.
func setWorld(t *testing.T) *world {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	w := &world{log: filepath.Join(root, "log"), store: filepath.Join(root, "store"), mods: filepath.Join(root, "mods"),
		releases: "[]", compareBody: `{"status": "ahead", "ahead_by": 2, "behind_by": 0, "total_commits": 2,
			"commits": [{"sha": "1111111111111111", "commit": {"message": "Add x\n\nbody"}}, {"sha": "2222222222222222", "commit": {"message": "Fix y"}}]}`}
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "nix"), []byte(fakeNix), 0o755)
	// A download runs in an environment of its own, which leaves out the
	// fake's variables.
	goScript := strings.NewReplacer("$FAKE_LOG", w.log, "$FAKE_MODS", w.mods).Replace(fakeGo)
	os.WriteFile(filepath.Join(bin, "go"), []byte(goScript), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", w.log)
	t.Setenv("FAKE_STORE", w.store)
	t.Setenv("FAKE_MODS", w.mods)
	t.Setenv("GH_TOKEN", "test-token")
	old := storeDir
	storeDir = w.store + "/"
	t.Cleanup(func() { storeDir = old })
	w.private = map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		repo, visibility := strings.CutPrefix(r.URL.Path, "/repos/")
		visibility = visibility && strings.Count(repo, "/") == 1
		w.mu.Lock()
		if visibility {
			w.looked = append(w.looked, repo)
		} else {
			w.requests = append(w.requests, r.URL.Path)
		}
		private := w.private[repo]
		w.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(rw, "no token", http.StatusUnauthorized)
			return
		}
		switch {
		case visibility && strings.HasPrefix(repo, "gone/"):
			http.NotFound(rw, r)
			return
		case visibility:
			fmt.Fprintf(rw, `{"full_name": %q, "private": %v}`, repo, private)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases"):
			rw.Write([]byte(w.releases))
		case strings.Contains(r.URL.Path, "/compare/"):
			rw.Write([]byte(w.compareBody))
		default:
			http.NotFound(rw, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(gh.SetAPI(srv.URL))
	w.o = Options{Cache: filepath.Join(root, "cache"), Clones: filepath.Join(root, "clones"), Limits: DefaultLimits}
	return w
}

// tree serves a source tree for github:owner/repo/ref with NAR hash hash,
// holding files, each "path=content".
func (w *world) tree(t *testing.T, owner, repo, ref, hash string, files ...string) {
	t.Helper()
	dir := filepath.Join(w.store, owner+"_"+repo+"_"+ref)
	writeFiles(t, dir, files...)
	os.WriteFile(dir+".hash", []byte(hash), 0o644)
}

func writeFiles(t *testing.T, dir string, files ...string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	for _, f := range files {
		path, content, _ := strings.Cut(f, "=")
		full := filepath.Join(dir, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// clone makes a local clone of owner/repo with two commits after a root,
// and returns the root and the last.
func (w *world) clone(t *testing.T, owner, repo string) (string, string) {
	t.Helper()
	dir := filepath.Join(w.o.Clones, owner, repo)
	os.MkdirAll(dir, 0o755)
	gitIn(t, dir, "init", "-q", "-b", "main")
	old := commit(t, dir, "Start")
	commit(t, dir, "Add the widget")
	return old, commit(t, dir, "Drop the old flag")
}

func flakePin(owner, repo, old, next string) Pin {
	return Pin{Key: "flake.lock#" + repo, File: "flake.lock", Kind: "flake", Type: "github", Owner: owner, Repo: repo,
		Old: old, New: next, OldRef: old, NewRef: next, OldHash: "sha256-o=", NewHash: "sha256-n=",
		OldTime: time.Unix(1700000000, 0).UTC(), NewTime: time.Unix(1700086400, 0).UTC()}
}

func sources(g Gathered) string {
	var s []string
	for _, t := range g.Texts {
		s = append(s, t.Source)
	}
	return strings.Join(s, "\n")
}

// A github flake node is gathered from its local clone, its source trees'
// changelog files and its releases between the two times, and GitHub's
// comparison is left out once the clone gave the history.
func TestGatherFlake(t *testing.T) {
	w := setWorld(t)
	old, next := w.clone(t, "acme", "lib")
	w.tree(t, "acme", "lib", old, "sha256-o=", "CHANGELOG.md=# 1.0\n", "src/x=1\n")
	w.tree(t, "acme", "lib", next, "sha256-n=", "CHANGELOG.md=# 1.1\n\n- BREAKING: the old flag is gone\n\n# 1.0\n", "src/x=2\n")
	w.releases = `[{"tag_name": "v1.2", "name": "later", "body": "after", "published_at": "2023-11-20T00:00:00Z"},
		{"tag_name": "v1.1", "name": "one", "body": "Drops the old flag.", "published_at": "2023-11-15T12:00:00Z"},
		{"tag_name": "v1.0", "name": "zero", "body": "before", "published_at": "2023-11-01T00:00:00Z"}]`
	got := Gather(t.Context(), []Pin{flakePin("acme", "lib", old, next)}, w.o)
	if len(got) != 1 || !got[0].Read() || len(got[0].Texts) != 3 {
		t.Fatalf("gathered %+v", got)
	}
	texts := got[0].Texts
	if !strings.Contains(texts[0].Body, "Drop the old flag") || !strings.Contains(texts[0].Body, "Add the widget") ||
		strings.Contains(texts[0].Body, "Start") || !strings.Contains(texts[0].Source, "local clone") {
		t.Errorf("clone log %+v", texts[0])
	}
	if !strings.Contains(texts[1].Body, "+- BREAKING: the old flag is gone") || strings.Contains(texts[1].Body, "src/x") ||
		len(texts[1].Paths) != 2 || !strings.HasPrefix(texts[1].Paths[0], w.store) {
		t.Errorf("source trees %+v", texts[1])
	}
	if !strings.Contains(texts[2].Body, "Drops the old flag.") || strings.Contains(texts[2].Body, "after") ||
		strings.Contains(texts[2].Body, "before") {
		t.Errorf("releases %+v", texts[2])
	}
	requests, programs := w.calls(t)
	if strings.Join(requests, " ") != "/repos/acme/lib/releases" {
		t.Errorf("GitHub requests %v", requests)
	}
	if strings.Count(programs, "nix flake prefetch") != 2 || !strings.Contains(programs, "-- github:acme/lib/"+next) {
		t.Errorf("programs run:\n%s", programs)
	}
}

// A repository is read, from a clone, its trees or GitHub, only where
// GitHub says it is public, or, for the operator's own change, the
// private patterns name it: a pin of any other is left unread, with why,
// and nothing of it is fetched.
func TestGatherPrivateRepos(t *testing.T) {
	for _, c := range []struct {
		name, owner, patterns string
		private, own, read    bool
		why                   string
	}{
		{"public", "acme", "", false, true, true, ""},
		{"public, someone else's change", "acme", "", false, false, true, ""},
		{"private", "acme", "", true, true, false, "github.com/acme/lib is private"},
		{"private and named", "acme", "github.com/acme/*", true, true, true, ""},
		{"named in another case", "acme", "github.com/ACME/lib", true, true, true, ""},
		{"named, someone else's change", "acme", "github.com/acme/*", true, false, false, "not the operator's own"},
		{"visibility unknown", "gone", "", false, true, false, "could not be read"},
		{"named, unknown, someone else's", "gone", "github.com/gone/*", false, false, false, "could not be read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := setWorld(t)
			old, next := w.clone(t, c.owner, "lib")
			w.tree(t, c.owner, "lib", old, "sha256-o=", "CHANGELOG.md=# 1.0\n")
			w.tree(t, c.owner, "lib", next, "sha256-n=", "CHANGELOG.md=# 1.1\n\n# 1.0\n")
			w.private[c.owner+"/lib"] = c.private
			w.o.Private, w.o.Own = c.patterns, c.own
			got := Gather(t.Context(), []Pin{flakePin(c.owner, "lib", old, next)}, w.o)
			requests, programs := w.calls(t)
			if len(got) != 1 || got[0].Read() != c.read || !strings.Contains(got[0].Why, c.why) {
				t.Fatalf("gathered %+v", got)
			}
			if !c.read && (len(requests) != 0 || programs != "") {
				t.Errorf("an unread repository was fetched: requests %v, programs %q", requests, programs)
			}
		})
	}
}

// Without a local clone, GitHub's comparison gives the history; releases
// are picked by tag where both are released.
func TestGatherCompareAndTags(t *testing.T) {
	w := setWorld(t)
	w.releases = `[{"tag_name": "v5.1.0", "name": "", "body": "New input.", "published_at": "2024-02-01T00:00:00Z"},
		{"tag_name": "v5.0.2", "name": "", "body": "A fix.", "published_at": "2024-01-15T00:00:00Z"},
		{"tag_name": "v5.0.1", "name": "", "body": "Old.", "published_at": "2024-01-01T00:00:00Z"}]`
	p := Pin{Key: "ci.yml#actions/setup-go", Kind: "action", Owner: "actions", Repo: "setup-go", Old: "aaaaaaa", New: "bbbbbbb",
		OldRef: "aaaaaaa", NewRef: "bbbbbbb", OldTag: "v5.0.1", NewTag: "v5.1.0"}
	got := Gather(t.Context(), []Pin{p}, w.o)
	if len(got) != 1 || len(got[0].Texts) != 2 {
		t.Fatalf("gathered %+v", got)
	}
	rel, cmp := got[0].Texts[0].Body, got[0].Texts[1].Body
	if !strings.Contains(rel, "New input.") || !strings.Contains(rel, "A fix.") || strings.Contains(rel, "Old.") {
		t.Errorf("releases:\n%s", rel)
	}
	if !strings.Contains(cmp, "2 commits ahead") || !strings.Contains(cmp, "111111111111 Add x") || strings.Contains(cmp, "body") {
		t.Errorf("comparison:\n%s", cmp)
	}
	requests, _ := w.calls(t)
	if len(requests) != 2 || requests[1] != "/repos/actions/setup-go/compare/aaaaaaa...bbbbbbb" {
		t.Errorf("requests %v", requests)
	}

	// One call a pin takes only the releases.
	w2 := setWorld(t)
	w2.releases = w.releases
	w2.o.Limits.Calls = 1
	got = Gather(t.Context(), []Pin{p}, w2.o)
	if requests, _ := w2.calls(t); len(requests) != 1 || len(got[0].Texts) != 1 {
		t.Errorf("with one call: requests %v, texts %+v", requests, got[0].Texts)
	}
}

// nixpkgs is too long to list: its clone gives a count, its tree the
// release notes where nixpkgs keeps them, and GitHub only counts.
func TestGatherNixpkgs(t *testing.T) {
	w := setWorld(t)
	old, next := w.clone(t, "nixos", "nixpkgs")
	notes := "nixos/doc/manual/release-notes/rl-2611.section.md="
	w.tree(t, "nixos", "nixpkgs", old, "sha256-o=", notes+"# 26.11\n")
	w.tree(t, "nixos", "nixpkgs", next, "sha256-n=", notes+"# 26.11\n\n- services.x.enable is renamed\n")
	got := Gather(t.Context(), []Pin{flakePin("nixos", "nixpkgs", old, next)}, w.o)
	if len(got) != 1 || len(got[0].Texts) != 2 {
		t.Fatalf("gathered %+v", got)
	}
	if got[0].Texts[0].Body != "2 commits" || strings.Contains(got[0].Texts[0].Body, "widget") {
		t.Errorf("clone %+v", got[0].Texts[0])
	}
	if !strings.Contains(got[0].Texts[1].Body, "+- services.x.enable is renamed") {
		t.Errorf("notes %+v", got[0].Texts[1])
	}
	if requests, _ := w.calls(t); len(requests) != 0 {
		t.Errorf("requests %v", requests)
	}

	// Without the clone, GitHub's comparison gives the counts alone.
	w2 := setWorld(t)
	got = Gather(t.Context(), []Pin{flakePin("nixos", "nixpkgs", old, next)}, w2.o)
	if requests, _ := w2.calls(t); len(requests) != 1 || !strings.Contains(requests[0], "/compare/") {
		t.Errorf("requests %v", requests)
	}
	if len(got[0].Texts) != 1 || got[0].Texts[0].Body != "ahead: 2 commits ahead, 0 behind\n" {
		t.Errorf("texts %+v", got[0].Texts)
	}
}

// A source tree whose NAR hash is not the lock's is refused, and the
// pin's other sources still count.
func TestGatherHashMismatch(t *testing.T) {
	w := setWorld(t)
	w.tree(t, "acme", "lib", "aaaaaaa", "sha256-o=", "NEWS=old\n")
	w.tree(t, "acme", "lib", "bbbbbbb", "sha256-forged=", "NEWS=new\n")
	p := flakePin("acme", "lib", "aaaaaaa", "bbbbbbb")
	p.OldTime, p.NewTime = time.Time{}, time.Time{}
	w.compareBody = `{"message": "Not Found"}`
	got := Gather(t.Context(), []Pin{p}, w.o)
	if len(got) != 1 || len(got[0].Texts) != 1 || strings.Contains(got[0].Texts[0].Body, "new") {
		t.Fatalf("gathered %+v", got)
	}
	// With GitHub refusing too, nothing is gathered and the reasons say why.
	w.compareBody = ""
	w2 := setWorld(t)
	w2.tree(t, "acme", "lib", "aaaaaaa", "sha256-o=", "NEWS=old\n")
	w2.tree(t, "acme", "lib", "bbbbbbb", "sha256-forged=", "NEWS=new\n")
	w2.compareBody = "not json"
	got = Gather(t.Context(), []Pin{p}, w2.o)
	if got[0].Read() || !strings.Contains(got[0].Why, "sha256-forged=") || !strings.Contains(got[0].Why, "local clone: none at") {
		t.Errorf("gathered %+v", got)
	}
}

// A Go module's two versions come from the public proxy alone, with no
// version control; a module GOPRIVATE matches is requested from nowhere.
func TestGatherGoModule(t *testing.T) {
	w := setWorld(t)
	writeFiles(t, filepath.Join(w.mods, "github.com_a_b@v1.0.0"), "CHANGELOG.md=## 1.0.0\n")
	writeFiles(t, filepath.Join(w.mods, "github.com_a_b@v1.1.0"), "CHANGELOG.md=## 1.1.0\n- Renames Run to Start\n\n## 1.0.0\n")
	p := Pin{Key: "go.mod#github.com/a/b", Kind: "go-module", Module: "github.com/a/b", Owner: "a", Repo: "b",
		Old: "v1.0.0", New: "v1.1.0", OldRef: "v1.0.0", NewRef: "v1.1.0", OldTag: "v1.0.0", NewTag: "v1.1.0"}
	w.releases = `[]`
	got := Gather(t.Context(), []Pin{p}, w.o)
	if len(got) != 1 || !got[0].Read() || !strings.Contains(got[0].Texts[0].Body, "+- Renames Run to Start") {
		t.Fatalf("gathered %+v", got)
	}
	_, programs := w.calls(t)
	if !strings.Contains(programs, "go mod download -json -- github.com/a/b@v1.0.0 github.com/a/b@v1.1.0 "+
		"GOPROXY=https://proxy.golang.org GOVCS=*:off GOPRIVATE= GIT_CONFIG_GLOBAL=/dev/null") {
		t.Errorf("programs:\n%s", programs)
	}
	// The download's module cache is its own, and cleaned after it.
	cleaned, _ := os.ReadFile(w.log + ".clean")
	if c := strings.TrimSpace(string(cleaned)); !strings.HasPrefix(c, "clean GOMODCACHE=") || strings.HasPrefix(c, "clean GOMODCACHE="+w.o.Cache) {
		t.Errorf("cleaned %q", cleaned)
	} else if _, err := os.Stat(strings.TrimPrefix(c, "clean GOMODCACHE=")); err == nil {
		t.Errorf("the module cache %s is left behind", c)
	}

	// A major version's new path is the same module moved.
	w3 := setWorld(t)
	writeFiles(t, filepath.Join(w3.mods, "github.com_a_b@v1.9.0"), "CHANGELOG.md=## 1.9.0\n")
	writeFiles(t, filepath.Join(w3.mods, "github.com_a_b_v2@v2.0.0"), "CHANGELOG.md=## 2.0.0\n- Drops Run\n\n## 1.9.0\n")
	major := Pin{Key: "go.mod#github.com/a/b/v2", Kind: "go-module", Module: "github.com/a/b/v2", OldModule: "github.com/a/b",
		Owner: "a", Repo: "b", Old: "v1.9.0", New: "v2.0.0", OldRef: "v1.9.0", NewRef: "v2.0.0"}
	got = Gather(t.Context(), []Pin{major}, w3.o)
	if _, programs := w3.calls(t); len(got) != 1 || !got[0].Read() || !strings.Contains(got[0].Texts[0].Body, "+- Drops Run") ||
		!strings.Contains(programs, "-- github.com/a/b@v1.9.0 github.com/a/b/v2@v2.0.0 ") {
		t.Errorf("a major version: %+v\n%s", got, programs)
	}

	// A replacement moved to another module is downloaded at each side's
	// own path and version.
	w5 := setWorld(t)
	writeFiles(t, filepath.Join(w5.mods, "github.com_fork1_c@v1.0.0"), "CHANGELOG.md=## 1.0.0\n")
	writeFiles(t, filepath.Join(w5.mods, "github.com_fork2_c@v1.0.0"), "CHANGELOG.md=## 1.0.0\n- Forked\n")
	forked := goModulePin("go.mod#github.com/x/c", "go.mod", "github.com/fork1/c", "v1.0.0", "github.com/fork2/c", "v1.0.0")
	got = Gather(t.Context(), []Pin{forked}, w5.o)
	if _, programs := w5.calls(t); len(got) != 1 || !got[0].Read() || !strings.Contains(got[0].Texts[0].Body, "+- Forked") ||
		!strings.Contains(programs, "-- github.com/fork1/c@v1.0.0 github.com/fork2/c@v1.0.0 ") {
		t.Errorf("a replacement retargeted: %+v\n%s", got, programs)
	}

	// A module path that would be a flag is never passed to go.
	w4 := setWorld(t)
	flag := Pin{Key: "go.mod#-x", Kind: "go-module", Module: "-modfile=/tmp/x", Old: "v1.0.0", New: "v1.1.0"}
	if got = Gather(t.Context(), []Pin{flag}, w4.o); got[0].Read() {
		t.Errorf("a flag as a module: %+v", got)
	}
	if _, programs := w4.calls(t); programs != "" {
		t.Errorf("a flag as a module reached go: %q", programs)
	}

	// A private module never reaches the module proxy: it is read through
	// GitHub's API alone, and is unread where that gives nothing.
	for _, private := range []string{"example.com/private,github.com/a", "github.com/a/*"} {
		w2 := setWorld(t)
		writeFiles(t, filepath.Join(w2.mods, "github.com_a_b@v1.0.0"), "CHANGELOG.md=## 1.0.0\n")
		writeFiles(t, filepath.Join(w2.mods, "github.com_a_b@v1.1.0"), "CHANGELOG.md=## 1.1.0\n")
		w2.o.Private = private
		got = Gather(t.Context(), []Pin{p}, w2.o)
		requests, programs := w2.calls(t)
		if programs != "" || !got[0].Read() || len(got[0].Texts) != 1 || !strings.Contains(got[0].Texts[0].Source, "comparison") ||
			strings.Join(requests, " ") != "/repos/a/b/releases /repos/a/b/compare/v1.0.0...v1.1.0" {
			t.Errorf("private %s: %+v, requests %v, programs %q", private, got, requests, programs)
		}
		w2.compareBody = "not json"
		w2.o.Cache = t.TempDir()
		if got = Gather(t.Context(), []Pin{p}, w2.o); got[0].Read() || !strings.Contains(got[0].Why, "private module") {
			t.Errorf("private %s with GitHub failing: %+v", private, got)
		}
		if _, programs := w2.calls(t); programs != "" {
			t.Errorf("private %s reached the proxy: %q", private, programs)
		}
	}
}

// The private modules are the config's review.privateModules, ngrok's
// where it names none, with those Go's settings name.
func TestDefaultPrivateModules(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o755)
	// go env prints each variable asked for on a line of its own.
	os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n[ \"$*\" = \"env GOPRIVATE GONOPROXY GONOSUMDB\" ] || exit 64\n"+
		"printf 'goenv.example/x\\n\\nnosum.example\\n'\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GONOPROXY", "envproxy.example")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOSUMDB", "")
	config := filepath.Join(dir, "config.json")
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	const ngrok = "github.com/ngrok/*,github.com/ngrok-private/*,go.ngrok.com/*,"
	const goSettings = "envproxy.example,goenv.example/x,nosum.example"
	for _, c := range []struct{ config, want string }{
		{``, ngrok + goSettings},
		{`{"review": {}}`, ngrok + goSettings},
		{`{"review": {"privateModules": ["corp.example/*"]}}`, "corp.example/*," + goSettings},
		{`{"review": {"privateModules": []}}`, goSettings},
		{`not json`, ngrok + goSettings},
	} {
		os.Remove(config)
		if c.config != "" {
			os.WriteFile(config, []byte(c.config), 0o644)
		}
		o := DefaultOptions(t.Context())
		if o.Private != c.want {
			t.Errorf("config %q: private %q, want %q", c.config, o.Private, c.want)
		}
		for _, m := range []string{"github.com/ngrok/libngrok-go", "go.ngrok.com/lib", "github.com/ngrok-private/x"} {
			if want := strings.HasPrefix(c.want, ngrok); matchPrefixPatterns(o.Private, m) != want {
				t.Errorf("config %q: %s private %v, want %v", c.config, m, !want, want)
			}
		}
	}
}

// A flake node of another type, a lock file this does not read, and a Go
// release are never fetched, and say why.
func TestGatherNever(t *testing.T) {
	w := setWorld(t)
	pins := []Pin{
		{Key: "flake.lock#private", Kind: "flake", Type: "git", Old: "a", New: "b"},
		{Key: "package-lock.json", File: "web/package-lock.json", Kind: "lockfile", Old: "a", New: "b"},
		{Key: "go.mod#go", Kind: "go", Old: "1.25", New: "1.26"},
		{Key: "flake.lock#added", Kind: "flake", Type: "github", Owner: "o", Repo: "r", New: "b"},
	}
	got := Gather(t.Context(), pins, w.o)
	if len(got) != 3 {
		t.Fatalf("gathered %+v; an added pin has nothing to gather", got)
	}
	for i, want := range []string{"a git node", "reads no versions from package-lock.json", "Go release notes"} {
		if got[i].Read() || !strings.Contains(got[i].Why, want) {
			t.Errorf("%s: %+v, want %q", got[i].Pin.Key, got[i], want)
		}
	}
	if requests, programs := w.calls(t); len(requests) != 0 || programs != "" {
		t.Errorf("requests %v, programs %q", requests, programs)
	}
}

// A name that would leave the clones directory, or a ref that is an
// option, is never used.
func TestGatherHostileNames(t *testing.T) {
	w := setWorld(t)
	for _, p := range []Pin{
		{Key: "a", Kind: "action", Owner: "..", Repo: "x", Old: "a", New: "b", OldRef: "a", NewRef: "b"},
		{Key: "b", Kind: "action", Owner: "o", Repo: "r", Old: "a", New: "b", OldRef: "--upload-pack=x", NewRef: "b"},
		{Key: "c", Kind: "action", Owner: "o", Repo: "r", Old: "a", New: "b", OldRef: "a..b", NewRef: "b"},
	} {
		got := Gather(t.Context(), []Pin{p}, w.o)
		if got[0].Read() {
			t.Errorf("%+v gathered %+v", p, got)
		}
	}
	if requests, programs := w.calls(t); len(requests) != 0 || programs != "" {
		t.Errorf("requests %v, programs %q", requests, programs)
	}
}

// The limits cut each text, stop at the total, and gather for the first
// pins only.
func TestGatherLimits(t *testing.T) {
	w := setWorld(t)
	w.compareBody = `{"status": "ahead", "ahead_by": 1, "total_commits": 1, "commits": [{"sha": "1234567890123", "commit": {"message": "` +
		strings.Repeat("x", 3000) + `"}}]}`
	pin := func(name string) Pin {
		return Pin{Key: name, Kind: "action", Owner: "o", Repo: name, Old: "aaaaaaa", New: "bbbbbbb", OldRef: "aaaaaaa", NewRef: "bbbbbbb"}
	}
	w.o.Limits = Limits{Pins: 3, Text: 1000, Total: 1500, Calls: 2}
	got := Gather(t.Context(), []Pin{pin("one"), pin("two"), pin("three"), pin("four")}, w.o)
	if len(got) != 4 {
		t.Fatalf("gathered %+v", got)
	}
	if len(got[0].Texts[0].Body) > 1000 || !strings.HasSuffix(got[0].Texts[0].Body, "(cut here)") {
		t.Errorf("first text is %d bytes: %q", len(got[0].Texts[0].Body), got[0].Texts[0].Body[len(got[0].Texts[0].Body)-20:])
	}
	if len(got[1].Texts) != 1 || len(got[1].Texts[0].Body) > 500 {
		t.Errorf("the second pin takes what is left of the total: %+v", got[1].Texts)
	}
	if got[2].Read() || !strings.Contains(got[2].Why, "went to the pins before it") {
		t.Errorf("third %+v", got[2])
	}
	if got[3].Read() || !strings.Contains(got[3].Why, "first 3 moved pins") {
		t.Errorf("fourth %+v", got[3])
	}
}

// What was gathered is kept by source and versions: gathered again, it
// needs no network.
func TestGatherCache(t *testing.T) {
	w := setWorld(t)
	p := Pin{Key: "x", Kind: "action", Owner: "o", Repo: "r", Old: "aaaaaaa", New: "bbbbbbb", OldRef: "aaaaaaa", NewRef: "bbbbbbb"}
	first := Gather(t.Context(), []Pin{p}, w.o)
	again := Gather(t.Context(), []Pin{p}, w.o)
	requests, _ := w.calls(t)
	if !first[0].Read() || len(requests) != 1 {
		t.Fatalf("first %+v, requests %v", first, requests)
	}
	a, _ := json.Marshal(first[0].Texts)
	b, _ := json.Marshal(again[0].Texts)
	if string(a) != string(b) {
		t.Errorf("again %s, want %s", b, a)
	}
	entries, _ := os.ReadDir(w.o.Cache)
	for _, e := range entries {
		if info, _ := e.Info(); info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v", e.Name(), info.Mode())
		}
	}
	// Other versions are another entry.
	p.New, p.NewRef = "ccccccc", "ccccccc"
	Gather(t.Context(), []Pin{p}, w.o)
	if requests, _ := w.calls(t); len(requests) != 2 {
		t.Errorf("other versions came from the cache: requests %v", requests)
	}
}

func TestMatchPrefixPatterns(t *testing.T) {
	for _, c := range []struct {
		patterns, module string
		want             bool
	}{
		{"github.com/ngrok", "github.com/ngrok/x", true},
		{"github.com/ngrok", "github.com/ngrokx/x", false},
		{"*.corp.example", "git.corp.example/a/b", true},
		{"github.com/a/*", "github.com/a/b/c", true},
		{"", "github.com/a/b", false},
		{"x.example, github.com/a", "github.com/a/b", true},
		{"github.com/a/b/c", "github.com/a/b", false},
	} {
		if got := matchPrefixPatterns(c.patterns, c.module); got != c.want {
			t.Errorf("%q %q: %v", c.patterns, c.module, got)
		}
	}
}
