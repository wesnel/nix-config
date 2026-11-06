package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

// fakeGh stands in for `gh pr list`, logging each call, and lists the
// open pull request #7 from branch pr-branch.
const fakeGh = `#!/bin/sh
echo "$*" >> "$FAKE_GH_LOG"
if [ "$1 $2" != "pr list" ]; then echo "fake gh: unexpected $*" >&2; exit 64; fi
echo '[{"headRefName": "pr-branch", "number": 7}]'
`

// overlapFixture is setUp's repository as github.com/o/r with
// origin/main at its base, on branch work, and branches around it:
// other, unrelated and merged locally; and, as fetched, pr-branch with an
// open pull request, planned, which a plan names, stale, with no pull
// request, and old, from a month ago. Every branch but unrelated and
// merged changes code.txt, as the change does.
func overlapFixture(t *testing.T) *fixture {
	t.Helper()
	f := setUp(t, true)
	// The list keeps a budget by this clock; a clock that does not move keeps
	// a slow host from spending it. The tests of the budget set their own.
	frozen := time.Now()
	f.seams.now = func() time.Time { return frozen }
	run(t, f.repo, "git", "update-ref", "refs/remotes/origin/main", f.base)
	run(t, f.repo, "git", "update-ref", "refs/heads/merged", f.base)
	run(t, f.repo, "git", "switch", "-q", "-c", "work")
	// date is the committer date of the commits branch makes, which ago
	// sets.
	var date []string
	branch := func(name, file, content, subject, ref string) {
		t.Helper()
		run(t, f.repo, "git", "switch", "-q", "-c", "tmp-"+name, f.base)
		os.WriteFile(filepath.Join(f.repo, file), []byte(content), 0o644)
		run(t, f.repo, "git", "add", file)
		runWith(t, date, f.repo, "git", "commit", "-q", "-m", subject)
		run(t, f.repo, "git", "update-ref", ref, "HEAD")
		run(t, f.repo, "git", "switch", "-q", "work")
		run(t, f.repo, "git", "branch", "-q", "-D", "tmp-"+name)
	}
	ago := func(d time.Duration) {
		date = []string{"GIT_COMMITTER_DATE=" + time.Now().Add(-d).Format(time.RFC3339)}
	}
	ago(time.Hour)
	branch("other", "code.txt", "z\n", "Drop the check", "refs/heads/other")
	branch("unrelated", "x.txt", "x\n", "Add x", "refs/heads/unrelated")
	ago(3 * time.Hour)
	branch("pr", "code.txt", "y\n", "Change code with ghp_"+strings.Repeat("a", 36), "refs/remotes/origin/pr-branch")
	ago(2 * time.Hour)
	branch("planned", "code.txt", "p\n", "Plan the code", "refs/remotes/origin/planned")
	ago(4 * time.Hour)
	branch("stale", "code.txt", "s\n", "Stale code", "refs/remotes/origin/stale")
	ago(30 * 24 * time.Hour)
	branch("old", "code.txt", "o\n", "Old code", "refs/remotes/origin/old")
	ago(0)

	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, must(json.Marshal(map[string]any{"plans": c})), 0o644)
	f.setenv("AGENT_TOOLS_CONFIG", config)
	if _, err := c.New(t.Context(), plans.NewOptions{Title: "Plan", Slug: "plan", Branches: []string{"main..planned"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGh), 0o755)
	f.setenv("PATH", bin+string(os.PathListSeparator)+f.env.Get("PATH"))
	f.setenv("FAKE_GH_LOG", filepath.Join(t.TempDir(), "gh.log"))
	f.setenv("XDG_CACHE_HOME", t.TempDir())
	return f
}

func overlapText(t *testing.T, f *fixture) string {
	t.Helper()
	r, err := resolve(f.ctx(t), f.repo, f.base+"..work")
	if err != nil {
		t.Fatal(err)
	}
	return overlapSection(f.ctx(t), r)
}

func listed(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if ref, ok := strings.CutPrefix(line, "- "); ok {
			ref, _, _ = strings.Cut(ref, " ")
			out = append(out, ref)
		}
	}
	return out
}

// The security reviewer is shown the other open branches that change the
// files this change does, as this host knows them, without fetching.
func TestOverlap(t *testing.T) {
	t.Parallel()
	t.Run("main session", func(t *testing.T) {
		t.Parallel()
		f := overlapFixture(t)
		text := overlapText(t, f)
		expectRefs(t, text, "refs/heads/other", "refs/remotes/origin/planned", "refs/remotes/origin/pr-branch")
		for _, want := range []string{"Drop the check", "open pull request #7", "named in a plan", "code.txt", "[REDACTED"} {
			if !strings.Contains(text, want) {
				t.Errorf("the list lacks %q:\n%s", want, text)
			}
		}
		if strings.Contains(text, "ghp_aaaa") {
			t.Errorf("a token reached the prompt:\n%s", text)
		}
		// Asked again within the hour, gh is not.
		overlapText(t, f)
		log, _ := os.ReadFile(f.env.Get("FAKE_GH_LOG"))
		if calls := strings.Count(string(log), "pr list"); calls != 1 {
			t.Errorf("gh ran %d times", calls)
		}
	})

	t.Run("review-loop session", func(t *testing.T) {
		t.Parallel()
		f := overlapFixture(t)
		f.setenv("PR_LOOP_SESSION", "1")
		text := overlapText(t, f)
		expectRefs(t, text, "refs/heads/other", "refs/remotes/origin/planned", "refs/remotes/origin/pr-branch",
			"refs/remotes/origin/stale")
		if !strings.Contains(text, "recent, pull request state unknown") {
			t.Errorf("the list does not say what it does not know:\n%s", text)
		}
		if _, err := os.Stat(f.env.Get("FAKE_GH_LOG")); err == nil {
			t.Error("gh ran in a review-loop session")
		}
	})

	t.Run("bounded", func(t *testing.T) {
		t.Parallel()
		f := overlapFixture(t)
		start := time.Now()
		calls := 0
		f.seams.now = func() time.Time { calls++; return start.Add(time.Duration(calls) * 4 * time.Second) }
		text := overlapText(t, f)
		if !strings.Contains(text, "were not checked") || len(listed(text)) >= 3 {
			t.Errorf("past the budget:\n%s", text)
		}
	})

	// Finding the candidates counts against the budget too: once it is
	// spent, gh is not asked and nothing more is looked at.
	t.Run("spent before the checks", func(t *testing.T) {
		t.Parallel()
		f := overlapFixture(t)
		start, first := time.Now(), true
		f.seams.now = func() time.Time {
			if first {
				first = false
				return start
			}
			return start.Add(overlapBudget + time.Second)
		}
		text := overlapText(t, f)
		if _, err := os.Stat(f.env.Get("FAKE_GH_LOG")); err == nil {
			t.Error("gh ran past the budget")
		}
		if len(listed(text)) != 0 || !strings.Contains(text, "not checked within the time allowed") {
			t.Errorf("past the budget:\n%s", text)
		}
	})

	// gh lists the newest open pull requests up to a limit; the list says
	// when it reached it.
	t.Run("gh's limit", func(t *testing.T) {
		t.Parallel()
		f := overlapFixture(t)
		f.seams.prListLimit = 1
		if text := overlapText(t, f); !strings.Contains(text, "only its newest 1 open pull requests") {
			t.Errorf("the limit is not named:\n%s", text)
		}
	})
}

func expectRefs(t *testing.T, text string, want ...string) {
	t.Helper()
	got := listed(text)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("listed %v, want %v:\n%s", got, want, text)
	}
}
