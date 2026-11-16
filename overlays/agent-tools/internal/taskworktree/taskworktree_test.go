package taskworktree

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/refusal"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// fixture is a repository whose origin/main is one commit behind its main,
// checked out in the main checkout, so a worktree made from origin/main
// shows it was made from BASE and not from HEAD.
type fixture struct {
	main, root, base string
}

// newFixture makes the repository's main checkout at main, under a fresh
// directory; root is that directory's worktrees, where the task worktrees
// go.
func newFixture(t *testing.T, main string) fixture {
	t.Helper()
	dir := t.TempDir()
	f := fixture{main: filepath.Join(dir, main), root: filepath.Join(dir, "worktrees")}
	if err := os.MkdirAll(f.main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.main, "init", "-q", "-b", "main")
	gitIn(t, f.main, "commit", "-q", "--allow-empty", "-m", "First")
	f.base = gitIn(t, f.main, "rev-parse", "HEAD")
	gitIn(t, f.main, "update-ref", "refs/remotes/origin/main", f.base)
	gitIn(t, f.main, "commit", "-q", "--allow-empty", "-m", "Second")
	return f
}

// create runs Create from the main checkout.
func (f fixture) create(branch, base string) (Result, error) {
	return Create(f.main, f.root, branch, base)
}

// branchAt is the commit refs/heads/branch names, or "" without one.
func (f fixture) branchAt(t *testing.T, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", f.main, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// wantRefusal fails unless err is a refusal whose message holds each of
// want and goes on to say what to do instead.
func wantRefusal(t *testing.T, err error, want ...string) {
	t.Helper()
	var r refusal.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("got %v, want a refusal", err)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("refusal %q does not name %q", err, w)
		}
	}
	if !refusal.SaysInstead(err.Error()) {
		t.Errorf("refusal %q does not say what to do instead", err)
	}
}

func TestRoot(t *testing.T) {
	config := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("is the config's worktrees", func(t *testing.T) {
		path := config(t, `{"worktrees": "/h/.claude/worktrees"}`)
		if got, err := Root(path); err != nil || got != "/h/.claude/worktrees" {
			t.Errorf("got %q, %v, want /h/.claude/worktrees", got, err)
		}
	})

	t.Run("refuses a config without an absolute worktrees", func(t *testing.T) {
		for _, content := range []string{`{}`, `{"worktrees": "~/.claude/worktrees"}`, `not json`} {
			got, err := Root(config(t, content))
			if err == nil {
				t.Errorf("config %s: got %q, want an error", content, got)
			} else if !refusal.SaysInstead(err.Error()) {
				t.Errorf("config %s: %q does not say what to do instead", content, err)
			}
		}
	})
}

