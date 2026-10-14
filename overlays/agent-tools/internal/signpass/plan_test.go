package signpass

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
)

// TestPlan signs three stacks in one pass: a parent P with two children, each
// of the three in its own worktree, where P gained a fixup after C2 forked
// from it, and an independent branch X checked out nowhere. Local main is
// behind origin/main, whose unsigned commit is not the pass's to sign.
func TestPlan(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	global := signingConfig(t, dir)
	hub := filepath.Join(dir, "hub.git")
	gitIn(t, dir, "init", "-q", "--bare", hub)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "remote", "add", "origin", hub)
	gitIn(t, repo, "push", "-q", "origin", "main")
	gitIn(t, repo, "switch", "-q", "--detach")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "upstream work")
	gitIn(t, repo, "push", "-q", "origin", "HEAD:main")
	upstream := gitIn(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "fetch", "-q", "origin")
	gitIn(t, repo, "switch", "-q", "main")
	stale := gitIn(t, repo, "rev-parse", "main")

	write := func(wt, name, content string) {
		t.Helper()
		os.WriteFile(filepath.Join(wt, name), []byte(content), 0o644)
		gitIn(t, wt, "add", name)
	}
	gitIn(t, repo, "branch", "--no-track", "P", "origin/main")
	gitIn(t, repo, "branch", "--no-track", "X", "origin/main")
	wtP, wtC1, wtC2 := filepath.Join(dir, "wt-p"), filepath.Join(dir, "wt-c1"), filepath.Join(dir, "wt-c2")
	gitIn(t, repo, "worktree", "add", "-q", wtP, "P")
	write(wtP, "p", "p\n")
	gitIn(t, wtP, "commit", "-q", "-m", "P1")
	write(wtP, "p2", "p2\n")
	gitIn(t, wtP, "commit", "-q", "-m", "P2")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "C2", wtC2, "P")
	write(wtC2, "c2", "c2\n")
	gitIn(t, wtC2, "commit", "-q", "-m", "C2a")
	write(wtP, "p", "p fixed\n")
	gitIn(t, wtP, "commit", "-q", "--fixup=P~1")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "C1", wtC1, "P")
	write(wtC1, "c1", "c1\n")
	gitIn(t, wtC1, "commit", "-q", "-m", "C1a")
	gitIn(t, repo, "switch", "-q", "X")
	write(repo, "x", "x\n")
	gitIn(t, repo, "commit", "-q", "-m", "X1")
	gitIn(t, repo, "switch", "-q", "main")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_EDITOR", ":")
	git.Dir = repo
	defer func() { git.Dir = "" }()

	// Everything that can refuse does so before anything moves: here an
	// untracked file in the worktree of P, which only moves with C1.
	os.WriteFile(filepath.Join(wtP, "notes"), []byte("mine\n"), 0o644)
	refs := gitIn(t, repo, "for-each-ref")
	if _, err := Plan("", []string{"C2", "X", "C1"}, false, true); err == nil || !strings.Contains(err.Error(), wtP) {
		t.Errorf("a dirty worktree: %v", err)
	}
	if after := gitIn(t, repo, "for-each-ref"); after != refs {
		t.Errorf("refs changed before the refusal:\n%s\n%s", refs, after)
	}
	os.Remove(filepath.Join(wtP, "notes"))

	// The first step stops at a failed signature, and --resume on it carries
	// on with the rest.
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("[user]\n\tname = T\n\temail = t@e\n\tsigningkey = "+filepath.Join(dir, "nokey")+
		"\n[gpg]\n\tformat = ssh\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", bad)
	_, err := Plan("", []string{"C2", "X", "C1"}, false, true)
	if err == nil || !strings.Contains(err.Error(), "git -C "+wtC1+" rebase --continue") ||
		!strings.Contains(err.Error(), "git sign-pass --resume C1") {
		t.Fatalf("a failed signature: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	git.Dir = repo
	if _, err := Plan("", []string{"P"}, false, false); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Errorf("a branch of the stopped pass: %v", err)
	}
	if _, err := git.Run("-C", wtC1, "reset", "-q", "--hard"); err != nil {
		t.Fatal(err)
	}
	if _, err := git.Run("-C", wtC1, "rebase", "--continue"); err != nil {
		t.Fatal(err)
	}
	result, err := Resume("C1", false, false)
	if err != nil {
		t.Fatal(err)
	}
	git.Dir = repo

	got := result.(map[string]any)
	if plan := fmt.Sprint(got["branches"].(map[string]any)["plan"]); !strings.Contains(plan, "C2: replay its 1 commit(s)") ||
		!strings.Contains(plan, "C1: sign 3 commit(s) after") || !strings.Contains(plan, "moves P") ||
		!strings.Contains(plan, "X: sign 1 commit(s)") || !strings.Contains(plan, "throwaway") {
		t.Errorf("plan: %s", plan)
	}
	for b, want := range map[string]string{
		"P":  "P2 G\nP1 G",
		"C1": "C1a G\nP2 G\nP1 G",
		"C2": "C2a G\nP2 G\nP1 G",
		"X":  "X1 G",
	} {
		if log, _ := git.Run("log", "--format=%s %G?", "origin/main.."+b); log != want {
			t.Errorf("%s:\n%s\nwant\n%s", b, log, want)
		}
		if !git.OK("merge-base", "--is-ancestor", upstream, b) {
			t.Errorf("%s left origin/main", b)
		}
	}
	p, _ := git.Run("rev-parse", "P")
	for _, b := range []string{"C1", "C2"} {
		if parent, _ := git.Run("rev-parse", b+"~1"); parent != p {
			t.Errorf("%s is not on the signed P", b)
		}
		if content, _ := git.Run("show", b+":p"); content != "p fixed" {
			t.Errorf("%s's p: %q", b, content)
		}
	}
	if got, _ := git.Run("rev-parse", "main"); got != stale {
		t.Error("the stale main moved")
	}
	for wt, b := range map[string]string{wtP: "P", wtC1: "C1", wtC2: "C2"} {
		if head := gitIn(t, wt, "symbolic-ref", "HEAD"); head != "refs/heads/"+b {
			t.Errorf("%s is on %s", wt, head)
		}
		if dirty := gitIn(t, wt, "status", "--porcelain"); dirty != "" {
			t.Errorf("%s is not clean:\n%s", wt, dirty)
		}
	}
	for _, b := range []string{"C1", "C2", "P", "X"} {
		if remote, local := gitIn(t, hub, "rev-parse", b), gitIn(t, repo, "rev-parse", b); remote != local {
			t.Errorf("the remote's %s is %s, want %s", b, remote, local)
		}
	}
	if list := gitIn(t, repo, "worktree", "list"); strings.Count(list, "\n") != 3 {
		t.Errorf("worktrees left:\n%s", list)
	}
	state := filepath.Join(repo, ".git", "sign-pass")
	filepath.WalkDir(state, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("state left: %s", path)
		}
		return nil
	})
}

