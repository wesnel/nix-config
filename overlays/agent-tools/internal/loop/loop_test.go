package loop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
)

func TestIdentity(t *testing.T) {
	got, err := identity("Wesley Nelson <wgn@wgn.dev> 1790750341 -0700")
	if err != nil {
		t.Fatal(err)
	}
	// The date `git log --date=iso-strict` prints for this commit.
	if got["name"] != "Wesley Nelson" || got["email"] != "wgn@wgn.dev" || got["date"] != "2026-09-29T23:39:01-07:00" {
		t.Errorf("identity: %v", got)
	}
	if got, _ := identity("A <a@b> 0 +0530"); got["date"] != "1970-01-01T05:30:00+05:30" {
		t.Errorf("positive offset: %v", got)
	}
}

func TestVerdict(t *testing.T) {
	verdicts := map[string]string{"approval": "Approval recommended", "changes": "Changes recommended", "error": "encountered an error"}
	body := "<!-- ccr-overview-v2 -->\n## Copilot review overview\n### 🟡 Changes recommended\nApproval recommended once fixed."
	if v := verdictOf(verdicts, body); v != "changes" {
		t.Errorf("heading should win over prose: %s", v)
	}
	if v := verdictOf(verdicts, "Copilot encountered an error and was unable to review"); v != "error" {
		t.Errorf("body fallback: %s", v)
	}
	if v := verdictOf(verdicts, "nothing"); v != "unknown" {
		t.Errorf("unknown: %s", v)
	}
}

func TestStackOrder(t *testing.T) {
	prs := map[string]*PR{"a": {BaseRefName: "main"}, "b": {BaseRefName: "a"}, "c": {BaseRefName: "b"}, "x": {BaseRefName: "main"}}
	if got := strings.Join(stackOrder(prs, "b"), ","); got != "a,b,c" {
		t.Errorf("stack order: %s", got)
	}
}

func TestCheckFixups(t *testing.T) {
	c := func(message string) Commit { return Commit{Message: message, Author: "T <t@e>"} }
	a, b := c("A first"), c("B second")
	fix := c("fixup! A first\n\nReview-Thread: x")
	nested := c("fixup! fixup! B second")
	if err := checkFixups("br", []Commit{a, b}, []Commit{a, b, fix, nested}); err != nil {
		t.Errorf("fixups refused: %v", err)
	}
	if err := checkFixups("br", []Commit{a, b}, []Commit{a, b, c("stray")}); err == nil {
		t.Error("a plain commit was accepted")
	}
	if err := checkFixups("br", []Commit{a}, []Commit{a, c("fixup! elsewhere")}); err == nil {
		t.Error("a fixup of another branch's commit was accepted")
	}
	if err := checkFixups("br", []Commit{a, b}, []Commit{c("A reworded"), b}); err == nil {
		t.Error("a rewritten original was accepted")
	}
}

func TestCommitBody(t *testing.T) {
	raw := "tree abc\nparent p1\nauthor A <a@b> 1700000000 +0000\ncommitter C <c@d> 1700000001 -0100\n" +
		"gpgsig -----BEGIN PGP SIGNATURE-----\n \n line\n -----END PGP SIGNATURE-----\n\nSubject\n\nBody\n"
	body, err := commitBody("sha", []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if body["message"] != "Subject\n\nBody\n" || body["tree"] != "abc" {
		t.Errorf("body: %v", body)
	}
	if body["signature"] != "-----BEGIN PGP SIGNATURE-----\n\nline\n-----END PGP SIGNATURE-----" {
		t.Errorf("signature: %q", body["signature"])
	}
	if _, err := commitBody("sha", []byte("tree a\nmergetag x\n\nm")); err == nil {
		t.Error("an unreproducible header was accepted")
	}
}

// gitIn runs git in dir with a fixed identity and signing off.
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

func TestWorktrees(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, dir, "branch", "feature")
	gitIn(t, dir, "config", "commit.gpgsign", "true")
	git.Dir = dir
	defer func() { git.Dir = "" }()
	state := &State{Repo: "o/r", Order: []string{"feature"}, Branches: map[string]*Branch{"feature": {PR: 7, Base: "main"}}}
	if err := state.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := Worktrees(false); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".pr-loop", "7")
	// Read without gitIn, which forces signing off.
	read := func(where string) string {
		out, _ := exec.Command("git", "-C", where, "config", "commit.gpgsign").Output()
		return strings.TrimSpace(string(out))
	}
	if got := read(path); got != "false" {
		t.Errorf("worktree signs: %s", got)
	}
	if got := read(dir); got != "true" {
		t.Errorf("main worktree stopped signing: %s", got)
	}
	if got := gitIn(t, dir, "status", "--porcelain"); got != "" {
		t.Errorf(".pr-loop shows in the main worktree: %s", got)
	}
	if _, err := Worktrees(false); err != nil {
		t.Errorf("not idempotent: %v", err)
	}
	if _, err := Worktrees(true); err != nil || exists(path) {
		t.Errorf("remove: %v, still there: %v", err, exists(path))
	}
}

