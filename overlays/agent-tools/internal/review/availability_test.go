package review

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/usage"
)

func runAvailability(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errs bytes.Buffer
	code := Main(t.Context(), append([]string{"availability"}, args...), strings.NewReader(""), &out, &errs)
	return code, out.String() + errs.String()
}

func noProviderCall(t *testing.T, f *fixture, files ...string) {
	t.Helper()
	for _, file := range files {
		if _, err := os.Stat(filepath.Join(f.log, file)); !os.IsNotExist(err) {
			t.Errorf("unavailable provider ran: %s, stat %v", file, err)
		}
	}
}

func TestProviderAvailability(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := askFixture(t, true)
			if code, out := runAvailability(t, "--disable", provider); code != 0 {
				t.Fatalf("disable provider: exit %d, %s", code, out)
			}
			for _, reviewer := range []string{provider, "diagram"} {
				code, _, errs := runDelegate(t, "--reviewer", reviewer, "--goal", ".git/goal.md", f.base+"..HEAD")
				if code != 4 || !strings.Contains(errs, "primary harness") {
					t.Errorf("%s must request native fallback: exit %d, %s", reviewer, code, errs)
				}
			}
			if code, _, errs := askCodex(t, "--reviewer", provider, "--question", ".git/question.md", "code.txt"); code != 4 {
				t.Errorf("disabled design question: exit %d, %s", code, errs)
			}
			if provider == "claude" {
				code, _, errs := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
				if code != 4 {
					t.Errorf("disabled focus: exit %d, %s", code, errs)
				}
				noProviderCall(t, f, "calls", "adversarial-reviewer-runs", "security-reviewer-runs", "review-guide-runs", "diagrammer-runs")
			} else {
				noProviderCall(t, f, "calls", "diagrammer-runs")
			}
			if code, out := runAvailability(t, "--clear", provider); code != 0 {
				t.Fatalf("clear: %d %s", code, out)
			}
			t.Setenv("FAKE_CODEX", "answer")
			f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{"answer": "Recovered.", "unverified": []string{}}, ""))
			if code, _, errs := askCodex(t, "--reviewer", provider, "--question", ".git/question.md", "code.txt"); code != 0 {
				t.Fatalf("cleared provider did not run: %d %s", code, errs)
			}
		})
	}
	t.Run("validate provider", func(t *testing.T) {
		setUp(t, true)
		if code, _ := runAvailability(t, "--disable", "../other"); code != 2 {
			t.Fatalf("invalid provider: %d", code)
		}
	})
}

