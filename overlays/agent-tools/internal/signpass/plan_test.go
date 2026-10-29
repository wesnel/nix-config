package signpass

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
)

func TestPlan(t *testing.T) {
	t.Run("three stacks", testPlanThreeStacks)
	t.Run("fold kinds", testPlanFoldKinds)
	t.Run("fork conflict", testPlanForkConflict)
	t.Run("nothing to sign", testPlanNothingToSign)
	t.Run("push unmoved", testPlanPushUnmoved)
	t.Run("shared folds", testPlanSharedFolds)
}

func testPlanSharedFolds(t *testing.T) {
	for _, tc := range []struct {
		name, leaf, carrier                         string
		own, shared, cycle, independent, signedLeaf bool
	}{
		{name: "later carrier", leaf: "A", carrier: "Z"},
		{name: "earlier carrier", leaf: "Z", carrier: "A"},
		{name: "own tail", leaf: "A", carrier: "Z", own: true},
		{name: "shared fold", leaf: "A", carrier: "Z", shared: true},
		{name: "incompatible carriers", leaf: "A", carrier: "Z", cycle: true},
		{name: "same subject different target", leaf: "A", carrier: "Z", independent: true},
		{name: "already signed sibling", leaf: "A", carrier: "Z", signedLeaf: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			global := signingConfig(t, dir)
			signer, err := exec.LookPath("ssh-keygen")
			if err != nil {
				t.Fatal(err)
			}
			audit := filepath.Join(dir, "sign-actions")
			wrapper := filepath.Join(dir, "signer")
			program := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = -Y ] && [ \"$2\" = sign ]; then printf 'sign\\n' >> %q; fi\nexec %q \"$@\"\n", audit, signer)
			if err := os.WriteFile(wrapper, []byte(program), 0o755); err != nil {
				t.Fatal(err)
			}
			repo := filepath.Join(dir, "repo")
			if err := os.Mkdir(repo, 0o755); err != nil {
				t.Fatal(err)
			}
			gitIn(t, repo, "init", "-q", "-b", "main")
			commit := func(message string) {
				t.Helper()
				gitIn(t, repo, "-c", "commit.gpgsign=false", "commit", "-q", "-m", message)
			}
			write := func(name, text string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
				gitIn(t, repo, "add", name)
			}
			gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
			gitIn(t, repo, "switch", "-qc", "P")
			write("p", "original\n")
			commit("Parent")
			if tc.signedLeaf {
				gitIn(t, repo, "-c", "include.path="+global, "-c", "commit.gpgsign=true", "commit", "-q", "--amend", "--no-edit")
			}
			if tc.shared {
				write("p", "fixed\n")
				commit("fixup! Parent")
			}
			gitIn(t, repo, "switch", "-qc", tc.leaf)
			if tc.cycle {
				write("p", "other fix\n")
				commit("fixup! Parent")
			}
			write("leaf", "leaf\n")
			commit("Leaf")
			if tc.signedLeaf {
				gitIn(t, repo, "-c", "include.path="+global, "-c", "commit.gpgsign=true", "commit", "-q", "--amend", "--no-edit")
			}
			parent := "P"
			if tc.independent {
				parent = "main"
			}
			gitIn(t, repo, "switch", "-qc", tc.carrier, parent)
			if tc.independent {
				write("p", "independent\n")
				commit("Parent")
			}
			if !tc.own && !tc.shared {
				write("p", "fixed\n")
				commit("fixup! Parent")
			}
			write("carrier", "original\n")
			commit("Carrier")
			if tc.own {
				write("carrier", "fixed\n")
				commit("fixup! Carrier")
			}
			gitIn(t, repo, "switch", "-q", "main")
			before := gitIn(t, repo, "show-ref", "--heads")
			gitIn(t, repo, "config", "--file", global, "gpg.ssh.program", wrapper)
			t.Setenv("GIT_CONFIG_GLOBAL", global)
			t.Setenv("GIT_EDITOR", "false")
			t.Setenv("GIT_SEQUENCE_EDITOR", "false")
			useDir(t, repo)
			result, err := Plan("", []string{tc.carrier, tc.leaf}, false, false)
			if tc.cycle || tc.signedLeaf {
				want := "cannot order"
				if tc.signedLeaf {
					want = "already signed"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("incompatible fold order: %v", err)
				}
				if actions, err := os.ReadFile(audit); !os.IsNotExist(err) || len(actions) != 0 {
					t.Errorf("signed before refusing: %q, %v", actions, err)
				}
				if after := gitIn(t, repo, "show-ref", "--heads"); after != before {
					t.Errorf("branches changed before refusal:\n%s", after)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if actions, err := os.ReadFile(audit); err != nil || len(actions) == 0 {
				t.Fatalf("no actual signing: %q, %v", actions, err)
			}
			branches := result.(map[string]any)["branches"].(map[string]any)
			first := "A"
			if !tc.own && !tc.shared && !tc.independent {
				first = tc.carrier
			}
			if lines := branches["plan"].([]string); !strings.HasPrefix(lines[0], first+":") {
				t.Errorf("first top: %s, want %s", lines[0], first)
			}
			if tc.own {
				if got := gitIn(t, repo, "log", "--format=%s", "main.."+tc.carrier); got != "Carrier\nParent" {
					t.Errorf("folded history: %q", got)
				}
				if got := branches[tc.carrier].(map[string]any)["squashed"]; got != true {
					t.Errorf("folded fork reports squashed=%v", got)
				}
			} else {
				for _, b := range []string{tc.leaf, tc.carrier} {
					want := "fixed"
					if tc.independent && b == tc.leaf {
						want = "original"
					}
					if got := gitIn(t, repo, "show", b+":p"); got != want {
						t.Errorf("%s lost shared fix: %q", b, got)
					}
				}
			}
		})
	}
}

