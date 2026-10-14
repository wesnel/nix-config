package review

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/refusal"
)

// fakeCodex stands in for `codex`: it accepts only the flags the real
// `codex exec --help` lists that fresh-review is meant to pass, records
// where it ran, and replies as $FAKE_CODEX says.
const fakeCodex = `#!/bin/sh
if [ "$1" = login ] && [ "$2" = status ]; then
  if [ "$FAKE_CODEX" = unauthenticated ]; then echo "Not logged in"; exit 1; fi
  echo "Logged in"; exit 0
fi
if [ "$1" != exec ]; then echo "fake codex: unexpected $*" >&2; exit 64; fi
shift
wt= schema= out= stdin=
while [ $# -gt 0 ]; do
  case "$1" in
    -s) if [ "$2" != read-only ]; then echo "fake codex: sandbox $2" >&2; exit 64; fi; shift 2 ;;
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
git -C "$wt" rev-parse HEAD > "$FAKE_CODEX_LOG/head"
ls -A "$wt" > "$FAKE_CODEX_LOG/files"
case "$FAKE_CODEX" in
  fail) echo "fake codex: model error" >&2; exit 1 ;;
  hang) exec sleep 60 ;;
  none) echo '{"findings": [], "unverified": []}' > "$out" ;;
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
	code := Main(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
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

func TestCodexReview(t *testing.T) {
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

func TestNoFindings(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	if code, out, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+".."+f.tip); code != 0 || len(out.Findings) != 0 {
		t.Errorf("exit %d, findings %v\n%s", code, out.Findings, stderr)
	}
	f.checkCleanedUp(t)
}

func TestCodexFailures(t *testing.T) {
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
			}
		})
	}
}

func TestPrintPrompt(t *testing.T) {
	f := setUp(t, false)
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"repos": {"o/r": {"test": "true", "testFiles": ["*_test.txt"]}}}`), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	run(t, f.repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
	os.WriteFile(filepath.Join(f.repo, ".git", "items.md"), []byte("Thread T1: make it three lines\n"), 0o644)
	os.WriteFile(filepath.Join(f.repo, ".git", "prior.json"), []byte(`{"findings": [{"id": "7"}]}`), 0o644)

	var stdout, stderr bytes.Buffer
	args := []string{"--print-prompt", "--goal", ".git/goal.md", "--items", ".git/items.md", "--prior", ".git/prior.json", f.base + "..HEAD"}
	if code := Main(context.Background(), args, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	prompt := stdout.String()
	for _, want := range []string{"Grow the code to three lines.", "Thread T1: make it three lines", `"id": "7"`,
		f.base, f.tip, "Grow the code", "no-test-files", "git show " + f.tip + ":"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
	if !strings.HasPrefix(prompt, Contract) {
		t.Error("the prompt does not start with the reviewer's contract")
	}

	stdout.Reset()
	args = []string{"--print-prompt", "--no-red-check", "--goal", ".git/goal.md", f.base + "..HEAD"}
	if code := Main(context.Background(), args, nil, &stdout, &stderr); code != 0 || strings.Contains(stdout.String(), "no-test-files") {
		t.Errorf("--no-red-check: exit %d\n%s", code, stdout.String())
	}
}

func TestCheck(t *testing.T) {
	f := setUp(t, false)
	reply := filepath.Join(f.repo, ".git", "reply.md")
	os.WriteFile(reply, []byte("Here is my review.\n```json\n"+
		`{"findings": [{"id": "1", "path": "./code.txt", "line": 3, "kind": "untested", "claim": "c", "evidence": "e"}], "unverified": []}`+
		"\n```\n"), 0o644)
	code, out, stderr := freshReview(t, "--check", reply, f.base+"..HEAD")
	if code != 3 || out.Reviewer != "subagent" || len(out.Findings) != 1 || out.Findings[0]["path"] != "code.txt" {
		t.Errorf("exit %d, %+v\n%s", code, out, stderr)
	}

	os.WriteFile(reply, []byte(`{"findings": [], "unverified": []}`), 0o644)
	if code, _, stderr := freshReview(t, "--check", reply, f.base+"..HEAD"); code != 0 {
		t.Errorf("no findings: exit %d\n%s", code, stderr)
	}

	os.WriteFile(reply, []byte("Looks good to me."), 0o644)
	if code, _, _ := freshReview(t, "--check", reply, f.base+"..HEAD"); code != 1 {
		t.Errorf("a reply that is not JSON: exit %d", code)
	}
}

func TestRefusals(t *testing.T) {
	f := setUp(t, true)
	os.WriteFile(filepath.Join(f.repo, ".git", "empty.md"), []byte("\n"), 0o644)
	for _, args := range [][]string{
		{f.base + "..HEAD"},
		{"--goal", ".git/empty.md", f.base + "..HEAD"},
		{"--goal", ".git/goal.md", "HEAD"},
		{"--goal", ".git/goal.md", "HEAD..HEAD"},
		{"--goal", ".git/goal.md", "HEAD...HEAD~1"},
	} {
		code, _, stderr := freshReview(t, args...)
		reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: refused: ")
		if code != 2 || !refusal.SaysInstead(reason) {
			t.Errorf("%v: exit %d, %q", args, code, stderr)
		}
	}
	f.checkCleanedUp(t)
}
