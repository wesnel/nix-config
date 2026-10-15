package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// fakeCodex stands in for `codex`: it accepts only the flags the real
// `codex exec --help` and `codex sandbox --help` list that fresh-review is
// meant to pass, records them one to a line and where it ran, and replies as
// $FAKE_CODEX says.
const fakeCodex = `#!/bin/sh
if [ "$1" = login ] && [ "$2" = status ]; then
  if [ "$FAKE_CODEX" = unauthenticated ]; then echo "Not logged in"; exit 1; fi
  echo "Logged in"; exit 0
fi
if [ "$1" = sandbox ]; then
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
      -c|-P|--disable) printf '%s\n%s\n' "$1" "$2" >> "$FAKE_CODEX_LOG/sandbox-args"; shift 2 ;;
      -C) printf '%s\n%s\n' "$1" "$2" >> "$FAKE_CODEX_LOG/sandbox-args"; cd "$2" || exit 1; shift 2 ;;
      --) shift; break ;;
      *) echo "fake codex: unknown sandbox flag $1" >&2; exit 64 ;;
    esac
  done
  if [ "$FAKE_CODEX" = nosandbox ]; then echo "sandbox-exec: sandbox_apply: Operation not permitted" >&2; exit 71; fi
  exec "$@"
fi
if [ "$1" != exec ]; then echo "fake codex: unexpected $*" >&2; exit 64; fi
shift
wt= schema= out= stdin=
while [ $# -gt 0 ]; do
  case "$1" in
    -c|--disable) printf '%s\n%s\n' "$1" "$2" >> "$FAKE_CODEX_LOG/exec-args"; shift 2 ;;
    --ignore-user-config|--ignore-rules) echo "$1" >> "$FAKE_CODEX_LOG/exec-args"; shift ;;
    --ephemeral) shift ;;
    -C) wt=$2; shift 2 ;;
    --output-schema) schema=$2; shift 2 ;;
    -o) out=$2; shift 2 ;;
    -m) shift 2 ;;
    -) stdin=1; shift ;;
    *) echo "fake codex: unknown flag $1" >&2; exit 64 ;;
  esac
done
if [ -z "$wt" ] || [ ! -s "$schema" ] || [ -z "$out" ] || [ -z "$stdin" ]; then
  echo "fake codex: missing -C, --output-schema, -o or -" >&2; exit 64
fi
printf 'OpenAI Codex v0\n--------\nworkdir: %s\nmodel: fake-model\nsandbox: read-only\n--------\n' "$wt" >&2
cat > "$FAKE_CODEX_LOG/prompt"
echo "$wt" > "$FAKE_CODEX_LOG/worktree"
printf '%s' "$GIT_DIR$GIT_WORK_TREE" > "$FAKE_CODEX_LOG/location"
printf '%s' "$PATH" > "$FAKE_CODEX_LOG/path"
git -C "$wt" rev-parse HEAD > "$FAKE_CODEX_LOG/head"
ls -A "$wt" > "$FAKE_CODEX_LOG/files"
case "$FAKE_CODEX" in
  fail) echo "fake codex: model error" >&2; exit 1 ;;
  hang) exec sleep 60 ;;
  fork) echo $$ > "$FAKE_CODEX_LOG/group"; i=0; while [ $i -lt 300 ]; do sleep 30 > /dev/null 2>&1 & i=$((i+1)); if [ $i = 20 ]; then echo > "$FAKE_CODEX_LOG/started"; fi; done; wait ;;
  none) echo '{"findings": [], "unverified": []}' > "$out" ;;
  answer) echo '{"answer": "Extend the seam.", "unverified": ["the callers"]}' > "$out" ;;
  *) cat > "$out" <<EOF
{"findings": [
 {"id": "1", "path": "$wt/code.txt", "line": 2, "endLine": 9, "kind": "bug", "claim": "c", "evidence": "e", "about": null, "reopens": null},
 {"id": "2", "path": "gone.txt", "line": 1, "endLine": null, "kind": "bug", "claim": "c", "evidence": "e", "about": null, "reopens": null},
 {"id": "3", "path": "code.txt", "line": 40, "endLine": null, "kind": "regression", "claim": "c", "evidence": "e", "about": "T1", "reopens": null},
 {"id": "4", "path": "code.txt", "line": 1, "endLine": null, "kind": "style", "claim": "c", "evidence": "e", "about": null, "reopens": null}
], "unverified": ["the tests need a network"]}
EOF
  ;;
esac
`