func TestQuotaFallback(t *testing.T) {
	t.Run("quota after a schema error still requests fallback", func(t *testing.T) {
		f := setUp(t, true)
		f.claudeSays(t, "security-reviewer", claudeResult(nil, "No JSON."), `{"is_error":true,"errors":["API Error: 429 rate_limit_error"]}`)
		code, _, errs := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 4 || !strings.Contains(errs, "primary harness") || !strings.Contains(errs, "--print-prompt") {
			t.Fatalf("lost fallback: %d %s", code, errs)
		}
		noProviderCall(t, f, "review-guide-runs")
	})
	t.Run("Claude suppresses retry guide and later commands", func(t *testing.T) {
		f := askFixture(t, true)
		f.claudeSays(t, "security-reviewer", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"You've hit your limit · resets later"}`)
		code, _, errs := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 4 || !strings.Contains(errs, "quota") {
			t.Fatalf("quota: %d %s", code, errs)
		}
		if got := strings.TrimSpace(f.claudeLog(t, "security-reviewer", "runs")); got != "1" {
			t.Fatalf("quota was retried: %s", got)
		}
		if code, _, errs := askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt"); code != 4 {
			t.Fatalf("subsequent ask: %d %s", code, errs)
		}
		noProviderCall(t, f, "review-guide-runs", "adversarial-reviewer-runs")
	})
	t.Run("Codex terminal event blocks later invocations", func(t *testing.T) {
		f := askFixture(t, true)
		t.Setenv("FAKE_CODEX", "quota")
		if code, _, errs := askCodex(t, "--question", ".git/question.md", "code.txt"); code != 4 || !strings.Contains(errs, "quota") {
			t.Fatalf("quota ask: %d %s", code, errs)
		}
		before := string(must(os.ReadFile(filepath.Join(f.log, "calls"))))
		if code, _, errs := runDelegate(t, "--reviewer", "codex", "--goal", ".git/goal.md", f.base+"..HEAD"); code != 4 {
			t.Fatalf("later review: %d %s", code, errs)
		}
		if after := string(must(os.ReadFile(filepath.Join(f.log, "calls")))); after != before {
			t.Fatal("blocked Codex executed again, including a preflight")
		}
	})
	t.Run("tool output is not quota evidence", func(t *testing.T) {
		f := askFixture(t, true)
		t.Setenv("FAKE_CODEX", "quota-prose")
		askCodex(t, "--question", ".git/question.md", "code.txt")
		t.Setenv("FAKE_CODEX", "answer")
		if code, _, errs := askCodex(t, "--question", ".git/question.md", "code.txt"); code != 0 {
			t.Fatalf("tool output disabled Codex: %d %s", code, errs)
		}
		f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{"answer": "You've hit your limit is the example text.", "unverified": []string{}}, ""))
		for range 2 {
			if code, _, errs := askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt"); code != 0 {
				t.Fatalf("successful prose disabled Claude: %d %s", code, errs)
			}
		}
	})
}

func TestClaudeQuotaCode(t *testing.T) {
	for _, tc := range []struct {
		name, apiError string
		failed, quota  bool
	}{
		{"terminal quota with unfamiliar wording", "usage_limit_reached", true, true},
		{"success is not quota evidence", "usage_limit_reached", false, false},
		{"other terminal error is not account exhaustion", "model_requires_usage_credits", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := askFixture(t, true)
			out := must(json.Marshal(map[string]any{
				"type": "result", "subtype": "success", "is_error": tc.failed,
				"api_error": tc.apiError, "result": "Account-specific diagnostic.",
				"structured_output": map[string]any{"answer": "The file is sufficient.", "unverified": []string{}},
			}))
			f.claudeSays(t, "adversarial-reviewer", string(out))
			code, _, errs := askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt")
			if tc.quota && (code != 4 || !strings.Contains(errs, "quota")) {
				t.Fatalf("structured exhaustion was not recognized: %d %s", code, errs)
			}
			f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{"answer": "Recovered.", "unverified": []string{}}, ""))
			code, _, errs = askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt")
			wantCode, wantRuns := 0, "2"
			if tc.quota {
				wantCode, wantRuns = 4, "1"
			}
			if code != wantCode || strings.TrimSpace(f.claudeLog(t, "adversarial-reviewer", "runs")) != wantRuns {
				t.Fatalf("subsequent review: exit %d, want %d, runs %s, want %s: %s", code, wantCode,
					f.claudeLog(t, "adversarial-reviewer", "runs"), wantRuns, errs)
			}
		})
	}
}

func TestClaudeQuotaMessages(t *testing.T) {
	for _, message := range []string{"You're out of usage credits", "You're out of extra usage",
		"You've hit your channel's monthly spend limit.", "You've hit your monthly spend limit."} {
		t.Run(message, func(t *testing.T) {
			f := setUp(t, true)
			out := must(json.Marshal(map[string]any{"type": "result", "is_error": true, "result": message}))
			f.claudeSays(t, "security-reviewer", string(out))
			code, _, errs := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
			if code != 4 || !strings.Contains(errs, "quota") {
				t.Fatalf("unrecognized exhaustion: %d %s", code, errs)
			}
			if runs := strings.TrimSpace(f.claudeLog(t, "security-reviewer", "runs")); runs != "1" {
				t.Fatalf("exhausted Claude retried: %s", runs)
			}
			noProviderCall(t, f, "review-guide-runs")
		})
	}
}

func TestFallbackPrompts(t *testing.T) {
	f := askFixture(t, true)
	for _, p := range []string{"claude", "codex"} {
		if code, out := runAvailability(t, "--disable", p); code != 0 {
			t.Fatalf("disable: %d %s", code, out)
		}
	}
	code, prompt, errs := delegateCmd(t, "ask", "--print-prompt", "--question", ".git/question.md", "code.txt", "docs/design.md")
	if code != 0 || !strings.Contains(prompt, askContract) || !strings.Contains(prompt, "Two ways to add the flag.") || !strings.Contains(prompt, "a\nb\nc\n") {
		t.Fatalf("design fallback lacks contract or file snapshot: %d %s\n%s", code, errs, prompt)
	}
	code, prompt, errs = delegateCmd(t, "--print-prompt", "--reviewer", "diagram", "--goal", ".git/goal.md", f.base+"..HEAD")
	if strings.Contains(prompt, "Every citation has already been checked mechanically") || !strings.Contains(prompt, "Without mechanical results") {
		t.Error("fallback claims checks that it did not run instead of assigning them to the critic")
	}
	for _, want := range []string{drawContract, diagramChecks, `"diagrams"`, "separate fresh", "citation"} {
		if code != 0 || !strings.Contains(prompt, want) {
			t.Errorf("diagram fallback lacks %q: %d %s", want, code, errs)
		}
	}
	noProviderCall(t, f, "calls", "adversarial-reviewer-runs", "diagrammer-runs", "review-guide-runs")
}

func TestDiagramQuota(t *testing.T) {
	for _, stage := range []string{"drawing", "critique", "redraw"} {
		t.Run(stage, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv("FAKE_CODEX", "critique")
			t.Setenv("FAKE_CHECKED", `["N1","N2","E1"]`)
			quota := `{"is_error":true,"result":"You've hit your limit"}`
			switch stage {
			case "drawing":
				f.claudeSays(t, "diagrammer", quota)
			case "critique":
				f.claudeSays(t, "diagrammer", diagramsReply(codeDiagram(f.tip, 3)))
				t.Setenv("FAKE_CODEX", "quota")
			case "redraw":
				f.claudeSays(t, "diagrammer", diagramsReply(codeDiagram(f.tip, 3)), quota)
				t.Setenv("FAKE_CODEX", "fault")
			}
			code, _, errs := runDelegate(t, "--reviewer", "diagram", "--goal", ".git/goal.md", f.base+"..HEAD")
			if code != 4 || !strings.Contains(errs, "quota") || !strings.Contains(errs, "--print-prompt") {
				t.Fatalf("quota was reported as a completed diagram review: %d %s", code, errs)
			}
			if stage != "critique" {
				noProviderCall(t, f, "review-guide-runs")
			}
		})
	}
}

func TestKnownClaudeQuota(t *testing.T) {
	f := setUp(t, true)
	now := time.Now()
	if err := usage.Record(usage.File(), []usage.Window{{Name: "7d", Used: 100, ResetsAt: now.Add(time.Hour).Unix(), ObservedAt: now.Add(-time.Hour).Unix()}}); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD"); code != 4 {
		t.Fatalf("known quota: %d %s", code, errs)
	}
	noProviderCall(t, f, "security-reviewer-runs", "review-guide-runs")
	// Replace the test's snapshot with a window whose reset has passed.
	if err := os.WriteFile(usage.File(), []byte(`{"seven_day":{"used_percentage":100,"resets_at":1,"observed_at":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD"); code != 0 {
		t.Fatalf("expired window prevented recovery: %d %s", code, errs)
	}
}

func TestAvailabilityState(t *testing.T) {
	for _, tc := range []struct {
		data      string
		clearCode int
	}{
		{`{`, 0},
		{`null`, 0},
		{`{"five_hour":{}}`, 0},
		{`{"five_hor":{"used_percentage":100,"resets_at":100,"observed_at":1}}`, 0},
		{`{"five_hour":{"used_percentage":100,"resets_at":-1,"observed_at":1}}`, 0},
		{`{"five_hour":"bad","seven_day":{"used_percentage":100,"resets_at":4102444800,"observed_at":1}}`, 4},
	} {
		t.Run("corrupt usage "+tc.data, func(t *testing.T) {
			f := askFixture(t, true)
			f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{"answer": "Ready.", "unverified": []string{}}, ""))
			if err := os.MkdirAll(filepath.Dir(usage.File()), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(usage.File(), []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if code, _, errs := askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt"); code != 4 {
				t.Fatalf("corrupt usage admitted Claude: %d %s", code, errs)
			}
			noProviderCall(t, f, "adversarial-reviewer-runs")
			if err := usage.Record(usage.File(), []usage.Window{{Name: "5h", Used: 1, ResetsAt: 4102444800, ObservedAt: 1}}); err == nil {
				t.Fatal("ordinary recording repaired malformed state without explicit clear")
			}
			if got, err := os.ReadFile(usage.File()); err != nil || string(got) != tc.data {
				t.Fatalf("failed Record changed malformed evidence: %s, %v", got, err)
			}
			if code, out := runAvailability(t, "--clear", "claude"); code != 0 {
				t.Fatalf("clear malformed usage: %d %s", code, out)
			}
			if code, _, errs := askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt"); code != tc.clearCode {
				t.Fatalf("after clear: exit %d, want %d: %s", code, tc.clearCode, errs)
			}
		})
	}
	t.Run("unknown reset remains blocked", func(t *testing.T) {
		f := askFixture(t, true)
		f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{"answer": "Ready.", "unverified": []string{}}, ""))
		if err := os.MkdirAll(filepath.Dir(usage.File()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(usage.File(), []byte(`{"five_hour":{"used_percentage":100,"resets_at":0,"observed_at":1}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _, errs := askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt"); code != 4 {
			t.Fatalf("unknown reset admitted Claude: %d %s", code, errs)
		}
		noProviderCall(t, f, "adversarial-reviewer-runs")
		if code, out := runAvailability(t, "--clear", "claude"); code != 0 {
			t.Fatalf("clear unknown reset: %d %s", code, out)
		}
		if code, _, errs := askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt"); code != 0 {
			t.Fatalf("explicit clear did not restore eligibility: %d %s", code, errs)
		}
	})
	t.Run("unknown reading cannot erase a known exhausted reset", func(t *testing.T) {
		f := askFixture(t, true)
		f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{"answer": "Ready.", "unverified": []string{}}, ""))
		if err := usage.Record(usage.File(), []usage.Window{{Name: "5h", Used: 100, ResetsAt: 4102444800, ObservedAt: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := usage.Record(usage.File(), []usage.Window{{Name: "5h", Used: 100, ObservedAt: 2}}); err == nil {
			t.Error("Record accepted an unknown reset")
		}
		if code, out := runAvailability(t, "--clear", "claude"); code != 0 {
			t.Fatalf("clear: %d %s", code, out)
		}
		if code, _, errs := askCodex(t, "--reviewer", "claude", "--question", ".git/question.md", "code.txt"); code != 4 {
			t.Fatalf("clear erased known exhaustion: %d %s", code, errs)
		}
		noProviderCall(t, f, "adversarial-reviewer-runs")
	})
	t.Run("corrupt state refuses calls and explicit clear repairs it", func(t *testing.T) {
		f := askFixture(t, true)
		path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "agent-tools", "review-availability", "codex.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"reason":"unrecognized"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, _, errs := askCodex(t, "--question", ".git/question.md", "code.txt"); code != 4 || !strings.Contains(errs, "state") {
			t.Fatalf("bad state: %d %s", code, errs)
		}
		noProviderCall(t, f, "calls")
		if code, out := runAvailability(t, "--clear", "codex"); code != 0 {
			t.Fatalf("clear: %d %s", code, out)
		}
		t.Setenv("FAKE_CODEX", "answer")
		if code, _, errs := askCodex(t, "--question", ".git/question.md", "code.txt"); code != 0 {
			t.Fatalf("recovery: %d %s", code, errs)
		}
	})
	t.Run("failed persistence still suppresses later stages", func(t *testing.T) {
		f := setUp(t, true)
		path := filepath.Join(os.Getenv("XDG_STATE_HOME"), "agent-tools", "review-availability")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(path, "claude.json.lock"), 0o700); err != nil {
			t.Fatal(err)
		}
		f.claudeSays(t, "security-reviewer", `{"is_error":true,"result":"You've hit your limit"}`)
		code, _, errs := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 4 || !strings.Contains(errs, "could not remember") {
			t.Fatalf("state write failure: %d %s", code, errs)
		}
		noProviderCall(t, f, "review-guide-runs")
	})
	t.Run("concurrent disables preserve both providers", func(t *testing.T) {
		f := askFixture(t, true)
		var wg sync.WaitGroup
		for _, p := range []string{"claude", "codex", "claude", "codex"} {
			wg.Go(func() {
				if code, out := runAvailability(t, "--disable", p); code != 0 {
					t.Errorf("disable %s: %d %s", p, code, out)
				}
			})
		}
		wg.Wait()
		for _, p := range []string{"claude", "codex"} {
			if code, _, errs := askCodex(t, "--reviewer", p, "--question", ".git/question.md", "code.txt"); code != 4 {
				t.Errorf("lost %s: %d %s", p, code, errs)
			}
		}
		noProviderCall(t, f, "calls", "adversarial-reviewer-runs")
	})
}
