package loop

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/env"
	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// inRepo is the test's context with an Env of its own, this process's as
// TestMain left it with vars set, and git aimed at dir (git.At), which
// drops any GIT_DIR and GIT_WORK_TREE among vars from what git sees.
func inRepo(t *testing.T, dir string, vars ...string) context.Context {
	t.Helper()
	return git.At(env.With(t.Context(), env.Process().Set(vars...)), dir)
}

func TestIdentity(t *testing.T) {
	t.Parallel()
	t.Run("negative offset", func(t *testing.T) {
		got, err := identity("Wesley Nelson <wgn@wgn.dev> 1790750341 -0700")
		if err != nil {
			t.Fatal(err)
		}
		// The date `git log --date=iso-strict` prints for this commit.
		if got["name"] != "Wesley Nelson" || got["email"] != "wgn@wgn.dev" || got["date"] != "2026-09-29T23:39:01-07:00" {
			t.Errorf("identity: %v", got)
		}
	})

	t.Run("positive offset", func(t *testing.T) {
		if got, _ := identity("A <a@b> 0 +0530"); got["date"] != "1970-01-01T05:30:00+05:30" {
			t.Errorf("positive offset: %v", got)
		}
	})
}

func TestVerdict(t *testing.T) {
	t.Parallel()
	verdicts := map[string]string{"approval": "Approval recommended", "changes": "Changes recommended", "error": "encountered an error"}

	t.Run("heading wins over prose", func(t *testing.T) {
		body := "<!-- ccr-overview-v2 -->\n## Copilot review overview\n### 🟡 Changes recommended\nApproval recommended once fixed."
		if v := verdictOf(verdicts, body); v != "changes" {
			t.Errorf("heading should win over prose: %s", v)
		}
	})

	t.Run("body fallback", func(t *testing.T) {
		if v := verdictOf(verdicts, "Copilot encountered an error and was unable to review"); v != "error" {
			t.Errorf("body fallback: %s", v)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		if v := verdictOf(verdicts, "nothing"); v != "unknown" {
			t.Errorf("unknown: %s", v)
		}
	})
}

func TestStackOrder(t *testing.T) {
	t.Parallel()
	prs := map[string]*PR{"a": {BaseRefName: "main"}, "b": {BaseRefName: "a"}, "c": {BaseRefName: "b"}, "x": {BaseRefName: "main"}}
	if got := strings.Join(stackOrder(prs, "b"), ","); got != "a,b,c" {
		t.Errorf("stack order: %s", got)
	}
}

func TestCheckFixups(t *testing.T) {
	t.Parallel()
	c := func(message string) Commit { return Commit{Message: message, Author: "T <t@e>"} }
	a, b := c("A first"), c("B second")

	t.Run("fixups and nested fixups", func(t *testing.T) {
		fix := c("fixup! A first\n\nReview-Thread: x")
		nested := c("fixup! fixup! B second")
		if err := checkFixups(t.Context(), "br", []Commit{a, b}, []Commit{a, b, fix, nested}); err != nil {
			t.Errorf("fixups refused: %v", err)
		}
	})

	t.Run("plain commit", func(t *testing.T) {
		if err := checkFixups(t.Context(), "br", []Commit{a, b}, []Commit{a, b, c("stray")}); err == nil {
			t.Error("a plain commit was accepted")
		}
	})

	t.Run("fixup of another branch's commit", func(t *testing.T) {
		if err := checkFixups(t.Context(), "br", []Commit{a}, []Commit{a, c("fixup! elsewhere")}); err == nil {
			t.Error("a fixup of another branch's commit was accepted")
		}
	})

	t.Run("rewritten original", func(t *testing.T) {
		if err := checkFixups(t.Context(), "br", []Commit{a, b}, []Commit{c("A reworded"), b}); err == nil {
			t.Error("a rewritten original was accepted")
		}
	})

	t.Run("fixup of an amend", func(t *testing.T) {
		if err := checkFixups(t.Context(), "br", []Commit{a, b}, []Commit{a, b, c("fixup! amend! A first\n\nignored")}); err != nil {
			t.Errorf("a fixup! of an amend! refused: %v", err)
		}
	})

	for name, rewrite := range map[string]string{
		"amend":          "amend! A first\n\nA renamed",
		"squash":         "squash! B second\n\nmore",
		"amend of fixup": "amend! fixup! A first\n\nA renamed",
	} {
		t.Run("rewrite/"+name, func(t *testing.T) {
			if err := checkFixups(t.Context(), "br", []Commit{a, b}, []Commit{a, b, c(rewrite)}); err == nil || !strings.Contains(err.Error(), "--fixup=<commit>") {
				t.Errorf("%q: %v", rewrite, err)
			}
		})
	}

	for message, problem := range map[string]string{
		"fixup! A first\n\nReview-Thread: x\nSigned-off-by: T <t@e>": "Signed-off-by",
		"fixup! A first\n\nCo-Authored-By: C <c@e>":                  "Co-Authored-By",
		"fixup! A first\n\nAs in thread tZ1Cm.\n\nReview-Thread: x":  "tZ1Cm",
	} {
		t.Run("trailer/"+problem, func(t *testing.T) {
			err := checkFixups(t.Context(), "br", []Commit{a, b}, []Commit{a, b, c(message)})
			if _, ok := err.(Refusal); !ok || !strings.Contains(err.Error(), problem) || !strings.Contains(err.Error(), `"fixup! A first"`) ||
				!refusal.SaysInstead(err.Error()) {
				t.Errorf("%q: %v", message, err)
			}
		})
	}
}

func TestCommitBody(t *testing.T) {
	t.Parallel()
	t.Run("signed commit", func(t *testing.T) {
		raw := "tree abc\nparent p1\nauthor A <a@b> 1700000000 +0000\ncommitter C <c@d> 1700000001 -0100\n" +
			"gpgsig -----BEGIN PGP SIGNATURE-----\n \n line\n -----END PGP SIGNATURE-----\n\nSubject\n\nBody\n"
		body, err := commitBody("sha", []byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if body["message"] != "Subject\n\nBody\n" || body["tree"] != "abc" {
			t.Errorf("body: %v", body)
		}
		if body["signature"] != "-----BEGIN PGP SIGNATURE-----\n\nline\n-----END PGP SIGNATURE-----" {
			t.Errorf("signature: %q", body["signature"])
		}
	})

	t.Run("unreproducible header", func(t *testing.T) {
		if _, err := commitBody("sha", []byte("tree a\nmergetag x\n\nm")); err == nil {
			t.Error("an unreproducible header was accepted")
		}
	})
}

func TestCheckOpen(t *testing.T) {
	t.Parallel()
	repo := &Repo{Slug: "o/r", Default: "trunk", Viewer: "me"}
	pr := func(number int, head, author string) *PR {
		p := &PR{Number: number, HeadRefName: head}
		p.Author.Login = author
		return p
	}
	// view is what GitHub shows of o/r; each case builds its own.
	view := func() *remoteView {
		return &remoteView{
			Heads: map[string]string{"trunk": "t0", "parent": "p0", "theirs": "x0", "pushed": "q0"},
			PRs:   map[string]*PR{"parent": pr(1, "parent", "me"), "theirs": pr(2, "theirs", "other"), "taken": pr(3, "taken", "me"), "gone": pr(4, "gone", "me")},
		}
	}
	refused := map[string][2]string{
		"default head":       {"trunk", "trunk"},
		"main head":          {"main", "trunk"},
		"own base":           {"new", "new"},
		"head with a PR":     {"taken", "trunk"},
		"head at other tip":  {"pushed", "trunk"},
		"base without a PR":  {"new", "pushed"},
		"someone else's PR":  {"new", "theirs"},
		"base not on GitHub": {"new", "gone"},
	}
	for name, c := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			// l0 is each head's local tip.
			_, err := checkOpen(repo, view(), c[0], c[1], "l0")
			if _, ok := err.(Refusal); !ok {
				t.Errorf("%s: %v", name, err)
			}
		})
	}

	t.Run("on the default branch", func(t *testing.T) {
		if oid, err := checkOpen(repo, view(), "new", "trunk", "l0"); err != nil || oid != "t0" {
			t.Errorf("on the default branch: %s, %v", oid, err)
		}
	})

	t.Run("head on GitHub at the local tip", func(t *testing.T) {
		if oid, err := checkOpen(repo, view(), "pushed", "trunk", "q0"); err != nil || oid != "t0" {
			t.Errorf("head on GitHub at the local tip: %s, %v", oid, err)
		}
	})

	t.Run("stacked", func(t *testing.T) {
		if oid, err := checkOpen(repo, view(), "new", "parent", "l0"); err != nil || oid != "p0" {
			t.Errorf("stacked: %s, %v", oid, err)
		}
	})
}