type fixture struct {
	repo, log, base, tip string
}

func run(t *testing.T, dir string, name string, args ...string) string {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setUp makes a repository with a two-commit change, an uncommitted notes
// file beside it, and a PATH whose only codex is the fake, if withCodex.
func setUp(t *testing.T, withCodex bool) *fixture {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "T", "GIT_AUTHOR_EMAIL": "t@e", "GIT_COMMITTER_NAME": "T", "GIT_COMMITTER_EMAIL": "t@e",
		"PR_LOOP_SESSION": "", "SANDBOX_RELAYED": "", "CODEX_SANDBOX": "", "FAKE_CODEX": "",
		"AGENT_TOOLS_CONFIG": filepath.Join(t.TempDir(), "none.json"),
		// A review's receipt goes to the plan of the session running it.
		"CLAUDE_CODE_SESSION_ID": "",
	} {
		t.Setenv(k, v)
	}
	f := &fixture{repo: t.TempDir(), log: t.TempDir()}
	t.Setenv("FAKE_CODEX_LOG", f.log)
	run(t, f.repo, "git", "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(f.repo, "code.txt"), []byte("a\n"), 0o644)
	run(t, f.repo, "git", "add", "code.txt")
	run(t, f.repo, "git", "commit", "-q", "-m", "Start")
	f.base = run(t, f.repo, "git", "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(f.repo, "code.txt"), []byte("a\nb\nc\n"), 0o644)
	run(t, f.repo, "git", "commit", "-q", "-am", "Grow the code")
	f.tip = run(t, f.repo, "git", "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(f.repo, "notes.md"), []byte("the author's reasoning\n"), 0o644)
	os.WriteFile(filepath.Join(f.repo, ".git", "goal.md"), []byte("Grow the code to three lines.\n"), 0o644)

	bin := t.TempDir()
	if withCodex {
		os.WriteFile(filepath.Join(bin, "codex"), []byte(fakeCodex), 0o755)
	}
	path := []string{bin}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, "codex")); err != nil {
			path = append(path, dir)
		}
	}
	t.Setenv("PATH", strings.Join(path, string(os.PathListSeparator)))
	t.Chdir(f.repo)
	return f
}

type output struct {
	Reviewer   string           `json:"reviewer"`
	Model      *string          `json:"model"`
	Range      string           `json:"range"`
	Tip        string           `json:"tip"`
	Findings   []map[string]any `json:"findings"`
	Dropped    []map[string]any `json:"dropped"`
	Unverified []string         `json:"unverified"`
}

func freshReview(t *testing.T, args ...string) (int, output, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Main(t.Context(), args, strings.NewReader(""), &stdout, &stderr)
	var out output
	if stdout.Len() > 0 {
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
		}
	}
	return code, out, stderr.String()
}

// checkCleanedUp holds the run to leaving no worktree behind.
func (f *fixture) checkCleanedUp(t *testing.T) {
	t.Helper()
	if wt, err := os.ReadFile(filepath.Join(f.log, "worktree")); err == nil {
		if _, err := os.Stat(strings.TrimSpace(string(wt))); err == nil {
			t.Errorf("the review's worktree %s is still there", wt)
		}
	}
	if list := run(t, f.repo, "git", "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Errorf("worktrees left behind:\n%s", list)
	}
	if left, _ := os.ReadDir(filepath.Join(f.repo, ".git", "fresh-review")); len(left) != 0 {
		t.Errorf("files left under .git/fresh-review: %v", left)
	}
}

