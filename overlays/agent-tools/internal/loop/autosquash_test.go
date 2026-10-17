package loop

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A trial autosquash runs in a scratch worktree it removes afterwards,
// with the ignored generated files the repository's settings name copied
// in, so each squashed commit can be built.
func TestAutosquash(t *testing.T) {
	// squashRepo is a checkout of o/r with feat on main: "Add a", a fixup
	// of it, and an ignored generated file. It returns the checkout.
	squashRepo := func(t *testing.T) string {
		t.Helper()
		t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
		t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
		dir := t.TempDir()
		write := func(name, text string) {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
			os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644)
		}
		gitIn(t, dir, "init", "-q", "-b", "main")
		gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
		write(".gitignore", "gen/\n")
		gitIn(t, dir, "add", ".gitignore")
		gitIn(t, dir, "commit", "-qm", "base")
		gitIn(t, dir, "switch", "-qc", "feat")
		write("a", "1\n")
		gitIn(t, dir, "add", "a")
		gitIn(t, dir, "commit", "-qm", "Add a")
		write("a", "2\n")
		gitIn(t, dir, "commit", "-qam", "fixup! Add a")
		write("gen/api/x.pb.go", "generated\n")
		write("gen/other.txt", "not named\n")
		useDir(t, dir)
		return dir
	}
	worktrees := func(t *testing.T, dir string) int {
		t.Helper()
		return strings.Count(gitIn(t, dir, "worktree", "list", "--porcelain"), "worktree ")
	}

	t.Run("squashes and runs each commit", func(t *testing.T) {
		dir := squashRepo(t)
		var log bytes.Buffer
		r, err := Autosquash("main", []string{"gen/**/*.pb.go"}, "test -f gen/api/x.pb.go && test ! -e gen/other.txt && grep -qx 2 a", &log)
		if err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
		if !r.SameTree || len(r.Commits) != 1 || r.Commits[0].Subject != "Add a" || r.Commits[0].Exit == nil || *r.Commits[0].Exit != 0 {
			t.Errorf("result %+v\n%s", r, log.String())
		}
		if strings.Join(r.Copied, " ") != "gen/api/x.pb.go" {
			t.Errorf("copied %v", r.Copied)
		}
		if n := worktrees(t, dir); n != 1 {
			t.Errorf("%d worktrees left", n)
		}
	})

	// A command that changes tracked files, as a formatter does, does not
	// stop the next commit's checkout.
	t.Run("a command that changes tracked files", func(t *testing.T) {
		dir := squashRepo(t)
		os.WriteFile(filepath.Join(dir, "a"), []byte("3\n"), 0o644)
		gitIn(t, dir, "commit", "-qam", "Change a")
		var log bytes.Buffer
		r, err := Autosquash("main", nil, "echo changed >> a", &log)
		if err != nil {
			t.Fatalf("%v\n%s", err, log.String())
		}
		if len(r.Commits) != 2 || !r.OK() {
			t.Errorf("result %+v\n%s", r, log.String())
		}
	})

	t.Run("a failing command", func(t *testing.T) {
		squashRepo(t)
		r, err := Autosquash("main", nil, "false", &bytes.Buffer{})
		if err != nil || r.Commits[0].Exit == nil || *r.Commits[0].Exit != 1 || r.OK() {
			t.Errorf("result %+v, %v", r, err)
		}
	})

	// A fixup that does not apply where autosquash moves it is named, and
	// the scratch worktree is still removed.
	t.Run("a fixup that conflicts", func(t *testing.T) {
		dir := squashRepo(t)
		os.WriteFile(filepath.Join(dir, "a"), []byte("3\n"), 0o644)
		gitIn(t, dir, "commit", "-qam", "Change a")
		os.WriteFile(filepath.Join(dir, "a"), []byte("4\n"), 0o644)
		gitIn(t, dir, "commit", "-qam", "fixup! Add a")
		_, err := Autosquash("main", nil, "", &bytes.Buffer{})
		if !refused(err) || !strings.Contains(err.Error(), "fixup! Add a") {
			t.Errorf("conflict: %v", err)
		}
		if n := worktrees(t, dir); n != 1 {
			t.Errorf("%d worktrees left", n)
		}
	})

	// A scratch worktree a killed run left behind is cleared first.
	t.Run("a stale scratch worktree", func(t *testing.T) {
		dir := squashRepo(t)
		stale := filepath.Join(t.TempDir(), autosquashPrefix+"old")
		gitIn(t, dir, "worktree", "add", "-q", "--detach", stale, "HEAD")
		os.RemoveAll(stale)
		if _, err := Autosquash("main", nil, "", &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		if n := worktrees(t, dir); n != 1 {
			t.Errorf("%d worktrees left", n)
		}
	})

	// In a loop, the branch's parent is the upstream by default.
	t.Run("the loop's parent", func(t *testing.T) {
		squashRepo(t)
		state := &State{Repo: "o/r", Order: []string{"feat"}, Branches: map[string]*Branch{"feat": {PR: 1, Base: "main"}}}
		if err := state.save(); err != nil {
			t.Fatal(err)
		}
		if r, err := Autosquash("", nil, "", &bytes.Buffer{}); err != nil || !r.SameTree || len(r.Commits) != 1 {
			t.Errorf("result %+v, %v", r, err)
		}
	})
}
