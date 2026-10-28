package review

import (
	"slices"
	"strings"
	"testing"
)

func TestClaudeGeneral(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("PR_LOOP_SESSION", "1")
	t.Setenv("CODEX_SANDBOX", "seatbelt")
	f.claudeSays(t, "adversarial-reviewer", claudeResult(map[string]any{
		"findings": []any{}, "unverified": []any{},
		"routes": []any{map[string]any{"route": "security", "paths": []string{"code.txt"}, "why": "input handling"}},
	}, "Reviewed."))
	code, run, stderr := runDelegate(t, "--reviewer", "claude", "--goal", ".git/goal.md", f.base+"..HEAD")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	out := run.review("claude")
	if out.Reviewer != "claude" || len(out.Findings) != 0 {
		t.Fatalf("general review: %+v", out)
	}
	if !strings.Contains(stderr, "git delegate --reviewer security") {
		t.Errorf("missing focused review instruction: %s", stderr)
	}
	args := claudeArgs(t, f, "adversarial-reviewer", 1)
	if !slices.Contains(args, "adversarial-reviewer") || !strings.Contains(args[len(args)-1], "goal") {
		t.Errorf("general Claude invocation: %q", args)
	}
	f.checkCleanedUp(t)
}
