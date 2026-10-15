package redcheck

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// The throwaway repositories are Go modules, so that a test needing the new
// code fails to build as it would in a real repository.
const (
	goTest = "go test ./..."
	goVet  = "go vet ./..."
)

var goTests = []string{"**/*_test.go"}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo makes a module whose Add subtracts, committed on main, with an
// uncommitted file standing in for the caller's work in progress.
func newRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOWORK", "off")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "go.mod", "module example.com/m\n\ngo 1.21\n")
	write(t, dir, "calc.go", "package m\n\nfunc Add(a, b int) int { return a - b }\n")
	commit(t, dir, "Add Add")
	write(t, dir, "notes.txt", "work in progress\n")
	return dir
}

// fixedRepo makes a module as newRepo does, then commits a fix to Add with
// its test, and returns the module and the fix.
func fixedRepo(t *testing.T) (dir, fix string) {
	t.Helper()
	dir = newRepo(t)
	write(t, dir, "calc.go", fixedAdd)
	write(t, dir, "calc_test.go", addTest)
	return dir, commit(t, dir, "Make Add add")
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, message string) string {
	t.Helper()
	gitIn(t, dir, "add", "--", ".", ":!notes.txt")
	gitIn(t, dir, "commit", "-q", "-m", message)
	return gitIn(t, dir, "rev-parse", "HEAD")
}

const fixedAdd = "package m\n\nfunc Add(a, b int) int { return a + b }\n"

const addTest = `package m

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2, 3) = %d", got)
	}
}
`

// untouched fails the test if the check changed the caller's refs, files
// or worktrees, or left its own worktree behind.
func untouched(t *testing.T, dir string) func() {
	t.Helper()
	before := snapshot(t, dir)
	return func() {
		t.Helper()
		if after := snapshot(t, dir); after != before {
			t.Errorf("the caller's repository changed:\nbefore:\n%s\nafter:\n%s", before, after)
		}
		if _, err := os.Stat(filepath.Join(dir, ".git", "red-check")); !os.IsNotExist(err) {
			t.Errorf("the throwaway worktree's directory is left: %v", err)
		}
		if entries, _ := os.ReadDir(filepath.Join(dir, ".git", "worktrees")); len(entries) > 0 {
			t.Errorf("git's record of the throwaway worktree is left: %v", entries)
		}
	}
}

func snapshot(t *testing.T, dir string) string {
	t.Helper()
	return strings.Join([]string{
		gitIn(t, dir, "for-each-ref"),
		gitIn(t, dir, "rev-parse", "HEAD"),
		gitIn(t, dir, "status", "--porcelain", "--untracked-files=all"),
		gitIn(t, dir, "worktree", "list", "--porcelain"),
	}, "\n")
}