// TestRehearse builds a two-branch stack with fixups in both branches and
// checks the rehearsal accepts it, then that it refuses a stray commit and a
// fixup that conflicts.
func TestRehearse(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "T")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@e")
	t.Setenv("GIT_COMMITTER_NAME", "T")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@e")
	dir := t.TempDir()
	write := func(name, content string) { os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644) }
	gitIn(t, dir, "init", "-q", "-b", "main")
	write("f", "base\n")
	gitIn(t, dir, "add", "f")
	gitIn(t, dir, "commit", "-qm", "base")
	gitIn(t, dir, "switch", "-qc", "lower")
	write("a", "a\n")
	gitIn(t, dir, "add", "a")
	gitIn(t, dir, "commit", "-qm", "L1 add a")
	write("b", "b\n")
	gitIn(t, dir, "add", "b")
	gitIn(t, dir, "commit", "-qm", "L2 add b")
	gitIn(t, dir, "switch", "-qc", "upper")
	write("c", "c\n")
	gitIn(t, dir, "add", "c")
	gitIn(t, dir, "commit", "-qm", "U1 add c")

	git.Dir = dir
	defer func() { git.Dir = "" }()
	original := func(base, tip string) []Commit {
		c, err := commitsBetween(base, tip)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	state := &State{Order: []string{"lower", "upper"}, Branches: map[string]*Branch{
		"lower": {Base: "main", Commits: original("main", "lower")},
		"upper": {Base: "lower", Commits: original("lower", "upper")},
	}}

	oldLower := gitIn(t, dir, "rev-parse", "lower")
	gitIn(t, dir, "switch", "-q", "lower")
	write("a", "a fixed\n")
	gitIn(t, dir, "commit", "-qa", "--fixup=lower~1", "--trailer", "Review-Thread: https://x/1")
	gitIn(t, dir, "switch", "-q", "upper")
	gitIn(t, dir, "rebase", "-q", "--onto", "lower", oldLower, "upper")
	write("c", "c fixed\n")
	gitIn(t, dir, "commit", "-qa", "--fixup=HEAD")

	signingFor := func() *Signing {
		s := &Signing{Upstream: gitIn(t, dir, "rev-parse", "main"), Trees: map[string]string{}}
		for _, b := range state.Order {
			s.Trees[b] = gitIn(t, dir, "rev-parse", b+"^{tree}")
		}
		return s
	}
	if err := rehearse(state, signingFor()); err != nil {
		t.Fatalf("rehearsal refused a clean stack: %v", err)
	}
	if got := gitIn(t, dir, "worktree", "list"); strings.Count(got, "\n") != 0 {
		t.Errorf("rehearsal left a worktree behind:\n%s", got)
	}

	// A fixup aimed at L1 that touches a line L2 later changed conflicts.
	gitIn(t, dir, "switch", "-q", "lower")
	before := gitIn(t, dir, "rev-parse", "lower")
	write("b", "b again\n")
	gitIn(t, dir, "commit", "-qa", "--fixup=lower~2")
	gitIn(t, dir, "rebase", "-q", "--onto", "lower", before, "upper")
	err := rehearse(state, signingFor())
	if _, ok := err.(Refusal); !ok || !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("conflicting fixup: %v", err)
	}
}
