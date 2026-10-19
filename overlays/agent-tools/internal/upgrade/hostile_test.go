package upgrade

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// A local clone whose config names a program is never run in: its
// gpg.program, which log.showSignature would run on a signed commit,
// never runs, and the pin is not gathered from it.
func TestCloneConfigRefused(t *testing.T) {
	w := setWorld(t)
	old, next := w.clone(t, "acme", "lib")
	dir := filepath.Join(w.o.Clones, "acme", "lib")
	// A signed commit on top, so that a log showing signatures runs gpg.
	tree := gitIn(t, dir, "rev-parse", next+"^{tree}")
	body := "tree " + tree + "\nparent " + next + "\nauthor a <a@b> 1700000000 +0000\ncommitter a <a@b> 1700000000 +0000\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n AAAA\n -----END PGP SIGNATURE-----\n\nSigned\n"
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir, cmd.Env, cmd.Stdin = dir, gitenv.Without(os.Environ()), strings.NewReader(body)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	signed := strings.TrimSpace(string(out))
	pwned := filepath.Join(t.TempDir(), "PWNED")
	evil := filepath.Join(t.TempDir(), "evil.sh")
	os.WriteFile(evil, []byte("#!/bin/sh\ntouch "+pwned+"\nexit 1\n"), 0o755)
	p := Pin{Key: "x", Kind: "action", Owner: "acme", Repo: "lib", Old: old, New: signed, OldRef: old, NewRef: signed}

	// The user's own config can turn signatures on, which the clone's
	// commands turn off.
	global := filepath.Join(t.TempDir(), "gitconfig")
	os.WriteFile(global, []byte("[log]\n\tshowSignature = true\n[gpg]\n\tprogram = "+evil+"\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	got := Gather(t.Context(), []Pin{p}, w.o)
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("the user's gpg.program ran during the review")
	}
	if !got[0].Read() || !strings.Contains(got[0].Texts[0].Body, "Signed") {
		t.Errorf("gathered %+v", got)
	}

	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	gitIn(t, dir, "config", "gpg.program", evil)
	gitIn(t, dir, "config", "log.showSignature", "true")
	w.o.Cache = t.TempDir()
	got = Gather(t.Context(), []Pin{p}, w.o)
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("the clone's gpg.program ran during the review")
	}
	if strings.Contains(sources(got[0]), "local clone") {
		t.Errorf("gathered from a clone that names a program: %+v", got)
	}
	// With GitHub giving nothing either, the refusal is the reason.
	w.compareBody = "not json"
	w.o.Cache = t.TempDir()
	if got = Gather(t.Context(), []Pin{p}, w.o); got[0].Read() || !strings.Contains(got[0].Why, "gpg.program") {
		t.Errorf("gathered %+v", got)
	}
}

// A changelog path reached through a symbolic link in a source tree, the
// path's own or a directory's on the way, is never read.
func TestNotesSymlinks(t *testing.T) {
	w := setWorld(t)
	outside := t.TempDir()
	writeFiles(t, outside, "manual/release-notes/rl.md=SECRET outside the tree\n", "passwd=root:x:0:0\n")
	w.tree(t, "nixos", "nixpkgs", "aaaaaaa", "sha256-o=")
	w.tree(t, "nixos", "nixpkgs", "bbbbbbb", "sha256-n=", "doc/release-notes/rl.md=# notes\n- real\n")
	next := filepath.Join(w.store, "nixos_nixpkgs_bbbbbbb")
	os.MkdirAll(filepath.Join(next, "nixos", "doc"), 0o755)
	os.Symlink(filepath.Join(outside, "manual"), filepath.Join(next, "nixos", "doc", "manual"))
	os.Symlink(filepath.Join(outside, "passwd"), filepath.Join(next, "CHANGELOG"))
	p := flakePin("nixos", "nixpkgs", "aaaaaaa", "bbbbbbb")
	p.OldTime, p.NewTime = time.Time{}, time.Time{}
	got := Gather(t.Context(), []Pin{p}, w.o)
	all := ""
	for _, t := range got[0].Texts {
		all += t.Body
	}
	if strings.Contains(all, "SECRET") || strings.Contains(all, "root:x") || !strings.Contains(all, "+- real") {
		t.Errorf("gathered:\n%s", all)
	}
}

// A diff longer than the text a pin takes is cut while it is read.
func TestNotesDiffCapped(t *testing.T) {
	old, next := t.TempDir(), t.TempDir()
	writeFiles(t, old, "CHANGELOG=a\n")
	writeFiles(t, next, "CHANGELOG="+strings.Repeat("a long line of the changelog\n", 10000))
	out, err := notesDiff(t.Context(), old, next, nil, 1000)
	if err != nil || len(out) > 1001 || len(out) < 900 {
		t.Errorf("%d bytes, %v", len(out), err)
	}
}

// A cache directory another user could have written is neither read nor
// written.
func TestCacheModes(t *testing.T) {
	w := setWorld(t)
	p := Pin{Key: "x", Kind: "action", Owner: "o", Repo: "r", Old: "aaaaaaa", New: "bbbbbbb", OldRef: "aaaaaaa", NewRef: "bbbbbbb"}
	Gather(t.Context(), []Pin{p}, w.o)
	if info, err := os.Stat(w.o.Cache); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("cache %v %v", info, err)
	}
	fetched := func() int {
		requests, _ := w.calls(t)
		return len(requests)
	}
	if Gather(t.Context(), []Pin{p}, w.o); fetched() != 1 {
		t.Fatalf("not kept: %d requests", fetched())
	}
	os.Chmod(w.o.Cache, 0o777)
	if Gather(t.Context(), []Pin{p}, w.o); fetched() != 2 {
		t.Errorf("read from a cache others may write: %d requests", fetched())
	}
	// A link in place of the directory is refused too.
	os.Chmod(w.o.Cache, 0o700)
	link := filepath.Join(t.TempDir(), "cache")
	os.Symlink(w.o.Cache, link)
	linked := w.o
	linked.Cache = link
	if Gather(t.Context(), []Pin{p}, linked); fetched() != 3 {
		t.Errorf("read through a link: %d requests", fetched())
	}
}

// A pin whose ref is the same on both sides, only its hash moved, has no
// upstream changes to read, and is never counted as read.
func TestHashOnly(t *testing.T) {
	w := setWorld(t)
	p := Pin{Key: "x.nix#acme/t", Kind: "nix", Owner: "acme", Repo: "t", Old: "sha256-V1=", New: "sha256-V2=",
		OldRef: "v1.0", NewRef: "v1.0"}
	got := Gather(t.Context(), []Pin{p}, w.o)
	if got[0].Read() || !strings.Contains(got[0].Why, "only its hash or its directory moved") {
		t.Errorf("gathered %+v", got)
	}
	if requests, programs := w.calls(t); len(requests) != 0 || programs != "" {
		t.Errorf("requests %v, programs %q", requests, programs)
	}
}

// Private module patterns ignore case.
func TestPrivateCase(t *testing.T) {
	patterns := strings.Join(DefaultPrivateModules, ",")
	for _, m := range []string{"github.com/NGROK/secret", "GitHub.com/ngrok/x/sub", "go.ngrok.com/lib", "github.com/Ngrok-Private/y"} {
		if !matchPrefixPatterns(patterns, m) {
			t.Errorf("%s is not private", m)
		}
	}
	if matchPrefixPatterns(patterns, "github.com/ngrokx/y") {
		t.Error("github.com/ngrokx/y is private")
	}
}
