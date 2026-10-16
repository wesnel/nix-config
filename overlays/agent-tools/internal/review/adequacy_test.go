package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/redcheck"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// adequacyFixture is a repository, as setUp makes, with test settings and a
// last commit that adds a test, and the red-check JSON the session would
// give the review with --red-check.
type adequacyFixture struct {
	*fixture
	// commit is the commit that adds the test; verdict what red-check
	// says of it. The range's other commits are no-code-change.
	commit, verdict string
	made            int
	// start is what code_test.txt starts with, before the line its tests
	// follow.
	start string
	// edit, where set, changes the JSON's commits before it is written.
	edit func(checks []*redcheck.Check) []*redcheck.Check
}

func setUpAdequacy(t *testing.T, verdict string) *adequacyFixture {
	t.Helper()
	f := setUp(t, true)
	t.Setenv("FAKE_CODEX", "none")
	// Baked in, as the module's build bakes it, and named by the
	// environment too, which a test may change.
	configHome := t.TempDir()
	hostpaths.Bake(t, configHome, t.TempDir())
	config := filepath.Join(configHome, "agent-tools", "config.json")
	os.MkdirAll(filepath.Dir(config), 0o755)
	os.WriteFile(config, must(json.Marshal(map[string]any{
		"repos": map[string]any{"o/r": map[string]any{"test": "true", "testFiles": []string{"*_test.txt"}}}})), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	run(t, f.repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
	// The test file the last commit adds to, so its tests start at line 2.
	os.WriteFile(filepath.Join(f.repo, "code_test.txt"), []byte("x\n"), 0o644)
	run(t, f.repo, "git", "add", "code_test.txt")
	run(t, f.repo, "git", "commit", "-q", "-m", "Start the tests")
	a := &adequacyFixture{fixture: f, verdict: verdict}
	a.commitTest(t, "d\n")
	return a
}

// commitTest makes the change's last commit one that adds code_test.txt,
// holding test, and a line of code, in place of the one an earlier call
// made.
func (a *adequacyFixture) commitTest(t *testing.T, test string) {
	t.Helper()
	if a.commit != "" {
		run(t, a.repo, "git", "reset", "-q", "--hard", "HEAD~1")
	}
	os.WriteFile(filepath.Join(a.repo, "code.txt"), []byte("a\nb\nc\nd\n"), 0o644)
	os.WriteFile(filepath.Join(a.repo, "code_test.txt"), []byte(a.start+"x\n"+test), 0o644)
	run(t, a.repo, "git", "add", "code.txt", "code_test.txt")
	// A new date, so that a commit made again is a new commit.
	a.made++
	date := fmt.Sprintf("2026-01-01T00:00:%02d", a.made)
	t.Setenv("GIT_AUTHOR_DATE", date)
	t.Setenv("GIT_COMMITTER_DATE", date)
	run(t, a.repo, "git", "commit", "-q", "-m", "Test the code")
	a.commit = run(t, a.repo, "git", "rev-parse", "HEAD")
	a.tip = a.commit
}

// restart makes the change again on a first commit whose code_test.txt
// starts with start, and the same last commit on it.
func (a *adequacyFixture) restart(t *testing.T, start string) {
	t.Helper()
	run(t, a.repo, "git", "reset", "-q", "--hard", a.base)
	a.start = start
	os.WriteFile(filepath.Join(a.repo, "code_test.txt"), []byte(start+"x\n"), 0o644)
	run(t, a.repo, "git", "add", "code_test.txt")
	run(t, a.repo, "git", "commit", "-q", "-m", "Start the tests")
	a.commit = ""
	a.commitTest(t, "d\n")
}

// redCheck writes git red-check's JSON on the range, as the session would
// run it, and returns its path.
func (a *adequacyFixture) redCheck(t *testing.T) string {
	t.Helper()
	checks := []*redcheck.Check{}
	for _, c := range strings.Fields(run(t, a.repo, "git", "rev-list", "--reverse", "--no-merges", a.base+"..HEAD")) {
		v := redcheck.NoCodeChange
		if c == a.commit {
			v = a.verdict
		}
		checks = append(checks, &redcheck.Check{Commit: c, Subject: "s", Verdict: v, TestFiles: []string{}})
	}
	if a.edit != nil {
		checks = a.edit(checks)
	}
	path := filepath.Join(a.repo, ".git", "red-check.json")
	os.WriteFile(path, must(json.Marshal(redcheck.Result{Test: "true", TestFiles: []string{"*_test.txt"}, Commits: checks})), 0o644)
	return path
}

func (a *adequacyFixture) review(t *testing.T) (int, output, string) {
	t.Helper()
	return freshReview(t, "--goal", ".git/goal.md", "--red-check", a.redCheck(t), a.base+"..HEAD")
}

// raised reviews the change and returns its one red-check finding.
func (a *adequacyFixture) raised(t *testing.T) map[string]any {
	t.Helper()
	code, out, stderr := a.review(t)
	return mechanical(t, code, out, stderr)
}

// mechanical is the review's one red-check finding.
func mechanical(t *testing.T, code int, out output, stderr string) map[string]any {
	t.Helper()
	if code != 3 || len(out.Findings) != 1 {
		t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
	}
	return out.Findings[0]
}

// refused holds a review to a refusal that says what to do instead, made
// before Codex started.
func (f *fixture) refused(t *testing.T, code int, out output, stderr string) {
	t.Helper()
	reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: refused: ")
	if code != 2 || len(out.Findings) != 0 || !refusal.SaysInstead(reason) {
		t.Errorf("exit %d, %+v, %q", code, out, stderr)
	}
	if _, err := os.Stat(filepath.Join(f.log, "prompt")); err == nil {
		t.Error("Codex ran")
	}
}

func TestTestAdequacy(t *testing.T) {
	// A commit whose tests pass without the rest of it is untested, said so
	// by red-check rather than the reviewer, at its test file.
	t.Run("raised", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.PassesWithoutChange)
		f := a.raised(t)
		id, _ := f["id"].(string)
		if !strings.HasPrefix(id, "RC-") || f["kind"] != "untested" || f["reviewer"] != "red-check" ||
			f["path"] != "code_test.txt" || f["line"] != 2.0 || !strings.Contains(f["evidence"].(string), a.commit) {
			t.Errorf("the finding: %+v", f)
		}
		prompt := string(must(os.ReadFile(filepath.Join(a.log, "prompt"))))
		if !strings.Contains(prompt, id) {
			t.Errorf("the prompt does not tell the reviewer of %s:\n%s", id, prompt)
		}
		if !strings.Contains(prompt, "## git red-check "+a.base+".."+a.tip+"\n\n```json\n") ||
			!strings.Contains(prompt, `"verdict":"passes-without-change"`) {
			t.Errorf("the prompt lacks red-check's JSON:\n%s", prompt)
		}
	})

	// The key is the commit's test diff, so a rebase or a change of the code
	// keeps it.
	t.Run("same key for the same tests", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.PassesWithoutChange)
		first := a.raised(t)["id"]
		old := a.commit
		a.commitTest(t, "d\n")
		if a.commit == old {
			t.Fatal("the commit was not made again")
		}
		if again := a.raised(t)["id"]; again != first {
			t.Errorf("the key moved with the commit: %v, then %v", first, again)
		}
	})

	// A rebase that moves the tests' lines keeps the key.
	t.Run("same key after a rebase", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.PassesWithoutChange)
		a.restart(t, "1\n2\n3\n4\n")
		first := a.raised(t)["id"]
		a.restart(t, "0\n1\n2\n3\n4\n")
		if again := a.raised(t)["id"]; again != first {
			t.Errorf("the key moved with the lines: %v, then %v", first, again)
		}
	})

	// The key is the tests as written, whitespace included: a test of "a b"
	// is not a test of "ab".
	t.Run("whitespace changes the key", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.PassesWithoutChange)
		a.commitTest(t, "assert \"a b\"\n")
		first := a.raised(t)["id"]
		a.commitTest(t, "assert \"ab\"\n")
		if again := a.raised(t)["id"]; again == first {
			t.Errorf("both tests have the key %v", first)
		}
	})

	t.Run("other verdicts", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.FailsAsAsserted)
		if code, out, stderr := a.review(t); code != 0 || len(out.Findings) != 0 {
			t.Errorf("exit %d, %+v\n%s", code, out, stderr)
		}
	})

	// A commit whose tests only fail to build without it raises nothing,
	// but the reviewer is asked to judge its tests.
	t.Run("tests that only fail to build", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.FailsToBuildWithoutChange)
		if code, out, stderr := a.review(t); code != 0 || len(out.Findings) != 0 {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		prompt := string(must(os.ReadFile(filepath.Join(a.log, "prompt"))))
		var line string
		for _, l := range strings.Split(prompt, "\n") {
			if strings.Contains(l, "fails-to-build-without-change") && strings.Contains(l, "`untested`") {
				line = l
			}
		}
		if !strings.Contains(line, a.commit[:12]) {
			t.Errorf("the prompt does not ask for a judgement of %s's tests:\n%s", a.commit[:12], prompt)
		}
	})

	// The JSON must be red-check's on the range under review, every commit
	// of it in order and nothing else, or the verdicts are not this
	// change's.
	t.Run("refused", func(t *testing.T) {
		for _, c := range []struct {
			name string
			edit func(a *adequacyFixture, checks []*redcheck.Check) []*redcheck.Check
		}{
			{"without TIP's commit", func(_ *adequacyFixture, checks []*redcheck.Check) []*redcheck.Check {
				return checks[:len(checks)-1]
			}},
			{"with a commit outside the range", func(a *adequacyFixture, checks []*redcheck.Check) []*redcheck.Check {
				return append(checks, &redcheck.Check{Commit: a.base, Verdict: redcheck.PassesWithoutChange})
			}},
			{"with a short commit", func(_ *adequacyFixture, checks []*redcheck.Check) []*redcheck.Check {
				last := checks[len(checks)-1]
				last.Commit = last.Commit[:12]
				return checks
			}},
			{"out of order", func(_ *adequacyFixture, checks []*redcheck.Check) []*redcheck.Check {
				slices.Reverse(checks)
				return checks
			}},
			{"with a commit twice", func(_ *adequacyFixture, checks []*redcheck.Check) []*redcheck.Check {
				return append(checks, checks[len(checks)-1])
			}},
			// The prompt would tell the reviewer red-check judged the tests.
			{"with a verdict red-check does not give", func(_ *adequacyFixture, checks []*redcheck.Check) []*redcheck.Check {
				checks[len(checks)-1].Verdict = "passes"
				return checks
			}},
		} {
			t.Run(c.name, func(t *testing.T) {
				a := setUpAdequacy(t, redcheck.PassesWithoutChange)
				a.edit = func(checks []*redcheck.Check) []*redcheck.Check { return c.edit(a, checks) }
				code, out, stderr := a.review(t)
				a.refused(t, code, out, stderr)
			})
		}
	})

	// Verdicts from a run of other test settings say nothing of this
	// repository's tests.
	t.Run("refused for other test settings", func(t *testing.T) {
		for name, result := range map[string]redcheck.Result{
			"another test command": {Test: "go test ./...", TestFiles: []string{"*_test.txt"}},
			"a build command":      {Test: "true", Build: "make", TestFiles: []string{"*_test.txt"}},
			"other test files":     {Test: "true", TestFiles: []string{"*.txt"}},
		} {
			t.Run(name, func(t *testing.T) {
				a := setUpAdequacy(t, redcheck.PassesWithoutChange)
				var checks []*redcheck.Check
				for _, c := range strings.Fields(run(t, a.repo, "git", "rev-list", "--reverse", "--no-merges", a.base+"..HEAD")) {
					checks = append(checks, &redcheck.Check{Commit: c, Verdict: redcheck.PassesWithoutChange})
				}
				result.Commits = checks
				file := filepath.Join(a.repo, ".git", "other.json")
				os.WriteFile(file, must(json.Marshal(result)), 0o644)
				code, out, stderr := freshReview(t, "--goal", ".git/goal.md", "--red-check", file, a.base+"..HEAD")
				a.refused(t, code, out, stderr)
			})
		}
	})

	t.Run("refused if it does not parse", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.PassesWithoutChange)
		bad := filepath.Join(a.repo, ".git", "bad.json")
		os.WriteFile(bad, []byte(`{"commits": [`), 0o644)
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", "--red-check", bad, a.base+"..HEAD")
		a.refused(t, code, out, stderr)
	})

	// Without the repository's test globs, an untested finding has no test
	// file to be placed at.
	t.Run("refused without test settings", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_CODEX", "none")
		file := filepath.Join(f.repo, ".git", "red-check.json")
		os.WriteFile(file, must(json.Marshal(redcheck.Result{Commits: []*redcheck.Check{
			{Commit: f.tip, Verdict: redcheck.PassesWithoutChange}}})), 0o644)
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", "--red-check", file, f.base+"..HEAD")
		f.refused(t, code, out, stderr)
	})

	// Without --red-check there are no red-check results: no section, no
	// finding, and the reviewer judges the tests itself.
	t.Run("without --red-check", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.PassesWithoutChange)
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", a.base+"..HEAD")
		if code != 0 || len(out.Findings) != 0 || !strings.Contains(stderr, "red-check runs in the session") {
			t.Errorf("exit %d, %+v\n%s", code, out, stderr)
		}
		prompt := string(must(os.ReadFile(filepath.Join(a.log, "prompt"))))
		if strings.Contains(prompt, "## git red-check") || !strings.Contains(prompt, "no mechanical test adequacy") {
			t.Errorf("the prompt:\n%s", prompt)
		}
	})

	// Nothing in the environment the review runs in points it at settings
	// of the caller's own: where the build bakes the config directory in, a
	// config without the repository's test settings is not used.
	t.Run("not redirected by the environment", func(t *testing.T) {
		a := setUpAdequacy(t, redcheck.PassesWithoutChange)
		config := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(config, must(json.Marshal(map[string]any{"repos": map[string]any{}})), 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		if f := a.raised(t); f["reviewer"] != "red-check" {
			t.Errorf("with a config without test settings: %+v", f)
		}
	})

	// A reviewer cannot speak for red-check: its finding with red-check's
	// id is dropped, and one naming red-check as its reviewer is the
	// reviewer's own.
	t.Run("a reply cannot pass as red-check", func(t *testing.T) {
		blob := func(string) ([]byte, error) { return []byte("a\n"), nil }
		out, err := Validate(`{"findings": [
 {"id": "RC-0123456789ab", "path": "code.txt", "line": 1, "kind": "untested", "claim": "c", "evidence": "e"},
 {"id": "1", "path": "code.txt", "line": 1, "kind": "bug", "claim": "c", "evidence": "e", "reviewer": "red-check"}
], "unverified": []}`, "base..tip", "tip", blob, "")
		if err != nil || len(out.Findings) != 1 || out.Findings[0].ID != "1" || out.Findings[0].Reviewer != "" ||
			len(out.Dropped) != 1 {
			t.Errorf("%+v, %v", out, err)
		}
	})
}
