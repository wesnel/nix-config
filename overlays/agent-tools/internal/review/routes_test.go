package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/routes"
)

// routedFixture is setUp's repository with origin/main at its first
// commit, as github.com/o/r, a commit running a program above the change,
// and a config whose repos settings are settings and whose plan, which it
// returns, names the worktree.
func routedFixture(t *testing.T, settings string) (*fixture, plans.Plan, string) {
	t.Helper()
	f := setUp(t, true)
	run(t, f.repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
	run(t, f.repo, "git", "update-ref", "refs/remotes/origin/main", f.base)
	os.WriteFile(filepath.Join(f.repo, "run.go"), []byte("package main\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n"), 0o644)
	run(t, f.repo, "git", "add", "run.go")
	run(t, f.repo, "git", "commit", "-q", "-m", "Run a helper")
	exec := run(t, f.repo, "git", "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(f.repo, "later.txt"), []byte("later\n"), 0o644)
	run(t, f.repo, "git", "add", "later.txt")
	run(t, f.repo, "git", "commit", "-q", "-m", "Add a later note")
	top := run(t, f.repo, "git", "rev-parse", "--show-toplevel")
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, []byte(`{"plans": `+string(must(json.Marshal(c)))+`, "repos": {"o/r": `+settings+`}}`), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	made, err := c.New(plans.NewOptions{Title: "Grow the code", Slug: "grow", Worktrees: []string{top}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return f, must(c.Open(made.Plan)), exec
}

func routedEvents(t *testing.T, p plans.Plan) []plans.Event {
	t.Helper()
	var out []plans.Event
	for _, e := range must(p.Events()) {
		if e.Kind == "routed" {
			out = append(out, e)
		}
	}
	return out
}

// A general review routes the change by its scan, from where the change
// starts whatever BASE it is given, and by its reviewer's routes, prints
// them, says how to run each, and logs them in the plan.
func TestRouting(t *testing.T) {
	t.Run("scan and reviewer", func(t *testing.T) {
		f, p, exec := routedFixture(t, `{}`)
		t.Setenv("FAKE_CODEX", "routed")
		// BASE above the commit that runs a program.
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", "HEAD~1..HEAD")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		head := run(t, f.repo, "git", "rev-parse", "HEAD")
		if len(out.Routes) != 1 || out.Routes[0].Route != "security" ||
			!slices.Equal(out.Routes[0].Commits, []string{exec, head}) || len(out.Routes[0].Why) != 2 {
			t.Errorf("routes %+v, want security for %s (the scan) and %s (the reviewer's paths)", out.Routes, exec, head)
		}
		if !strings.Contains(stderr, "git fresh-review --focus security --goal FILE HEAD~1..HEAD") {
			t.Errorf("stderr does not say how to review it: %s", stderr)
		}
		if !strings.Contains(stderr, `"style"`) {
			t.Errorf("stderr does not report the unknown route: %s", stderr)
		}
		events := routedEvents(t, p)
		if len(events) != 1 || events[0].Branch != "main" || !strings.Contains(events[0].Routes, "security:"+exec+","+head) {
			t.Errorf("routed events %+v", events)
		}
	})

	t.Run("operator rules", func(t *testing.T) {
		f, _, exec := routedFixture(t, `{"routes": [{"route": "security", "paths": ["code.txt"], "why": "the code"}]}`)
		t.Setenv("FAKE_CODEX", "none")
		grow := run(t, f.repo, "git", "rev-parse", f.tip)
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 0 || len(out.Routes) != 1 || !slices.Equal(out.Routes[0].Commits, []string{grow, exec}) {
			t.Errorf("exit %d, routes %+v\n%s", code, out.Routes, stderr)
		}
	})

	t.Run("nothing to route", func(t *testing.T) {
		f, p, _ := routedFixture(t, `{}`)
		t.Setenv("FAKE_CODEX", "none")
		run(t, f.repo, "git", "update-ref", "refs/remotes/origin/main", "HEAD~1")
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", "HEAD~1..HEAD")
		if code != 0 || len(out.Routes) != 0 || len(routedEvents(t, p)) != 0 || strings.Contains(stderr, "--focus") {
			t.Errorf("exit %d, routes %+v, events %v\n%s", code, out.Routes, routedEvents(t, p), stderr)
		}
	})

	// Taking a route out of REVIEWS routes the next review to security.
	t.Run("REVIEWS taken out", func(t *testing.T) {
		f, p, _ := routedFixture(t, `{}`)
		t.Setenv("FAKE_CODEX", "none")
		run(t, f.repo, "git", "update-ref", "refs/remotes/origin/main", "HEAD~1")
		for _, v := range [][]string{{"security"}, nil} {
			if err := p.SetProperty("", "REVIEWS", v, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", "HEAD~1..HEAD")
		if code != 0 || len(out.Routes) != 1 || !strings.Contains(strings.Join(out.Routes[0].Why, " "), "REVIEWS") {
			t.Errorf("exit %d, routes %+v\n%s", code, out.Routes, stderr)
		}
		// Once routed, it is not routed again for that.
		code, out, stderr = freshReview(t, "--goal", ".git/goal.md", "HEAD~1..HEAD")
		if code != 0 || len(out.Routes) != 0 {
			t.Errorf("again: exit %d, routes %+v\n%s", code, out.Routes, stderr)
		}
	})

	// Settings the review cannot use refuse it, saying what to fix, rather
	// than leave out the test settings beside them.
	t.Run("a broken rule", func(t *testing.T) {
		routedFixture(t, `{"test": "go test ./...", "testFiles": ["*_test.go"], "routes": [{"route": "security", "why": "nothing"}]}`)
		t.Setenv("FAKE_CODEX", "none")
		for _, args := range [][]string{{"--print-prompt"}, nil} {
			var stdout, stderr strings.Builder
			code := Main(t.Context(), append(args, "--goal", ".git/goal.md", "HEAD~1..HEAD"), nil, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), "refused") || !strings.Contains(stderr.String(), "fix the rule") ||
				strings.Contains(stdout.String(), "no test settings") {
				t.Errorf("%v: exit %d\n%s\n%s", args, code, stdout.String(), stderr.String())
			}
		}
	})
}

// The reply's routes are held to the routes a focused review exists for.
func TestSchemaRoutes(t *testing.T) {
	var schema struct {
		Required   []string `json:"required"`
		Properties struct {
			Routes struct {
				Items struct {
					Properties struct {
						Route struct {
							Enum []string `json:"enum"`
						} `json:"route"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"routes"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(Schema, &schema); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(schema.Required, "routes") || !slices.Equal(schema.Properties.Routes.Items.Properties.Route.Enum, routes.Known) {
		t.Errorf("schema %+v, want routes required, of %v", schema, routes.Known)
	}
	if !strings.Contains(Contract, "## Routes") || !strings.Contains(Contract, `"routes": [`) {
		t.Error("the contract does not ask for routes")
	}
}

// A changed file that holds tests beside its code is named in the prompt,
// for the reviewer to judge its tests.
func TestSharedFilePrompt(t *testing.T) {
	f := setUp(t, false)
	os.WriteFile(filepath.Join(f.repo, "lib.rs"), []byte("fn f() {}\n#[cfg(test)]\nmod tests {}\n"), 0o644)
	run(t, f.repo, "git", "add", "lib.rs")
	run(t, f.repo, "git", "commit", "-q", "-m", "Add lib")
	var stdout, stderr strings.Builder
	if code := Main(t.Context(), []string{"--print-prompt", "--goal", ".git/goal.md", f.base + "..HEAD"}, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "shared-file") || !strings.Contains(stdout.String(), "- lib.rs\n") {
		t.Errorf("the prompt does not name the shared file:\n%s", stdout.String())
	}
}
