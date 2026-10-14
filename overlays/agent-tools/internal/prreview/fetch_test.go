package prreview

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetch(t *testing.T) {
	t.Run("first round", func(t *testing.T) {
		r := newRemote(t, nil)
		f := setUp(t, r)
		pr, err := Fetch(t.Context(), "o/r", 7)
		if err != nil {
			t.Fatal(err)
		}
		if pr.Head != r.head || pr.Base != r.base || pr.BaseRef != "main" || pr.Title != "Make A two" {
			t.Errorf("fetched %+v, want head %s and base %s", pr, r.head, r.base)
		}
		cache := os.Getenv("XDG_CACHE_HOME")
		if pr.Clone != filepath.Join(cache, "pr-review", "o", "r", "7", "clone") ||
			pr.Dir != filepath.Join(cache, "pr-review", "o", "r", "7", r.head[:12]) {
			t.Errorf("clone %s, review %s", pr.Clone, pr.Dir)
		}
		if info, err := os.Stat(pr.Dir); err != nil || !info.IsDir() {
			t.Errorf("no review directory: %v", err)
		}
		if bare := gitT(t, pr.Clone, "rev-parse", "--is-bare-repository"); bare != "true" {
			t.Errorf("the clone has a work tree, which a checkout on this side could fill")
		}
		if len(pr.Issues) != 1 || pr.Issues[0].Number != 3 {
			t.Errorf("issues: %+v", pr.Issues)
		}
		if f.mutations != 0 {
			t.Errorf("%d writes reached GitHub", f.mutations)
		}
	})

	// The next round fetches into the same clone.
	t.Run("next round", func(t *testing.T) {
		r := newRemote(t, nil)
		f := setUp(t, r)
		if _, err := Fetch(t.Context(), "o/r", 7); err != nil {
			t.Fatal(err)
		}
		r.push(t)
		f.pr["headRefOid"] = r.head
		pr, err := Fetch(t.Context(), "o/r", 7)
		if err != nil || pr.Head != r.head {
			t.Fatalf("second round: %+v, %v", pr, err)
		}
		if f.mutations != 0 {
			t.Errorf("%d writes reached GitHub", f.mutations)
		}
	})

	// A head other than the one GitHub reports, such as one pushed between
	// the query and the fetch, is refused.
	t.Run("pins the head", func(t *testing.T) {
		r := newRemote(t, nil)
		f := setUp(t, r)
		f.pr["headRefOid"] = r.base
		_, err := Fetch(t.Context(), "o/r", 7)
		if err == nil || !strings.Contains(err.Error(), r.base) {
			t.Fatalf("a head that moved was accepted: %v", err)
		}
		if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "pr-review", "o", "r", "7", r.head[:12])); err == nil {
			t.Error("a review directory was made for an unpinned head")
		}
	})

	for _, c := range []struct {
		slug   string
		number int
	}{{"o/..", 7}, {"../r", 7}, {"o/r/x", 7}, {"o", 7}, {"o/r", 0}, {"o/r", -1}, {".git/r", 7}} {
		t.Run("refuses/"+c.slug+"#"+strconv.Itoa(c.number), func(t *testing.T) {
			r := newRemote(t, nil)
			setUp(t, r)
			if _, err := Fetch(t.Context(), c.slug, c.number); err == nil {
				t.Errorf("%s#%d was fetched", c.slug, c.number)
			}
		})
	}

	t.Run("hooks do not run", testFetchHooksDoNotRun)
	t.Run("LFS stays inert", testFetchLFSStaysInert)
	t.Run("symlinks check out as files", testFetchSymlinksCheckOutAsFiles)
}