func check(t *testing.T, dir, rev string, opts Options) *Result {
	t.Helper()
	opts.Dir = dir
	if opts.Test == "" {
		opts.Test = goTest
	}
	if opts.TestFiles == nil {
		opts.TestFiles = goTests
	}
	defer untouched(t, dir)()
	result, err := Do(t.Context(), rev, opts)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func only(t *testing.T, r *Result) *Check {
	t.Helper()
	if len(r.Commits) != 1 {
		t.Fatalf("checked %d commits, want 1", len(r.Commits))
	}
	return r.Commits[0]
}

func TestVerdict(t *testing.T) {
	t.Run("fails without the fix", func(t *testing.T) {
		dir, fix := fixedRepo(t)
		r := check(t, dir, "HEAD", Options{Build: goVet, Lines: 5})
		c := only(t, r)
		if c.Commit != fix || c.Verdict != FailsAsAsserted || r.Exit() != ExitProven {
			t.Fatalf("verdict %s, exit %d: %+v", c.Verdict, r.Exit(), c)
		}
		if c.Build.Exit != 0 || c.Without.Exit == 0 || c.With.Exit != 0 {
			t.Errorf("runs: build %+v, without %+v, with %+v", c.Build, c.Without, c.With)
		}
		if !strings.Contains(c.Without.Output, "Add(2, 3) = -1") {
			t.Errorf("the failing run's output is missing: %q", c.Without.Output)
		}
		if n := len(strings.Split(c.Without.Output, "\n")); n > 5 {
			t.Errorf("kept %d lines of output, want at most 5", n)
		}
		if strings.Join(c.TestFiles, ",") != "calc_test.go" {
			t.Errorf("test files: %v", c.TestFiles)
		}

		if c.Why != "" {
			t.Errorf("a proven commit says why it is not: %q", c.Why)
		}
	})

	// Without a build command, a failure cannot be told from a failure to
	// build, which is weak evidence.
	t.Run("without a build command", func(t *testing.T) {
		dir, _ := fixedRepo(t)
		r := check(t, dir, "HEAD", Options{})
		if c := only(t, r); c.Verdict != FailsWithoutChange || c.Build != nil || r.Exit() != ExitUnproven {
			t.Errorf("without --build: %s, exit %d, %+v", c.Verdict, r.Exit(), c.Build)
		} else if !strings.Contains(c.Why, "build command") {
			t.Errorf("without --build, why: %q", c.Why)
		}
	})

	t.Run("passes either way", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "calc.go", fixedAdd)
		write(t, dir, "calc_test.go", "package m\n\nimport \"testing\"\n\nfunc TestAddZero(t *testing.T) {\n\tif Add(0, 0) != 0 {\n\t\tt.Fatal()\n\t}\n}\n")
		commit(t, dir, "Make Add add")

		r := check(t, dir, "HEAD", Options{Build: goVet})
		if c := only(t, r); c.Verdict != PassesWithoutChange || r.Exit() != ExitDisproven {
			t.Errorf("verdict %s, exit %d", c.Verdict, r.Exit())
		}
	})

	t.Run("fails with the change", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "calc.go", "package m\n\nfunc Add(a, b int) int { return a * b }\n")
		write(t, dir, "calc_test.go", addTest)
		commit(t, dir, "Make Add multiply")

		r := check(t, dir, "HEAD", Options{})
		if c := only(t, r); c.Verdict != FailsWithChange || r.Exit() != ExitDisproven {
			t.Errorf("verdict %s, exit %d", c.Verdict, r.Exit())
		}
	})

	t.Run("no test files", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "calc.go", fixedAdd)
		commit(t, dir, "Make Add add")

		r := check(t, dir, "HEAD", Options{})
		c := only(t, r)
		if c.Verdict != NoTestFiles || r.Exit() != ExitUnproven || c.Without != nil || c.With != nil {
			t.Errorf("verdict %s, exit %d, runs %+v %+v", c.Verdict, r.Exit(), c.Without, c.With)
		}
	})

	t.Run("only tests", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "calc.go", fixedAdd)
		commit(t, dir, "Make Add add")
		write(t, dir, "calc_test.go", addTest)
		commit(t, dir, "Test Add")
		if c := only(t, check(t, dir, "HEAD", Options{})); c.Verdict != NoCodeChange {
			t.Errorf("a commit of only tests: %s", c.Verdict)
		}
	})

	t.Run("needs the new code to build", func(t *testing.T) {
		dir := newRepo(t)
		write(t, dir, "mul.go", "package m\n\nfunc Mul(a, b int) int { return a * b }\n")
		write(t, dir, "mul_test.go", "package m\n\nimport \"testing\"\n\nfunc TestMul(t *testing.T) {\n\tif Mul(2, 3) != 6 {\n\t\tt.Fatal()\n\t}\n}\n")
		commit(t, dir, "Add Mul")

		r := check(t, dir, "HEAD", Options{Build: goVet})
		c := only(t, r)
		if c.Verdict != FailsToBuildWithoutChange || r.Exit() != ExitUnproven {
			t.Errorf("verdict %s, exit %d", c.Verdict, r.Exit())
		}
		if !strings.Contains(c.Build.Output, "undefined: Mul") {
			t.Errorf("the build's output: %q", c.Build.Output)
		}
	})
}

func TestFixups(t *testing.T) {
	// withFixup makes a module with two commits after base, the second
	// untested and wrong, and a tested fixup! of it.
	withFixup := func(t *testing.T) (dir, base, fixup string) {
		t.Helper()
		dir = newRepo(t)
		base = gitIn(t, dir, "rev-parse", "HEAD")
		write(t, dir, "calc.go", fixedAdd)
		commit(t, dir, "Make Add add")
		write(t, dir, "calc.go", fixedAdd+"\nfunc Sub(a, b int) int { return b - a }\n")
		commit(t, dir, "Add Sub")
		write(t, dir, "calc.go", fixedAdd+"\nfunc Sub(a, b int) int { return a - b }\n")
		write(t, dir, "calc_test.go", "package m\n\nimport \"testing\"\n\nfunc TestSub(t *testing.T) {\n\tif Sub(3, 1) != 2 {\n\t\tt.Fatal()\n\t}\n}\n")
		return dir, base, commit(t, dir, "fixup! Add Sub")
	}

	t.Run("of a range", func(t *testing.T) {
		dir, base, fixup := withFixup(t)
		r := check(t, dir, base+"..HEAD", Options{Fixups: true})
		if c := only(t, r); c.Commit != fixup || c.Verdict != FailsWithoutChange || r.Exit() != ExitUnproven {
			t.Errorf("checked %s: %s, exit %d", c.Subject, c.Verdict, r.Exit())
		}
	})

	t.Run("whole range without --fixups", func(t *testing.T) {
		dir, base, _ := withFixup(t)
		r := check(t, dir, base+"..HEAD", Options{})
		if len(r.Commits) != 3 || r.Exit() != ExitUnproven {
			t.Errorf("the whole range: %d commits, exit %d", len(r.Commits), r.Exit())
		}
	})

	t.Run("without a range", func(t *testing.T) {
		dir, _, _ := withFixup(t)
		if _, err := Do(t.Context(), "HEAD", Options{Dir: dir, Test: goTest, TestFiles: goTests, Fixups: true}); err == nil {
			t.Error("--fixups without a range was accepted")
		}
	})
}

