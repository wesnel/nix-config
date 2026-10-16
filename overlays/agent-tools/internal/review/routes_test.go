package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/routes"
)

// routedFixture is setUp's repository with origin/main at its first
// commit, as github.com/o/r, a commit running a program above the change,
// which it returns, and a config whose repos settings are settings.
func routedFixture(t *testing.T, settings string) (*fixture, string) {
	t.Helper()
	f := setUp(t, true)
	run(t, f.repo, "git", "update-ref", "refs/remotes/origin/main", f.base)
	os.WriteFile(filepath.Join(f.repo, "run.go"), []byte("package main\n\nimport \"os/exec\"\n\nvar _ = exec.Command\n"), 0o644)
	run(t, f.repo, "git", "add", "run.go")
	run(t, f.repo, "git", "commit", "-q", "-m", "Run a helper")
	exec := run(t, f.repo, "git", "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(f.repo, "later.txt"), []byte("later\n"), 0o644)
	run(t, f.repo, "git", "add", "later.txt")
	run(t, f.repo, "git", "commit", "-q", "-m", "Add a later note")
	writeConfig(t, `{"o/r": `+settings+`}`)
	return f, exec
}

// A general review routes the change by its scan, from where the change
// starts whatever BASE it is given, and by its reviewer's routes. Without
// --reviewer, delegate runs the types the scan picks with it, and says how
// to run one only the reviewer named.
func TestRouting(t *testing.T) {
	t.Run("scan and reviewer", func(t *testing.T) {
		f, exec := routedFixture(t, `{}`)
		t.Setenv("FAKE_CODEX", "routed")
		// BASE above the commit that runs a program.
		code, run_, stderr := runDelegate(t, "--goal", ".git/goal.md", "HEAD~1..HEAD")
		out := run_.review("codex")
		if code != 0 {
			t.Fatalf("exit %d\n%s", code, stderr)
		}
		head := run(t, f.repo, "git", "rev-parse", "HEAD")
		if len(out.Routes) != 1 || out.Routes[0].Route != "security" ||
			!slices.Equal(out.Routes[0].Commits, []string{exec, head}) || len(out.Routes[0].Why) != 2 {
			t.Errorf("routes %+v, want security for %s (the scan) and %s (the reviewer's paths)", out.Routes, exec, head)
		}
		if run_.review("security").Reviewer != "security" || !strings.Contains(stderr, "reviewing for security too") {
			t.Errorf("the routed security review did not run: %+v\n%s", run_, stderr)
		}
		if !strings.Contains(stderr, `"style"`) {
			t.Errorf("stderr does not report the unknown route: %s", stderr)
		}
	})

	t.Run("named only by the reviewer", func(t *testing.T) {
		f, _ := routedFixture(t, `{}`)
		t.Setenv("FAKE_CODEX", "routed")
		run(t, f.repo, "git", "update-ref", "refs/remotes/origin/main", "HEAD~1")
		code, run_, stderr := runDelegate(t, "--goal", ".git/goal.md", "HEAD~1..HEAD")
		if code != 0 || len(run_.Reviews) != 2 || run_.review("security").Reviewer != "" ||
			!strings.Contains(stderr, "run `git delegate --reviewer security --goal FILE HEAD~1..HEAD`") {
			t.Errorf("exit %d, %+v\n%s", code, run_, stderr)
		}
	})

	t.Run("operator rules", func(t *testing.T) {
		f, exec := routedFixture(t, `{"routes": [{"route": "security", "paths": ["code.txt"], "why": "the code"}]}`)
		t.Setenv("FAKE_CODEX", "none")
		grow := run(t, f.repo, "git", "rev-parse", f.tip)
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 0 || len(out.Routes) != 1 || !slices.Equal(out.Routes[0].Commits, []string{grow, exec}) {
			t.Errorf("exit %d, routes %+v\n%s", code, out.Routes, stderr)
		}
	})

	t.Run("nothing to route", func(t *testing.T) {
		f, _ := routedFixture(t, `{}`)
		t.Setenv("FAKE_CODEX", "none")
		run(t, f.repo, "git", "update-ref", "refs/remotes/origin/main", "HEAD~1")
		code, out, stderr := freshReview(t, "--goal", ".git/goal.md", "HEAD~1..HEAD")
		if code != 0 || len(out.Routes) != 0 || strings.Contains(stderr, "security") {
			t.Errorf("exit %d, routes %+v\n%s", code, out.Routes, stderr)
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
