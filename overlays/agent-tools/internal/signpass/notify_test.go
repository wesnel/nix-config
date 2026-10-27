package signpass

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/gitenv/gitenvtest"
	"github.com/wesnel/agent-tools/internal/plans"
)

// requested points agent-tools at a plans directory of the test's own,
// with a plan for session abc, and returns that plan and the ID of a
// sign-pass requested in it.
func requested(t *testing.T, dir string) (plans.Plan, string) {
	t.Helper()
	c := plans.Config{Dir: filepath.Join(dir, "agents"), Host: "box"}
	config := filepath.Join(dir, "config.json")
	data, _ := json.Marshal(map[string]any{"plans": c})
	os.WriteFile(config, data, 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	made, err := c.New(plans.NewOptions{Title: "Add the loop", Slug: "loop", Sessions: []string{"claude:abc"},
		Harness: "claude"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := c.Open(made.Plan)
	id, err := c.RequestSign("abc", "", []string{"one"}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p, id
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
	t.Run("signed and pushed", func(t *testing.T) {
		dir := t.TempDir()
		global := signingConfig(t, dir)
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
		p, id := requested(t, dir)

		t.Setenv("GIT_CONFIG_GLOBAL", global)
		gitenvtest.Decoy(t)
		useDir(t, repo)
		result, err := Run("", false, true, false)
		if err != nil {
			t.Fatal(err)
		}
		if file, err := Notify(id, result, err, time.Now()); err != nil || file != p.File() {
			t.Fatalf("notify: %q %v", file, err)
		}
		if e := last(t, p); e.Kind != "signed" || e.Exit != "0" || e.Sign != id || e.Summary != "2 signed; pushed one" {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("refused before any touch", func(t *testing.T) {
		dir := t.TempDir()
		global := signingConfig(t, dir)
		repo := filepath.Join(dir, "repo")
		os.Mkdir(repo, 0o755)
		gitIn(t, repo, "init", "-q", "-b", "main")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
		gitIn(t, repo, "switch", "-qc", "one")
		gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "Add a.")
		p, id := requested(t, dir)

		t.Setenv("GIT_CONFIG_GLOBAL", global)
		useDir(t, repo)
		result, err := Run("", false, false, false)
		if err == nil {
			t.Fatal("the pass signed a subject ending in a period")
		}
		if _, err := Notify(id, result, err, time.Now()); err != nil {
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
		dir, repo, hub, _, armed := mirrored(t)
		p, id := requested(t, dir)
		NotifyID = id
		t.Cleanup(func() { NotifyID = "" })

		t.Setenv("GIT_CONFIG_GLOBAL", signingConfig(t, dir))
		useDir(t, repo)
		noTerminal(t)
		arm(t, armed, 0)
		result, err := Run("", false, true, false)
		if err == nil {
			t.Fatal("a refused SSH login without a terminal")
		}
		if _, err := Notify(id, result, err, time.Now()); err != nil {
			t.Fatal(err)
		}
		e := last(t, p)
		if e.Kind != "sign-failed" || e.Exit != "1" || !strings.Contains(e.Summary, "pushed one to "+hub) ||
			!strings.Contains(e.Summary, "`"+testBinary(t)+" --resume --push --notify "+id+" one`") {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("several branches", func(t *testing.T) {
		dir := t.TempDir()
		global := signingConfig(t, dir)
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
		p, id := requested(t, dir)

		t.Setenv("GIT_CONFIG_GLOBAL", global)
		useDir(t, repo)
		result, err := Plan("", []string{"one", "two"}, false, false)
		if err != nil {
			t.Fatal(err)
		}
		Notify(id, result, err, time.Now())
		if e := last(t, p); e.Kind != "signed" || e.Summary != "3 signed" {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("nothing to sign", func(t *testing.T) {
		dir := t.TempDir()
		p, id := requested(t, dir)
		Notify(id, map[string]string{"branch": "one", "signed": "every commit since the base is already signed"}, nil, time.Now())
		if e := last(t, p); e.Kind != "signed" || e.Summary != "nothing to sign" {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("nothing to sign, one pushed and one not", func(t *testing.T) {
		dir := t.TempDir()
		p, id := requested(t, dir)
		Notify(id, map[string]any{"branch": "one", "signed": "every commit since the base is already signed",
			"pushed": map[string]string{"one": "origin refs/heads/one", "two": "not pushed: no upstream"}}, nil, time.Now())
		if e := last(t, p); e.Kind != "signed" || e.Summary != "nothing to sign; pushed one" {
			t.Errorf("logged %+v", e)
		}
	})

	t.Run("an ID no plan holds", func(t *testing.T) {
		dir := t.TempDir()
		p, id := requested(t, dir)
		if _, err := Notify(p.Name+".0badc0de", map[string]any{"signed": 1}, nil, time.Now()); err == nil {
			t.Error("logged a result for a sign-pass never requested")
		}
		if _, err := Notify("not-an-id", map[string]any{"signed": 1}, nil, time.Now()); err == nil {
			t.Error("took a malformed ID")
		}
		if e := last(t, p); e.Kind != "sign-requested" || e.Sign != id {
			t.Errorf("logged %+v", e)
		}
	})
}
