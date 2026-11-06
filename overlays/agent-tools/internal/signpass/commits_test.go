package signpass

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
)

func TestPlanCommitRewrites(t *testing.T) {
	t.Run("fork duplicate subjects and reword", func(t *testing.T) {
		ctx, repo, _ := flakyRepo(t)
		gitIn(t, repo, "remote", "add", "origin", "git@github.com:o/r.git")
		gitIn(t, repo, "commit", "--amend", "--allow-empty", "-m", "a")
		first := gitIn(t, repo, "rev-parse", "one~1")
		second := gitIn(t, repo, "rev-parse", "one")
		gitIn(t, repo, "switch", "-c", "C2")
		gitIn(t, repo, "commit", "--allow-empty", "-m", "Child two")
		childTwo := gitIn(t, repo, "rev-parse", "HEAD")
		gitIn(t, repo, "switch", "one")
		gitIn(t, repo, "commit", "--allow-empty", "-m", "fixup! a")
		fixup := gitIn(t, repo, "rev-parse", "HEAD")
		gitIn(t, repo, "commit", "--allow-empty", "-m", "amend! a", "-m", "Renamed")
		amend := gitIn(t, repo, "rev-parse", "HEAD")
		gitIn(t, repo, "switch", "-c", "C1")
		gitIn(t, repo, "commit", "--allow-empty", "-m", "Child one")
		childOne := gitIn(t, repo, "rev-parse", "HEAD")
		ctx, c, p, _ := commitPlan(t, ctx, repo)
		for _, oid := range []string{first, second, childTwo, fixup, amend, childOne} {
			e, err := plans.CommitIdentity(t.Context(), repo, oid)
			if err != nil {
				t.Fatal(err)
			}
			e.Kind = "committed"
			if _, err := p.Record("", e, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Plan(ctx, "", []string{"C1", "C2"}, false, false); err != nil {
			t.Fatal(err)
		}
		for old, ref := range map[string]string{first: "C1~2", second: "C1~1", fixup: "C1~2", amend: "C1~2", childOne: "C1", childTwo: "C2"} {
			want := gitIn(t, repo, "rev-parse", ref)
			got, err := c.Commits(plans.CommitOptions{Repository: "o/r", Revision: old})
			if err != nil || len(got) == 0 {
				t.Fatalf("association %s: %+v, %v", old, got, err)
			}
			for _, a := range got {
				if a.Revision != want {
					t.Errorf("%s resolves to %s, want %s", old, a.Revision, want)
				}
			}
		}
	})
	t.Run("fixup identities", func(t *testing.T) {
		ctx, repo, _ := flakyRepo(t)
		gitIn(t, repo, "remote", "add", "origin", "git@github.com:o/r.git")
		gitIn(t, repo, "commit", "--allow-empty", "-m", "fixup! a")
		c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
		ctx, _ = plansConfig(t, ctx, c)
		made, err := c.New(t.Context(), plans.NewOptions{Title: "Link commits", Slug: "links", Worktrees: []string{repo}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		p, err := c.Open(made.Plan)
		if err != nil {
			t.Fatal(err)
		}
		var old []string
		for _, rev := range []string{"one~2", "one~1", "one"} {
			e, err := plans.CommitIdentity(t.Context(), repo, rev)
			if err != nil {
				t.Fatal(err)
			}
			e.Kind = "committed"
			if _, err := p.Record("", e, time.Now()); err != nil {
				t.Fatal(err)
			}
			old = append(old, e.Revision)
		}
		if _, err := Run(ctx, "main", false, false, false); err != nil {
			t.Fatal(err)
		}
		want := []string{gitIn(t, repo, "rev-parse", "one~1"), gitIn(t, repo, "rev-parse", "one"), gitIn(t, repo, "rev-parse", "one~1")}
		for i, before := range old {
			got, err := c.Commits(plans.CommitOptions{Revision: before, Repository: "o/r"})
			if err != nil || len(got) == 0 {
				t.Fatalf("associations: %+v, %v", got, err)
			}
			for _, a := range got {
				if a.Revision != want[i] {
					t.Errorf("%s resolves to %s, want %s", before, a.Revision, want[i])
				}
			}
		}
	})
	// The plans are a convenience the signing never waits on: a pass that
	// cannot record its rewrites signs all the same, says so on stderr, and
	// leaves no state behind to resume.
	for _, c := range []struct {
		name string
		// spoil breaks the plans, which config points at; it returns what
		// puts them right again.
		spoil func(t *testing.T, p plans.Plan, config string) (restore func())
	}{
		{"log changed outside agent-plan", func(t *testing.T, p plans.Plan, _ string) func() {
			log := strings.TrimSuffix(p.File(), ".org") + ".log.org"
			original, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(log, append(original, []byte("\nchanged outside agent-plan\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.WriteFile(log, original, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"unreadable log", func(t *testing.T, p plans.Plan, _ string) func() {
			log := strings.TrimSuffix(p.File(), ".org") + ".log.org"
			if err := os.Chmod(log, 0); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.Chmod(log, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}},
		{"malformed config", func(t *testing.T, p plans.Plan, config string) func() {
			original, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte("{"), 0o644); err != nil {
				t.Fatal(err)
			}
			return func() {
				if err := os.WriteFile(config, original, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}},
	} {
		t.Run(c.name+" warns and signs", func(t *testing.T) {
			ctx, repo, _ := flakyRepo(t)
			gitIn(t, repo, "remote", "add", "origin", "git@github.com:o/r.git")
			ctx, cfg, p, config := commitPlan(t, ctx, repo)
			identity := recordCommitted(t, p, repo, "one")
			restore := c.spoil(t, p, config)
			stderr := captureStderr(t)
			result, err := Run(ctx, "main", false, false, false)
			if err != nil {
				t.Fatalf("a pass that could not record its rewrites failed: %v", err)
			}
			if got := result.(map[string]any)["signed"]; got != 2 {
				t.Errorf("signed %v commits", got)
			}
			if marks, _ := git.In(ctx).Run("log", "--format=%G?", "main..one"); marks != "G\nG" {
				t.Errorf("signatures %q", marks)
			}
			if out := stderr(); !strings.Contains(out, "could not record the commit rewrites in the plans") {
				t.Errorf("no warning on stderr:\n%s", out)
			}
			assertNoPending(t, ctx, "one")
			restore()
			got, err := cfg.Commits(plans.CommitOptions{Revision: identity.Revision})
			if err != nil || len(got) != 1 || got[0].Revision != identity.Revision {
				t.Errorf("a rewrite was recorded: %+v, %v", got, err)
			}
		})
	}

	// A rewrite whose commits do not pair up is recorded not at all, rather
	// than wrongly, and the pass still ends. The pass itself cannot get there
	// once verify has passed, so finish is given a pending state whose
	// unsigned tip has a commit more than the signed branch.
	t.Run("count mismatch warns and records nothing", func(t *testing.T) {
		ctx, repo, _ := flakyRepo(t)
		gitIn(t, repo, "remote", "add", "origin", "git@github.com:o/r.git")
		ctx, cfg, p, _ := commitPlan(t, ctx, repo)
		if _, err := Run(ctx, "main", false, false, false); err != nil {
			t.Fatal(err)
		}
		extra := gitIn(t, repo, "commit-tree", "--no-gpg-sign", "-p", BackupRef+"one", "-m", "c", gitIn(t, repo, "rev-parse", "one^{tree}"))
		gitIn(t, repo, "update-ref", "refs/heads/extra", extra)
		identity := recordCommitted(t, p, repo, "extra")
		shape, err := fullShape(ctx, "main", "one")
		if err != nil {
			t.Fatal(err)
		}
		path, err := statePath(ctx, "pending", "one")
		if err != nil {
			t.Fatal(err)
		}
		pass := &pending{Branch: "one", Start: "main", Shape: shape, Before: map[string]string{"one": extra}, Dir: repo}
		if err := writeJSON(path, pass); err != nil {
			t.Fatal(err)
		}
		stderr := captureStderr(t)
		if _, _, err := finish(ctx, pass, path); err != nil {
			t.Fatalf("a rewrite that does not pair up failed the pass: %v", err)
		}
		if out := stderr(); !strings.Contains(out, "could not record the commit rewrites in the plans") || !strings.Contains(out, "count differs") {
			t.Errorf("no warning on stderr:\n%s", out)
		}
		assertNoPending(t, ctx, "one")
		got, err := cfg.Commits(plans.CommitOptions{Revision: identity.Revision})
		if err != nil || len(got) != 1 || got[0].Revision != identity.Revision {
			t.Errorf("a rewrite was recorded: %+v, %v", got, err)
		}
	})
}

// recordCommitted logs ref as committed in p, as the commit hook does, and
// returns the event.
func recordCommitted(t *testing.T, p plans.Plan, repo, ref string) plans.Event {
	t.Helper()
	identity, err := plans.CommitIdentity(t.Context(), repo, ref)
	if err != nil {
		t.Fatal(err)
	}
	identity.Kind = "committed"
	if _, err := p.Record("", identity, time.Now()); err != nil {
		t.Fatal(err)
	}
	return identity
}

// assertNoPending fails unless branch has no pass left to resume.
func assertNoPending(t *testing.T, ctx context.Context, branch string) {
	t.Helper()
	path, err := statePath(ctx, "pending", branch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("pending state left at %s: %v", path, err)
	}
}

// plansConfig points agent-tools at c, through the config file in the
// config home ctx bakes in (bakedHome), where a baked build reads it, and
// returns ctx with that home and the file.
func plansConfig(t *testing.T, ctx context.Context, c plans.Config) (context.Context, string) {
	t.Helper()
	ctx, home := bakedHome(t, ctx)
	config := filepath.Join(home, "agent-tools", "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return ctx, config
}

// commitPlan makes a plan for repo in plans of the test's own, which the
// context it returns points agent-tools at through config (plansConfig).
func commitPlan(t *testing.T, ctx context.Context, repo string) (_ context.Context, c plans.Config, p plans.Plan, config string) {
	t.Helper()
	repo, _ = filepath.EvalSymlinks(repo)
	c = plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	ctx, config = plansConfig(t, ctx, c)
	made, err := c.New(t.Context(), plans.NewOptions{Title: "Link commits", Slug: "links", Worktrees: []string{repo}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p, err = c.Open(made.Plan); err != nil {
		t.Fatal(err)
	}
	return ctx, c, p, config
}
