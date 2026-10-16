package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkedAll(t *testing.T, leaveOut string) string {
	t.Helper()
	var ids []string
	for _, c := range Categories {
		if c != leaveOut {
			ids = append(ids, c)
		}
	}
	return string(must(json.Marshal(ids)))
}

// securityPrompt holds prompt to the general rules and the security
// checklist, with every category, in place of the general checks.
func securityPrompt(t *testing.T, prompt string) {
	t.Helper()
	general := Contract[strings.Index(Contract, "## Checks"):]
	if !strings.HasPrefix(prompt, Contract[:strings.Index(Contract, "## Checks")]) || strings.Contains(prompt, general[:200]) ||
		!strings.Contains(prompt, "## Checklist") {
		t.Errorf("the prompt is not the rules and the security checklist:\n%s", prompt)
	}
	for _, c := range Categories {
		if !strings.Contains(prompt, "`"+c+"`") {
			t.Errorf("the prompt lacks the category %s", c)
		}
	}
}

// A security review runs the security checklist with the general rules,
// and requires every category to be named as checked.
func TestFocusCodex(t *testing.T) {
	t.Run("checked", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_CODEX", "security")
		t.Setenv("FAKE_CHECKED", checkedAll(t, ""))
		code, out, stderr := freshReview(t, "--focus", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 3 || out.Focus != "security" || len(out.Findings) != 1 ||
			out.Findings[0]["id"] != "security-1" || out.Findings[0]["kind"] != "vulnerability" {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		securityPrompt(t, string(must(os.ReadFile(filepath.Join(f.log, "prompt")))))
		schema := string(must(os.ReadFile(filepath.Join(f.log, "schema"))))
		if !strings.Contains(schema, `"checked"`) || !strings.Contains(schema, `"vulnerability"`) || strings.Contains(schema, `"routes"`) {
			t.Errorf("schema:\n%s", schema)
		}
	})

	t.Run("a category left out", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_CODEX", "security")
		t.Setenv("FAKE_CHECKED", checkedAll(t, "secrets"))
		code, _, stderr := freshReview(t, "--focus", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 1 || !strings.Contains(stderr, "secrets") {
			t.Errorf("exit %d\n%s", code, stderr)
		}
	})

	// A focused review judges no test adequacy, so red-check's results
	// given to it would be read by nothing.
	t.Run("red-check results", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("FAKE_CODEX", "security")
		t.Setenv("FAKE_CHECKED", checkedAll(t, ""))
		file := filepath.Join(f.repo, ".git", "red-check.json")
		os.WriteFile(file, []byte(`{"commits": []}`), 0o644)
		code, out, stderr := freshReview(t, "--focus", "security", "--red-check", file, "--goal", ".git/goal.md", f.base+"..HEAD")
		f.refused(t, code, out, stderr)
	})

	t.Run("an unknown focus", func(t *testing.T) {
		f := setUp(t, true)
		if code, _, stderr := freshReview(t, "--focus", "style", "--goal", ".git/goal.md", f.base+"..HEAD"); code != 2 {
			t.Errorf("exit %d\n%s", code, stderr)
		}
	})
}

// --print-prompt --focus security prints the security review's prompt, the
// contract a security-reviewer agent works to.
func TestFocusPrompt(t *testing.T) {
	f := setUp(t, false)
	var stdout, stderr strings.Builder
	if code := Main(t.Context(), []string{"--print-prompt", "--focus", "security", "--goal", ".git/goal.md", f.base + "..HEAD"},
		nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	securityPrompt(t, stdout.String())
}