// TestPlanForkConflict refuses, before any touch, a fork child whose replay
// onto its parent's signed copy would conflict with a fixup the parent gained.
func TestPlanForkConflict(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "P")
	os.WriteFile(filepath.Join(repo, "p"), []byte("p\n"), 0o644)
	gitIn(t, repo, "add", "p")
	gitIn(t, repo, "commit", "-q", "-m", "P1")
	gitIn(t, repo, "switch", "-qc", "C2")
	os.WriteFile(filepath.Join(repo, "p"), []byte("p by C2\n"), 0o644)
	gitIn(t, repo, "commit", "-q", "-a", "-m", "C2a")
	gitIn(t, repo, "switch", "-q", "P")
	os.WriteFile(filepath.Join(repo, "p"), []byte("p fixed\n"), 0o644)
	gitIn(t, repo, "commit", "-q", "-a", "--fixup=P")
	gitIn(t, repo, "switch", "-qc", "C1")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "C1a")
	gitIn(t, repo, "switch", "-q", "main")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	git.Dir = repo
	defer func() { git.Dir = "" }()
	refs := gitIn(t, repo, "for-each-ref")
	if _, err := Plan("", []string{"C1", "C2"}, false, false); err == nil || !strings.Contains(err.Error(), "replaying C2") {
		t.Errorf("a conflicting replay: %v", err)
	}
	if after := gitIn(t, repo, "for-each-ref"); after != refs {
		t.Errorf("refs changed before the refusal:\n%s\n%s", refs, after)
	}
	if list := gitIn(t, repo, "worktree", "list"); strings.Count(list, "\n") != 0 {
		t.Errorf("worktrees left:\n%s", list)
	}

	// A stack with nothing to sign is reported, not refused.
	result, err := Plan("main", []string{"main"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(result.(map[string]any)["branches"]); !strings.Contains(got, "main: already signed") {
		t.Errorf("an empty plan: %s", got)
	}
}
