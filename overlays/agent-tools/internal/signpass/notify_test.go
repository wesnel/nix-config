package signpass

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/plans"
)

// requested points agent-tools at a plans directory of the test's own,
// with a plan for session abc, and returns ctx with it, that plan and the
// ID of a sign-pass requested in it. The config goes in the config home
// ctx bakes in (bakedHome), where a baked build reads it.
func requested(t *testing.T, ctx context.Context, dir string) (context.Context, plans.Plan, string) {
	t.Helper()
	c := plans.Config{Dir: filepath.Join(dir, "agents"), Host: "box"}
	ctx, home := bakedHome(t, ctx)
	config := filepath.Join(home, "agent-tools", "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	if err := os.MkdirAll(filepath.Dir(config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, data, 0o644); err != nil {
		t.Fatal(err)
	}
	made, err := c.New(t.Context(), plans.NewOptions{Title: "Add the loop", Slug: "loop", Sessions: []string{"claude:abc"},
		Harness: "claude"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c.Open(made.Plan)
	id, err := c.RequestSign(t.Context(), plans.SignRequest{Session: "abc", Branches: []string{"one"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ctx, p, id
}

// last is the newest event in p's log.
func last(t *testing.T, p plans.Plan) plans.Event {
	t.Helper()
	events, err := p.Events()
	if err != nil || len(events) == 0 {
		t.Fatalf("events: %v", err)
	}
	return events[len(events)-1]
}

// A finished pass's result lands in the plan that requested it, however
// the pass ended.
func TestNotify(t *testing.T) {
	t.Parallel()
	t.Run("signed and pushed", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ctx, global := signingConfig(t, dir)
		hub := filepath.Join(dir, "hub.git")
		gitIn(t, dir, "init", "-q", "--bare", hub)
		repo := filepath.Join(dir, "repo")
		os.Mkdir(repo, 0o755)
		gitIn(t, repo, "init", "-q", "-b", "main")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
		gitIn(t, repo, "remote", "add", "origin", hub)
		gitIn(t, repo, "push", "-q", "-u", "origin", "main")
		gitIn(t, repo, "switch", "-qc", "one")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "O1")
		gitIn(t, repo, "push", "-q", "-u", "origin", "one")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "O2")
		ctx, p, id := requested(t, ctx, dir)

		ctx = decoy(t, under(ctx, "GIT_CONFIG_GLOBAL="+global))
		ctx = git.At(ctx, repo)
		result, err := Run(ctx, "", false, true, false)
		if err != nil {
			t.Fatal(err)
		}
		if file, err := Notify(ctx, id, result, err, time.Now()); err != nil || file != p.File() {
			t.Fatalf("notify: %q %v", file, err)
		}
		if e := last(t, p); e.Kind != "signed" || e.Exit != "0" || e.Sign != id || e.Summary != "2 signed; pushed one" {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("refused before any touch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ctx, global := signingConfig(t, dir)
		repo := filepath.Join(dir, "repo")
		os.Mkdir(repo, 0o755)
		gitIn(t, repo, "init", "-q", "-b", "main")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
		gitIn(t, repo, "switch", "-qc", "one")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "Add a.")
		ctx, p, id := requested(t, ctx, dir)

		ctx = under(ctx, "GIT_CONFIG_GLOBAL="+global)
		ctx = git.At(ctx, repo)
		result, err := Run(ctx, "", false, false, false)
		if err == nil {
			t.Fatal("the pass signed a subject ending in a period")
		}
		if _, err := Notify(ctx, id, result, err, time.Now()); err != nil {
			t.Fatal(err)
		}
		e := last(t, p)
		if e.Kind != "sign-refused" || e.Exit != "2" || !strings.HasPrefix(e.Summary, `one's commit "Add a." would be signed`) {
			t.Errorf("logged %+v", e)
		}
	})

	// The session hears which URLs have the branch and the exact command
	// that pushes the rest.
	t.Run("pushed to one URL of two", func(t *testing.T) {
		t.Parallel()
		dir, repo, hub, _, armed, ssh := mirrored(t)
		ctx, global := signingConfig(t, dir)
		ctx, p, id := requested(t, ctx, dir)
		ctx = tuned(ctx, func(o *Options) { o.NotifyID = id })

		ctx = under(ctx, append(ssh, "GIT_CONFIG_GLOBAL="+global)...)
		ctx = noTerminal(t, git.At(ctx, repo))
		arm(t, armed, 0)
		result, err := Run(ctx, "", false, true, false)
		if err == nil {
			t.Fatal("a refused SSH login without a terminal")
		}
		if _, err := Notify(ctx, id, result, err, time.Now()); err != nil {
			t.Fatal(err)
		}
		e := last(t, p)
		if e.Kind != "sign-failed" || e.Exit != "1" || !strings.Contains(e.Summary, "pushed one to "+hub) ||
			!strings.Contains(e.Summary, "`"+testBinary(t)+" --resume --push --notify "+id+" one`") {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("several branches", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ctx, global := signingConfig(t, dir)
		repo := filepath.Join(dir, "repo")
		os.Mkdir(repo, 0o755)
		gitIn(t, repo, "init", "-q", "-b", "main")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
		gitIn(t, repo, "switch", "-qc", "one")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "O1")
		gitIn(t, repo, "switch", "-qc", "two", "main")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "T1")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "T2")
		gitIn(t, repo, "switch", "-q", "main")
		ctx, p, id := requested(t, ctx, dir)

		ctx = under(ctx, "GIT_CONFIG_GLOBAL="+global)
		ctx = git.At(ctx, repo)
		result, err := Plan(ctx, "", []string{"one", "two"}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		Notify(ctx, id, result, err, time.Now())
		if e := last(t, p); e.Kind != "signed" || e.Summary != "3 signed" {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("nothing to sign", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ctx, p, id := requested(t, t.Context(), dir)
		Notify(ctx, id, map[string]string{"branch": "one", "signed": "every commit since the base is already signed"}, nil, time.Now())
		if e := last(t, p); e.Kind != "signed" || e.Summary != "nothing to sign" {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("nothing to sign, one pushed and one not", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ctx, p, id := requested(t, t.Context(), dir)
		Notify(ctx, id, map[string]any{"branch": "one", "signed": "every commit since the base is already signed",
			"pushed": map[string]string{"one": "origin refs/heads/one", "two": "not pushed: no upstream"}}, nil, time.Now())
		if e := last(t, p); e.Kind != "signed" || e.Summary != "nothing to sign; pushed one" {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("an ID no plan holds", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		ctx, p, id := requested(t, t.Context(), dir)
		if _, err := Notify(ctx, p.Name+".0badc0de", map[string]any{"signed": 1}, nil, time.Now()); err == nil {
			t.Error("logged a result for a sign-pass never requested")
		}
		if _, err := Notify(ctx, "not-an-id", map[string]any{"signed": 1}, nil, time.Now()); err == nil {
			t.Error("took a malformed ID")
		}
		if e := last(t, p); e.Kind != "sign-requested" || e.Sign != id {
			t.Errorf("logged %+v", e)
		}
	})
}
