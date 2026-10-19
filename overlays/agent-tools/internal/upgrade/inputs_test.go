package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func flakeNode(owner, rev, extra string) string {
	return `{"locked": {"type": "github", "owner": "` + owner + `", "repo": "nixpkgs", "rev": "` + rev + `", "narHash": "sha256-` +
		rev[:4] + `="` + extra + `}, "original": {"type": "github", "owner": "` + owner + `", "repo": "nixpkgs"}}`
}

func keys(pins []Pin) string {
	var k []string
	for _, p := range pins {
		k = append(k, p.Key)
	}
	return strings.Join(k, " ")
}

// What an input resolves to is a pin, whichever node holds it: an input
// pointed at another node moves though no node does.
func TestFlakeInputEdges(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	swap := func(rootTo, hmTo string) []byte {
		return []byte(`{"nodes": {"nixpkgs": ` + flakeNode("nixos", a, "") + `, "nixpkgs_2": ` + flakeNode("nixos", b, "") +
			`, "hm": {"inputs": {"nixpkgs": "` + hmTo + `"}, "locked": {"type": "github", "owner": "nix-community", ` +
			`"repo": "home-manager", "rev": "` + a + `", "narHash": "sha256-h="}}, "root": {"inputs": {"nixpkgs": "` + rootTo +
			`", "hm": "hm"}}}, "root": "root", "version": 7}`)
	}
	pins := flakePins("flake.lock", swap("nixpkgs", "nixpkgs_2"), swap("nixpkgs_2", "nixpkgs"))
	if keys(pins) != "flake.lock#input hm/nixpkgs flake.lock#input nixpkgs" {
		t.Fatalf("swapped inputs: %s", keys(pins))
	}
	for _, p := range pins {
		if !p.Moved() || p.Owner != "nixos" {
			t.Errorf("%+v", p)
		}
	}
	if p := pins[1]; p.OldRef != a || p.NewRef != b {
		t.Errorf("root's nixpkgs %+v", p)
	}

	// An input pointed at a new node is that node moved, not only added.
	repoint := func(withNew bool, rootTo string) []byte {
		s := `{"nodes": {"nixpkgs": ` + flakeNode("nixos", a, "")
		if withNew {
			s += `, "nixpkgs_9": ` + flakeNode("nixos", b, "")
		}
		return []byte(s + `, "root": {"inputs": {"nixpkgs": "` + rootTo + `"}}}, "root": "root", "version": 7}`)
	}
	pins = flakePins("flake.lock", repoint(false, "nixpkgs"), repoint(true, "nixpkgs_9"))
	if !strings.Contains(keys(pins), "flake.lock#input nixpkgs") {
		t.Errorf("repointed: %s", keys(pins))
	}
	for _, p := range pins {
		if p.Key == "flake.lock#input nixpkgs" && (!p.Moved() || p.OldRef != a || p.NewRef != b) {
			t.Errorf("%+v", p)
		}
	}

	// An input following another resolves to that one's node.
	follows := func(to string) []byte {
		return []byte(`{"nodes": {"nixpkgs": ` + flakeNode("nixos", a, "") + `, "nixpkgs_2": ` + flakeNode("nixos", b, "") +
			`, "hm": {"inputs": {"nixpkgs": ["nixpkgs"]}, "locked": {"type": "github", "owner": "nix-community", ` +
			`"repo": "home-manager", "rev": "` + a + `", "narHash": "sha256-h="}}, "root": {"inputs": {"nixpkgs": "` + to +
			`", "hm": "hm"}}}, "root": "root", "version": 7}`)
	}
	if got := keys(flakePins("flake.lock", follows("nixpkgs"), follows("nixpkgs_2"))); got != "flake.lock#input hm/nixpkgs flake.lock#input nixpkgs" {
		t.Errorf("followed: %s", got)
	}

	// A node's dir is part of what it locks.
	dirs := func(dir string) []byte {
		return []byte(`{"nodes": {"nixpkgs": ` + flakeNode("nixos", a, `, "dir": "`+dir+`"`) +
			`, "root": {"inputs": {"nixpkgs": "nixpkgs"}}}, "root": "root", "version": 7}`)
	}
	if pins := flakePins("flake.lock", dirs("lib"), dirs("pkgs")); len(pins) != 1 || !pins[0].Moved() {
		t.Errorf("dir moved: %+v", pins)
	}
}

// A clone command that hangs is stopped, and the configuration the
// environment passes git never reaches one.
func TestCloneCommands(t *testing.T) {
	w := setWorld(t)
	old, next := w.clone(t, "acme", "lib")
	p := Pin{Owner: "acme", Repo: "lib", OldRef: old, NewRef: next}
	for k, v := range map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": "core.abbrev", "GIT_CONFIG_VALUE_0": "40",
		"GIT_CONFIG_PARAMETERS": "'core.abbrev'='40'"} {
		t.Setenv(k, v)
	}
	text, err := cloneLog(t.Context(), w.o.Clones, p)
	if err != nil || strings.Contains(text.Body, next) || !strings.Contains(text.Body, next[:7]+" Drop the old flag") {
		t.Errorf("with the environment's config: %q, %v", text.Body, err)
	}

	dir := filepath.Join(w.o.Clones, "acme", "lib")
	if err := syscall.Mkfifo(filepath.Join(dir, ".git", "objects", "info", "alternates"), 0o644); err != nil {
		t.Fatal(err)
	}
	old2 := cloneTimeout
	cloneTimeout = 2 * time.Second
	t.Cleanup(func() { cloneTimeout = old2 })
	done := make(chan error, 1)
	go func() {
		_, err := cloneLog(context.Background(), w.o.Clones, p)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a clone whose alternates never end was read")
		}
	case <-time.After(20 * time.Second):
		f, _ := os.OpenFile(filepath.Join(dir, ".git", "objects", "info", "alternates"), os.O_WRONLY, 0)
		f.Close()
		t.Fatal("cloneLog hangs on a FIFO")
	}
}

// A refused cache says why, naming the directory and the mode it needs.
func TestCacheRefusedSays(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "upgrade")
	os.Mkdir(dir, 0o755)
	os.Chmod(dir, 0o755)
	if err := CacheProblem(Options{Cache: dir}); err == nil || !strings.Contains(err.Error(), dir) ||
		!strings.Contains(err.Error(), "0700") {
		t.Errorf("CacheProblem: %v", err)
	}
}
