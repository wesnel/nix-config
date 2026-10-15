package signpass

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
	"github.com/wesnel/agent-tools/internal/refusal"
)

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

// useDir points the package's git at dir for the rest of the test.
func useDir(t *testing.T, dir string) {
	t.Helper()
	git.Dir = dir
	t.Cleanup(func() { git.Dir = "" })
}

// withStdin makes f the pass's standard input for the rest of the test.
func withStdin(t *testing.T, f *os.File) {
	t.Helper()
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old })
}

// noTerminal gives the pass a standard input that is not a terminal, though
// Enter waits on it.
func noTerminal(t *testing.T) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString("\n")
	w.Close()
	t.Cleanup(func() { r.Close() })
	withStdin(t, r)
}

// typed gives the pass a terminal on which answer has already been typed.
func typed(t *testing.T, answer string) {
	t.Helper()
	term, tty := openPTY(t)
	if _, err := term.WriteString(answer); err != nil {
		t.Fatal(err)
	}
	withStdin(t, tty)
}

// refusing writes a program to dir that fails as the hardware key does at a
// missed touch, printing refusal, once armed (see arm), and otherwise runs
// then with the program's arguments. Only runs for which the shell
// condition key holds count, as runs that need the key. It is a fake
// because a missed touch is a failure the real key will not produce on
// demand.
func refusing(t *testing.T, dir, name, key, refusal, then string) (program, armed string) {
	t.Helper()
	program, armed = filepath.Join(dir, name), filepath.Join(dir, name+".armed")
	script := fmt.Sprintf(`#!/bin/sh
if %[4]s && [ -e '%[1]s' ]; then
  n=$(cat '%[1]s')
  if [ "${n:-0}" -gt 0 ]; then
    echo $((n - 1)) > '%[1]s'
  else
    mv '%[1]s' '%[1]s.used'
    printf '%%s\n' %[2]s >&2
    exit 255
  fi
fi
%[3]s
`, armed, refusal, then, key)
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return program, armed
}

// arm makes the program refusing wrote refuse once, after running after
// times as it would.
func arm(t *testing.T, armed string, after int) {
	t.Helper()
	if err := os.WriteFile(armed, []byte(fmt.Sprint(after)), 0o644); err != nil {
		t.Fatal(err)
	}
}

// testBinary is the path of the running test binary, which every command the
// pass prints for resuming names, since no git-sign-pass on PATH is it.
func testBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestRun(t *testing.T) {
	t.Run("signs and folds a stack", testRunStack)
	t.Run("lint", testRunLint)
	t.Run("worktree branch", testRunWorktreeBranch)
	t.Run("failed push", testRunFailedPush)
	t.Run("missed touch", testRunMissedTouch)
}

func TestPush(t *testing.T) {
	t.Run("moved branches", testPushMoved)
	t.Run("destination", testPushDestination)
	t.Run("remote", testPushRemote)
	t.Run("each URL", testPushEachURL)
}

