package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// TestMain runs main instead of the tests when asked to, so a test can run
// this program as the test binary, which is what the commands it prints for
// resuming name.
func TestMain(m *testing.M) {
	if os.Getenv("GIT_SIGN_PASS_RUN_MAIN") == "1" {
		main()
	}
	gitenv.Isolate()
	os.Exit(m.Run())
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// run runs this program in dir with args, signing with the git config
// global, and returns its exit status and stderr.
func run(t *testing.T, dir, global string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_SIGN_PASS_RUN_MAIN=1", "GIT_CONFIG_GLOBAL="+global)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), stderr.String()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, stderr.String()
}

// The command a failed push prints for resuming is one this program
// accepts, and it pushes what is left.
func TestPrintedResume(t *testing.T) {
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

	hub := filepath.Join(dir, "hub.git")
	gitIn(t, dir, "init", "-q", "--bare", hub)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "remote", "add", "origin", hub)
	gitIn(t, repo, "push", "-q", "-u", "origin", "main")
	gitIn(t, repo, "switch", "-qc", "one")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "O1")
	gitIn(t, repo, "push", "-q", "-u", "origin", "one")
	// The hub's branch moves away from what the push leases on, and back.
	leased := gitIn(t, hub, "rev-parse", "one")
	gitIn(t, repo, "push", "-q", "-f", hub, "main:refs/heads/one")

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, stderr := run(t, repo, global, exe, "--push")
	m := regexp.MustCompile("run `([^`]*)`").FindStringSubmatch(stderr)
	if code == 0 || m == nil {
		t.Fatalf("a failed push: exit %d\n%s", code, stderr)
	}
	gitIn(t, repo, "push", "-q", "-f", hub, leased+":refs/heads/one")
	if code, stderr := run(t, repo, global, strings.Fields(m[1])...); code != 0 {
		t.Fatalf("%s: exit %d\n%s", m[1], code, stderr)
	}
	if got, want := gitIn(t, hub, "rev-parse", "one"), gitIn(t, repo, "rev-parse", "one"); got != want {
		t.Errorf("the hub has one at %s, want %s", got, want)
	}
}
