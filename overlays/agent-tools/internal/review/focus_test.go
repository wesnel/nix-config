package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/refusal"
)

func refused(t *testing.T, what string, code int, stderr string) {
	t.Helper()
	reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: refused: ")
	if code != 2 || !refusal.SaysInstead(reason) {
		t.Errorf("%s: exit %d, %q", what, code, stderr)
	}
}

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

// The security prompt carries the operator's threat notes, redacted, or
// says there are none, and the overlapping branches; notes the repository
// holds are refused.
func TestThreatNotesPrompt(t *testing.T) {
	notes := func(t *testing.T, value string) *fixture {
		t.Helper()
		f := setUp(t, true)
		run(t, f.repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
		value = strings.ReplaceAll(value, "TOP", run(t, f.repo, "git", "rev-parse", "--show-toplevel"))
		config := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(config, must(json.Marshal(map[string]any{"repos": map[string]any{"o/r": map[string]any{"threatNotes": value}}})), 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		return f
	}
	printed := func(t *testing.T, f *fixture) (int, string, string) {
		t.Helper()
		var stdout, stderr strings.Builder
		code := Main(t.Context(), []string{"--print-prompt", "--focus", "security", "--goal", ".git/goal.md", f.base + "..HEAD"},
			nil, &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}

	t.Run("inline", func(t *testing.T) {
		f := notes(t, "T1: the token ghp_"+strings.Repeat("b", 36)+" never leaves the host")
		code, prompt, stderr := printed(t, f)
		if code != 0 || !strings.Contains(prompt, "## Threat notes") || !strings.Contains(prompt, "T1: the token [REDACTED") ||
			strings.Contains(prompt, "bbbbbbbb") || !strings.Contains(prompt, "## Overlapping branches") {
			t.Errorf("exit %d\n%s\n%s", code, prompt, stderr)
		}
	})

	t.Run("none", func(t *testing.T) {
		f := notes(t, "")
		if code, prompt, stderr := printed(t, f); code != 0 || !strings.Contains(prompt, "keeps no threat notes") {
			t.Errorf("exit %d\n%s\n%s", code, prompt, stderr)
		}
	})

	t.Run("in the repository", func(t *testing.T) {
		f := notes(t, "TOP/THREATS.md")
		code, _, stderr := printed(t, f)
		refused(t, "in the repository", code, stderr)
	})
}

// What the tests printed, and the items and earlier findings,
// which carry CI logs and threads, reach the reviewer with known secret
// shapes taken out.
func TestRedCheckRedacted(t *testing.T) {
	text := prompt(inputs{r: &span{given: "a..b", base: "a", tip: "b"}, goal: "g",
		redCheck: `{"output": "AWS_SECRET_KEY=hunter2 and AKIA` + strings.Repeat("Q", 16) + `"}`,
		items:    "CI log: Authorization: Bearer itemsecret1",
		prior:    "1. the log printed password: priorsecret2"})
	for _, secret := range []string{"hunter2", "QQQQQQQQ", "itemsecret1", "priorsecret2"} {
		if strings.Contains(text, secret) {
			t.Errorf("prompt keeps %s:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "CI log: Authorization: [REDACTED]") || !strings.Contains(text, "the log printed password: [REDACTED]") {
		t.Errorf("prompt:\n%s", text)
	}
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
