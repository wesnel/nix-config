package signpass

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	t.Run("gains", testRunGains)
	t.Run("fork gains", testRunForkGains)
	t.Run("message gains", testRunMessageGains)
	t.Run("failed push", testRunFailedPush)
	t.Run("missed touch", testRunMissedTouch)
}

func TestPush(t *testing.T) {
	t.Run("moved branches", testPushMoved)
	t.Run("destination", testPushDestination)
	t.Run("remote", testPushRemote)
	t.Run("each URL", testPushEachURL)
	t.Run("stack bottom up", testPushStackBottomUp)
	t.Run("control path too long", testPushControlPathTooLong)
	t.Run("interrupted", testPushInterrupted)
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
	gitIn(t, repo, "commit", "-q", "-m", "Add a.", "-m", "The symlinked case (tZ1Cm) needs it.", "-m", "Co-Authored-By: C <c@e>")
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
			!strings.Contains(err.Error(), "Co-Authored-By") || !strings.Contains(err.Error(), `"tZ1Cm"`) ||
			!refusal.SaysInstead(err.Error()) {
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

// commitFile writes content to file in repo and commits it with message.
func commitFile(t *testing.T, repo, file, content string, message ...string) {
	t.Helper()
	os.WriteFile(filepath.Join(repo, file), []byte(content), 0o644)
	gitIn(t, repo, "add", file)
	args := []string{"commit", "-q"}
	for _, m := range message {
		args = append(args, "-m", m)
	}
	gitIn(t, repo, args...)
}

// testRunGains signs a stack whose lower branch, checked out in a worktree of
// its own, ends in a fixup, while fixups above it fold into its commits: the
// pass names lower as gaining them before the first touch, whether or not it
// prints its whole plan, and refuses afterwards a stacked branch on any
// commit but the signed copy of the last one it keeps.
func testRunGains(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "lower")
	commitFile(t, repo, "x1", "x1\n", "Add x1")
	commitFile(t, repo, "x2", "x2\n", "Add x2")
	commitFile(t, repo, "x1", "x1 fixed\n", "fixup! Add x1")
	gitIn(t, repo, "switch", "-qc", "upper")
	commitFile(t, repo, "y", "y\n", "Add y")
	commitFile(t, repo, "x2", "x2 fixed\n", "fixup! Add x2")
	wt := filepath.Join(dir, "lower-wt")
	gitIn(t, repo, "worktree", "add", "-q", wt, "lower")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	gitenvtest.Decoy(t)
	useDir(t, repo)
	const named = "lower gains fixups from commits it does not contain\n"
	stderr := captureStderr(t)
	if _, err := Run("", false, false, false); err != nil {
		t.Fatal(err)
	}
	out := stderr()
	if at := strings.Index(out, named); at < 0 || at > strings.Index(out, "touch the key") {
		t.Errorf("a single-branch pass did not name lower before the touches:\n%s", out)
	}

	// A brief pass over named branches prints no plan, but names it still.
	Brief = true
	t.Cleanup(func() { Brief = false })
	commitFile(t, repo, "x2", "x2 fixed again\n", "fixup! Add x2")
	skip := len(stderr())
	if _, err := Plan("", []string{"upper"}, false, false); err != nil {
		t.Fatal(err)
	}
	out = stderr()[skip:]
	if at := strings.Index(out, named); at < 0 || at > strings.Index(out, "touch the key") {
		t.Errorf("a brief pass did not name lower before the touches:\n%s", out)
	}
	Brief = false

	commitFile(t, repo, "x1", "x1 fixed again\n", "fixup! Add x1")
	res, err := Plan("", []string{"upper"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	branches := res.(map[string]any)["branches"].(map[string]any)
	if lines := fmt.Sprint(branches["plan"]); !strings.Contains(lines, "moves lower; lower gains fixups from commits it does not contain") {
		t.Errorf("plan: %s", lines)
	}

	// A branch the rebase left on any other signed commit is refused, naming
	// where it belongs.
	lower := gitIn(t, repo, "rev-parse", "lower")
	unsigned := gitIn(t, repo, "rev-parse", BackupRef+"upper~1")
	shape, err := fullShape("main", "upper")
	if err != nil {
		t.Fatal(err)
	}
	p := &pending{Branch: "upper", Start: "main", Shape: shape, Stacked: []string{"lower"},
		Before: map[string]string{"lower": unsigned}, Lands: map[string]int{"lower": 2}}
	gitIn(t, wt, "reset", "-q", "--keep", "upper~2")
	if _, err := verify(p); err == nil || !strings.Contains(err.Error(), "lower belongs at "+lower) {
		t.Errorf("a misplaced branch: %v", err)
	}
	gitIn(t, wt, "reset", "-q", "--keep", lower)
	if _, err := verify(p); err != nil {
		t.Errorf("a branch where it belongs: %v", err)
	}
}

// testRunMessageGains signs a stack whose upper branch rewords a commit of
// the lower one with an amend! that changes no files: lower gains it, which
// the pass says before the first touch and reports once signed.
func testRunMessageGains(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "lower")
	commitFile(t, repo, "x1", "x1\n", "Add x1")
	commitFile(t, repo, "x2", "x2\n", "Add x2")
	gitIn(t, repo, "switch", "-qc", "upper")
	commitFile(t, repo, "y", "y\n", "Add y")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "amend! Add x1", "-m", "Add the first x")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	gitenvtest.Decoy(t)
	useDir(t, repo)
	stderr := captureStderr(t)
	result, err := Run("", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if out := stderr(); !strings.Contains(out, "lower gains fixups from commits it does not contain") {
		t.Errorf("lower was not named before the touches:\n%s", out)
	}
	if got := fmt.Sprint(result.(map[string]any)["gainedFixups"]); got != "[lower]" {
		t.Errorf("gainedFixups: %s", got)
	}
	if subjects, _ := git.Run("log", "--format=%s", "main..lower"); subjects != "Add x2\nAdd the first x" {
		t.Errorf("lower's commits: %q", subjects)
	}
}

// testRunForkGains signs a fork whose earlier child folds a fixup into the
// fork point: the later child's branches, its tip included, gain it, from
// commits they do not contain, which the plan says before the first touch
// and the result reports once signed.
func testRunForkGains(t *testing.T) {
	dir := t.TempDir()
	global := signingConfig(t, dir)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "switch", "-qc", "lower")
	commitFile(t, repo, "x1", "x1\n", "Add x1")
	commitFile(t, repo, "x2", "x2\n", "Add x2")
	gitIn(t, repo, "switch", "-qc", "c1")
	commitFile(t, repo, "b", "b\n", "Add b")
	commitFile(t, repo, "x2", "x2 fixed\n", "fixup! Add x2")
	gitIn(t, repo, "switch", "-qc", "c2", "lower")
	commitFile(t, repo, "d", "d\n", "Add d")
	gitIn(t, repo, "switch", "-qc", "c2top")
	commitFile(t, repo, "e", "e\n", "Add e")
	gitIn(t, repo, "switch", "-q", "c1")

	t.Setenv("GIT_CONFIG_GLOBAL", global)
	gitenvtest.Decoy(t)
	useDir(t, repo)
	res, err := Plan("", []string{"c1", "c2top"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	branches := res.(map[string]any)["branches"].(map[string]any)
	if lines := fmt.Sprint(branches["plan"]); !strings.Contains(lines, "moves c2; c2, c2top gain fixups from commits they do not contain") {
		t.Errorf("plan: %s", lines)
	}
	if got := fmt.Sprint(branches["c2top"].(map[string]any)["gainedFixups"]); got != "[c2 c2top]" {
		t.Errorf("c2top's gainedFixups: %s", got)
	}
	if x2, _ := git.Run("show", "c2:x2"); x2 != "x2 fixed" {
		t.Errorf("c2 did not gain the fix: %q", x2)
	}
}

// Where landings puts each kind of branch tip, and which gaining finds
// gaining fixups: a fixup, a fixup of a fixup, a squash! and an empty amend!
// each leave a branch on the last commit it keeps.
func TestLandings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", signingConfig(t, dir))
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	base := gitIn(t, repo, "rev-parse", "HEAD")
	gitIn(t, repo, "switch", "-qc", "top")
	commitFile(t, repo, "x1", "x1\n", "Add x1")
	commitFile(t, repo, "x1", "x1 f\n", "fixup! Add x1")
	gitIn(t, repo, "branch", "fix")
	commitFile(t, repo, "x2", "x2\n", "Add x2")
	gitIn(t, repo, "branch", "kept")
	commitFile(t, repo, "x1", "x1 ff\n", "fixup! fixup! Add x1")
	gitIn(t, repo, "branch", "chain")
	commitFile(t, repo, "x2", "x2 s\n", "squash! Add x2", "More about x2.")
	gitIn(t, repo, "branch", "squash")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "amend! Add x1", "-m", "Add the first x")
	gitIn(t, repo, "branch", "amend")
	commitFile(t, repo, "y", "y\n", "Add y")
	stacked := []string{"amend", "chain", "fix", "kept", "squash"}

	useDir(t, repo)
	before, err := localBranches()
	if err != nil {
		t.Fatal(err)
	}
	lands, err := landings(before, stacked, base)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(lands); got != "map[amend:2 chain:2 fix:1 kept:2 squash:2]" {
		t.Errorf("landings: %s", got)
	}
	kept, err := keptCommits(base, "top")
	if err != nil {
		t.Fatal(err)
	}
	head, err := rehearseSquash(base, "top", kept)
	if err != nil {
		t.Fatal(err)
	}
	gains, err := gaining(lands, before, base, base, head)
	if err != nil {
		t.Fatal(err)
	}
	// squash has every change to files that folds into its commits, but not
	// the amend! above it, which rewords Add x1; amend has both.
	if got := fmt.Sprint(gains); got != "[chain fix kept squash]" {
		t.Errorf("gaining: %s", got)
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

// sharingSSH writes a fake OpenSSH to dir that serves every host from the
// bare repositories in dir, and shares connections as OpenSSH does: a login
// authenticates, which is a key touch, unless a master is up on its
// ControlPath (%C there standing for the host), and one started with
// ControlMaster=auto and a ControlPersist stays up until `-O exit`. It logs
// "auth HOST", "open PATH" and "close PATH" to the log it returns. While
// dir/hang exists, a login writes dir/ready and then hangs until git closes
// its standard input. It is a
// fake because a test must not reach a forge or touch a key. The ssh it puts
// first on PATH only fails, so a push that loses the configured command
// fails instead of reaching the real one.
func sharingSSH(t *testing.T, dir string) (program, log string) {
	t.Helper()
	program, log = filepath.Join(dir, "sharing-ssh"), filepath.Join(dir, "ssh.log")
	script := `#!/bin/sh
path= master= persist= op=
while [ $# -gt 0 ]; do
  case $1 in
  -o) shift
    case $1 in
    ControlPath=*) path=${1#ControlPath=} ;;
    ControlMaster=*) master=${1#ControlMaster=} ;;
    ControlPersist=*) persist=${1#ControlPersist=} ;;
    esac ;;
  -O) shift; op=$1 ;;
  -p) shift ;;
  -*) ;;
  *) break ;;
  esac
  shift
done
host=$1
case $path in *%C) path="${path%??}$host" ;; esac
if [ "$op" = exit ]; then
  if [ -z "$path" ] || [ ! -e "$path" ]; then
    echo "Control socket connect($path): No such file or directory" >&2
    exit 255
  fi
  rm "$path"
  echo "close $path" >> 'LOG'
  exit 0
fi
if [ -z "$path" ] || [ ! -e "$path" ]; then
  echo "auth $host" >> 'LOG'
  if [ -n "$path" ] && [ "$master" = auto ] && [ -n "$persist" ] && [ "$persist" != no ]; then
    : > "$path"
    echo "open $path" >> 'LOG'
  fi
fi
if [ -e 'DIR/hang' ]; then
  echo > 'DIR/ready'
  cat > /dev/null
  exit 255
fi
# A remote's git never sees the client's -c settings.
unset GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
cd 'DIR' && exec sh -c "$2"
`
	script = strings.NewReplacer("LOG", log, "DIR", dir).Replace(script)
	if err := os.WriteFile(program, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	os.Mkdir(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\necho 'the ssh on PATH ran' >&2\nexit 255\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_SSH_VARIANT", "ssh")
	return program, log
}

// sshLog counts each line of the fake ssh's log, and empties it.
func sshLog(t *testing.T, log string) map[string]int {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	os.Remove(log)
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if kind, what, ok := strings.Cut(line, " "); ok {
			if kind != "auth" {
				what = "socket"
			}
			counts[kind+" "+what]++
		}
	}
	return counts
}

// receiving makes a bare repository at dir/NAME.git whose post-receive hook
// logs each push it takes as one line naming the branches it moved.
func receiving(t *testing.T, dir, name string) (bare, log string) {
	t.Helper()
	bare, log = filepath.Join(dir, name+".git"), filepath.Join(dir, name+".pushes")
	gitIn(t, dir, "init", "-q", "--bare", bare)
	hook := "#!/bin/sh\ncut -d' ' -f3 | sed 's,^refs/heads/,,' | paste -sd' ' - >> '" + log + "'\n"
	if err := os.WriteFile(filepath.Join(bare, "hooks", "post-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	return bare, log
}

// pushesTo is the pushes a repository from receiving has taken since the
// last call, one line each.
func pushesTo(t *testing.T, log string) string {
	t.Helper()
	data, _ := os.ReadFile(log)
	os.Remove(log)
	return strings.TrimSpace(string(data))
}

// sshStack makes a three-branch stack, parent, child on it and grandchild on
// that, each unsigned and tracking its own name on origin, which pushes over
// the fake sharingSSH to two hosts: hub, then mirror. The names sort
// otherwise than the stack does. While dir/reject exists, the mirror
// refuses child.
func sshStack(t *testing.T) (dir, repo, sshLogFile, hubPushes, mirrorPushes string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", signingConfig(t, dir))
	program, sshLogFile := sharingSSH(t, dir)
	t.Setenv("GIT_SSH_COMMAND", program)
	hub, hubPushes := receiving(t, dir, "hub")
	mirror, mirrorPushes := receiving(t, dir, "mirror")
	reject := "#!/bin/sh\nwhile read old new ref; do\n  if [ \"$ref\" = refs/heads/child ] && [ -e '" +
		filepath.Join(dir, "reject") + "' ]; then echo 'child is refused here' >&2; exit 1; fi\ndone\n"
	if err := os.WriteFile(filepath.Join(mirror, "hooks", "pre-receive"), []byte(reject), 0o755); err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	gitIn(t, repo, "init", "-q", "-b", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "remote", "add", "origin", "hub:hub.git")
	gitIn(t, repo, "remote", "set-url", "--add", "--push", "origin", "hub:hub.git")
	gitIn(t, repo, "remote", "set-url", "--add", "--push", "origin", "mirror:mirror.git")
	for _, b := range []string{"parent", "child", "grandchild"} {
		gitIn(t, repo, "switch", "-qc", b)
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "Add "+b)
		gitIn(t, repo, "update-ref", "refs/remotes/origin/"+b, b)
		gitIn(t, repo, "branch", "-q", "--set-upstream-to=origin/"+b, b)
	}
	for _, bare := range []string{hub, mirror} {
		gitIn(t, repo, "push", "-q", bare, "main", "parent", "child", "grandchild")
	}
	pushesTo(t, hubPushes)
	pushesTo(t, mirrorPushes)
	sshLog(t, sshLogFile)
	useDir(t, repo)
	return dir, repo, sshLogFile, hubPushes, mirrorPushes
}

// testPushStackBottomUp pushes a moved stack one branch per push, bottom
// up, to each push URL, so each pull request's CI starts once, while the
// pushes to one URL share a single SSH login, one key touch. A push refused
// partway is resumed from the first branch that URL lacks.
func testPushStackBottomUp(t *testing.T) {
	dir, repo, sshLogFile, hubPushes, mirrorPushes := sshStack(t)
	before := map[string]string{}
	for _, b := range []string{"parent", "child", "grandchild"} {
		before[b] = gitIn(t, repo, "rev-parse", b)
	}
	os.WriteFile(filepath.Join(dir, "reject"), nil, 0o644)

	noTerminal(t)
	_, err := Run("", false, true, false)
	if err == nil || !strings.Contains(err.Error(), "`"+testBinary(t)+" --resume --push grandchild`") {
		t.Fatalf("a push the mirror refuses: %v", err)
	}
	if got := pushesTo(t, hubPushes); got != "parent\nchild\ngrandchild" {
		t.Errorf("the hub took these pushes:\n%s", got)
	}
	if got := pushesTo(t, mirrorPushes); got != "parent" {
		t.Errorf("the mirror took these pushes:\n%s", got)
	}
	want := map[string]int{"auth hub": 1, "auth mirror": 1, "open socket": 2, "close socket": 2}
	if got := sshLog(t, sshLogFile); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("SSH logins and shared connections: %v, want %v", got, want)
	}
	for b, old := range before {
		if got := gitIn(t, repo, "rev-parse", "origin/"+b); got != old {
			t.Errorf("origin/%s moved to %s while the mirror lacks it", b, got)
		}
	}

	os.Remove(filepath.Join(dir, "reject"))
	result, err := Run("", true, false, false)
	if err != nil {
		t.Fatalf("resuming the mirror's push: %v", err)
	}
	if got := pushesTo(t, hubPushes); got != "" {
		t.Errorf("the resumed pass pushed to the hub again:\n%s", got)
	}
	if got := pushesTo(t, mirrorPushes); got != "child\ngrandchild" {
		t.Errorf("the resumed pass pushed these to the mirror:\n%s", got)
	}
	want = map[string]int{"auth mirror": 1, "open socket": 1, "close socket": 1}
	if got := sshLog(t, sshLogFile); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("SSH logins and shared connections on resuming: %v, want %v", got, want)
	}
	pushed := fmt.Sprint(result.(map[string]any)["pushed"])
	if want := "map[child:origin refs/heads/child grandchild:origin refs/heads/grandchild parent:origin refs/heads/parent]"; pushed != want {
		t.Errorf("pushed: %s", pushed)
	}
	for _, b := range []string{"parent", "child", "grandchild"} {
		signed := gitIn(t, repo, "rev-parse", b)
		if signed == before[b] {
			t.Errorf("%s did not move", b)
		}
		for where, ref := range map[string]string{filepath.Join(dir, "hub.git"): b, filepath.Join(dir, "mirror.git"): b, repo: "origin/" + b} {
			if got := gitIn(t, where, "rev-parse", ref); got != signed {
				t.Errorf("%s has %s at %s, want %s", where, ref, got, signed)
			}
		}
	}
}

// testPushControlPathTooLong refuses, before any key touch, a push whose
// shared SSH connection would need a socket path longer than a Unix socket
// takes.
func testPushControlPathTooLong(t *testing.T) {
	dir, _, sshLogFile, hubPushes, _ := sshStack(t)
	long := filepath.Join(dir, strings.Repeat("d", 120))
	if err := os.Mkdir(long, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	for name, pass := range map[string]func() (any, error){
		"one branch": func() (any, error) { return Run("", false, true, false) },
		"named":      func() (any, error) { return Plan("", []string{"grandchild"}, true, false) },
	} {
		_, err := pass()
		var r refusal.Refusal
		if !errors.As(err, &r) || !strings.Contains(err.Error(), "TMPDIR") || !refusal.SaysInstead(err.Error()) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if marks, _ := git.Run("log", "--format=%G?", "main..grandchild"); marks != "N\nN\nN" {
		t.Errorf("the refused pass signed: %q", marks)
	}
	if got := pushesTo(t, hubPushes); got != "" {
		t.Errorf("the refused pass pushed:\n%s", got)
	}
	if got := sshLog(t, sshLogFile); len(got) != 0 {
		t.Errorf("the refused pass logged in: %v", got)
	}
}

// testPushInterrupted stops a pass with SIGTERM while its first push waits
// on SSH. The pass returns instead of dying, having ended its shared
// connection and removed the directory of its control sockets.
func testPushInterrupted(t *testing.T) {
	dir, _, sshLogFile, _, _ := sshStack(t)
	ready := filepath.Join(dir, "ready")
	if err := syscall.Mkfifo(ready, 0o600); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "hang"), nil, 0o644)
	go func() {
		if r, err := os.Open(ready); err == nil {
			bufio.NewReader(r).ReadString('\n')
			r.Close()
			syscall.Kill(os.Getpid(), syscall.SIGTERM)
		}
	}()
	noTerminal(t)
	if _, err := Run("", false, true, false); err == nil {
		t.Fatal("the interrupted pass succeeded")
	}
	data, _ := os.ReadFile(sshLogFile)
	var sockets string
	for _, line := range strings.Split(string(data), "\n") {
		if path, ok := strings.CutPrefix(line, "open "); ok {
			sockets = filepath.Dir(path)
		}
	}
	if want := map[string]int{"auth hub": 1, "open socket": 1, "close socket": 1}; fmt.Sprint(sshLog(t, sshLogFile)) != fmt.Sprint(want) {
		t.Errorf("SSH logins and shared connections: %s, want %v", data, want)
	}
	if _, err := os.Stat(sockets); sockets == "" || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the control sockets' directory %q is left: %v", sockets, err)
	}
}
