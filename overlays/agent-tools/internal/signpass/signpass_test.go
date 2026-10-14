package signpass

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// signingConfig writes a global git config that signs with a throwaway SSH
// key, which git verifies through an allowed-signers file, so a whole pass
// runs without a hardware key.
func signingConfig(t *testing.T, dir string) string {
	t.Helper()
	key := filepath.Join(dir, "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("no ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(dir, "signers")
	os.WriteFile(signers, []byte("t@e namespaces=\"git\" "+string(pub)), 0o644)
	global := filepath.Join(dir, "gitconfig")
	os.WriteFile(global, []byte("[user]\n\tname = T\n\temail = t@e\n\tsigningkey = "+key+
		"\n[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tallowedSignersFile = "+signers+"\n"), 0o644)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return global
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)

	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "lower")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a\n"), 0o644)
	gitIn(t, repo, "add", "a")
	gitIn(t, repo, "commit", "-q", "-m", "A1")
	gitIn(t, repo, "switch", "-qc", "upper")
	os.WriteFile(filepath.Join(repo, "b"), []byte("b\n"), 0o644)
	gitIn(t, repo, "add", "b")
	gitIn(t, repo, "commit", "-q", "-m", "B1")
	before := gitIn(t, repo, "log", "--format=%T %s", "main..upper")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	git.Dir = repo
	defer func() { git.Dir = "" }()
	result, err := Run("", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any)["signed"]; got != 2 {
		t.Errorf("signed %v commits", got)
	}
	marks, _ := git.Run("log", "--format=%G?", "main..upper")
	if marks != "G\nG" {
		t.Errorf("signatures: %q", marks)
	}
	if after, _ := git.Run("log", "--format=%T %s", "main..upper"); after != before {
		t.Errorf("content changed:\n%s\n%s", before, after)
	}
	lower, _ := git.Run("rev-parse", "lower")
	parent, _ := git.Run("rev-parse", "upper~1")
	if lower != parent {
		t.Error("the stacked branch did not move with the signed commits")
	}
	if again, err := Run("", false, false, false); err != nil || again.(map[string]string)["signed"] == "" {
		t.Errorf("a signed branch: %v %v", again, err)
	}

	// A fixup folds into its target, here an already-signed commit, which the
	// pass re-signs along with everything after it.
	os.WriteFile(filepath.Join(repo, "a"), []byte("a fixed\n"), 0o644)
	gitIn(t, repo, "commit", "-q", "-a", "--fixup=lower")
	wantTree := gitIn(t, repo, "rev-parse", "upper^{tree}")
	result, err = Run("", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)
	if got["squashed"] != true || got["signed"] != 2 || fmt.Sprint(got["gainedFixups"]) != "[lower]" {
		t.Errorf("squash result: %v", got)
	}
	if subjects, _ := git.Run("log", "--format=%s %G?", "main..upper"); subjects != "B1 G\nA1 G" {
		t.Errorf("after squashing: %q", subjects)
	}
	if got, _ := git.Run("rev-parse", "upper^{tree}"); got != wantTree {
		t.Error("squashing changed the files")
	}
	if a, _ := git.Run("show", "lower:a"); a != "a fixed" {
		t.Errorf("the fix did not land in A1: %q", a)
	}
	if !git.OK("rev-parse", "--verify", "-q", BackupRef+"upper") {
		t.Error("no backup of the unsigned branch")
	}

	// On the default branch with no remote, the base is not guessed.
	gitIn(t, repo, "switch", "-q", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "on main")
	if _, err := Run("", false, false, false); err == nil || !strings.Contains(err.Error(), "--base") {
		t.Errorf("signing main without a base: %v", err)
	}
	if _, err := Run("main~1", false, false, false); err != nil {
		t.Errorf("signing main with a base: %v", err)
	}
	if marks, _ := git.Run("log", "-1", "--format=%G?", "main"); marks != "G" {
		t.Errorf("main's commit: %q", marks)
	}
}

