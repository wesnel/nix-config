package main

import (
	"encoding/json"
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
	for _, test := range []struct{ repo, from, to string }{{"o/r", a, b}, {"o/r", b, b}, {"other/r", a, a}} {
		code, out, errs := agentPlan(t, "", "resolve-commit", "--repo", test.repo, "--revision", test.from)
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
}
