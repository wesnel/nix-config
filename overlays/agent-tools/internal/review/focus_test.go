package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/plans"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// TestSecurityContract holds the security contract to where the package's
// Nix passthru.securityReviewerPrompt builds it for the security-reviewer
// agent: the general contract split at its one "## Checks" line, then the
// security checks.
func TestSecurityContract(t *testing.T) {
	const heading = "\n## Checks\n"
	if n := strings.Count(Contract, heading); n != 1 {
		t.Fatalf("reviewer.md has %d %q lines; the Nix split needs one", n, strings.TrimSpace(heading))
	}
	want := Contract[:strings.Index(Contract, heading)+1] + securityChecks
	if got := focuses["security"].contract(); got != want {
		t.Errorf("focus.contract() is not the general contract up to %q and then security.md, as the Nix split builds it",
			strings.TrimSpace(heading))
	}
}

// focusFixture is setUp's repository with a plan, of no session, that
// names its worktree.
func focusFixture(t *testing.T) (*fixture, plans.Plan) {
	t.Helper()
	f := setUp(t, true)
	top := run(t, f.repo, "git", "rev-parse", "--show-toplevel")
	c := plans.Config{Dir: filepath.Join(t.TempDir(), "agents"), Host: "box"}
	config := filepath.Join(t.TempDir(), "config.json")
	os.WriteFile(config, must(json.Marshal(map[string]any{"plans": c})), 0o644)
	t.Setenv("AGENT_TOOLS_CONFIG", config)
	made, err := c.New(plans.NewOptions{Title: "Grow the code", Slug: "grow", Worktrees: []string{top}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return f, must(c.Open(made.Plan))
}

func eventsOf(t *testing.T, p plans.Plan, kind string) []plans.Event {
	t.Helper()
	var out []plans.Event
	for _, e := range must(p.Events()) {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
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

func refused(t *testing.T, what string, code int, stderr string) {
	t.Helper()
	reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-fresh-review: refused: ")
	if code != 2 || !refusal.SaysInstead(reason) {
		t.Errorf("%s: exit %d, %q", what, code, stderr)
	}
}

// A security review runs the security checklist with the general rules,
// requires every category to be named as checked, and logs a route
// receipt of its own, which no general review counts.
func TestFocusCodex(t *testing.T) {
	t.Run("checked", func(t *testing.T) {
		f, p := focusFixture(t)
		t.Setenv("FAKE_CODEX", "security")
		t.Setenv("FAKE_CHECKED", checkedAll(t, ""))
		code, out, stderr := freshReview(t, "--focus", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 3 || out.Focus != "security" || out.Round != 1 || len(out.Findings) != 1 ||
			out.Findings[0]["id"] != "security-1" || out.Findings[0]["kind"] != "vulnerability" {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		prompt := string(must(os.ReadFile(filepath.Join(f.log, "prompt"))))
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
		schema := string(must(os.ReadFile(filepath.Join(f.log, "schema"))))
		if !strings.Contains(schema, `"checked"`) || !strings.Contains(schema, `"vulnerability"`) || strings.Contains(schema, `"routes"`) {
			t.Errorf("schema:\n%s", schema)
		}
		receipts := eventsOf(t, p, "route-receipt")
		if len(receipts) != 1 || receipts[0].Focus != "security" || receipts[0].Findings != "security-1" ||
			receipts[0].Branch != "main" || receipts[0].Range != f.base+"..HEAD" {
			t.Errorf("route receipts %+v", receipts)
		}
		if got := eventsOf(t, p, "review-receipt"); len(got) != 0 {
			t.Errorf("a focused review logged a general receipt: %+v", got)
		}
		if !strings.Contains(stderr, "--review 1.security-1") {
			t.Errorf("stderr does not say how to settle the finding: %s", stderr)
		}
	})

	t.Run("a category left out", func(t *testing.T) {
		f, p := focusFixture(t)
		t.Setenv("FAKE_CODEX", "security")
		t.Setenv("FAKE_CHECKED", checkedAll(t, "secrets"))
		code, _, stderr := freshReview(t, "--focus", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 1 || !strings.Contains(stderr, "secrets") || len(eventsOf(t, p, "route-receipt")) != 0 {
			t.Errorf("exit %d\n%s", code, stderr)
		}
	})

	t.Run("an unknown focus", func(t *testing.T) {
		f, _ := focusFixture(t)
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
		f, _ := focusFixture(t)
		run(t, f.repo, "git", "remote", "add", "origin", "https://github.com/o/r.git")
		config := os.Getenv("AGENT_TOOLS_CONFIG")
		var c map[string]any
		json.Unmarshal(must(os.ReadFile(config)), &c)
		value = strings.ReplaceAll(value, "TOP", run(t, f.repo, "git", "rev-parse", "--show-toplevel"))
		c["repos"] = map[string]any{"o/r": map[string]any{"threatNotes": value}}
		os.WriteFile(config, must(json.Marshal(c)), 0o644)
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

// What the tests printed in the VM, and the items and earlier findings,
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

var nonce = regexp.MustCompile(`"issued": "([0-9a-f]+)"`)

// printFor prints the prompt for range in the fixture, with focus, and
// returns the nonce it issued.
func printFor(t *testing.T, given string, args ...string) string {
	t.Helper()
	var stdout, stderr strings.Builder
	if code := Main(t.Context(), append(append([]string{"--print-prompt", "--goal", ".git/goal.md"}, args...), given), nil,
		&stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	m := nonce.FindStringSubmatch(stdout.String())
	if m == nil {
		t.Fatalf("the prompt asks for no nonce:\n%s", stdout.String())
	}
	return m[1]
}

func reply(t *testing.T, f *fixture, text string) string {
	t.Helper()
	path := filepath.Join(f.repo, ".git", "reply.md")
	os.WriteFile(path, []byte(text), 0o644)
	return path
}

// The focus of a reply checked with --check is the one its prompt was
// issued with, never what the reply says; an issued prompt is checked
// once, and only while its change stands.
func TestFocusCheck(t *testing.T) {
	t.Run("issued for security", func(t *testing.T) {
		f, p := focusFixture(t)
		n := printFor(t, f.base+"..HEAD", "--focus", "security")
		issued := eventsOf(t, p, "review-issued")
		if len(issued) != 1 || issued[0].Focus != "security" || issued[0].Nonce != n || issued[0].Range != f.base+"..HEAD" ||
			issued[0].PatchID == "" {
			t.Fatalf("issued %+v", issued)
		}
		path := reply(t, f, `{"findings": [], "unverified": [], "checked": `+checkedAll(t, "")+`, "tip": "`+f.tip+`", "issued": "`+n+`"}`)
		code, out, stderr := freshReview(t, "--check", path, f.base+"..HEAD")
		if code != 0 || out.Focus != "security" || len(eventsOf(t, p, "route-receipt")) != 1 {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		code, _, stderr = freshReview(t, "--check", path, f.base+"..HEAD")
		refused(t, "checked again", code, stderr)
	})

	t.Run("a general prompt relabelled", func(t *testing.T) {
		f, p := focusFixture(t)
		n := printFor(t, f.base+"..HEAD")
		path := reply(t, f, `{"findings": [], "unverified": [], "focus": "security", "checked": `+checkedAll(t, "")+
			`, "tip": "`+f.tip+`", "issued": "`+n+`"}`)
		if code, out, stderr := freshReview(t, "--check", path, f.base+"..HEAD"); code != 0 || out.Focus != "" {
			t.Fatalf("exit %d, %+v\n%s", code, out, stderr)
		}
		if len(eventsOf(t, p, "route-receipt")) != 0 || len(eventsOf(t, p, "review-receipt")) != 1 {
			t.Errorf("receipts: %+v", must(p.Events()))
		}
	})

	t.Run("the change moved", func(t *testing.T) {
		f, p := focusFixture(t)
		n := printFor(t, f.base+"..HEAD", "--focus", "security")
		os.WriteFile(filepath.Join(f.repo, "code.txt"), []byte("a\nb\nd\n"), 0o644)
		run(t, f.repo, "git", "commit", "-q", "-a", "--amend", "--no-edit")
		path := reply(t, f, `{"findings": [], "unverified": [], "checked": `+checkedAll(t, "")+`, "tip": "`+f.tip+`", "issued": "`+n+`"}`)
		code, _, stderr := freshReview(t, "--check", path, f.base+"..HEAD")
		refused(t, "moved", code, stderr)
		if len(eventsOf(t, p, "route-receipt")) != 0 {
			t.Error("a receipt for a change that moved")
		}
	})

	t.Run("a nonce never issued", func(t *testing.T) {
		f, _ := focusFixture(t)
		path := reply(t, f, `{"findings": [], "unverified": [], "tip": "`+f.tip+`", "issued": "0123456789abcdef"}`)
		code, _, stderr := freshReview(t, "--check", path, f.base+"..HEAD")
		refused(t, "never issued", code, stderr)
	})

	t.Run("a focus with --check", func(t *testing.T) {
		f, _ := focusFixture(t)
		path := reply(t, f, `{"findings": [], "unverified": [], "tip": "`+f.tip+`"}`)
		if code, _, _ := freshReview(t, "--check", path, "--focus", "security", f.base+"..HEAD"); code != 2 {
			t.Errorf("exit %d", code)
		}
	})

	// A focused review's receipt needs a plan to go in, so its prompt is
	// not issued without one.
	t.Run("no plan", func(t *testing.T) {
		f := setUp(t, false)
		var stdout, stderr strings.Builder
		code := Main(t.Context(), []string{"--print-prompt", "--focus", "security", "--goal", ".git/goal.md", f.base + "..HEAD"},
			nil, &stdout, &stderr)
		refused(t, "no plan", code, stderr.String())
	})
}
