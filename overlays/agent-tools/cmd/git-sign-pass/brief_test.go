package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// runOutput runs this program in dir with args, signing with the git config
// global, with standard output and error each a pipe, never a terminal, and
// returns the exit status and both.
func runOutput(t *testing.T, dir, global string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.Without(os.Environ()), "GIT_SIGN_PASS_RUN_MAIN=1", "GIT_CONFIG_GLOBAL="+global,
		"GIT_EXEC_PATH="+filepath.Join(filepath.Dir(global), "exec"))
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); ok {
		return exit.ExitCode(), out.String(), errs.String()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0, out.String(), errs.String()
}

// pushedRepo is a clone of a bare hub on branch one, which the hub has, with n
// unsigned empty commits on it. Everything the hub's receive-pack prints is
// a banner, as a forge's is. The global git config it returns signs with a
// throwaway SSH key.
func pushedRepo(t *testing.T, n int) (repo, global string) {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("no ssh-keygen: %v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(dir, "signers")
	os.WriteFile(signers, []byte("t@e namespaces=\"git\" "+string(pub)), 0o644)
	// A push to a local path runs git-receive-pack from git's exec path (see
	// runOutput), here a wrapper that prints what a forge prints.
	os.Mkdir(filepath.Join(dir, "exec"), 0o755)
	os.WriteFile(filepath.Join(dir, "exec", "git-receive-pack"),
		[]byte("#!/bin/sh\necho \"remote: Create a pull request for 'one' on GitHub\" >&2\nexec git receive-pack \"$@\"\n"), 0o755)
	global = filepath.Join(dir, "gitconfig")
	os.WriteFile(global, []byte("[user]\n\tname = T\n\temail = t@e\n\tsigningkey = "+key+
		"\n[gpg]\n\tformat = ssh\n[gpg \"ssh\"]\n\tallowedSignersFile = "+signers+"\n"), 0o644)

	hub := filepath.Join(dir, "hub.git")
	gitIn(t, dir, "init", "-q", "--bare", hub)
	repo = filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "remote", "add", "origin", hub)
	gitIn(t, repo, "push", "-q", "-u", "origin", "main")
	gitIn(t, repo, "switch", "-qc", "one")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "first")
	gitIn(t, repo, "push", "-q", "-u", "origin", "one")
	for i := 1; i < n; i++ {
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "Change "+strings.Repeat("x", i%7)+string(rune('a'+i%26)))
	}
	return repo, global
}

// With standard output a pipe, a pass prints a handful of lines however many
// commits it signs, and leaves out what git says about its own work; a
// failure keeps git's own words.
func TestBriefOutput(t *testing.T) {
	repo, global := pushedRepo(t, 200)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runOutput(t, repo, global, exe, "--push")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) > 20 {
		t.Errorf("%d lines of progress on standard error:\n%s", len(lines), stderr)
	}
	for _, banned := range []string{"Rebasing", "Successfully", "Create a pull request", "remote:"} {
		if strings.Contains(stderr, banned) {
			t.Errorf("standard error has %q:\n%s", banned, stderr)
		}
	}
	for _, want := range []string{"signing 200 commits", "touch the key when it blinks", "25/200", "175/200", "200/200",
		"pushed one -> origin " + gitIn(t, repo, "rev-parse", "--short=7", "one")} {
		if !strings.Contains(stderr, want) {
			t.Errorf("standard error lacks %q:\n%s", want, stderr)
		}
	}
	if n := strings.Count(stderr, "/200"); n != 8 {
		t.Errorf("%d progress lines, want one per 25 signatures:\n%s", n, stderr)
	}
	var result struct {
		Signed int               `json:"signed"`
		Pushed map[string]string `json:"pushed"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || result.Signed != 200 || result.Pushed["one"] == "" {
		t.Errorf("summary %q: %v", stdout, err)
	}
	if len(strings.Split(strings.TrimSpace(stdout), "\n")) > 12 {
		t.Errorf("the summary is long:\n%s", stdout)
	}
	signers := "gpg.ssh.allowedSignersFile=" + filepath.Join(filepath.Dir(global), "signers")
	if marks := gitIn(t, repo, "-c", "gpg.format=ssh", "-c", signers, "log", "--format=%G?", "main..one"); strings.Contains(marks, "N") || len(strings.Fields(marks)) != 200 {
		t.Errorf("not every commit is signed: %q", marks)
	}
}

// --no-brief keeps git's words when standard output is a pipe, and --brief
// drops them whatever standard output is.
func TestBriefFlags(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo, global := pushedRepo(t, 4)
	code, _, stderr := runOutput(t, repo, global, exe, "--no-brief", "--push")
	if code != 0 || !strings.Contains(stderr, "Rebasing") || !strings.Contains(stderr, "Create a pull request") {
		t.Errorf("--no-brief: exit %d\n%s", code, stderr)
	}
	repo, global = pushedRepo(t, 4)
	code, _, stderr = runOutput(t, repo, global, exe, "--brief", "--push")
	if code != 0 || strings.Contains(stderr, "Rebasing") || !strings.Contains(stderr, "pushed one -> origin") {
		t.Errorf("--brief: exit %d\n%s", code, stderr)
	}
}

// A failing push shows git's error in full though the pass is brief.
func TestBriefKeepsErrors(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repo, global := pushedRepo(t, 3)
	hub := gitIn(t, repo, "remote", "get-url", "origin")
	gitIn(t, repo, "push", "-q", "-f", hub, "main:refs/heads/one")
	code, _, stderr := runOutput(t, repo, global, exe, "--push")
	if code == 0 || !strings.Contains(stderr, "stale info") && !strings.Contains(stderr, "rejected") {
		t.Errorf("a push that fails: exit %d\n%s", code, stderr)
	}
}