func TestCleanup(t *testing.T) {
	// The test command breaks the throwaway worktree, so that moving it to
	// the commit fails.
	t.Run("on failure", func(t *testing.T) {
		dir, _ := fixedRepo(t)
		defer untouched(t, dir)()
		_, err := Do(t.Context(), "HEAD", Options{Dir: dir, Test: "rm .git; exit 1", TestFiles: goTests})
		if err == nil {
			t.Fatal("a broken worktree was not reported")
		}
	})

	t.Run("on interrupt", func(t *testing.T) {
		dir, _ := fixedRepo(t)
		// The test command starts a child that runs until it is stopped,
		// then writes a line to started; reading the line is the cue to
		// interrupt the check.
		started := filepath.Join(t.TempDir(), "started")
		if err := syscall.Mkfifo(started, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() {
			if f, err := os.Open(started); err == nil {
				bufio.NewReader(f).ReadString('\n')
				f.Close()
			}
			cancel()
		}()

		defer untouched(t, dir)()
		start := time.Now()
		_, err := Do(ctx, "HEAD", Options{Dir: dir, Test: "sleep 30 & echo > " + started + "; wait", TestFiles: goTests})
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("an interrupted check: %v", err)
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("the test command outlived the interruption by %s", d)
		}
	})

	t.Run("on interrupt, while the test command forks", func(t *testing.T) {
		dir, _ := fixedRepo(t)
		// The test command records its process group, then starts children
		// without pause, sending the cue to interrupt the check among them,
		// so that the interruption lands while it forks. The children's
		// output goes elsewhere, so a survivor cannot hold the check up.
		tmp := t.TempDir()
		started, group := filepath.Join(tmp, "started"), filepath.Join(tmp, "group")
		if err := syscall.Mkfifo(started, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		go func() {
			if f, err := os.Open(started); err == nil {
				bufio.NewReader(f).ReadString('\n')
				f.Close()
			}
			cancel()
		}()

		test := "echo $$ > " + group + "; i=0; while [ $i -lt 300 ]; do sleep 30 > /dev/null 2>&1 & i=$((i+1)); " +
			"if [ $i = 20 ]; then echo > " + started + "; fi; done; wait"
		_, err := Do(ctx, "HEAD", Options{Dir: dir, Test: test, TestFiles: goTests})
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("an interrupted check: %v", err)
		}
		data, _ := os.ReadFile(group)
		pgid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			t.Fatalf("the test command recorded no process group: %q", data)
		}
		// Whatever survives is this test's to stop.
		t.Cleanup(func() { syscall.Kill(-pgid, syscall.SIGKILL) })
		if err := syscall.Kill(-pgid, 0); !errors.Is(err, syscall.ESRCH) {
			left, _ := exec.Command("ps", "-o", "pid,stat,command", "-g", strconv.Itoa(pgid)).CombinedOutput()
			t.Errorf("the test command's process group %d outlived the check (%v):\n%s", pgid, err, left)
		}
	})
}

// The repository checked is the one opts.Dir names, or else the one git finds
// from the process's directory, whatever repository the environment names.
func TestRepository(t *testing.T) {
	t.Run("named by Dir", func(t *testing.T) {
		dir, fix := fixedRepo(t)
		gitenvtest.Decoy(t)
		if c := only(t, check(t, dir, "HEAD", Options{})); c.Commit != fix || c.Verdict != FailsWithoutChange {
			t.Errorf("with GIT_DIR naming another repository: %s %s", c.Commit, c.Verdict)
		}
	})

	t.Run("found from a directory GIT_DIR does not name", func(t *testing.T) {
		dir, _ := fixedRepo(t)
		gitenvtest.Decoy(t)
		os.Unsetenv("GIT_WORK_TREE")
		t.Chdir(dir)
		_, err := Do(t.Context(), "HEAD", Options{Test: goTest, TestFiles: goTests})
		if _, ok := err.(refusal.Refusal); !ok {
			t.Errorf("found from a directory GIT_DIR does not name: %v", err)
		}
	})
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		opts Options
	}{
		{"no test command", Options{TestFiles: goTests}},
		{"no test files", Options{Test: goTest}},
	} {
		t.Run(c.name, func(t *testing.T) {
			opts := c.opts
			opts.Dir = newRepo(t)
			if _, err := Do(t.Context(), "HEAD", opts); err == nil {
				t.Errorf("%+v was accepted", opts)
			}
		})
	}

	t.Run("root commit", func(t *testing.T) {
		dir := newRepo(t)
		if _, err := Do(t.Context(), "HEAD", Options{Dir: dir, Test: goTest, TestFiles: goTests}); err == nil {
			t.Error("a root commit was accepted")
		}
	})
}
