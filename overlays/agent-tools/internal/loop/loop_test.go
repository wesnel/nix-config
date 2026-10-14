package loop

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
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
	if err := checkFixups("br", []Commit{a, b}, []Commit{a, b, c("fixup! amend! A first\n\nignored")}); err != nil {
		t.Errorf("a fixup! of an amend! refused: %v", err)
	}
	for _, rewrite := range []string{"amend! A first\n\nA renamed", "squash! B second\n\nmore", "amend! fixup! A first\n\nA renamed"} {
		if err := checkFixups("br", []Commit{a, b}, []Commit{a, b, c(rewrite)}); err == nil || !strings.Contains(err.Error(), "--fixup=<commit>") {
			t.Errorf("%q: %v", rewrite, err)
		}
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

func TestCheckOpen(t *testing.T) {
	repo := &Repo{Slug: "o/r", Default: "trunk", Viewer: "me"}
	pr := func(number int, head, author string) *PR {
		p := &PR{Number: number, HeadRefName: head}
		p.Author.Login = author
		return p
	}
	view := &remoteView{
		Heads: map[string]string{"trunk": "t0", "parent": "p0", "theirs": "x0", "pushed": "q0"},
		PRs:   map[string]*PR{"parent": pr(1, "parent", "me"), "theirs": pr(2, "theirs", "other"), "taken": pr(3, "taken", "me"), "gone": pr(4, "gone", "me")},
	}
	refused := map[string][2]string{
		"default head":       {"trunk", "trunk"},
		"main head":          {"main", "trunk"},
		"own base":           {"new", "new"},
		"head with a PR":     {"taken", "trunk"},
		"head on GitHub":     {"pushed", "trunk"},
		"base without a PR":  {"new", "pushed"},
		"someone else's PR":  {"new", "theirs"},
		"base not on GitHub": {"new", "gone"},
	}
	for name, c := range refused {
		_, err := checkOpen(repo, view, c[0], c[1])
		if _, ok := err.(Refusal); !ok {
			t.Errorf("%s: %v", name, err)
		}
	}
	if oid, err := checkOpen(repo, view, "new", "trunk"); err != nil || oid != "t0" {
		t.Errorf("on the default branch: %s, %v", oid, err)
	}
	if oid, err := checkOpen(repo, view, "new", "parent"); err != nil || oid != "p0" {
		t.Errorf("stacked: %s, %v", oid, err)
	}
}

func TestCheckLocal(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	write := func(name string) { os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644) }
	gitIn(t, dir, "init", "-q", "-b", "trunk")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/o/r.git")
	gitIn(t, dir, "remote", "add", "another", "git@github.com:o/r.git")
	gitIn(t, dir, "remote", "add", "fork", "https://github.com/f/r.git")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	trunk := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "update-ref", "refs/remotes/origin/trunk", trunk)
	gitIn(t, dir, "switch", "-qc", "parent")
	write("p")
	gitIn(t, dir, "add", "p")
	gitIn(t, dir, "commit", "-qm", "parent")
	parent := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "switch", "-qc", "child")
	write("c")
	gitIn(t, dir, "add", "c")
	gitIn(t, dir, "commit", "-qm", "child")
	child := gitIn(t, dir, "rev-parse", "HEAD")
	gitIn(t, dir, "switch", "-qc", "aside", "trunk")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "aside")
	gitIn(t, dir, "branch", "empty", "trunk")
	git.Dir = dir
	defer func() { git.Dir = "" }()
	repo := &Repo{Slug: "o/r", Default: "trunk"}
	unfetched := strings.Repeat("1", 40)

	local, known, err := checkLocal(repo, "child", "parent", parent)
	if err != nil || local != child {
		t.Fatalf("stacked: %s, %v", local, err)
	}
	if got := strings.Join(known, " "); got != parent+" --remotes=origin/* --remotes=another/*" {
		t.Errorf("known: %s", got)
	}
	objects, err := missingObjects([]string{local}, known)
	if err != nil {
		t.Fatal(err)
	}
	if got := objects.counts(); got["commit"] != 1 || got["tree"] != 1 || got["blob"] != 1 || len(objects.commits) != 1 {
		t.Errorf("objects over the parent: %v", got)
	}
	// The default branch may have moved on GitHub past what was fetched;
	// what was fetched is still left out of the upload.
	if _, known, err := checkLocal(repo, "child", "trunk", unfetched); err != nil || strings.Join(known, " ") != "--remotes=origin/* --remotes=another/*" {
		t.Errorf("default base not fetched: %v, %v", known, err)
	}
	refused := map[string][3]string{
		"no local branch":    {"gone", "trunk", trunk},
		"no new commits":     {"empty", "trunk", trunk},
		"parent not fetched": {"child", "parent", unfetched},
		"not on its parent":  {"aside", "parent", parent},
	}
	for name, c := range refused {
		_, _, err := checkLocal(repo, c[0], c[1], c[2])
		if _, ok := err.(Refusal); !ok {
			t.Errorf("%s: %v", name, err)
		}
	}

	gitIn(t, dir, "branch", "--set-upstream-to=origin/trunk", "child")
	if err := setUpstream(repo, "child"); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, dir, "config", "branch.child.remote") + " " + gitIn(t, dir, "config", "branch.child.merge"); got != "origin refs/heads/child" {
		t.Errorf("upstream: %s", got)
	}
}