// localStack is a checkout of o/r, with origin's trunk fetched, and these
// branches: parent on trunk and child on parent, each adding a file; aside,
// on trunk but not on parent; empty, with no commits of its own; period,
// whose commit's subject ends with one; reworded, with an amend! that fixes
// it; and attributed, whose fixup! credits a co-author.
type localStack struct {
	dir, trunk, parent, child string
	// ctx aims the package's git at dir.
	ctx context.Context
}

func newLocalStack(t *testing.T) *localStack {
	t.Helper()
	s := &localStack{dir: t.TempDir()}
	dir := s.dir
	write := func(name string) { os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644) }
	gitIn(t, dir, "init", "-q", "-b", "trunk")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, dir, "remote", "add", "another", "git@github.com:o/r.git")
	gitIn(t, dir, "remote", "add", "fork", "https://github.com/f/r.git")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	s.trunk = gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "update-ref", "refs/remotes/origin/trunk", s.trunk)
	gitIn(t, dir, "switch", "-qc", "parent")
	write("p")
	gitIn(t, dir, "add", "p")
	gitIn(t, dir, "commit", "-qm", "parent")
	s.parent = gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "switch", "-qc", "child")
	write("c")
	gitIn(t, dir, "add", "c")
	gitIn(t, dir, "commit", "-qm", "child")
	s.child = gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "switch", "-qc", "aside", "trunk")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "aside")
	gitIn(t, dir, "branch", "empty", "trunk")
	gitIn(t, dir, "switch", "-qc", "period", "trunk")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add x.")
	gitIn(t, dir, "switch", "-qc", "reworded")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "amend! Add x.", "-m", "Add x")
	gitIn(t, dir, "switch", "-qc", "attributed", "trunk")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add y")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "fixup! Add y", "-m", "Co-Authored-By: C <c@e>")
	s.ctx = inRepo(t, dir)
	return s
}