func TestCreate(t *testing.T) {
	t.Run("makes the branch from BASE and its worktree", func(t *testing.T) {
		f := newFixture(t, "src/repo")
		got, err := f.create("topic/one", "origin/main")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(f.root, "repo", "topic", "one")
		if got.Worktree != want || got.Branch != "topic/one" || got.Base != "refs/remotes/origin/main" || got.Commit != f.base || got.Recovered {
			t.Errorf("got %+v, want worktree %s on topic/one from refs/remotes/origin/main at %s", got, want, f.base)
		}
		if b := gitIn(t, want, "branch", "--show-current"); b != "topic/one" {
			t.Errorf("worktree is on %q, want topic/one", b)
		}
		if head := gitIn(t, want, "rev-parse", "HEAD"); head != f.base {
			t.Errorf("worktree is at %s, want BASE %s", head, f.base)
		}
		// --no-track: the signing pass refuses a branch that tracks the
		// default branch.
		if out, err := exec.Command("git", "-C", f.main, "config", "branch.topic/one.merge").Output(); err == nil {
			t.Errorf("topic/one tracks %s", out)
		}
		if b := gitIn(t, f.main, "branch", "--show-current"); b != "main" {
			t.Errorf("main checkout moved to %q", b)
		}
	})

	t.Run("refuses a worktree in the main checkout", func(t *testing.T) {
		// The main checkout is where the task worktrees go, so the
		// worktree would be inside it.
		f := newFixture(t, "worktrees/repo")
		_, err := f.create("topic", "origin/main")
		wantRefusal(t, err, "main checkout", f.main)
		if at := f.branchAt(t, "topic"); at != "" {
			t.Errorf("refused, but made topic at %s", at)
		}
	})

	t.Run("refuses a worktree in the main checkout reached through a link", func(t *testing.T) {
		// root/repo is a link to the main checkout, so the worktree's path
		// names the main checkout only once the link is resolved.
		f := newFixture(t, "src/repo")
		if err := os.MkdirAll(f.root, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(f.main, filepath.Join(f.root, "repo")); err != nil {
			t.Fatal(err)
		}
		_, err := f.create("topic", "origin/main")
		wantRefusal(t, err, "main checkout")
		if at := f.branchAt(t, "topic"); at != "" {
			t.Errorf("refused, but made topic at %s", at)
		}
	})

	t.Run("refuses a worktree whose path leads out of the root through a link", func(t *testing.T) {
		// A sandboxed agent can write root, so a link there could send the
		// worktree, made outside the sandbox, anywhere.
		f := newFixture(t, "src/repo")
		outside := t.TempDir()
		if err := os.MkdirAll(filepath.Join(f.root, "repo"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(f.root, "repo", "x")); err != nil {
			t.Fatal(err)
		}
		_, err := f.create("x/y", "origin/main")
		wantRefusal(t, err, "link")
		if _, err := os.Stat(filepath.Join(outside, "y")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("refused, but made the worktree through the link: %v", err)
		}
		if at := f.branchAt(t, "x/y"); at != "" {
			t.Errorf("refused, but made x/y at %s", at)
		}
	})

	t.Run("checks out with git-lfs told to leave pointers alone", func(t *testing.T) {
		// The lfs filter is TestMain's stand-in for git-lfs, which would
		// download outside the sandbox.
		f := newFixture(t, "src/repo")
		for name, content := range map[string]string{".gitattributes": "*.bin filter=lfs\n", "a.bin": "pointer\n"} {
			if err := os.WriteFile(filepath.Join(f.main, name), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gitIn(t, f.main, "add", ".gitattributes", "a.bin")
		gitIn(t, f.main, "commit", "-q", "-m", "Track a.bin in LFS")
		got, err := f.create("topic", "main")
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(got.Worktree, "a.bin"))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "skip=1\n" {
			t.Errorf("the lfs smudge wrote %q, want skip=1", data)
		}
	})

	t.Run("recovers a branch an earlier attempt left without a worktree", func(t *testing.T) {
		f := newFixture(t, "src/repo")
		// What a sandboxed `git worktree add -b` leaves: the branch, and
		// no worktree.
		gitIn(t, f.main, "branch", "--no-track", "topic", "origin/main")
		got, err := f.create("topic", "origin/main")
		if err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(f.root, "repo", "topic")
		if got.Worktree != want || !got.Recovered || got.Commit != f.base {
			t.Errorf("got %+v, want worktree %s recovered at %s", got, want, f.base)
		}
		if b := gitIn(t, want, "branch", "--show-current"); b != "topic" {
			t.Errorf("worktree is on %q, want topic", b)
		}
	})

	t.Run("refuses a branch that exists elsewhere than BASE", func(t *testing.T) {
		f := newFixture(t, "src/repo")
		gitIn(t, f.main, "branch", "--no-track", "topic", "main")
		moved := f.branchAt(t, "topic")
		_, err := f.create("topic", "origin/main")
		wantRefusal(t, err, "topic", "already exists")
		if at := f.branchAt(t, "topic"); at != moved {
			t.Errorf("topic moved from %s to %s", moved, at)
		}
		if _, err := os.Stat(filepath.Join(f.root, "repo", "topic")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("refused, but made the worktree: %v", err)
		}
	})

	t.Run("refuses a BASE that is no ref", func(t *testing.T) {
		f := newFixture(t, "src/repo")
		for _, base := range []string{f.base, "origin/main~1", "nonesuch", "--orphan"} {
			_, err := f.create("topic", base)
			wantRefusal(t, err, "not a ref")
			if at := f.branchAt(t, "topic"); at != "" {
				t.Errorf("BASE %q: refused, but made topic at %s", base, at)
			}
		}
	})

	t.Run("refuses a BRANCH git takes for no branch name", func(t *testing.T) {
		f := newFixture(t, "src/repo")
		for _, branch := range []string{"-f", "a..b", "@{-1}", "../out", ""} {
			_, err := f.create(branch, "origin/main")
			wantRefusal(t, err, "branch name")
		}
		if out := gitIn(t, f.main, "worktree", "list", "--porcelain"); strings.Count(out, "worktree ") != 1 {
			t.Errorf("refused, but made a worktree:\n%s", out)
		}
	})
}