// gitIn runs git in dir with a fixed identity and signing off.
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

func TestWorktrees(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, dir, "branch", "feature")
	gitIn(t, dir, "config", "commit.gpgsign", "true")
	gitenvtest.Decoy(t)
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
		cmd := exec.Command("git", "-C", where, "config", "commit.gpgsign")
		cmd.Env = gitenv.Without(os.Environ())
		out, _ := cmd.Output()
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

func TestSetup(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	config := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, dir, "remote", "add", "origin", "https://github.com/O/R.git")
	gitenvtest.Decoy(t)
	git.Dir = dir
	defer func() { git.Dir = "" }()

	if s, err := SetupCommand(); err != nil || s.Command != "" {
		t.Errorf("without a config: %+v, %v", s, err)
	}
	os.WriteFile(config, []byte(`{"repos": {"o/r": {"setup": "echo generated > gen"}, "o/other": {"setup": "false"}}}`), 0o644)
	s, err := SetupCommand()
	if err != nil || s.Command != "echo generated > gen" || s.Repo != "o/r" {
		t.Fatalf("configured: %+v, %v", s, err)
	}
	gitIn(t, dir, "remote", "add", "fork", "https://github.com/o/other.git")
	if _, err := SetupCommand(); err == nil {
		t.Error("two remotes with different setup commands were accepted")
	}
	gitIn(t, dir, "remote", "remove", "fork")

	gitIn(t, dir, "branch", "feature")
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, dir, "worktree", "add", "-q", wt, "feature")
	if !NeedsSetup(wt) {
		t.Error("a fresh worktree does not need setup")
	}
	var log bytes.Buffer
	runs := RunSetup(s.Command, []string{wt}, &log)
	if len(runs) != 1 || runs[0].Exit != 0 || !exists(filepath.Join(wt, "gen")) {
		t.Fatalf("setup did not run in the worktree: %+v\n%s", runs, log.String())
	}
	if !strings.Contains(log.String(), "setup in "+wt+": echo generated > gen\n") || !strings.Contains(log.String(), ": exit 0\n") {
		t.Errorf("the command or its status was not reported:\n%s", log.String())
	}
	if NeedsSetup(wt) {
		t.Error("needs setup again right after it")
	}
	if !NeedsSetup(dir) {
		t.Error("another worktree counts as set up")
	}
	if got := gitIn(t, wt, "status", "--porcelain"); got != "?? gen" {
		t.Errorf("the record of setup shows in the worktree: %q", got)
	}
	gitIn(t, wt, "commit", "-q", "--allow-empty", "-m", "moved")
	if !NeedsSetup(wt) {
		t.Error("a moved worktree does not need setup")
	}
	if runs := RunSetup("exit 3", []string{wt}, &log); runs[0].Exit != 3 || !NeedsSetup(wt) {
		t.Errorf("a failed setup: %+v, needs setup: %v", runs, NeedsSetup(wt))
	}
	// The command's git finds the worktree's repository.
	RunSetup("git rev-parse --absolute-git-dir > where", []string{wt}, &log)
	where, _ := os.ReadFile(filepath.Join(wt, "where"))
	if got, want := strings.TrimSpace(string(where)), gitIn(t, wt, "rev-parse", "--absolute-git-dir"); got != want {
		t.Errorf("the setup command's git found %q, not %q", got, want)
	}
}

// TestRehearse builds a two-branch stack with fixups in both branches and
// checks the rehearsal accepts it without moving a branch, even with
// rebase.updateRefs on, then that it refuses a stray commit and a fixup that
// conflicts.
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

	gitenvtest.Decoy(t)
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
	// With neither branch checked out, rebase.updateRefs would move both onto
	// the rehearsal's unsigned commits.
	gitIn(t, dir, "config", "rebase.updateRefs", "true")
	gitIn(t, dir, "switch", "-q", "main")
	branches := gitIn(t, dir, "for-each-ref", "refs/heads/")
	if err := rehearse(state, signingFor()); err != nil {
		t.Fatalf("rehearsal refused a clean stack: %v", err)
	}
	if got := gitIn(t, dir, "worktree", "list"); strings.Count(got, "\n") != 0 {
		t.Errorf("rehearsal left a worktree behind:\n%s", got)
	}
	if got := gitIn(t, dir, "for-each-ref", "refs/heads/"); got != branches {
		t.Errorf("rehearsal moved branches:\nbefore:\n%s\nafter:\n%s", branches, got)
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
