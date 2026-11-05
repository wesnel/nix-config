package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitstage"
)

// realTempDir is a temporary directory without symlinks on its path, which
// staging requires of its private directory; $TMPDIR may be one, such as
// /tmp on macOS.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func privateStageArgs(t *testing.T, repo string, paths ...string) []string {
	t.Helper()
	private := realTempDir(t)
	index, objects := filepath.Join(private, "index"), filepath.Join(private, "objects")
	data, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(objects, 0o700); err != nil {
		t.Fatal(err)
	}
	return append([]string{repo, index, objects}, paths...)
}

// TestGitStageNative stages through the real Codex sandbox, which cannot
// nest in another sandbox, so it runs only where PR_LOOP_NATIVE_CODEX names
// the Codex executable, from a terminal outside every agent's sandbox:
//
//	PR_LOOP_NATIVE_CODEX=$HOME/.local/bin/codex go test ./cmd/pr-loop -run TestGitStageNative -count=1 -v
func TestGitStageNative(t *testing.T) {
	codex := os.Getenv("PR_LOOP_NATIVE_CODEX")
	if codex == "" {
		t.Skip("set PR_LOOP_NATIVE_CODEX to the Codex executable's absolute path, outside any sandbox, to run")
	}
	root := realTempDir(t)
	main, task := filepath.Join(root, "main"), filepath.Join(root, "task")
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
		}
		return string(out)
	}
	git(root, "init", "-q", "-b", "main", main)
	if err := os.WriteFile(filepath.Join(main, "code.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(main, "add", "--", "code.txt")
	git(main, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	git(main, "worktree", "add", "-q", "-b", "task", task)
	if err := os.WriteFile(filepath.Join(task, "code.txt"), []byte("native\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gitStageCodexEnv, codex)
	var out, stderr bytes.Buffer
	if code := gitStage(t.Context(), task, []string{"code.txt"}, &out, &stderr); code != 0 {
		t.Fatalf("stage through Codex's sandbox: exit %d: %s", code, stderr.String())
	}
	if got := git(task, "show", ":code.txt"); got != "native\n" {
		t.Fatalf("staged %q", got)
	}
	// The child's sandbox, as gitStage starts it: a workspace of its own,
	// where it writes, and the repository, where it may not.
	layout, err := gitstage.Resolve(task)
	if err != nil {
		t.Fatal(err)
	}
	workspace := realTempDir(t)
	self, err := os.Executable()
	if err == nil {
		self, err = filepath.EvalSymlinks(self)
	}
	if err != nil {
		t.Fatal(err)
	}
	if codex, err = filepath.EvalSymlinks(codex); err != nil {
		t.Fatal(err)
	}
	sandbox, err := gitStageSandbox(layout, workspace, codex, self)
	if err != nil {
		t.Fatal(err)
	}
	confined := func(command ...string) error {
		cmd := exec.Command(codex, append(sandbox, command...)...) // #nosec G204 -- the test's own fixture paths.
		cmd.Dir = workspace
		if data, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, data)
		}
		return nil
	}
	if err := confined("/usr/bin/touch", filepath.Join(workspace, "written")); err != nil {
		t.Fatalf("the sandbox could not write its own workspace, so the probes below show nothing: %v", err)
	}
	if err := confined("/bin/ls", filepath.Join(main, ".git", "refs")); err != nil {
		t.Fatalf("the sandbox could not read the repository's refs, so the read probe below shows nothing: %v", err)
	}
	for _, target := range []string{
		filepath.Join(task, "probe"),
		filepath.Join(main, ".git", "probe"),
		filepath.Join(main, ".git", "objects", "probe"),
		filepath.Join(main, ".git", "worktrees", "task", "probe"),
		filepath.Join(main, ".git", "refs", "heads", "probe"),
	} {
		err := confined("/usr/bin/touch", target)
		if _, statErr := os.Lstat(target); err == nil || statErr == nil {
			t.Errorf("a sandboxed command wrote %s (%v)", target, err)
		}
	}
	if err := confined("/bin/ls", filepath.Join(main, ".git", "objects")); err == nil {
		t.Error("a sandboxed command read the shared objects")
	}
}

// readOnlyDir is a new directory no one may create files in, as Codex's
// sandbox makes the repository for git_stage's probe.
func readOnlyDir(t *testing.T) string {
	t.Helper()
	dir := realTempDir(t)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir
}

func TestGitStageProbe(t *testing.T) {
	probe := func(args ...string) (int, string, string) {
		var out, stderr bytes.Buffer
		code := run("git-stage-probe", args, strings.NewReader(""), &out, &stderr)
		return code, out.String(), stderr.String()
	}
	t.Run("reports confined when only its workspace takes writes", func(t *testing.T) {
		workspace := realTempDir(t)
		code, out, stderr := probe(workspace, readOnlyDir(t), readOnlyDir(t))
		if code != 0 || out != "confined\n" {
			t.Fatalf("exit %d, %q: %s", code, out, stderr)
		}
		if left, _ := os.ReadDir(workspace); len(left) != 0 {
			t.Fatalf("probe left %v in its workspace", left)
		}
	})
	t.Run("refuses a target it can write, and removes what it wrote", func(t *testing.T) {
		writable := realTempDir(t)
		code, out, stderr := probe(realTempDir(t), readOnlyDir(t), writable)
		if code == 0 || out != "" || !strings.Contains(stderr, "lets git_stage's child write "+writable) {
			t.Fatalf("exit %d, %q: %s", code, out, stderr)
		}
		if left, _ := os.ReadDir(writable); len(left) != 0 {
			t.Fatalf("probe left %v in %s", left, writable)
		}
	})
	t.Run("refuses when it cannot write its workspace", func(t *testing.T) {
		code, out, stderr := probe(readOnlyDir(t), readOnlyDir(t))
		if code == 0 || out != "" || !strings.Contains(stderr, "workspace") {
			t.Fatalf("exit %d, %q: %s", code, out, stderr)
		}
	})
	t.Run("refuses a target that does not exist", func(t *testing.T) {
		missing := filepath.Join(realTempDir(t), "missing")
		code, out, stderr := probe(realTempDir(t), missing)
		if code == 0 || out != "" || !strings.Contains(stderr, missing) {
			t.Fatalf("exit %d, %q: %s", code, out, stderr)
		}
	})
}

// TestGitStageSandbox checks the profile git_stage's child runs in, as
// Codex is given it; TestGitStageNative checks that Codex enforces it.
func TestGitStageSandbox(t *testing.T) {
	layout := gitstage.Layout{Worktree: "/w/task", GitDir: "/r/.git/worktrees/task", CommonDir: "/r/.git"}
	args, err := gitStageSandbox(layout, "/p/private", "/bin/codex", "/bin/pr-loop")
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(args, " ")
	for _, want := range []string{
		"sandbox ", " -P git-stage ", " -C /p/private --",
		`":workspace_roots"={"."="write"}`,
		`"/w/task"="read"`, `"/r/.git/worktrees/task"="read"`,
		`"/r/.git/HEAD"="read"`, `"/r/.git/config"="read"`, `"/r/.git/refs"="read"`,
		`"/r/.git/packed-refs"="read"`, `"/r/.git/info"="read"`,
		`"/bin/codex"="read"`, `"/bin/pr-loop"="read"`, `"/private/tmp"="deny"`,
		// Denied, not only left out, in case a read above holds it.
		`"/r/.git/objects"="deny"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("profile lacks %s:\n%s", want, line)
		}
	}
	if n := strings.Count(line, `="write"`); n != 1 {
		t.Errorf("profile grants %d writes, want only its workspace:\n%s", n, line)
	}
	for _, never := range []string{`"/r/.git/objects"="read"`, `"/r/.git"=`, `"/r"=`} {
		if strings.Contains(line, never) {
			t.Errorf("profile names %s:\n%s", never, line)
		}
	}
	if args[len(args)-1] != "--" {
		t.Errorf("profile does not end its options: %q", args[len(args)-1])
	}
	// A TOML table holds each key once, or Codex cannot load the profile.
	t.Setenv("TMPDIR", "/tmp")
	if args, err = gitStageSandbox(layout, "/p/private", "/bin/codex", "/bin/pr-loop"); err != nil {
		t.Fatal(err)
	}
	if line = strings.Join(args, " "); strings.Count(line, `"/tmp"=`) != 1 {
		t.Errorf("profile names /tmp more than once:\n%s", line)
	}
	if _, err := gitStageSandbox(layout, "/p/private", "/tmp", "/bin/pr-loop"); err == nil {
		t.Error("a path both read and denied reached Codex's configuration")
	}
	layout.Worktree = "/w/task\n"
	if _, err := gitStageSandbox(layout, "/p/private", "/bin/codex", "/bin/pr-loop"); err == nil {
		t.Error("a path with a control character reached Codex's configuration")
	}
}

func TestGitStageChild(t *testing.T) {
	t.Run("refuses a sparse index", func(t *testing.T) {
		f := newMCPFixture(t)
		for _, dir := range []string{"a", "b"} {
			if err := os.Mkdir(filepath.Join(f.repo, dir), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.repo, dir, "file"), []byte(dir), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{
			{"add", "--", "a/file", "b/file"},
			{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "fixture"},
			{"sparse-checkout", "init", "--cone", "--sparse-index"},
			{"sparse-checkout", "set", "a"},
		} {
			cmd := exec.Command("git", append([]string{"-C", f.repo}, args...)...)
			cmd.Env = f.env
			if data, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("prepare sparse index: %s: %v", data, err)
			}
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", privateStageArgs(t, f.repo, "a/file"), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "sparse index") {
			t.Fatalf("stage with sparse index: exit %d: %s", code, stderr.String())
		}
	})
	t.Run("refuses a split index", func(t *testing.T) {
		f := newMCPFixture(t)
		cmd := exec.Command("git", "-C", f.repo, "update-index", "--split-index")
		cmd.Env = f.env
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("enable split index: %s: %v", data, err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", privateStageArgs(t, f.repo, "code.txt"), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "split index") {
			t.Fatalf("stage with split index: exit %d: %s", code, stderr.String())
		}
	})
	t.Run("refuses automatic line ending conversion", func(t *testing.T) {
		f := newMCPFixture(t)
		cmd := exec.Command("git", "-C", f.repo, "config", "core.autocrlf", "true")
		cmd.Env = f.env
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("configure conversion: %s: %v", data, err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", privateStageArgs(t, f.repo, "code.txt"), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "conversion") {
			t.Fatalf("stage with automatic conversion: exit %d: %s", code, stderr.String())
		}
	})
	t.Run("does not honor inherited Git trace output", func(t *testing.T) {
		f := newMCPFixture(t)
		args := privateStageArgs(t, f.repo, "code.txt")
		marker := filepath.Join(t.TempDir(), "trace")
		t.Setenv("GIT_TRACE", marker)
		var out, stderr bytes.Buffer
		if code := run("git-stage-child", args, strings.NewReader(""), &out, &stderr); code != 0 {
			t.Fatalf("stage: exit %d: %s", code, stderr.String())
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("inherited Git trace wrote a file: %v", err)
		}
	})
	t.Run("refuses a deleted directory path", func(t *testing.T) {
		f := newMCPFixture(t)
		dir := filepath.Join(f.repo, "dir")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"a", "b"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("git", "-C", f.repo, "add", "--", "dir/a", "dir/b")
		cmd.Env = f.env
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("prepare index: %s: %v", data, err)
		}
		for _, name := range []string{"a", "b"} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", privateStageArgs(t, f.repo, "dir"), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "file") {
			t.Fatalf("stage deleted directory: exit %d: %s", code, stderr.String())
		}
	})
	t.Run("refuses a text conversion attribute", func(t *testing.T) {
		f := newMCPFixture(t)
		if err := os.WriteFile(filepath.Join(f.repo, ".gitattributes"), []byte("code.txt text\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", privateStageArgs(t, f.repo, "code.txt"), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "attributes") {
			t.Fatalf("stage with text conversion: exit %d: %s", code, stderr.String())
		}
	})
	t.Run("refuses a private index hardlinked to the real index", func(t *testing.T) {
		f := newMCPFixture(t)
		args := privateStageArgs(t, f.repo, "code.txt")
		if err := os.Remove(args[1]); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(filepath.Join(f.repo, ".git", "index"), args[1]); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", args, strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "aliases the real index") {
			t.Fatalf("stage with hardlinked index: exit %d: %s", code, stderr.String())
		}
	})
	t.Run("stages privately without changing the real index", func(t *testing.T) {
		f := newMCPFixture(t)
		private := realTempDir(t)
		index := filepath.Join(private, "index")
		objects := filepath.Join(private, "objects")
		data, err := os.ReadFile(filepath.Join(f.repo, ".git", "index"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(index, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(objects, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(f.repo, "code.txt"), []byte("private change\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", []string{f.repo, index, objects, "code.txt"}, strings.NewReader(""), &out, &stderr)
		if code != 0 {
			t.Fatalf("private stage: exit %d: %s", code, stderr.String())
		}
		captured, err := os.ReadFile(filepath.Join(private, "inputs", "0"))
		if err != nil || string(captured) != "private change\n" {
			t.Fatalf("private staged input %q: %v", captured, err)
		}
		cmd := exec.Command("git", "-C", f.repo, "show", ":code.txt")
		// The private objects alone hold the staged blob.
		cmd.Env = append(f.env, "GIT_INDEX_FILE="+index, "GIT_OBJECT_DIRECTORY="+objects)
		data, err = cmd.Output()
		if err != nil || string(data) != "private change\n" {
			t.Fatalf("private index content %q: %v", data, err)
		}
		cmd = exec.Command("git", "-C", f.repo, "show", ":code.txt")
		cmd.Env = f.env
		data, err = cmd.Output()
		if err != nil || string(data) != "a\nb\nc\n" {
			t.Fatalf("real index changed: %q: %v", data, err)
		}
	})
	t.Run("stages a deleted tracked file", func(t *testing.T) {
		f := newMCPFixture(t)
		if err := os.Remove(filepath.Join(f.repo, "code.txt")); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		args := privateStageArgs(t, f.repo, "code.txt")
		if code := run("git-stage-child", args, strings.NewReader(""), &out, &stderr); code != 0 {
			t.Fatalf("stage deletion: exit %d: %s", code, stderr.String())
		}
		cmd := exec.Command("git", "-C", f.repo, "ls-files", "--", "code.txt")
		cmd.Env = append(f.env, "GIT_INDEX_FILE="+args[1], "GIT_OBJECT_DIRECTORY="+args[2])
		data, err := cmd.Output()
		if err != nil || len(data) != 0 {
			t.Fatalf("deleted file remains in index: %q: %v", data, err)
		}
	})
	t.Run("refuses malformed repository configuration", func(t *testing.T) {
		f := newMCPFixture(t)
		if err := os.WriteFile(filepath.Join(f.repo, ".git", "config"), []byte("[invalid\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", privateStageArgs(t, f.repo, "code.txt"), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "refused unchecked") {
			t.Fatalf("stage with malformed config: exit %d: %s", code, stderr.String())
		}
	})
	t.Run("refuses a filter attribute without a configured driver", func(t *testing.T) {
		f := newMCPFixture(t)
		if err := os.WriteFile(filepath.Join(f.repo, ".gitattributes"), []byte("code.txt filter=missing\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", privateStageArgs(t, f.repo, "code.txt"), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "filters") {
			t.Fatalf("stage with filter attribute: exit %d: %s", code, stderr.String())
		}
	})
	t.Run("refuses a path through an outside symlink", func(t *testing.T) {
		f := newMCPFixture(t)
		outside := t.TempDir()
		if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(f.repo, "escape")); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		if code := run("git-stage-child", privateStageArgs(t, f.repo, "escape/secret"), strings.NewReader(""), &out, &stderr); code == 0 {
			t.Fatalf("staging through an outside symlink succeeded: %s", out.String())
		}
		cmd := exec.Command("git", "-C", f.repo, "ls-files", "--", "escape/secret")
		cmd.Env = f.env
		data, err := cmd.Output()
		if err != nil || len(data) != 0 {
			t.Fatalf("outside path in index: %q: %v", data, err)
		}
	})
	t.Run("refuses a repository clean filter", func(t *testing.T) {
		f := newMCPFixture(t)
		marker := filepath.Join(t.TempDir(), "filter-ran")
		cmd := exec.Command("git", "-C", f.repo, "config", "filter.evil.clean", "touch "+marker)
		cmd.Env = f.env
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("configure filter: %s: %v", data, err)
		}
		if err := os.WriteFile(filepath.Join(f.repo, ".gitattributes"), []byte("code.txt filter=evil\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		code := run("git-stage-child", privateStageArgs(t, f.repo, "code.txt"), strings.NewReader(""), &out, &stderr)
		if code == 0 || !strings.Contains(stderr.String(), "filter.evil.clean") {
			t.Fatalf("stage with filter: exit %d: %s", code, stderr.String())
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("filter ran or marker could not be checked: %v", err)
		}
	})
}