func TestCodex(t *testing.T) {
	t.Run("review", testCodexReview)
	t.Run("GIT_DIR naming the repository under review", testCodexGitDirUnderReview)
	t.Run("GIT_DIR naming another repository", testCodexGitDirElsewhere)
	t.Run("no findings", testCodexNoFindings)
	t.Run("confinement", testCodexConfinement)
	t.Run("failure", testCodexFailures)
}

func testCodexReview(t *testing.T) {
	f := setUp(t, true)
	code, out, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+"..HEAD")
	if code != 3 {
		t.Fatalf("exit %d, want 3 for findings\n%s", code, stderr)
	}
	if out.Reviewer != "codex" || out.Tip != f.tip || out.Range != f.base+"..HEAD" {
		t.Errorf("reviewer %q, tip %q, range %q", out.Reviewer, out.Tip, out.Range)
	}
	if out.Model == nil || *out.Model != "fake-model" {
		t.Errorf("model %v, want the one Codex reported", out.Model)
	}
	if len(out.Findings) != 1 || out.Findings[0]["path"] != "code.txt" || out.Findings[0]["line"] != 2.0 {
		t.Fatalf("findings: %v", out.Findings)
	}
	if _, ok := out.Findings[0]["endLine"]; ok {
		t.Errorf("an endLine past the end of the file was kept: %v", out.Findings[0])
	}
	if len(out.Dropped) != 3 {
		t.Errorf("dropped: %v", out.Dropped)
	}
	for _, d := range out.Dropped {
		if d["reason"] == "" || d["finding"] == nil {
			t.Errorf("a dropped finding without its reason: %v", d)
		}
	}
	if len(out.Unverified) != 1 || out.Unverified[0] != "the tests need a network" {
		t.Errorf("unverified: %v", out.Unverified)
	}
	head, _ := os.ReadFile(filepath.Join(f.log, "head"))
	if strings.TrimSpace(string(head)) != f.tip {
		t.Errorf("Codex ran at %s, not TIP %s", head, f.tip)
	}
	files, _ := os.ReadFile(filepath.Join(f.log, "files"))
	if strings.Contains(string(files), "notes.md") {
		t.Errorf("Codex could see the uncommitted notes: %s", files)
	}
	prompt, _ := os.ReadFile(filepath.Join(f.log, "prompt"))
	if !strings.Contains(string(prompt), "Grow the code to three lines.") || !strings.HasPrefix(string(prompt), Contract) {
		t.Errorf("Codex was not given the contract and the goal:\n%s", prompt)
	}
	f.checkCleanedUp(t)
}

// GIT_DIR naming the repository under review, as git sets it for a hook,
// reaches neither the review's worktree nor Codex.
func testCodexGitDirUnderReview(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	t.Setenv("GIT_DIR", filepath.Join(f.repo, ".git"))
	if code, _, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+"..HEAD"); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if location, _ := os.ReadFile(filepath.Join(f.log, "location")); len(location) != 0 {
		t.Errorf("Codex inherited %s", location)
	}
	f.checkCleanedUp(t)
}

// GIT_DIR naming another repository is refused.
func testCodexGitDirElsewhere(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	gitenvtest.Decoy(t)
	os.Unsetenv("GIT_WORK_TREE")
	if code, _, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+"..HEAD"); code != 2 || !strings.Contains(stderr, "GIT_DIR") {
		t.Errorf("GIT_DIR naming another repository: exit %d\n%s", code, stderr)
	}
}

func testCodexNoFindings(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	if code, out, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+".."+f.tip); code != 0 || len(out.Findings) != 0 {
		t.Errorf("exit %d, findings %v\n%s", code, out.Findings, stderr)
	}
	f.checkCleanedUp(t)
}