func TestCheckLocal(t *testing.T) {
	t.Parallel()
	repo := &Repo{Slug: "o/r", Default: "trunk"}
	unfetched := strings.Repeat("1", 40)

	t.Run("stacked", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		local, known, err := checkLocal(s.ctx, repo, "child", "parent", s.parent, s.child, false)
		if err != nil || local != s.child {
			t.Fatalf("stacked: %s, %v", local, err)
		}
		if got := strings.Join(known, " "); got != s.parent+" --remotes=origin/* --remotes=another/*" {
			t.Errorf("known: %s", got)
		}
		objects, err := missingObjects(s.ctx, []string{local}, known)
		if err != nil {
			t.Fatal(err)
		}
		if got := objects.counts(); got["commit"] != 1 || got["tree"] != 1 || got["blob"] != 1 || len(objects.commits) != 1 {
			t.Errorf("objects over the parent: %v", got)
		}
	})

	// The default branch may have moved on GitHub past what was fetched;
	// what was fetched is still left out of the upload.
	t.Run("default base not fetched", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		if _, known, err := checkLocal(s.ctx, repo, "child", "trunk", unfetched, s.child, false); err != nil || strings.Join(known, " ") != "--remotes=origin/* --remotes=another/*" {
			t.Errorf("default base not fetched: %v, %v", known, err)
		}
	})

	// Each case names its branch, its base, and the base's commit, which is
	// trunk's unless it says otherwise.
	refused := map[string][3]string{
		"no new commits":     {"empty", "trunk", ""},
		"parent not fetched": {"child", "parent", unfetched},
		"not on its parent":  {"aside", "parent", "parent"},
		"Add x.":             {"period", "trunk", ""},
		"Co-Authored-By":     {"attributed", "trunk", ""},
	}
	for name, c := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			t.Parallel()
			s := newLocalStack(t)
			oid := c[2]
			switch oid {
			case "":
				oid = s.trunk
			case "parent":
				oid = s.parent
			}
			_, _, err := checkLocal(s.ctx, repo, c[0], c[1], oid, gitIn(t, s.dir, "rev-parse", c[0]), false)
			if _, ok := err.(Refusal); !ok || !refusal.SaysInstead(err.Error()) {
				t.Errorf("%s: %v", name, err)
			}
			if (name == "Add x." || name == "Co-Authored-By") && !strings.Contains(fmt.Sprint(err), name) {
				t.Errorf("%s: %v does not name it", name, err)
			}
		})
	}

	t.Run("message an amend! rewords", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		if _, _, err := checkLocal(s.ctx, repo, "reworded", "trunk", s.trunk, gitIn(t, s.dir, "rev-parse", "reworded"), false); err != nil {
			t.Errorf("a message an amend! rewords: %v", err)
		}
	})

	t.Run("upstream", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		gitIn(t, s.dir, "branch", "--set-upstream-to=origin/trunk", "child")
		if err := setUpstream(s.ctx, repo, "child"); err != nil {
			t.Fatal(err)
		}
		if got := gitIn(t, s.dir, "config", "branch.child.remote") + " " + gitIn(t, s.dir, "config", "branch.child.merge"); got != "origin refs/heads/child" {
			t.Errorf("upstream: %s", got)
		}
	})
}

