package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrimaryHarness(t *testing.T) {
	for _, marker := range []string{"CODEX_SANDBOX", "CODEX_THREAD_ID"} {
		t.Run(marker, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv(marker, "codex-session")
			f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{
				"findings": []any{}, "unverified": []string{}, "routes": []any{},
			}, "Reviewed."))
			code, out, stderr := runDelegate(t, "--plan", f.plan, f.base+"..HEAD")
			if code != 0 || out.review("claude").Reviewer != "claude" {
				t.Fatalf("Codex primary must get a Claude general review: exit %d, %+v\n%s", code, out, stderr)
			}
			if _, err := os.Stat(filepath.Join(f.log, "prompt")); !os.IsNotExist(err) {
				t.Fatal("Codex was invoked for a Codex author")
			}
			args := claudeArgs(t, f, "adversarial-reviewer", 1)
			if !strings.HasPrefix(args[len(args)-1], Contract) {
				t.Fatal("Claude did not receive the general review contract")
			}
		})
	}
	// Where Claude cannot review a Codex author's change, Codex does, in a
	// run of its own, rather than leave the general review undone.
	t.Run("Claude held", func(t *testing.T) {
		f := setUp(t, true)
		t.Setenv("CODEX_SANDBOX", "seatbelt")
		t.Setenv("FAKE_CODEX", "none")
		if code, out := runAvailability(t, "--disable", "claude"); code != 0 {
			t.Fatalf("disable claude: exit %d, %s", code, out)
		}
		_, out, stderr := runDelegate(t, "--plan", f.plan, f.base+"..HEAD")
		if out.review("codex").Reviewer != "codex" {
			t.Fatalf("no Codex general review for a Codex author while Claude is held: %+v\n%s", out, stderr)
		}
		for _, n := range out.NotRun {
			if n["reviewer"] == "claude" {
				t.Errorf("the general review is listed as not run, though Codex ran it: %v", out.NotRun)
			}
		}
		noProviderCall(t, f, "adversarial-reviewer-runs")
	})
	t.Run("design question", func(t *testing.T) {
		f := askFixture(t, true)
		t.Setenv("CODEX_SANDBOX", "seatbelt")
		f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{"answer": "Extend the seam.", "unverified": []string{}}, ""))
		code, out, stderr := askCodex(t, "--question", ".git/question.md", "code.txt")
		if code != 0 || out["reviewer"] != "claude" || out["answer"] != "Extend the seam." {
			t.Fatalf("Codex design question: exit %d, %v\n%s", code, out, stderr)
		}
	})
}

func TestCodexJSONModel(t *testing.T) {
	for _, model := range []string{"", "chosen-model"} {
		t.Run(model, func(t *testing.T) {
			f := setUp(t, true)
			t.Setenv("FAKE_CODEX", "none")
			args := []string{"--reviewer", "codex", "--plan", f.plan}
			if model != "" {
				args = append(args, "--model", model)
			}
			code, out, errs := runDelegate(t, append(args, f.base+"..HEAD")...)
			if code != 0 {
				t.Fatalf("review: %d %s", code, errs)
			}
			got := out.review("codex").Model
			if model == "" && got != nil || model != "" && (got == nil || *got != model) {
				t.Fatalf("JSON mode must keep unknown or requested model, got %v for %q", got, model)
			}
		})
	}
}