// testPlanPushUnmoved pushes, beside a branch the pass signs, a named branch
// already signed that the remote lacks, and leaves one whose tip the remote
// already has.
func testPlanPushUnmoved(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	hub := filepath.Join(dir, "hub.git")
	gitIn(t, dir, "init", "-q", "--bare", hub)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "remote", "add", "origin", hub)
	gitIn(t, repo, "push", "-q", "-u", "origin", "main")
	for _, b := range []string{"done", "same", "fresh"} {
		gitIn(t, repo, "switch", "-qc", b, "main")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", b+" 1")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	useDir(t, repo)
	for _, b := range []string{"done", "same"} {
		gitIn(t, repo, "switch", "-q", b)
		if _, err := Run("", false, false, false); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, repo, "push", "-q", "-u", "origin", "same")
	sameOnHub := gitIn(t, hub, "rev-parse", "same")

	result, err := Plan("", []string{"done", "fresh", "same"}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	pushed := result.(map[string]any)["pushed"].(map[string]string)
	if pushed["done"] != "origin refs/heads/done, now its upstream" ||
		pushed["fresh"] != "origin refs/heads/fresh, now its upstream" || pushed["same"] != "" {
		t.Errorf("pushed: %v", pushed)
	}
	for _, b := range []string{"done", "fresh"} {
		got, _ := git.Run("-C", hub, "rev-parse", "-q", "--verify", "refs/heads/"+b)
		if want := gitIn(t, repo, "rev-parse", b); got != want {
			t.Errorf("the remote's %s is %q, want %s", b, got, want)
		}
	}
	if got := gitIn(t, hub, "rev-parse", "same"); got != sameOnHub {
		t.Errorf("the remote's same moved to %s", got)
	}
}

// testPlanThreeStacks signs three stacks in one pass: a parent P with two
// children, each of the three in its own worktree, where P gained a fixup
// after C2 forked from it, and an independent branch X checked out nowhere.
// Local main is behind origin/main, whose unsigned commit is not the pass's
// to sign.
func testPlanThreeStacks(t *testing.T) {
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
	gitenvtest.Decoy(t)
	useDir(t, repo)

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
		!strings.Contains(err.Error(), "`"+testBinary(t)+" --resume --set-upstream C1`") {
		t.Fatalf("a failed signature: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	git.Dir = repo
	if _, err := Plan("", []string{"P"}, false, false); err == nil || !strings.Contains(err.Error(), "in progress") {
		t.Errorf("a branch of the stopped pass: %v", err)
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

// testPlanFoldKinds signs a fork whose parent gained an amend! after one
// child forked from it, and whose other child folds in a squash! of its own
// during its replay. Editors that fail stand in for ones that would wait for
// input: neither rebase may open one.
func testPlanFoldKinds(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	write := func(name, content string) {
		t.Helper()
		os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644)
		gitIn(t, repo, "add", name)
	}
	gitIn(t, repo, "switch", "-qc", "P")
	write("p", "p\n")
	gitIn(t, repo, "commit", "-q", "-m", "P1")
	write("p2", "p2\n")
	gitIn(t, repo, "commit", "-q", "-m", "P2")
	gitIn(t, repo, "switch", "-qc", "C2")
	write("c2", "c2\n")
	gitIn(t, repo, "commit", "-q", "-m", "C2a")
	write("c2", "c2 more\n")
	gitIn(t, repo, "commit", "-q", "-m", "squash! C2a", "-m", "More about C2.")
	gitIn(t, repo, "switch", "-q", "P")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "amend! P1", "-m", "P1 renamed", "-m", "Why.")
	gitIn(t, repo, "switch", "-qc", "C1")
	write("c1", "c1\n")
	gitIn(t, repo, "commit", "-q", "-m", "C1a")
	gitIn(t, repo, "switch", "-q", "main")
	trees := map[string]string{}
	for _, b := range []string{"C1", "C2"} {
		trees[b] = gitIn(t, repo, "rev-parse", b+"^{tree}")
	}

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_EDITOR", "false")
	t.Setenv("GIT_SEQUENCE_EDITOR", "false")
	useDir(t, repo)
	result, err := Plan("", []string{"C1", "C2"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan := fmt.Sprint(result.(map[string]any)["branches"].(map[string]any)["plan"]); !strings.Contains(plan, "C1: sign 3 commit(s)") ||
		!strings.Contains(plan, "C2: replay its 1 commit(s)") {
		t.Errorf("plan: %s", plan)
	}
	for b, want := range map[string]string{
		"P":  "P2\nG\nP1 renamed\n\nWhy.\nG",
		"C1": "C1a\nG\nP2\nG\nP1 renamed\n\nWhy.\nG",
		"C2": "C2a\n\nMore about C2.\nG\nP2\nG\nP1 renamed\n\nWhy.\nG",
	} {
		if log, _ := git.Run("log", "--format=%B%G?", "main.."+b); log != want {
			t.Errorf("%s:\n%q\nwant\n%q", b, log, want)
		}
	}
	for b, tree := range trees {
		if got, _ := git.Run("rev-parse", b+"^{tree}"); got != tree {
			t.Errorf("%s's files changed", b)
		}
	}
	p, _ := git.Run("rev-parse", "P")
	if parent, _ := git.Run("rev-parse", "C2~1"); parent != p {
		t.Error("C2 is not on the signed P")
	}
}

// forkConflict makes a repository where P gained a fixup that conflicts with
// C2, which forked from P before it, and points the package's git at it with
// a signing config.
func forkConflict(t *testing.T) string {
	t.Helper()
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
	useDir(t, repo)
	return repo
}

// testPlanForkConflict refuses, before any touch, a fork child whose replay
// onto its parent's signed copy would conflict with a fixup the parent
// gained.
func testPlanForkConflict(t *testing.T) {
	repo := forkConflict(t)
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
}

// A stack with nothing to sign is reported, not refused.
func testPlanNothingToSign(t *testing.T) {
	forkConflict(t)
	result, err := Plan("main", []string{"main"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(result.(map[string]any)["branches"]); !strings.Contains(got, "main: already signed") {
		t.Errorf("an empty plan: %s", got)
	}
}
