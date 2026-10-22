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

// TestRun signs with a throwaway SSH key, which git verifies through an
// allowed-signers file, so the whole pass runs without a hardware key.
func TestRun(t *testing.T) {
	dir := t.TempDir()
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