// TestCheckLocalParent covers a branch that contains a branch GitHub has
// but its base lacks: its pull request would carry the parent's commits.
func TestCheckLocalParent(t *testing.T) {
	t.Parallel()
	repo := &Repo{Slug: "o/r", Default: "trunk"}
	// pushed publishes branches to a bare repository that origin pushes to,
	// which leaves origin/<branch> in the checkout as a fetch would.
	pushed := func(t *testing.T, s *localStack, branches ...string) {
		t.Helper()
		bare := t.TempDir()
		gitIn(t, bare, "init", "-q", "--bare")
		gitIn(t, s.dir, "config", "remote.origin.pushurl", bare)
		gitIn(t, s.dir, append([]string{"push", "-q", "origin"}, branches...)...)
	}
	grandchild := func(t *testing.T, s *localStack) {
		t.Helper()
		gitIn(t, s.dir, "switch", "-qc", "grandchild", "child")
		gitIn(t, s.dir, "commit", "-q", "--allow-empty", "-m", "grandchild")
	}

	t.Run("default base refused, naming the parent", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		pushed(t, s, "parent")
		_, _, err := checkLocal(s.ctx, repo, "child", "trunk", s.trunk, s.child, false)
		if _, ok := err.(Refusal); !ok || !refusal.SaysInstead(err.Error()) || !strings.Contains(fmt.Sprint(err), "--base parent") {
			t.Errorf("default base over a pushed parent: %v", err)
		}
	})

	t.Run("nearest parent when several", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		grandchild(t, s)
		pushed(t, s, "parent", "child")
		_, _, err := checkLocal(s.ctx, repo, "grandchild", "trunk", s.trunk, gitIn(t, s.dir, "rev-parse", "grandchild"), false)
		if _, ok := err.(Refusal); !ok || !strings.Contains(fmt.Sprint(err), "--base child") {
			t.Errorf("nearest parent: %v", err)
		}
	})

	t.Run("explicit base that is the parent passes", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		pushed(t, s, "parent")
		if _, _, err := checkLocal(s.ctx, repo, "child", "parent", s.parent, s.child, false); err != nil {
			t.Errorf("explicit parent base: %v", err)
		}
	})

	t.Run("explicit base above the nearest parent refused", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		grandchild(t, s)
		pushed(t, s, "parent", "child")
		_, _, err := checkLocal(s.ctx, repo, "grandchild", "parent", s.parent, gitIn(t, s.dir, "rev-parse", "grandchild"), false)
		if _, ok := err.(Refusal); !ok || !strings.Contains(fmt.Sprint(err), "--base child") {
			t.Errorf("base below the nearest parent: %v", err)
		}
	})

	t.Run("parent already in the base passes", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		pushed(t, s, "parent")
		if _, _, err := checkLocal(s.ctx, repo, "child", "trunk", s.parent, s.child, false); err != nil {
			t.Errorf("merged parent: %v", err)
		}
	})

	t.Run("unrelated remote branch passes", func(t *testing.T) {
		t.Parallel()
		s := newLocalStack(t)
		pushed(t, s, "aside")
		if _, _, err := checkLocal(s.ctx, repo, "child", "trunk", s.trunk, s.child, false); err != nil {
			t.Errorf("unrelated remote branch: %v", err)
		}
	})
}

// gitIn runs git in dir with a fixed identity and signing off.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitWith(t, dir, nil, args...)
}

