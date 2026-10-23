package review

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/loop"
	"github.com/wesnel/agent-tools/internal/routes"
)

// checkedOf is every category of route's checklist but leaveOut, as a
// reply's checked.
func checkedOf(t *testing.T, route, leaveOut string) string {
	t.Helper()
	ids := []string{}
	for _, c := range focuses[route].categories {
		if c != leaveOut {
			ids = append(ids, c)
		}
	}
	return string(must(json.Marshal(ids)))
}

// description writes a pull request description to a new file in the
// fixture's .git and returns its path.
func description(t *testing.T, f *fixture, text string) string {
	t.Helper()
	file, err := os.CreateTemp(filepath.Join(f.repo, ".git"), "description-*.md")
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString(text)
	file.Close()
	return file.Name()
}

const reverting = "Grows the code.\n\n## Verification beyond CI\n\n- ran it\n\n## Reverting\n\n" +
	"Revert the commit: no reader keeps the third line. The token ghp_" + "cccccccccccccccccccccccccccccccccccc" +
	" stays.\n\n## Limits\n\nNot the fourth line.\n"

func compatReply(t *testing.T, checked string) string {
	t.Helper()
	return `{"findings": [{"id": "1", "path": "code.txt", "line": 3, "endLine": null, "kind": "incompatible", "claim": "c",
		"evidence": "e", "about": null, "reopens": null}], "unverified": [], "checked": ` + checked + `}`
}

// compatSays has the fake claude reply as the compat-reviewer agent with
// reply, a JSON object.
func compatSays(t *testing.T, f *fixture, reply string) {
	t.Helper()
	var structured map[string]any
	must(0, json.Unmarshal([]byte(reply), &structured))
	f.claudeSays(t, "compat-reviewer", claudeResult(structured, "Reviewed."))
}

// Each known route has a focused review and a reviewer type, and each
// focused review a known route.
func TestFocusesKnown(t *testing.T) {
	if got := slices.Sorted(maps.Keys(focuses)); !slices.Equal(got, slices.Sorted(slices.Values(routes.Known))) {
		t.Errorf("focuses %v, routes %v", got, routes.Known)
	}
	for _, route := range routes.Known {
		if reviewerTypes[route].focus != route {
			t.Errorf("no reviewer type runs the %s focus", route)
		}
	}
}

