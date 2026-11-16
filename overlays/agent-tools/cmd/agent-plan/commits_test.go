package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
)

func TestResolveCommit(t *testing.T) {
	c := setup(t)
	plan := newPlan(t)
	p, err := c.Open(plan)
	if err != nil {
		t.Fatal(err)
	}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	if _, err := p.Record("", plans.Event{Kind: "commit-rewritten", Repository: "o/r", OldRevision: a, Revision: b}, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The rewrite is of the object ID; --repo is a label, echoed back.
	for _, test := range []struct{ repo, from, to string }{{"o/r", a, b}, {"o/r", b, b}, {"other/r", a, b}, {"", a, b}} {
		args := []string{"resolve-commit", "--revision", test.from}
		if test.repo != "" {
			args = append(args, "--repo", test.repo)
		}
		code, out, errs := agentPlan(t, "", args...)
		if code != 0 {
			t.Fatalf("resolve: %d %s %s", code, out, errs)
		}
		var got map[string]string
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if got["repository"] != test.repo || got["original_revision"] != test.from || got["revision"] != test.to {
			t.Fatalf("resolution: %s", out)
		}
	}
}

func TestCommits(t *testing.T) {
	t.Run("legacy subjects are unresolved", func(t *testing.T) {
		setup(t)
		plan := newPlan(t)
		if code, _, errs := agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "done", "--commit", "Add the loop"); code != 0 {
			t.Fatal(errs)
		}
		code, out, errs := agentPlan(t, "", "commits", plan)
		if code != 0 || strings.TrimSpace(out) != "[]" {
			t.Fatalf("legacy lookup: %d %s %s", code, out, errs)
		}
		if code, _, _ := agentPlan(t, "", "commits", "--item", "S1"); code == 0 {
			t.Fatal("item without a plan accepted")
		}
		if code, _, _ := agentPlan(t, "", "commits", "--revision", "HEAD"); code == 0 {
			t.Fatal("non-exact lookup accepted")
		}
	})
	t.Run("exact revision", func(t *testing.T) {
		setup(t)
		plan := newPlan(t)
		repo := t.TempDir()
		git := func(args ...string) string {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", repo, "-c", "commit.gpgsign=false", "-c", "user.name=T", "-c", "user.email=t@e"}, args...)...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %s: %v", args, out, err)
			}
			return strings.TrimSpace(string(out))
		}
		git("init", "-q", "-b", "main")
		git("remote", "add", "origin", "git@github.com:o/r.git")
		git("commit", "-q", "--allow-empty", "-m", "Add the loop")
		revision := git("rev-parse", "HEAD")
		old, _ := os.Getwd()
		if err := os.Chdir(repo); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chdir(old) })
		code, out, errs := agentPlan(t, "", "log", plan, "--item", "S1", "--kind", "done", "--revision", "HEAD")
		if code != 0 {
			t.Fatalf("log revision: code %d, %s %s", code, out, errs)
		}
		code, out, errs = agentPlan(t, "", "commits", plan, "--repo", "o/r", "--revision", revision, "--item", "S1")
		if code != 0 {
			t.Fatalf("commits: code %d, %s %s", code, out, errs)
		}
		var got []map[string]string
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0]["revision"] != revision || got[0]["original_revision"] != revision || got[0]["subject"] != "Add the loop" || got[0]["item"] != "S1" || got[0]["file"] != plan || got[0]["plan"] != strings.TrimSuffix(filepath.Base(plan), ".org") {
			t.Fatalf("associations: %s", out)
		}
		if code, _, _ := agentPlan(t, "", "log", plan, "--item", "S2", "--kind", "done", "--commit", "Wrong subject", "--revision", "HEAD"); code == 0 {
			t.Fatal("mismatched subject accepted")
		}
		if code, _, errs := agentPlan(t, "", "log", plan, "--item", "S2", "--kind", "verified", "--revision", "HEAD"); code != 0 {
			t.Fatal(errs)
		}
		code, out, errs = agentPlan(t, "", "commits", "--repo", "o/r")
		if code != 0 {
			t.Fatal(errs)
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 2 {
			t.Fatalf("both steps: %s, %v", out, err)
		}
	})
	// A commit's identity is its object ID: plain git names it, whatever
	// the repository's remotes are.
	for name, origin := range map[string]string{"no remote": "", "an origin off GitHub": "https://git.sr.ht/~o/r"} {
		t.Run("exact revision with "+name, func(t *testing.T) {
			t.Parallel()
			repo, revision := commitIn(t, origin, "Add the loop")
			run := isolatedAgentPlan(t)
			code, out, errs := run(repo, "new", "--title", "Add the loop", "--slug", "add-loop")
			var made plans.Created
			if code != 0 || json.Unmarshal([]byte(out), &made) != nil {
				t.Fatalf("new: code %d, %s %s", code, out, errs)
			}
			if code, out, errs := run(repo, "log", made.Plan, "--item", made.ID, "--kind", "done", "--revision", "HEAD"); code != 0 {
				t.Fatalf("log revision: code %d, %s %s", code, out, errs)
			}
			code, out, errs = run(repo, "commits", made.Plan, "--revision", revision)
			if code != 0 {
				t.Fatalf("commits: code %d, %s %s", code, out, errs)
			}
			var got []map[string]string
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0]["revision"] != revision || got[0]["repository"] != "local/"+filepath.Base(repo) || got[0]["subject"] != "Add the loop" {
				t.Fatalf("associations in %s: %s", repo, out)
			}
		})
	}
}

// isolatedAgentPlan returns a runner of agent-plan as a child process in a
// directory, with a plans directory, state and config of the test's own in
// its environment, so a test that uses it changes no process-wide state
// and can run in parallel.
func isolatedAgentPlan(t *testing.T) func(dir string, args ...string) (int, string, string) {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(home, "config.json")
	data, _ := json.Marshal(map[string]any{"plans": plans.Config{Dir: filepath.Join(home, "agents"), Host: "box"}})
	if err := os.WriteFile(config, data, 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "AGENT_PLAN_TEST_MAIN=1", "AGENT_TOOLS_CONFIG="+config,
		"XDG_STATE_HOME="+filepath.Join(home, "state"), "CLAUDE_CODE_SESSION_ID=", "CODEX_THREAD_ID=")
	return func(dir string, args ...string) (int, string, string) {
		t.Helper()
		var stdout, stderr strings.Builder
		cmd := exec.Command(os.Args[0], args...)
		cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, &stdout, &stderr
		err := cmd.Run()
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit):
			return exit.ExitCode(), stdout.String(), stderr.String()
		case err != nil:
			t.Fatal(err)
		}
		return 0, stdout.String(), stderr.String()
	}
}

// commitIn makes a repository with origin at url, or no remote for "",
// and commits subject in it. It returns the repository and the commit's
// full object ID.
func commitIn(t *testing.T, url, subject string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "commit.gpgsign=false", "-c", "user.name=T", "-c", "user.email=t@e"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	if url != "" {
		git("remote", "add", "origin", url)
	}
	git("commit", "-q", "--allow-empty", "-m", subject)
	return repo, git("rev-parse", "HEAD")
}