// gitWith is gitIn with vars, each "NAME=value", added to git's
// environment.
func gitWith(t *testing.T, dir string, vars []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), vars...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSetup(t *testing.T) {
	t.Parallel()
	// setupRepo makes a clone and a decoy repository, and returns a context
	// that aims the package's git at the clone, one whose Env points git at
	// the decoy, as git does for a command it starts, the clone, and its
	// agent settings file, which the test may write.
	setupRepo := func(t *testing.T) (ctx, decoyed context.Context, dir, settings string) {
		t.Helper()
		dir, _ = filepath.EvalSymlinks(t.TempDir())
		gitIn(t, dir, "init", "-q", "-b", "main")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
		_, decoy := gitenvtest.DecoyEnv(t)
		settings = filepath.Join(dir, ".git", "info", "agents.json")
		os.MkdirAll(filepath.Dir(settings), 0o755)
		return inRepo(t, dir, decoy...), withEnv(t.Context(), decoy...), dir, settings
	}
	const configured = `{"setup": "echo generated > gen"}`

	t.Run("command without settings", func(t *testing.T) {
		t.Parallel()
		ctx, _, _, _ := setupRepo(t)
		if s, err := SetupCommand(ctx); err != nil || s.Command != "" {
			t.Errorf("without settings: %+v, %v", s, err)
		}
	})

	t.Run("configured command", func(t *testing.T) {
		t.Parallel()
		ctx, _, _, settings := setupRepo(t)
		os.WriteFile(settings, []byte(configured), 0o644)
		s, err := SetupCommand(ctx)
		if err != nil || s.Command != "echo generated > gen" || s.Settings != settings {
			t.Fatalf("configured: %+v, %v", s, err)
		}
	})

	t.Run("runs in a worktree", func(t *testing.T) {
		t.Parallel()
		ctx, decoyed, dir, settings := setupRepo(t)
		os.WriteFile(settings, []byte(configured), 0o644)
		s, err := SetupCommand(ctx)
		if err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "branch", "feature")
		wt := filepath.Join(t.TempDir(), "wt")
		gitIn(t, dir, "worktree", "add", "-q", wt, "feature")
		var log bytes.Buffer
		runs := RunSetup(decoyed, s.Command, []string{wt}, &log)
		if len(runs) != 1 || runs[0].Exit != 0 {
			t.Fatalf("setup did not run in the worktree: %+v\n%s", runs, log.String())
		}
		if _, err := os.Stat(filepath.Join(wt, "gen")); err != nil {
			t.Errorf("setup wrote nothing in the worktree: %v", err)
		}
		if !strings.Contains(log.String(), "setup in "+wt+": echo generated > gen\n") || !strings.Contains(log.String(), ": exit 0\n") {
			t.Errorf("the command or its status was not reported:\n%s", log.String())
		}
		if got := gitIn(t, wt, "status", "--porcelain"); got != "?? gen" {
			t.Errorf("setup left more than its output in the worktree: %q", got)
		}
		if runs := RunSetup(decoyed, "exit 3", []string{wt}, &log); runs[0].Exit != 3 {
			t.Errorf("a failed setup: %+v", runs)
		}
		// The command's git finds the worktree's repository.
		RunSetup(decoyed, "git rev-parse --absolute-git-dir > where", []string{wt}, &log)
		where, _ := os.ReadFile(filepath.Join(wt, "where"))
		if got, want := strings.TrimSpace(string(where)), gitIn(t, wt, "rev-parse", "--absolute-git-dir"); got != want {
			t.Errorf("the setup command's git found %q, not %q", got, want)
		}
	})
}

// The setup command runs with the variables of the context's Env, but
// those that would send its git to another repository.
func TestRunSetupEnv(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx := withEnv(t.Context(), "SETUP_SAYS=from the Env", "GIT_DIR="+filepath.Join(t.TempDir(), ".git"))
	var log bytes.Buffer
	runs := RunSetup(ctx, `printf '%s|%s' "$SETUP_SAYS" "${GIT_DIR-unset}" > said`, []string{dir}, &log)
	if len(runs) != 1 || runs[0].Exit != 0 {
		t.Fatalf("setup failed: %+v\n%s", runs, log.String())
	}
	if said, _ := os.ReadFile(filepath.Join(dir, "said")); string(said) != "from the Env|unset" {
		t.Errorf("the setup command saw %q, want the Env's SETUP_SAYS and no GIT_DIR", said)
	}
}

// fixedStack is a two-branch stack, lower and upper, with a fixup in each,
// rebase.updateRefs on and main checked out.
type fixedStack struct {
	dir string
	// ctx aims the package's git at dir, with a fixed identity.
	ctx context.Context
}