// testCodexConfinement holds Codex to reading the worktree under review and
// the parts of the repository's git directory git needs to show the change,
// with nothing of the user's Codex configuration, and checks that its
// sandbox starts before it reviews.
func testCodexConfinement(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	tools := t.TempDir()
	link := filepath.Join(t.TempDir(), "tools")
	if err := os.Symlink(tools, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+link)
	if code, _, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+"..HEAD"); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	path := filepath.SplitList(string(must(os.ReadFile(filepath.Join(f.log, "path")))))
	if slices.Contains(path, link) || !slices.Contains(path, must(filepath.EvalSymlinks(tools))) {
		t.Errorf("Codex's PATH names a directory by a symbolic link the sandbox cannot follow: %q", path)
	}
	read := func(name string) []string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(f.log, name))
		if err != nil {
			t.Fatalf("codex was not run with the arguments in %s: %v", name, err)
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	execArgs, sandboxArgs := read("exec-args"), read("sandbox-args")
	has := func(args []string, want ...string) bool {
		for i := range args {
			if i+len(want) <= len(args) && slices.Equal(args[i:i+len(want)], want) {
				return true
			}
		}
		return false
	}
	for _, want := range [][]string{
		{"--ignore-user-config"}, {"--ignore-rules"},
		{"-c", `default_permissions="fresh-review"`},
		{"-c", `web_search="disabled"`},
		{"-c", `shell_environment_policy.inherit="core"`},
		{"--disable", "plugins"}, {"--disable", "apps"}, {"--disable", "hooks"},
		{"--disable", "view_image"}, {"--disable", "multi_agent"}, {"--disable", "browser_use"},
	} {
		if !has(execArgs, want...) {
			t.Errorf("codex exec was not passed %q: %q", want, execArgs)
		}
	}

	var filesystem string
	for _, arg := range execArgs {
		if s, ok := strings.CutPrefix(arg, "permissions.fresh-review.filesystem="); ok {
			filesystem = s
		}
	}
	if filesystem == "" {
		t.Fatalf("no filesystem permissions in %q", execArgs)
	}
	wt := strings.TrimSpace(string(must(os.ReadFile(filepath.Join(f.log, "worktree")))))
	if !has(sandboxArgs, "-c", "permissions.fresh-review.filesystem="+filesystem) ||
		!has(sandboxArgs, "-P", "fresh-review") || !has(sandboxArgs, "-C", wt) {
		t.Errorf("the sandbox was not tried with the review's permissions in %s: %q", wt, sandboxArgs)
	}
	common := run(t, f.repo, "git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	codex := must(filepath.EvalSymlinks(must(exec.LookPath("codex"))))
	entries := regexp.MustCompile(`"([^"]+)"=("[a-z]+"|\{[^}]*\})`).FindAllStringSubmatch(filesystem, -1)
	readable := map[string]bool{}
	for _, e := range entries {
		path, access := e[1], e[2]
		switch {
		case access == `"deny"`:
		case path == ":workspace_roots" && access == `{"."="read"}`:
		case access == `"read"` && (path == ":minimal" || path == "/nix/store" || path == codex || strings.HasPrefix(path, common+"/")):
			readable[strings.TrimPrefix(path, common+"/")] = true
		default:
			t.Errorf("Codex may reach %s as %s: %s", path, access, filesystem)
		}
	}
	for _, want := range []string{":minimal", codex, "objects", "refs", "HEAD", "config"} {
		if !readable[want] {
			t.Errorf("Codex may not read %s: %s", want, filesystem)
		}
	}
	if !strings.Contains(filesystem, `":workspace_roots"={"."="read"}`) {
		t.Errorf("Codex may not read the worktree: %s", filesystem)
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func testCodexFailures(t *testing.T) {
	for _, c := range []struct {
		mode string
		args []string
	}{
		{"fail", nil},
		{"hang", []string{"--timeout", "1"}},
	} {
		t.Run(c.mode, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv("FAKE_CODEX", c.mode)
			args := append(append([]string{"--goal", ".git/goal.md"}, c.args...), f.base+"..HEAD")
			if code, _, stderr := freshReview(t, args...); code != 1 {
				t.Errorf("exit %d, want 1\n%s", code, stderr)
			}
			f.checkCleanedUp(t)
		})
	}
}

// TestUnavailable covers each case where Codex must not or cannot review,
// which exits 4 and says how to review with a sub-agent instead.
func TestUnavailable(t *testing.T) {
	for _, c := range []struct {
		name      string
		withCodex bool
		env       map[string]string
		args      []string
		want      int
	}{
		{"Codex's sandbox cannot start", true, map[string]string{"FAKE_CODEX": "nosandbox"}, nil, 4},
		{"missing", false, nil, nil, 4},
		{"unauthenticated", true, map[string]string{"FAKE_CODEX": "unauthenticated"}, nil, 4},
		{"in a loop session", true, map[string]string{"PR_LOOP_SESSION": "1"}, nil, 4},
		{"behind the relay", true, map[string]string{"SANDBOX_RELAYED": "1"}, nil, 4},
		{"loop session, Codex asked for", true, map[string]string{"PR_LOOP_SESSION": "1"}, []string{"--reviewer", "codex"}, 4},
		{"Codex author", true, map[string]string{"CODEX_SANDBOX": "seatbelt"}, nil, 4},
		{"Codex author, Codex asked for", true, map[string]string{"CODEX_SANDBOX": "seatbelt", "FAKE_CODEX": "none"}, []string{"--reviewer", "codex"}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := setUp(t, c.withCodex)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			args := append(append([]string{"--goal", ".git/goal.md"}, c.args...), f.base+"..HEAD")
			code, _, stderr := freshReview(t, args...)
			if code != c.want {
				t.Fatalf("exit %d, want %d\n%s", code, c.want, stderr)
			}
			if c.want == 4 {
				reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: ")
				if !strings.Contains(reason, "--print-prompt") || !refusal.SaysInstead(reason) {
					t.Errorf("does not say how to review instead: %q", stderr)
				}
				if _, err := os.Stat(filepath.Join(f.log, "worktree")); err == nil {
					t.Error("Codex ran")
				}
				f.checkCleanedUp(t)
			}
		})
	}
}

func TestRedCheck(t *testing.T) {
	t.Run("never on the host", testRedCheckNotOnHost)
	t.Run("in a VM", testRedCheckInVM)
	t.Run("VM failure", testRedCheckVMFailure)
}

// testRedCheckNotOnHost holds fresh-review to never running the
// repository's test command where it runs, whatever the settings say: the
// commits under review may be anyone's, and red-check runs them.
func testRedCheckNotOnHost(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"print-prompt", []string{"--print-prompt"}},
		{"codex", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv("FAKE_CODEX", "none")
			marker := filepath.Join(t.TempDir(), "ran")
			config := filepath.Join(t.TempDir(), "config.json")
			os.WriteFile(config, []byte(`{"repos": {"o/r": {"test": "touch `+marker+`; false", "build": "touch `+marker+`", `+
				`"testFiles": ["*_test.txt"]}}}`), 0o644)
			t.Setenv("AGENT_TOOLS_CONFIG", config)
			run(t, f.repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
			os.WriteFile(filepath.Join(f.repo, "code.txt"), []byte("a\nb\nc\nd\n"), 0o644)
			os.WriteFile(filepath.Join(f.repo, "code_test.txt"), []byte("d\n"), 0o644)
			run(t, f.repo, "git", "add", "code.txt", "code_test.txt")
			run(t, f.repo, "git", "commit", "-q", "-m", "Test the code")

			var stdout, stderr bytes.Buffer
			args := append(append([]string{}, c.args...), "--goal", ".git/goal.md", f.base+"..HEAD")
			if code := Main(t.Context(), args, nil, &stdout, &stderr); code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr.String())
			}
			if _, err := os.Stat(marker); err == nil {
				t.Error("the repository's test command ran on the host")
			}
			prompt := stdout.String()
			if c.args == nil {
				prompt = string(must(os.ReadFile(filepath.Join(f.log, "prompt"))))
			}
			if strings.Contains(strings.TrimPrefix(prompt, Contract), "red-check") {
				t.Errorf("the prompt has a red-check section:\n%s", prompt)
			}
			if !strings.Contains(stderr.String(), "red-check runs only in the review sandbox") {
				t.Errorf("stderr does not say red-check was skipped: %q", stderr.String())
			}
		})
	}
}