// TestWorktreeBranch signs a stack whose lower branch is checked out in a
// worktree of its own, which --update-refs alone would leave behind.
func TestWorktreeBranch(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("build\nc\n"), 0o644)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "lower")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a\n"), 0o644)
	gitIn(t, repo, "add", "a")
	gitIn(t, repo, "commit", "-q", "-m", "A1")
	gitIn(t, repo, "switch", "-qc", "upper")
	os.WriteFile(filepath.Join(repo, "b"), []byte("b\n"), 0o644)
	gitIn(t, repo, "add", "b")
	gitIn(t, repo, "commit", "-q", "-m", "B1")
	wt := filepath.Join(dir, "lower-wt")
	gitIn(t, repo, "worktree", "add", "-q", wt, "lower")
	os.WriteFile(filepath.Join(wt, "build"), []byte("output\n"), 0o644)

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	git.Dir = repo
	defer func() { git.Dir = "" }()
	check := func(result any, wantA string) {
		t.Helper()
		lower, _ := git.Run("rev-parse", "lower")
		if moved := result.(map[string]any)["moved"].(map[string]string); moved["lower"] != lower {
			t.Errorf("moved: %v", moved)
		}
		if parent, _ := git.Run("rev-parse", "upper~1"); lower != parent {
			t.Error("lower did not move with the signed commits")
		}
		if marks, _ := git.Run("log", "--format=%G?", "main..upper"); marks != "G\nG" {
			t.Errorf("signatures: %q", marks)
		}
		if head := gitIn(t, wt, "symbolic-ref", "HEAD"); head != "refs/heads/lower" {
			t.Errorf("the worktree is on %s", head)
		}
		if dirty := gitIn(t, wt, "status", "--porcelain"); dirty != "" {
			t.Errorf("the worktree is not clean:\n%s", dirty)
		}
		if a, _ := os.ReadFile(filepath.Join(wt, "a")); string(a) != wantA {
			t.Errorf("the worktree's a: %q", a)
		}
		if _, err := os.Stat(filepath.Join(wt, "build")); err != nil {
			t.Error("the worktree lost its ignored file")
		}
	}
	result, err := Run("", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	check(result, "a\n")

	// A worktree with untracked files is refused before anything is rewritten.
	os.WriteFile(filepath.Join(repo, "a"), []byte("a fixed\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "c"), []byte("c\n"), 0o644)
	gitIn(t, repo, "add", "-f", "c")
	gitIn(t, repo, "commit", "-q", "-a", "--fixup=lower")
	os.WriteFile(filepath.Join(wt, "notes"), []byte("mine\n"), 0o644)
	refs := gitIn(t, repo, "for-each-ref")
	if _, err := Run("", false, false, false); err == nil || !strings.Contains(err.Error(), wt) {
		t.Errorf("a dirty worktree: %v", err)
	}
	if after := gitIn(t, repo, "for-each-ref"); after != refs {
		t.Errorf("refs changed:\n%s\n%s", refs, after)
	}
	if head := gitIn(t, wt, "symbolic-ref", "HEAD"); head != "refs/heads/lower" {
		t.Errorf("the refused worktree is on %s", head)
	}
	os.Remove(filepath.Join(wt, "notes"))

	// A worktree busy rebasing its branch is refused too.
	rebase := exec.Command("git", "-c", "commit.gpgsign=false", "rebase", "-q", "--exec", "false", "main")
	rebase.Dir = wt
	rebase.Run()
	if _, err := Run("", false, false, false); err == nil || !strings.Contains(err.Error(), "being rebased") {
		t.Errorf("a worktree mid-rebase: %v", err)
	}
	gitIn(t, wt, "rebase", "--abort")

	// A rebase that stops leaves the worktree detached until --resume, which
	// switches it back, gaining the fixup's files, but not over a file in the
	// way, even an ignored one.
	bad := filepath.Join(dir, "bad")
	os.WriteFile(bad, []byte("[user]\n\tname = T\n\temail = t@e\n\tsigningkey = "+filepath.Join(dir, "nokey")+
		"\n[gpg]\n\tformat = ssh\n"), 0o644)
	t.Setenv("GIT_CONFIG_GLOBAL", bad)
	os.WriteFile(filepath.Join(wt, "c"), []byte("ignored\n"), 0o644)
	if _, err := Run("", false, false, false); err == nil || !strings.Contains(err.Error(), "--resume") {
		t.Fatalf("a failed signature: %v", err)
	}
	if git.OK("-C", wt, "symbolic-ref", "-q", "HEAD") {
		t.Error("the worktree was not detached for the rebase")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_EDITOR", ":")
	// Git reschedules the pick it could not sign, leaving its changes staged.
	if _, err := git.Run("reset", "-q", "--hard"); err != nil {
		t.Fatal(err)
	}
	if _, err := git.Run("rebase", "--continue"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run("", true, false, false); err == nil || !strings.Contains(err.Error(), "overwrite c") {
		t.Errorf("an ignored file in the way: %v", err)
	}
	if c, _ := os.ReadFile(filepath.Join(wt, "c")); string(c) != "ignored\n" {
		t.Errorf("the ignored file was overwritten: %q", c)
	}
	os.Remove(filepath.Join(wt, "c"))
	if result, err = Run("", true, false, false); err != nil {
		t.Fatal(err)
	}
	check(result, "a fixed\n")
	if got := fmt.Sprint(result.(map[string]any)["gainedFixups"]); got != "[lower]" {
		t.Errorf("gainedFixups: %s", got)
	}
}

func TestPushMoved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	hub, mirror := filepath.Join(dir, "hub.git"), filepath.Join(dir, "mirror.git")
	gitIn(t, dir, "init", "-q", "--bare", hub)
	gitIn(t, dir, "init", "-q", "--bare", mirror)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	// One remote that pushes to two places, as a mirrored repository does.
	gitIn(t, repo, "remote", "add", "origin", hub)
	gitIn(t, repo, "remote", "set-url", "--add", "--push", "origin", hub)
	gitIn(t, repo, "remote", "set-url", "--add", "--push", "origin", mirror)
	gitIn(t, repo, "switch", "-qc", "tracked")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "T1")
	gitIn(t, repo, "push", "-q", "-u", "origin", "tracked")
	gitIn(t, repo, "commit", "-q", "--amend", "--allow-empty", "-m", "T1 again")
	gitIn(t, repo, "switch", "-qc", "fresh")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "F1")
	git.Dir = repo
	defer func() { git.Dir = "" }()
	moved := map[string]string{"tracked": "", "fresh": ""}

	pushed, err := pushMoved(moved, false)
	if err != nil {
		t.Fatal(err)
	}
	if pushed["fresh"] != "not pushed: no upstream" || pushed["tracked"] != "origin refs/heads/tracked" {
		t.Errorf("--push: %v", pushed)
	}
	pushed, err = pushMoved(moved, true)
	if err != nil {
		t.Fatal(err)
	}
	if pushed["fresh"] != "origin refs/heads/fresh, now its upstream" {
		t.Errorf("--set-upstream: %v", pushed)
	}
	want := gitIn(t, repo, "rev-parse", "fresh")
	for _, remote := range []string{hub, mirror} {
		if got := gitIn(t, remote, "rev-parse", "fresh"); got != want {
			t.Errorf("%s has fresh at %s, want %s", remote, got, want)
		}
	}
	if upstream := gitIn(t, repo, "rev-parse", "--abbrev-ref", "fresh@{upstream}"); upstream != "origin/fresh" {
		t.Errorf("fresh's upstream: %q", upstream)
	}

	// A branch the remote already has is not published over, whether or not
	// this repository has fetched it.
	gitIn(t, repo, "push", "-q", "origin", "main:refs/heads/taken")
	gitIn(t, repo, "branch", "taken", "fresh")
	if _, err := pushMoved(map[string]string{"taken": ""}, true); err == nil || !strings.Contains(err.Error(), "--set-upstream-to=origin/taken") {
		t.Errorf("a fetched branch of the same name: %v", err)
	}
	gitIn(t, repo, "update-ref", "-d", "refs/remotes/origin/taken")
	if _, err := pushMoved(map[string]string{"taken": ""}, true); err == nil {
		t.Error("an unfetched branch of the same name was pushed over")
	}
	if got, base := gitIn(t, hub, "rev-parse", "taken"), gitIn(t, repo, "rev-parse", "main"); got != base {
		t.Errorf("the remote's branch moved to %s", got)
	}
	if git.OK("config", "--get", "branch.taken.remote") {
		t.Error("a refused branch got an upstream")
	}
}

// TestPushDestination holds --push to each branch's own name and away from
// the remote's default branch, refusing before the pass signs anything.
func TestPushDestination(t *testing.T) {
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
	gitIn(t, repo, "push", "-q", "origin", "main:refs/heads/develop")
	gitIn(t, repo, "fetch", "-q", "origin")
	// What `git switch -c feature origin/main` leaves: feature tracks main.
	gitIn(t, repo, "switch", "-q", "-c", "feature", "--track", "origin/main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "F1")
	hubMain := gitIn(t, hub, "rev-parse", "main")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	git.Dir = repo
	defer func() { git.Dir = "" }()
	refs := gitIn(t, repo, "for-each-ref")
	for _, setUpstream := range []bool{false, true} {
		_, err := Run("", false, true, setUpstream)
		if err == nil || !strings.Contains(err.Error(), "tracks origin/main") || !strings.Contains(err.Error(), "--unset-upstream feature") {
			t.Errorf("a branch tracking main (setUpstream %v): %v", setUpstream, err)
		}
	}
	if after := gitIn(t, repo, "for-each-ref"); after != refs {
		t.Errorf("refs changed before the refusal:\n%s\n%s", refs, after)
	}
	if marks := gitIn(t, repo, "log", "-1", "--format=%G?"); marks != "N" {
		t.Errorf("the refused branch was signed: %q", marks)
	}
	if got := gitIn(t, hub, "rev-parse", "main"); got != hubMain {
		t.Errorf("the remote's main moved to %s", got)
	}

	// Once the remote has the branch, the fix names it.
	gitIn(t, repo, "push", "-q", "origin", "feature")
	gitIn(t, repo, "fetch", "-q", "origin")
	if _, _, err := pushTargets([]string{"feature"}, false); err == nil || !strings.Contains(err.Error(), "--set-upstream-to=origin/feature feature") {
		t.Errorf("a branch tracking main, which the remote also has: %v", err)
	}
	gitIn(t, repo, "branch", "-q", "--set-upstream-to=origin/feature", "feature")
	if _, err := Run("", false, true, false); err != nil {
		t.Fatal(err)
	}
	if got, want := gitIn(t, hub, "rev-parse", "feature"), gitIn(t, repo, "rev-parse", "feature"); got != want {
		t.Errorf("the remote's feature is %s, want %s", got, want)
	}
	if got := gitIn(t, hub, "rev-parse", "main"); got != hubMain {
		t.Errorf("the remote's main moved to %s", got)
	}

	// The default branch itself is never a destination: main and master while
	// the remote's HEAD is unknown, and whatever HEAD names once it is.
	if _, _, err := pushTargets([]string{"main"}, false); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Errorf("main with no origin/HEAD: %v", err)
	}
	gitIn(t, repo, "branch", "-q", "--track", "develop", "origin/develop")
	if _, _, err := pushTargets([]string{"develop"}, false); err != nil {
		t.Errorf("develop with no origin/HEAD: %v", err)
	}
	gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	if _, _, err := pushTargets([]string{"develop"}, false); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Errorf("develop as origin/HEAD: %v", err)
	}
	if _, _, err := pushTargets([]string{"main"}, false); err != nil {
		t.Errorf("main beside origin/HEAD develop: %v", err)
	}
	gitIn(t, repo, "branch", "-q", "--unset-upstream", "develop")
	gitIn(t, repo, "update-ref", "-d", "refs/remotes/origin/develop")
	if _, _, err := pushTargets([]string{"develop"}, true); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Errorf("publishing develop as origin/HEAD: %v", err)
	}
}

func TestPushRemote(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitIn(t, dir, "init", "-q", "-b", "main")
	git.Dir = dir
	defer func() { git.Dir = "" }()
	check := func(want, wantErr string) {
		t.Helper()
		got, err := pushRemote("b")
		if got != want || (err == nil) != (wantErr == "") || (err != nil && !strings.Contains(err.Error(), wantErr)) {
			t.Errorf("got %q, %v; want %q, %q", got, err, want, wantErr)
		}
	}
	check("", "no remote")
	gitIn(t, dir, "remote", "add", "fork", "/nowhere/fork")
	check("fork", "")
	gitIn(t, dir, "remote", "add", "upstream", "/nowhere/upstream")
	check("", "remote.pushDefault")
	gitIn(t, dir, "remote", "add", "origin", "/nowhere/origin")
	check("origin", "")
	gitIn(t, dir, "config", "remote.pushDefault", "upstream")
	check("upstream", "")
	gitIn(t, dir, "config", "branch.b.pushRemote", "fork")
	check("fork", "")
}