func newFixedStack(t *testing.T) *fixedStack {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) { os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644) }
	gitIn(t, dir, "init", "-q", "-b", "main")
	write("f", "base\n")
	gitIn(t, dir, "add", "f")
	gitIn(t, dir, "commit", "-qm", "base")
	gitIn(t, dir, "switch", "-qc", "lower")
	write("a", "a\n")
	gitIn(t, dir, "add", "a")
	gitIn(t, dir, "commit", "-qm", "L1 add a")
	write("b", "b\n")
	gitIn(t, dir, "add", "b")
	gitIn(t, dir, "commit", "-qm", "L2 add b")
	gitIn(t, dir, "switch", "-qc", "upper")
	write("c", "c\n")
	gitIn(t, dir, "add", "c")
	gitIn(t, dir, "commit", "-qm", "U1 add c")

	_, decoy := gitenvtest.DecoyEnv(t)
	ctx := inRepo(t, dir, append(decoy, "GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@e",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@e")...)

	oldLower := gitIn(t, dir, "rev-parse", "lower")
	gitIn(t, dir, "switch", "-q", "lower")
	write("a", "a fixed\n")
	gitIn(t, dir, "commit", "-qa", "--fixup=lower~1", "--trailer", "Review-Thread: https://x/1")
	gitIn(t, dir, "switch", "-q", "upper")
	gitIn(t, dir, "rebase", "-q", "--onto", "lower", oldLower, "upper")
	write("c", "c fixed\n")
	gitIn(t, dir, "commit", "-qa", "--fixup=HEAD")

	// With neither branch checked out, rebase.updateRefs would move both onto
	// the rehearsal's unsigned commits.
	gitIn(t, dir, "config", "rebase.updateRefs", "true")
	gitIn(t, dir, "switch", "-q", "main")
	return &fixedStack{dir: dir, ctx: ctx}
}

// stack is what the rehearsal checks the stack against as it stands: each
// branch's own commits, L1 and L2, and U1, and its tree.
func (s *fixedStack) stack(t *testing.T) *signedStack {
	t.Helper()
	st := &signedStack{Order: []string{"lower", "upper"}, Upstream: gitIn(t, s.dir, "rev-parse", "main"),
		Trees: map[string]string{}, Commits: map[string][]Commit{}}
	below := "main"
	for _, b := range st.Order {
		st.Trees[b] = gitIn(t, s.dir, "rev-parse", b+"^{tree}")
		c, err := commitsBetween(s.ctx, below, b)
		if err != nil {
			t.Fatal(err)
		}
		st.Commits[b] = own(c)
		below = b
	}
	return st
}

// TestRehearse checks the rehearsal of a pass over a two-branch stack with
// fixups in both branches.
func TestRehearse(t *testing.T) {
	t.Parallel()
	// It accepts the stack without moving a branch, even with
	// rebase.updateRefs on.
	t.Run("clean stack", func(t *testing.T) {
		t.Parallel()
		s := newFixedStack(t)
		branches := gitIn(t, s.dir, "for-each-ref", "refs/heads/")
		if err := rehearse(s.ctx, s.stack(t)); err != nil {
			t.Fatalf("rehearsal refused a clean stack: %v", err)
		}
		if got := gitIn(t, s.dir, "worktree", "list"); strings.Count(got, "\n") != 0 {
			t.Errorf("rehearsal left a worktree behind:\n%s", got)
		}
		if got := gitIn(t, s.dir, "for-each-ref", "refs/heads/"); got != branches {
			t.Errorf("rehearsal moved branches:\nbefore:\n%s\nafter:\n%s", branches, got)
		}
	})

	// A fixup aimed at L1 that touches a line L2 later changed conflicts.
	t.Run("conflicting fixup", func(t *testing.T) {
		t.Parallel()
		s := newFixedStack(t)
		gitIn(t, s.dir, "switch", "-q", "lower")
		before := gitIn(t, s.dir, "rev-parse", "lower")
		os.WriteFile(filepath.Join(s.dir, "b"), []byte("b again\n"), 0o644)
		gitIn(t, s.dir, "commit", "-qa", "--fixup=lower~2")
		gitIn(t, s.dir, "rebase", "-q", "--onto", "lower", before, "upper")
		err := rehearse(s.ctx, s.stack(t))
		if _, ok := err.(Refusal); !ok || !strings.Contains(err.Error(), "conflicts") {
			t.Errorf("conflicting fixup: %v", err)
		}
	})
}