// A compat review runs headless as the compat-reviewer agent, with the
// compat checklist, the general rules and the description's Reverting
// section, and requires every category as checked.
func TestFocusCompat(t *testing.T) {
	t.Run("checked", func(t *testing.T) {
		f := setUp(t, true)
		compatSays(t, f, compatReply(t, checkedOf(t, "compat", "")))
		code, run, stderr := runDelegate(t, "--reviewer", "compat", "--goal", ".git/goal.md", "--description",
			description(t, f, reverting), f.base+"..HEAD")
		out := run.review("compat")
		if code != 3 || out.Focus != "compat" || len(out.Findings) != 1 || out.Findings[0]["id"] != "compat-1" ||
			out.Findings[0]["kind"] != "incompatible" {
			t.Fatalf("exit %d, %+v\n%s", code, run, stderr)
		}
		args := claudeArgs(t, f, "compat-reviewer", 1)
		if !slices.Contains(args, "compat-reviewer") || !slices.Contains(args, "/store/review-agents.json") {
			t.Errorf("claude was not run as the compat-reviewer agent: %q", args[:len(args)-1])
		}
		prompt := args[len(args)-1]
		if !strings.HasPrefix(prompt, generalRules()) || !strings.Contains(prompt, "This is a compatibility review") {
			t.Errorf("the prompt is not the rules and the compat checks:\n%s", prompt)
		}
		for _, c := range focuses["compat"].categories {
			if !strings.Contains(prompt, "`"+c+"`") {
				t.Errorf("the prompt lacks the category %s", c)
			}
		}
		for _, want := range []string{"## Reverting", "no reader keeps the third line", "## Overlapping branches", "[REDACTED"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("the prompt lacks %q:\n%s", want, prompt)
			}
		}
		for _, not := range []string{"Not the fourth line", "ran it", "## Threat notes", "cccccccc"} {
			if strings.Contains(prompt, not) {
				t.Errorf("the prompt holds %q:\n%s", not, prompt)
			}
		}
		i := slices.Index(args, "--json-schema")
		if i < 0 || !strings.Contains(args[i+1], `"incompatible"`) || strings.Contains(args[i+1], `"vulnerability"`) ||
			!strings.Contains(args[i+1], `"reverting"`) {
			t.Errorf("schema: %q", args)
		}
		f.checkCleanedUp(t)
	})

	t.Run("a category left out", func(t *testing.T) {
		f := setUp(t, true)
		compatSays(t, f, compatReply(t, checkedOf(t, "compat", "reverting")))
		code, run, stderr := runDelegate(t, "--reviewer", "compat", "--goal", ".git/goal.md", "--description",
			description(t, f, reverting), f.base+"..HEAD")
		if code != 4 || len(run.NotRun) != 1 || run.NotRun[0]["reviewer"] != "compat" || !strings.Contains(stderr, "reverting") {
			t.Errorf("exit %d, %+v\n%s", code, run, stderr)
		}
	})

	// The kind is the compat review's: a security review's reply that
	// uses it has the finding dropped.
	t.Run("its kind elsewhere", func(t *testing.T) {
		f := setUp(t, true)
		reply := map[string]any{}
		must(0, json.Unmarshal([]byte(compatReply(t, "[]")), &reply))
		reply["checked"] = Categories
		f.claudeSays(t, "security-reviewer", claudeResult(reply, ""))
		code, run, stderr := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if out := run.review("security"); code != 0 || len(out.Findings) != 0 || len(out.Dropped) != 1 {
			t.Errorf("exit %d, %+v\n%s", code, run, stderr)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f := setUp(t, true)
		compatSays(t, f, compatReply(t, checkedOf(t, "compat", "")))
		given := f.base + "..HEAD"
		for what, args := range map[string][]string{
			"no description": {"--reviewer", "compat", "--goal", ".git/goal.md", given},
			"no Reverting section": {"--reviewer", "compat", "--goal", ".git/goal.md", "--description",
				description(t, f, "Grows the code.\n\n## Limits\n\nNone.\n"), given},
			"an empty Reverting section": {"--reviewer", "compat", "--goal", ".git/goal.md", "--description",
				description(t, f, "Grows the code.\n\n## Reverting\n\n## Limits\n\nNone.\n"), given},
			"no description, printed": {"--print-prompt", "--reviewer", "compat", "--goal", ".git/goal.md", given},
		} {
			code, _, stderr := runDelegate(t, args...)
			refused(t, what, code, stderr)
			if !strings.Contains(stderr, "description") || strings.Contains(stderr, "usage:") {
				t.Errorf("%s: %s", what, stderr)
			}
		}
		if _, err := os.Stat(filepath.Join(f.log, "compat-reviewer-runs")); err == nil {
			t.Error("a refused compat review ran its reviewer")
		}
	})

	t.Run("printed", func(t *testing.T) {
		f := setUp(t, false)
		code, stdout, stderr := delegateCmd(t, "--print-prompt", "--reviewer", "compat", "--goal", ".git/goal.md",
			"--description", description(t, f, reverting), f.base+"..HEAD")
		if code != 0 || !strings.Contains(stdout, "This is a compatibility review") ||
			!strings.Contains(stdout, "no reader keeps the third line") {
			t.Errorf("exit %d\n%s\n%s", code, stdout, stderr)
		}
	})

	// A routed compat review with no description says why and how to give
	// one, and no fallback that would be refused again; a compat route not
	// run names --description too.
	t.Run("routed without a description", func(t *testing.T) {
		f := setUp(t, true)
		writeSettings(t, f, `{"routes": [{"route": "compat", "paths": ["code.txt"], "why": "it is read"}]}`)
		t.Setenv("FAKE_CODEX", "none")
		// The change is a branch's, from where main stands.
		run(t, f.repo, "git", "checkout", "-q", "-b", "feat")
		run(t, f.repo, "git", "branch", "-f", "main", f.base)
		code, run, stderr := runDelegate(t, "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 4 || len(run.NotRun) != 1 || run.NotRun[0]["reviewer"] != "compat" {
			t.Fatalf("exit %d, %+v\n%s", code, run, stderr)
		}
		if !strings.Contains(stderr, "the compat review could not run: a compat review checks") ||
			strings.Contains(stderr, "--print-prompt --reviewer compat") || strings.Contains(stderr, "outside any sandbox") {
			t.Errorf("stderr:\n%s", stderr)
		}
		code, _, stderr = runDelegate(t, "--reviewer", "codex", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 0 || !strings.Contains(stderr, "run `git delegate --reviewer compat --goal FILE --description FILE "+
			f.base+"..HEAD`") {
			t.Errorf("exit %d\n%s", code, stderr)
		}
	})

	// A type that cannot run here is hinted with a command that runs as
	// given, its goal included.
	t.Run("a hint that runs", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("PR_LOOP_SESSION", "1")
		code, _, stderr := runDelegate(t, "--reviewer", "codex", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 4 || !strings.Contains(stderr, "`git delegate --print-prompt --reviewer codex --goal FILE "+f.base+"..HEAD`") {
			t.Errorf("exit %d\n%s", code, stderr)
		}
	})

	// Where it cannot run headless, the hint names the agent type and a
	// command that prints its prompt, the description included.
	t.Run("the agent type named", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("AGENT_TOOLS_CONFIG", filepath.Join(t.TempDir(), "none.json"))
		code, _, stderr := runDelegate(t, "--reviewer", "compat", "--goal", ".git/goal.md", "--description",
			description(t, f, reverting), f.base+"..HEAD")
		if code != 4 || !strings.Contains(stderr, "start a new `compat-reviewer` sub-agent on what `git delegate "+
			"--print-prompt --reviewer compat --goal FILE --description FILE "+f.base+"..HEAD` prints") {
			t.Errorf("exit %d\n%s", code, stderr)
		}
	})

	// A pull request's own description is the one checked, unless
	// --description names another.
	t.Run("a pull request", func(t *testing.T) {
		f := setUp(t, true)
		r := newRemote(t, nil)
		gh := setUpPR(t, r)
		old := refreshThreads
		refreshThreads = func(string, int) ([]loop.Thread, error) { return nil, nil }
		t.Cleanup(func() { refreshThreads = old })
		compatSays(t, f, `{"findings": [], "unverified": [], "checked": `+checkedOf(t, "compat", "")+`}`)

		code, _, stderr := runDelegate(t, "--reviewer", "compat", "o/r#7")
		refused(t, "a pull request without a Reverting section", code, stderr)
		if !strings.Contains(stderr, "the description of o/r#7") {
			t.Errorf("the refusal does not name the pull request's description: %s", stderr)
		}

		gh.pr["body"] = reverting
		if code, run, stderr := runDelegate(t, "--reviewer", "compat", "o/r#7"); code != 0 || run.review("compat").Focus != "compat" {
			t.Fatalf("exit %d, %+v\n%s", code, run, stderr)
		}
		if args := claudeArgs(t, f, "compat-reviewer", 1); !strings.Contains(args[len(args)-1], "no reader keeps the third line") {
			t.Errorf("the prompt lacks the pull request's Reverting section:\n%s", args[len(args)-1])
		}

		gh.pr["body"] = "No section."
		if code, _, stderr := runDelegate(t, "--reviewer", "compat", "--description", description(t, f, reverting),
			"o/r#7"); code != 0 {
			t.Errorf("--description in place of the pull request's: exit %d\n%s", code, stderr)
		}
	})
}
