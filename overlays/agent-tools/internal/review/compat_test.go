package review

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

// Each known route has a focused review, and each focused review a known
// route.
func TestFocusesKnown(t *testing.T) {
	if got := slices.Sorted(maps.Keys(focuses)); !slices.Equal(got, slices.Sorted(slices.Values(routes.Known))) {
		t.Errorf("focuses %v, routes %v", got, routes.Known)
	}
}

// A compat review runs the compat checklist with the general rules and
// the description's Reverting section, requires every category as
// checked, and logs a route receipt of its own.
func TestFocusCompat(t *testing.T) {
	t.Run("checked", func(t *testing.T) {
		f, p := focusFixture(t)
		t.Setenv("FAKE_CODEX", "claims")
		t.Setenv("FAKE_REPLY", compatReply(t, checkedOf(t, "compat", "")))
		code, out, stderr := freshReview(t, "--focus", "compat", "--goal", ".git/goal.md", "--description",
			description(t, f, reverting), f.base+"..HEAD")
		if code != 3 || out.Focus != "compat" || out.Round != 1 || len(out.Findings) != 1 ||
			out.Findings[0]["id"] != "compat-1" || out.Findings[0]["kind"] != "incompatible" {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		prompt := string(must(os.ReadFile(filepath.Join(f.log, "prompt"))))
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
		schema := string(must(os.ReadFile(filepath.Join(f.log, "schema"))))
		if !strings.Contains(schema, `"incompatible"`) || strings.Contains(schema, `"vulnerability"`) ||
			!strings.Contains(schema, `"reverting"`) {
			t.Errorf("schema:\n%s", schema)
		}
		receipts := eventsOf(t, p, "route-receipt")
		if len(receipts) != 1 || receipts[0].Focus != "compat" || receipts[0].Findings != "compat-1" {
			t.Errorf("route receipts %+v", receipts)
		}
	})

	t.Run("a category left out", func(t *testing.T) {
		f, p := focusFixture(t)
		t.Setenv("FAKE_CODEX", "claims")
		t.Setenv("FAKE_REPLY", compatReply(t, checkedOf(t, "compat", "reverting")))
		code, _, stderr := freshReview(t, "--focus", "compat", "--goal", ".git/goal.md", "--description",
			description(t, f, reverting), f.base+"..HEAD")
		if code != 1 || !strings.Contains(stderr, "reverting") || len(eventsOf(t, p, "route-receipt")) != 0 {
			t.Errorf("exit %d\n%s", code, stderr)
		}
	})

	// The kind is the compat review's: a general review's reply, or a
	// security review's, that uses it has the finding dropped.
	t.Run("its kind elsewhere", func(t *testing.T) {
		f, _ := focusFixture(t)
		t.Setenv("FAKE_CODEX", "claims")
		t.Setenv("FAKE_REPLY", compatReply(t, checkedAll(t, "")))
		code, out, stderr := freshReview(t, "--focus", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 0 || len(out.Findings) != 0 || len(out.Dropped) != 1 {
			t.Errorf("exit %d, %+v\n%s", code, out, stderr)
		}
	})

	t.Run("refused", func(t *testing.T) {
		f, _ := focusFixture(t)
		t.Setenv("FAKE_CODEX", "claims")
		t.Setenv("FAKE_REPLY", compatReply(t, checkedOf(t, "compat", "")))
		given := f.base + "..HEAD"
		for what, args := range map[string][]string{
			"no description": {"--focus", "compat", "--goal", ".git/goal.md", given},
			"no Reverting section": {"--focus", "compat", "--goal", ".git/goal.md", "--description",
				description(t, f, "Grows the code.\n\n## Limits\n\nNone.\n"), given},
			"an empty Reverting section": {"--focus", "compat", "--goal", ".git/goal.md", "--description",
				description(t, f, "Grows the code.\n\n## Reverting\n\n## Limits\n\nNone.\n"), given},
			"no description, printed": {"--print-prompt", "--focus", "compat", "--goal", ".git/goal.md", given},
			"a description for security": {"--focus", "security", "--goal", ".git/goal.md", "--description",
				description(t, f, reverting), given},
			"a description for a general review": {"--goal", ".git/goal.md", "--description",
				description(t, f, reverting), given},
		} {
			code, _, stderr := freshReview(t, args...)
			refused(t, what, code, stderr)
			if !strings.Contains(stderr, "description") || strings.Contains(stderr, "usage:") {
				t.Errorf("%s: %s", what, stderr)
			}
		}
	})

	t.Run("printed and checked", func(t *testing.T) {
		f, p := focusFixture(t)
		n := printFor(t, f.base+"..HEAD", "--focus", "compat", "--description", description(t, f, reverting))
		path := reply(t, f, `{"findings": [], "unverified": [], "checked": `+checkedOf(t, "compat", "")+`, "tip": "`+f.tip+
			`", "issued": "`+n+`"}`)
		code, out, stderr := freshReview(t, "--check", path, f.base+"..HEAD")
		if code != 0 || out.Focus != "compat" || len(eventsOf(t, p, "route-receipt")) != 1 {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
	})
}