// The commands a pass prints for resuming name the program that is running,
// as `git sign-pass` only where that is what runs it, with the pass's flags.
func TestResumeCommand(t *testing.T) {
	exe := testBinary(t)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	NotifyID = "loop.0123abcd"
	t.Cleanup(func() { NotifyID = "" })
	check := func(prog string) {
		t.Helper()
		if got, want := resumeCommand("one", true, false), prog+" --resume --push --notify loop.0123abcd one"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
		if got, want := resumeCommand("one", false, true), prog+" --resume --set-upstream --notify loop.0123abcd one"; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
	check(exe)
	// Another git-sign-pass on PATH, such as an older install.
	other := filepath.Join(bin, "git-sign-pass")
	if err := os.WriteFile(other, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	check(exe)
	os.Remove(other)
	if err := os.Symlink(exe, other); err != nil {
		t.Fatal(err)
	}
	check("git sign-pass")
}

// testRunMissedTouch covers a signature the key refuses, the first or a
// later one: on a terminal the pass asks and, on Enter, signs that commit
// again; otherwise it stops, and the commands it names finish the pass.
func testRunMissedTouch(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	// Git runs the program to verify signatures too, which needs no key.
	program, armed := refusing(t, dir, "sign", `[ "$1 $2" = "-Y sign" ]`,
		`"Couldn't sign message (signer): agent refused operation"`, `exec ssh-keygen "$@"`)
	flaky := filepath.Join(dir, "flaky")
	os.WriteFile(flaky, []byte("[include]\n\tpath = "+global+"\n[gpg \"ssh\"]\n\tprogram = "+program+"\n"), 0o644)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "one")
	commit := func(name string) {
		t.Helper()
		os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o644)
		gitIn(t, repo, "add", name)
		gitIn(t, repo, "commit", "-q", "-m", name)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", flaky)
	t.Setenv("GIT_EDITOR", "false")
	useDir(t, repo)

	resume := "`" + testBinary(t) + " --resume one`"
	want := ""
	for i, c := range []struct {
		name  string
		stdin func(*testing.T)
		// signed is how many signatures the key gives before refusing one.
		signed int
		// cont is whether the pass stops for `git rebase --continue`.
		stops, cont bool
	}{
		{"first, Enter", func(t *testing.T) { typed(t, "\n") }, 0, false, false},
		{"later, Enter", func(t *testing.T) { typed(t, "\n") }, 1, false, false},
		{"first, no terminal", noTerminal, 0, true, false},
		{"later, no terminal", noTerminal, 1, true, true},
		{"later, answered q", func(t *testing.T) { typed(t, "q\n") }, 1, true, true},
	} {
		commit(fmt.Sprintf("a%d", i))
		commit(fmt.Sprintf("b%d", i))
		want = strings.TrimPrefix(want+"\nG\nG", "\n")
		c.stdin(t)
		arm(t, armed, c.signed)
		result, err := Run("", false, false, false)
		switch {
		case !c.stops && err != nil:
			t.Fatalf("%s: %v", c.name, err)
		case !c.stops:
			if got := result.(map[string]any)["signed"]; got != 2 {
				t.Errorf("%s: signed %v commits", c.name, got)
			}
		case err == nil || !strings.Contains(err.Error(), resume) ||
			strings.Contains(err.Error(), "`git rebase --continue`") != c.cont:
			t.Fatalf("%s: %v", c.name, err)
		default:
			if c.cont {
				if _, err := git.Run("rebase", "--continue"); err != nil {
					t.Fatalf("%s: continuing as the pass said: %v", c.name, err)
				}
			}
			if _, err := Run("", true, false, false); err != nil {
				t.Fatalf("%s: resuming as the pass said: %v", c.name, err)
			}
		}
		if marks, _ := git.Run("log", "--format=%G?", "main..one"); marks != want {
			t.Errorf("%s: signatures %q", c.name, marks)
		}
	}
}

func testRunStack(t *testing.T) {
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
	gitenvtest.Decoy(t)
	useDir(t, repo)
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

	// An amend! from --fixup=reword: replaces A1's whole message, subject
	// included, a squash! appends to B1's, and a fixup! alongside them
	// folds as before. Editors that fail stand in for ones that would wait
	// for input: the pass must open neither.
	t.Setenv("GIT_EDITOR", `printf 'amend! A1\n\nA renamed\n\nWhy.\n' >`)
	gitIn(t, repo, "commit", "-q", "--fixup=reword:lower")
	os.WriteFile(filepath.Join(repo, "b"), []byte("b squashed\n"), 0o644)
	gitIn(t, repo, "commit", "-q", "-a", "--squash=upper~1", "-m", "More about B.")
	os.WriteFile(filepath.Join(repo, "b"), []byte("b fixed\n"), 0o644)
	gitIn(t, repo, "commit", "-q", "-a", "--fixup=upper~2")
	wantTree = gitIn(t, repo, "rev-parse", "upper^{tree}")
	t.Setenv("GIT_EDITOR", "false")
	t.Setenv("GIT_SEQUENCE_EDITOR", "false")
	result, err = Run("", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.(map[string]any); got["squashed"] != true || got["signed"] != 2 {
		t.Errorf("amend and squash result: %v", got)
	}
	if messages, _ := git.Run("log", "--format=%B%G?", "main..upper"); messages != "B1\n\nMore about B.\nG\nA renamed\n\nWhy.\nG" {
		t.Errorf("after amend and squash: %q", messages)
	}
	if got, _ := git.Run("rev-parse", "upper^{tree}"); got != wantTree {
		t.Error("amend and squash changed the files")
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

// testRunLint covers the messages a pass would sign: it refuses one that
// breaks the message rules before touching anything, judging the message
// as signed, so an amend! that fixes it lets the pass through.
func testRunLint(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "br")
	os.WriteFile(filepath.Join(repo, "a"), []byte("a\n"), 0o644)
	gitIn(t, repo, "add", "a")
	gitIn(t, repo, "commit", "-q", "-m", "Add a.", "-m", "Co-Authored-By: C <c@e>")
	os.WriteFile(filepath.Join(repo, "b"), []byte("b\n"), 0o644)
	gitIn(t, repo, "add", "b")
	gitIn(t, repo, "commit", "-q", "-m", "Add b")
	tip := gitIn(t, repo, "rev-parse", "br")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	gitenvtest.Decoy(t)
	useDir(t, repo)
	for name, pass := range map[string]func() (any, error){
		"Run":  func() (any, error) { return Run("", false, false, false) },
		"Plan": func() (any, error) { return Plan("", []string{"br"}, false, false) },
	} {
		_, err := pass()
		var r refusal.Refusal
		if !errors.As(err, &r) || !strings.Contains(err.Error(), `"Add a."`) || !strings.Contains(err.Error(), "period") ||
			!strings.Contains(err.Error(), "Co-Authored-By") || !refusal.SaysInstead(err.Error()) {
			t.Errorf("%s: %v", name, err)
		}
		if now, _ := git.Run("rev-parse", "br"); now != tip {
			t.Errorf("%s moved the branch", name)
		}
	}

	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "amend! Add a.", "-m", "Add a")
	if _, err := Run("", false, false, false); err != nil {
		t.Fatal(err)
	}
	if subjects, _ := git.Run("log", "--format=%s %G?", "main..br"); subjects != "Add b G\nAdd a G" {
		t.Errorf("after the amend!: %q", subjects)
	}
}

// testRunWorktreeBranch signs a stack whose lower branch is checked out in
// a worktree of its own, which --update-refs alone would leave behind.
func testRunWorktreeBranch(t *testing.T) {
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
	gitenvtest.Decoy(t)
	useDir(t, repo)
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
	rebase.Env = gitenv.Without(os.Environ())
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

func testPushMoved(t *testing.T) {
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
	useDir(t, repo)
	moved := []string{"fresh", "tracked"}

	pushed, err := pushAll("tracked", pushes{Branches: moved})
	if err != nil {
		t.Fatal(err)
	}
	if pushed["fresh"] != "not pushed: no upstream" || pushed["tracked"] != "origin refs/heads/tracked" {
		t.Errorf("--push: %v", pushed)
	}
	pushed, err = pushAll("tracked", pushes{Branches: moved, SetUpstream: true})
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
	if _, err := pushAll("taken", pushes{Branches: []string{"taken"}, SetUpstream: true}); err == nil || !strings.Contains(err.Error(), "--set-upstream-to=origin/taken") {
		t.Errorf("a fetched branch of the same name: %v", err)
	}
	gitIn(t, repo, "update-ref", "-d", "refs/remotes/origin/taken")
	if _, err := pushAll("taken", pushes{Branches: []string{"taken"}, SetUpstream: true}); err == nil {
		t.Error("an unfetched branch of the same name was pushed over")
	}
	if got, base := gitIn(t, hub, "rev-parse", "taken"), gitIn(t, repo, "rev-parse", "main"); got != base {
		t.Errorf("the remote's branch moved to %s", got)
	}
	if git.OK("config", "--get", "branch.taken.remote") {
		t.Error("a refused branch got an upstream")
	}
}

// testPushDestination holds --push to each branch's own name and away from
// the remote's default branch, refusing before the pass signs anything.
func testPushDestination(t *testing.T) {
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
	useDir(t, repo)
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
	if _, _, err := pushTargets([]string{"feature"}, false, "retry"); err == nil || !strings.Contains(err.Error(), "--set-upstream-to=origin/feature feature") {
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
	if _, _, err := pushTargets([]string{"main"}, false, "retry"); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Errorf("main with no origin/HEAD: %v", err)
	}
	gitIn(t, repo, "branch", "-q", "--track", "develop", "origin/develop")
	if _, _, err := pushTargets([]string{"develop"}, false, "retry"); err != nil {
		t.Errorf("develop with no origin/HEAD: %v", err)
	}
	gitIn(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/develop")
	if _, _, err := pushTargets([]string{"develop"}, false, "retry"); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Errorf("develop as origin/HEAD: %v", err)
	}
	if _, _, err := pushTargets([]string{"main"}, false, "retry"); err != nil {
		t.Errorf("main beside origin/HEAD develop: %v", err)
	}
	gitIn(t, repo, "branch", "-q", "--unset-upstream", "develop")
	gitIn(t, repo, "update-ref", "-d", "refs/remotes/origin/develop")
	if _, _, err := pushTargets([]string{"develop"}, true, "retry"); err == nil || !strings.Contains(err.Error(), "default branch") {
		t.Errorf("publishing develop as origin/HEAD: %v", err)
	}
}

func testPushRemote(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	gitIn(t, dir, "init", "-q", "-b", "main")
	useDir(t, dir)
	check := func(want, wantErr string) {
		t.Helper()
		got, err := pushRemote("b", "retry")
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

// testRunFailedPush leaves a pass whose push failed with nothing but the
// push to retry, and blocks no other branch.
func testRunFailedPush(t *testing.T) {
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
	gitIn(t, repo, "switch", "-qc", "one")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "O1")
	gitIn(t, repo, "push", "-q", "-u", "origin", "one")
	gitIn(t, repo, "branch", "two", "main")
	// The remote's branch moves without this repository seeing it, so the
	// leased push fails.
	leased := gitIn(t, hub, "rev-parse", "one")
	gitIn(t, repo, "push", "-q", "-f", hub, "main:refs/heads/one")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	useDir(t, repo)
	if _, err := Run("", false, true, false); err == nil || !strings.Contains(err.Error(), "`"+testBinary(t)+" --resume --push one`") {
		t.Fatalf("a failed push: %v", err)
	}
	if marks, _ := git.Run("log", "--format=%G?", "main..one"); marks != "G" {
		t.Errorf("one is not signed: %q", marks)
	}
	state := filepath.Join(repo, ".git", "sign-pass")
	if _, err := os.Stat(filepath.Join(state, "pending", "one.json")); err == nil {
		t.Error("the signing state outlived the signing")
	}
	if _, err := os.Stat(filepath.Join(state, "push", "one.json")); err != nil {
		t.Error("the push to retry was not recorded")
	}

	// Another branch signs meanwhile, in another worktree.
	wt := filepath.Join(dir, "two-wt")
	gitIn(t, repo, "worktree", "add", "-q", wt, "two")
	gitIn(t, wt, "commit", "-q", "--allow-empty", "-m", "T1")
	git.Dir = wt
	if _, err := Run("", false, false, false); err != nil {
		t.Errorf("an unrelated branch: %v", err)
	}
	if _, err := Run("", true, false, false); err == nil || !strings.Contains(err.Error(), "no signing pass or push") {
		t.Errorf("--resume on a branch with nothing in progress: %v", err)
	}
	git.Dir = repo

	// --resume retries only the push, leased on what the remote held before
	// the pass, until it goes through: a fetch does not lift the lease, and
	// the remote's branch going back to that does.
	signed := gitIn(t, repo, "rev-parse", "one")
	gitIn(t, repo, "fetch", "-q", "origin")
	if _, err := Run("", true, false, false); err == nil || !strings.Contains(err.Error(), "the push of one to "+hub+" failed") {
		t.Errorf("a retry that fails again: %v", err)
	}
	gitIn(t, repo, "push", "-q", "-f", hub, leased+":refs/heads/one")
	result, err := Run("", true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(result.(map[string]any)["pushed"]); got != "map[one:origin refs/heads/one]" {
		t.Errorf("pushed: %s", got)
	}
	if got := gitIn(t, hub, "rev-parse", "one"); got != signed || gitIn(t, repo, "rev-parse", "one") != signed {
		t.Errorf("the remote's one is %s, want %s", got, signed)
	}
	if _, err := Run("", true, false, false); err == nil {
		t.Error("a finished push was retried")
	}

	// A pass in progress blocks only passes that would move its branches.
	if err := writeJSON(filepath.Join(state, "pending", "feature", "upper.json"),
		pending{Branch: "feature/upper", Stacked: []string{"lower"}}); err != nil {
		t.Fatal(err)
	}
	for _, branches := range [][]string{{"feature/upper"}, {"x", "lower"}} {
		if err := inProgress(branches); err == nil || !strings.Contains(err.Error(), "feature/upper") {
			t.Errorf("%v beside a pass on feature/upper: %v", branches, err)
		}
	}
	if err := inProgress([]string{"one", "two"}); err != nil {
		t.Errorf("unrelated branches: %v", err)
	}
}

// mirrored makes a repository whose origin pushes to two URLs, as a mirrored
// one does: the bare repository hub, by path, and mirror.git over SSH through
// a fake ssh that refuses each time the file armed exists. The fake stands in
// for a forge reached over SSH with a hardware key, which a test must not
// contact. Branch one is pushed to both, then gains an unsigned commit.
func mirrored(t *testing.T) (dir, repo, hub, mirror, armed string) {
	t.Helper()
	dir = t.TempDir()
	hub, mirror = filepath.Join(dir, "hub.git"), filepath.Join(dir, "mirror.git")
	gitIn(t, dir, "init", "-q", "--bare", hub)
	gitIn(t, dir, "init", "-q", "--bare", mirror)
	ssh, armed := refusing(t, dir, "ssh", "true",
		`'sign_and_send_pubkey: signing failed for ED25519-SK "id_ed25519_sk" from agent: agent refused operation' 'git@fake: Permission denied (publickey).'`,
		`cd '`+dir+`' && exec sh -c "$2"`)
	t.Setenv("GIT_SSH_COMMAND", ssh)
	t.Setenv("GIT_SSH_VARIANT", "simple")
	repo = filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "remote", "add", "origin", hub)
	gitIn(t, repo, "remote", "set-url", "--add", "--push", "origin", hub)
	gitIn(t, repo, "remote", "set-url", "--add", "--push", "origin", "fake:mirror.git")
	gitIn(t, repo, "push", "-q", "-u", "origin", "main")
	gitIn(t, repo, "switch", "-qc", "one")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "O1")
	gitIn(t, repo, "push", "-q", "-u", "origin", "one")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "O2")
	return dir, repo, hub, mirror, armed
}

// testPushEachURL pushes to each of a remote's push URLs on its own: a URL
// whose SSH login the key refuses is retried on Enter at a terminal, and
// otherwise left for --resume, which pushes only that URL, leased on what it
// held before the pass, however the remote-tracking ref has moved since.
func testPushEachURL(t *testing.T) {
	dir, repo, hub, mirror, armed := mirrored(t)
	t.Setenv("GIT_CONFIG_GLOBAL", signingConfig(t, dir))
	useDir(t, repo)
	before := gitIn(t, repo, "rev-parse", "origin/one")

	noTerminal(t)
	arm(t, armed, 0)
	_, err := Run("", false, true, false)
	if err == nil {
		t.Fatal("a refused SSH login without a terminal")
	}
	for _, want := range []string{"fake:mirror.git", "pushed one to " + hub, "`" + testBinary(t) + " --resume --push one`"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q: %v", want, err)
		}
	}
	signed := gitIn(t, repo, "rev-parse", "one")
	if got := gitIn(t, hub, "rev-parse", "one"); got != signed {
		t.Errorf("the hub has one at %s, want %s", got, signed)
	}
	if got := gitIn(t, mirror, "rev-parse", "one"); got != before {
		t.Errorf("the mirror has one at %s, want %s", got, before)
	}
	if got := gitIn(t, repo, "rev-parse", "origin/one"); got != before {
		t.Errorf("origin/one moved to %s while the mirror lacks it", got)
	}

	// A fetch moves origin/one to the signed tip, and the hub goes away, so
	// a push leased on origin/one, or one to the hub, would fail.
	gitIn(t, repo, "fetch", "-q", "origin")
	if err := os.Rename(hub, hub+".away"); err != nil {
		t.Fatal(err)
	}
	result, err := Run("", true, false, false)
	if err != nil {
		t.Fatalf("resuming the mirror's push: %v", err)
	}
	os.Rename(hub+".away", hub)
	if got := fmt.Sprint(result.(map[string]any)["pushed"]); got != "map[one:origin refs/heads/one]" {
		t.Errorf("pushed: %s", got)
	}
	if got := gitIn(t, mirror, "rev-parse", "one"); got != signed {
		t.Errorf("the mirror has one at %s, want %s", got, signed)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "sign-pass", "push", "one.json")); err == nil {
		t.Error("the finished push is still recorded")
	}

	typed(t, "\n")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "O3")
	arm(t, armed, 0)
	if _, err := Run("", false, true, false); err != nil {
		t.Fatalf("a refused SSH login retried on Enter: %v", err)
	}
	signed = gitIn(t, repo, "rev-parse", "one")
	for _, where := range []string{hub, mirror} {
		if got := gitIn(t, where, "rev-parse", "one"); got != signed {
			t.Errorf("%s has one at %s, want %s", where, got, signed)
		}
	}
	if got := gitIn(t, repo, "rev-parse", "origin/one"); got != signed {
		t.Errorf("origin/one is %s, want %s", got, signed)
	}
}