// testFetchHooksDoNotRun holds every git command on the clone and its
// checkouts to running no hook, wherever one is planted.
func testFetchHooksDoNotRun(t *testing.T) {
	r := newRemote(t, nil)
	f := setUp(t, r)
	pr, err := Fetch(t.Context(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, hook := range []string{"reference-transaction", "post-checkout", "post-index-change", "fsmonitor-watchman"} {
		write(t, filepath.Join(pr.Clone, "hooks", hook), "#!/bin/sh\ntouch "+marker+"\n")
		os.Chmod(filepath.Join(pr.Clone, "hooks", hook), 0o755)
	}
	r.push(t)
	f.pr["headRefOid"] = r.head
	if pr, err = Fetch(t.Context(), "o/r", 7); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(t.TempDir(), "tree")
	if err := Checkout(t.Context(), pr.Clone, tree, pr.Head); err != nil {
		t.Fatal(err)
	}
	RemoveCheckout(pr.Clone, tree)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a hook ran")
	}
	// The fixture's hooks run where git is not told otherwise.
	gitT(t, pr.Clone, "update-ref", "refs/probe", pr.Head)
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the fixture's hook does not run at all, so it shows nothing: %v", err)
	}
}

// testFetchLFSStaysInert: a pull request that marks its files for Git LFS
// and points LFS at a server of its choosing gets neither the user's LFS
// filter run on this side nor that server contacted.
func testFetchLFSStaysInert(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	var contacted atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			contacted.Add(1)
			conn.Close()
		}
	}()
	pointer := "version https://git-lfs.github.com/spec/v1\n" +
		"oid sha256:4d7a214614ab2935c943f9e0ff69d22eadbb8f32b1258daaa5e2ca24d17e2393\nsize 12345\n"
	r := newRemote(t, func(work string) {
		write(t, filepath.Join(work, ".gitattributes"), "*.bin filter=lfs diff=lfs merge=lfs -text\n")
		write(t, filepath.Join(work, ".lfsconfig"), "[lfs]\n\turl = http://"+listener.Addr().String()+"/\n")
		write(t, filepath.Join(work, "big.bin"), pointer)
	})
	setUp(t, r)

	// The user's global config defines the LFS filter as required, the way
	// `git lfs install` does, with a step that leaves a marker.
	marker := filepath.Join(t.TempDir(), "filter-ran")
	lfs := "cat"
	if path, err := exec.LookPath("git-lfs"); err == nil {
		lfs = path + " smudge -- %f"
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	write(t, global, "[filter \"lfs\"]\n\trequired = true\n"+
		"\tsmudge = \"sh -c 'touch "+marker+"; exec "+lfs+"' --\"\n"+
		"\tclean = \"sh -c 'touch "+marker+"; cat'\"\n")
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "")

	pr, err := Fetch(t.Context(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(t.TempDir(), "tree")
	if err := Checkout(t.Context(), pr.Clone, tree, pr.Head); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(tree, "big.bin")); string(data) != pointer {
		t.Errorf("big.bin is not the pointer as committed: %q", data)
	}
	RemoveCheckout(pr.Clone, tree)
	if _, err := os.Stat(marker); err == nil {
		t.Error("the user's LFS filter ran")
	}
	if n := contacted.Load(); n != 0 {
		t.Errorf("the pull request's LFS server was contacted %d times", n)
	}

	// The fixture does what it is there to show where git reads the
	// user's config.
	control := filepath.Join(t.TempDir(), "control")
	gitT(t, "", "clone", "-q", "--no-checkout", r.bare, control)
	exec.Command("git", "-C", control, "checkout", "-q", r.head).Run()
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the fixture's filter does not run under the user's config, so it shows nothing: %v", err)
	}
	if _, err := exec.LookPath("git-lfs"); err == nil && contacted.Load() == 0 {
		t.Error("Git LFS does not contact the fixture's server under the user's config, so it shows nothing")
	}
}

// testFetchSymlinksCheckOutAsFiles: a link in the pull request cannot point
// a reader of the checkout at a file outside it.
func testFetchSymlinksCheckOutAsFiles(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, secret, "token\n")
	r := newRemote(t, func(work string) {
		if err := os.Symlink(secret, filepath.Join(work, "link")); err != nil {
			t.Fatal(err)
		}
	})
	setUp(t, r)
	pr, err := Fetch(t.Context(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(t.TempDir(), "tree")
	if err := Checkout(t.Context(), pr.Clone, tree, pr.Head); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { RemoveCheckout(pr.Clone, tree) })
	info, err := os.Lstat(filepath.Join(tree, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Error("the link was checked out as a link")
	}
	if data, _ := os.ReadFile(filepath.Join(tree, "link")); string(data) == "token\n" {
		t.Error("the checkout reads the file outside it")
	}
}