// configuredForRedCheck makes a repository, as setUp does, whose settings
// give it a test command, and runs red-check with vm for the rest of the
// test. vm stands in for the review VM, which the test cannot start.
func configuredForRedCheck(t *testing.T, vm func(ctx context.Context, top, base, tip string, log io.Writer) (string, error)) *fixture {
	t.Helper()
	f := setUp(t, false)
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"repos": {"o/r": {"test": "true", "testFiles": ["*_test.txt"]}}}`), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	run(t, f.repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
	old := RedCheckInVM
	t.Cleanup(func() { RedCheckInVM = old })
	RedCheckInVM = vm
	return f
}

// Where a VM can run red-check, its results go in the prompt, for the range
// under review.
func testRedCheckInVM(t *testing.T) {
	var got []string
	f := configuredForRedCheck(t, func(ctx context.Context, top, base, tip string, log io.Writer) (string, error) {
		got = []string{top, base, tip}
		return "red-check from the VM: fails-as-asserted", nil
	})
	var stdout, stderr bytes.Buffer
	if code := Main(t.Context(), []string{"--print-prompt", "--goal", ".git/goal.md", f.base + "..HEAD"}, nil,
		&stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	top := run(t, f.repo, "git", "rev-parse", "--show-toplevel")
	if !slices.Equal(got, []string{top, f.base, f.tip}) {
		t.Errorf("red-check ran for %q", got)
	}
	if !strings.Contains(stdout.String(), "## git red-check "+f.base+".."+f.tip+"\n\nred-check from the VM: fails-as-asserted") {
		t.Errorf("the prompt lacks the VM's red-check:\n%s", stdout.String())
	}
}

// Where a VM cannot run red-check, stderr says why.
func testRedCheckVMFailure(t *testing.T) {
	f := configuredForRedCheck(t, func(context.Context, string, string, string, io.Writer) (string, error) {
		return "", errors.New("no VM here")
	})
	var stdout, stderr bytes.Buffer
	Main(t.Context(), []string{"--print-prompt", "--goal", ".git/goal.md", f.base + "..HEAD"}, nil, &stdout, &stderr)
	if !strings.Contains(stderr.String(), "no VM here") || strings.Contains(strings.TrimPrefix(stdout.String(), Contract), "red-check") {
		t.Errorf("a failed VM run: %s\n%s", stderr.String(), stdout.String())
	}
}

func TestPrintPrompt(t *testing.T) {
	f := setUp(t, false)
	os.WriteFile(filepath.Join(f.repo, ".git", "items.md"), []byte("Thread T1: make it three lines\n"), 0o644)
	os.WriteFile(filepath.Join(f.repo, ".git", "prior.json"), []byte(`{"findings": [{"id": "7"}]}`), 0o644)

	var stdout, stderr bytes.Buffer
	args := []string{"--print-prompt", "--goal", ".git/goal.md", "--items", ".git/items.md", "--prior", ".git/prior.json", f.base + "..HEAD"}
	if code := Main(t.Context(), args, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	prompt := stdout.String()
	for _, want := range []string{"Grow the code to three lines.", "Thread T1: make it three lines", `"id": "7"`,
		f.base, f.tip, "Grow the code", "git show " + f.tip + ":", `"tip": "` + f.tip + `"`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
	if !strings.HasPrefix(prompt, Contract) {
		t.Error("the prompt does not start with the reviewer's contract")
	}
}

// Asked for a sub-agent reviewer, fresh-review prints the prompt for one
// and leaves Codex alone, even where Codex could review.
func TestSubagentReviewer(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	var stdout, stderr bytes.Buffer
	if code := Main(t.Context(), []string{"--reviewer", "subagent", "--goal", ".git/goal.md", f.base + "..HEAD"}, nil,
		&stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	if prompt := stdout.String(); !strings.HasPrefix(prompt, Contract) || !strings.Contains(prompt, "git show "+f.tip+":") {
		t.Errorf("not the prompt for a sub-agent:\n%s", prompt)
	}
	if !strings.Contains(stderr.String(), "--check FILE "+f.base+"..HEAD") {
		t.Errorf("stderr does not say how to check the reply: %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(f.log, "worktree")); err == nil {
		t.Error("Codex ran")
	}
	f.checkCleanedUp(t)
}

// A review that ran, by Codex or checked from a sub-agent, leaves a
// receipt in the plan of the session or worktree: the range as given, its
// tip, and the patch ID of its change. A plan of another session and
// worktree gets none.
func TestReceipt(t *testing.T) {
	// planned gives the test a plans directory with a plan of session s1
	// for the worktree at top, and returns that plan.
	planned := func(t *testing.T, top string) plans.Plan {
		t.Helper()
		c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
		config := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(config, must(json.Marshal(map[string]any{"plans": c})), 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		made, err := c.New(plans.NewOptions{Title: "Grow the code", Slug: "grow", Worktrees: []string{top},
			Sessions: []string{"claude:s1"}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return must(c.Open(made.Plan))
	}
	receipts := func(t *testing.T, p plans.Plan) []string {
		t.Helper()
		var out []string
		for _, e := range must(p.Events()) {
			if e.Kind == "review-receipt" {
				out = append(out, strings.Join([]string{e.Range, e.Tip, e.PatchID, e.Agent, e.Model}, " "))
			}
		}
		return out
	}

	for _, c := range []struct {
		name, reviewer, model string
		args                  func(f *fixture) []string
	}{
		{"by Codex", "codex", "fake-model", func(f *fixture) []string { return []string{"--goal", ".git/goal.md", f.base + "..HEAD"} }},
		{"checked from a sub-agent", "subagent", "", func(f *fixture) []string {
			os.WriteFile(filepath.Join(f.repo, ".git", "reply.md"), []byte(`{"findings": [], "unverified": [], "tip": "`+f.tip+`"}`), 0o644)
			return []string{"--check", ".git/reply.md", f.base + "..HEAD"}
		}},
		// The receipt is for the tip the prompt was printed for, which the
		// reply carries, not for HEAD as it is when the reply is checked.
		{"checked after HEAD moved on", "subagent", "", func(f *fixture) []string {
			os.WriteFile(filepath.Join(f.repo, ".git", "reply.md"), []byte(`{"findings": [], "unverified": [], "tip": "`+f.tip+`"}`), 0o644)
			os.WriteFile(filepath.Join(f.repo, "later.txt"), []byte("later\n"), 0o644)
			run(t, f.repo, "git", "add", "later.txt")
			run(t, f.repo, "git", "commit", "-q", "-m", "Add a later change")
			return []string{"--check", ".git/reply.md", f.base + "..HEAD"}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv("FAKE_CODEX", "none")
			top := run(t, f.repo, "git", "rev-parse", "--show-toplevel")
			p := planned(t, top)
			// git's own pipeline for the change, as a person would type it.
			id, _, _ := strings.Cut(run(t, f.repo, "sh", "-c", "git diff "+f.base+"..."+f.tip+" | git patch-id --stable"), " ")
			if code, _, stderr := freshReview(t, c.args(f)...); code != 0 {
				t.Fatalf("exit %d\n%s", code, stderr)
			}
			want := strings.TrimSpace(strings.Join([]string{f.base + "..HEAD", f.tip, id, c.reviewer, c.model}, " "))
			if got := receipts(t, p); len(got) != 1 || strings.TrimSpace(got[0]) != want {
				t.Errorf("receipts %q, want %q", got, want)
			}
		})
	}

	t.Run("no plan of this session or worktree", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_CODEX", "none")
		p := planned(t, t.TempDir())
		t.Setenv("CLAUDE_CODE_SESSION_ID", "s2")
		code, _, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		if got := receipts(t, p); len(got) != 0 {
			t.Errorf("receipts in another's plan: %q", got)
		}
		if !strings.Contains(stderr, "no receipt") {
			t.Errorf("stderr does not say no receipt was written: %q", stderr)
		}
	})

	t.Run("in the session's plan, from another worktree", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_CODEX", "none")
		p := planned(t, t.TempDir())
		t.Setenv("CLAUDE_CODE_SESSION_ID", "s1")
		if code, _, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+"..HEAD"); code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		if got := receipts(t, p); len(got) != 1 {
			t.Errorf("receipts %q", got)
		}
	})
}

func TestCheck(t *testing.T) {
	// checkReply runs --check over a sub-agent's reply, in which TIP and
	// BASE stand for the tip and base of the range.
	checkReply := func(t *testing.T, reply string) (int, output, string) {
		t.Helper()
		f := setUp(t, false)
		path := filepath.Join(f.repo, ".git", "reply.md")
		os.WriteFile(path, []byte(strings.NewReplacer("TIP", f.tip, "BASE", f.base).Replace(reply)), 0o644)
		return freshReview(t, "--check", path, f.base+"..HEAD")
	}

	t.Run("findings", func(t *testing.T) {
		code, out, stderr := checkReply(t, "Here is my review.\n```json\n"+
			`{"findings": [{"id": "1", "path": "./code.txt", "line": 3, "kind": "untested", "claim": "c", "evidence": "e"}], "unverified": [], "tip": "TIP"}`+
			"\n```\n")
		if code != 3 || out.Reviewer != "subagent" || len(out.Findings) != 1 || out.Findings[0]["path"] != "code.txt" {
			t.Errorf("exit %d, %+v\n%s", code, out, stderr)
		}
	})

	t.Run("no findings", func(t *testing.T) {
		if code, _, stderr := checkReply(t, `{"findings": [], "unverified": [], "tip": "TIP"}`); code != 0 {
			t.Errorf("no findings: exit %d\n%s", code, stderr)
		}
	})

	t.Run("reply that is not JSON", func(t *testing.T) {
		if code, _, _ := checkReply(t, "Looks good to me."); code != 1 {
			t.Errorf("a reply that is not JSON: exit %d", code)
		}
	})

	// Which commit the reviewer read is in its reply, from the prompt.
	for name, reply := range map[string]string{
		"a reply without the prompt's tip": `{"findings": [], "unverified": []}`,
		"a tip that is no commit":          `{"findings": [], "unverified": [], "tip": "0123456789abcdef"}`,
		"the range's base as its tip":      `{"findings": [], "unverified": [], "tip": "BASE"}`,
	} {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := checkReply(t, reply)
			reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: refused: ")
			if code != 2 || !strings.Contains(reason, "--print-prompt") || !refusal.SaysInstead(reason) {
				t.Errorf("exit %d, %q", code, stderr)
			}
		})
	}
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct {
		name string
		// args returns the arguments, given the base of the change.
		args func(base string) []string
	}{
		{"no goal", func(base string) []string { return []string{base + "..HEAD"} }},
		{"empty goal", func(base string) []string { return []string{"--goal", ".git/empty.md", base + "..HEAD"} }},
		{"no range", func(string) []string { return []string{"--goal", ".git/goal.md", "HEAD"} }},
		{"empty range", func(string) []string { return []string{"--goal", ".git/goal.md", "HEAD..HEAD"} }},
		{"symmetric difference", func(string) []string { return []string{"--goal", ".git/goal.md", "HEAD...HEAD~1"} }},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := setUp(t, true)
			os.WriteFile(filepath.Join(f.repo, ".git", "empty.md"), []byte("\n"), 0o644)
			args := c.args(f.base)
			code, _, stderr := freshReview(t, args...)
			reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: refused: ")
			if code != 2 || !refusal.SaysInstead(reason) {
				t.Errorf("%v: exit %d, %q", args, code, stderr)
			}
			f.checkCleanedUp(t)
		})
	}
}
