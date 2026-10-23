package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/record"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// TestFocusContracts holds each focus's contract to where the package's
// Nix passthru.focusPrompts.ROUTE builds it for the ROUTE-reviewer agent:
// the general contract split at its one "## Checks" line, then ROUTE.md;
// and each focus's reviewer type to that agent.
func TestFocusContracts(t *testing.T) {
	const heading = "\n## Checks\n"
	if n := strings.Count(Contract, heading); n != 1 {
		t.Fatalf("reviewer.md has %d %q lines; the Nix split needs one", n, strings.TrimSpace(heading))
	}
	for route, f := range focuses {
		checks, err := os.ReadFile(route + ".md")
		if err != nil {
			t.Fatalf("the %s focus has no %s.md, which the Nix split reads: %v", route, route, err)
		}
		if got, want := f.contract(), Contract[:strings.Index(Contract, heading)+1]+string(checks); got != want {
			t.Errorf("the %s focus's contract is not the general contract up to %q and then %s.md, as the Nix split builds it",
				route, strings.TrimSpace(heading), route)
		}
		if ty := reviewerTypes[route]; !ty.claude || ty.agent != route+"-reviewer" {
			t.Errorf("the %s type runs %+v, not Claude Code's %s-reviewer agent", route, ty, route)
		}
	}
}

// Where a focused review cannot run headless, delegate names the agent
// type to start on the prompt it prints.
func TestFocusAgentNamed(t *testing.T) {
	f := setUp(t, true)
	t.Setenv("AGENT_TOOLS_CONFIG", filepath.Join(t.TempDir(), "none.json"))
	code, _, stderr := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
	if code != 4 || !strings.Contains(stderr, "start a new `security-reviewer` sub-agent on what `git delegate --print-prompt "+
		"--reviewer security") {
		t.Errorf("exit %d\n%s", code, stderr)
	}
}

func refused(t *testing.T, what string, code int, stderr string) {
	t.Helper()
	reason := strings.TrimPrefix(strings.TrimSpace(stderr), "git-delegate: refused: ")
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

// claudeArgs are the arguments of the fake claude's nth run as agent.
func claudeArgs(t *testing.T, f *fixture, agent string, n int) []string {
	t.Helper()
	data := f.claudeLog(t, agent, fmt.Sprintf("args-%d", n))
	return strings.Split(strings.TrimSuffix(data, "\x00"), "\x00")
}

// The security review runs headless in Claude Code, with the security
// checklist and the general rules, and requires every category to be named
// as checked.
func TestSecurityReviewer(t *testing.T) {
	finding := map[string]any{"id": "1", "path": "code.txt", "line": 2, "endLine": nil, "kind": "vulnerability",
		"claim": "c", "evidence": "e", "about": nil, "reopens": nil}
	t.Run("checked", func(t *testing.T) {
		f := setUp(t, true)
		f.claudeSays(t, "security-reviewer", claudeResult(securityReply(finding), "Reviewed."))
		code, run, stderr := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		out := run.review("security")
		if code != 3 || out.Focus != "security" || len(out.Findings) != 1 || out.Findings[0]["id"] != "security-1" ||
			out.Findings[0]["kind"] != "vulnerability" || out.Findings[0]["finding"] != "F1" || len(run.Reviews) != 2 ||
			run.Reviews[1].Reviewer != "guide" {
			t.Fatalf("exit %d, %+v\n%s", code, run, stderr)
		}
		args := claudeArgs(t, f, "security-reviewer", 1)
		securityPrompt(t, args[len(args)-1])
		has := func(want ...string) bool {
			for i := range args {
				if i+len(want) <= len(args) && slices.Equal(args[i:i+len(want)], want) {
					return true
				}
			}
			return false
		}
		for _, want := range [][]string{{"-p"}, {"--setting-sources", ""}, {"--strict-mcp-config"},
			{"--mcp-config", `{"mcpServers":{}}`}, {"--agents", "/store/review-agents.json"}, {"--agent", "security-reviewer"},
			{"--settings", "/store/reviewer-settings.json"}, {"--permission-mode", "dontAsk"}, {"--output-format", "json"},
			{"--no-session-persistence"}} {
			if !has(want...) {
				t.Errorf("claude was not passed %q: %q", want, args[:len(args)-1])
			}
		}
		i := slices.Index(args, "--json-schema")
		if i < 0 || !strings.Contains(args[i+1], `"checked"`) || !strings.Contains(args[i+1], `"vulnerability"`) ||
			strings.Contains(args[i+1], `"routes"`) {
			t.Errorf("schema: %q", args)
		}
		env := f.claudeLog(t, "security-reviewer", "env-1")
		if !strings.Contains(env, "\nCLAUDE_CODE_DISABLE_CLAUDE_MDS=1\n") || strings.Contains(env, "\nCLAUDECODE=") {
			t.Errorf("the reviewer's environment:\n%s", env)
		}
		if head := strings.TrimSpace(f.claudeLog(t, "security-reviewer", "head")); head != f.tip {
			t.Errorf("the reviewer ran at %s, not TIP %s", head, f.tip)
		}
		if files := f.claudeLog(t, "security-reviewer", "files"); strings.Contains(files, "notes.md") {
			t.Errorf("the reviewer could see the uncommitted notes: %s", files)
		}
		f.checkCleanedUp(t)
	})

	// The reply is taken from the structured output, a fenced json block
	// of the result, or the result's last JSON object; with none, the type
	// is asked once more, then dropped.
	reply := string(must(json.Marshal(securityReply(finding))))
	for _, c := range []struct {
		name string
		outs []string
		code int
		runs string
	}{
		{"a fenced block", []string{claudeResult(nil, "Findings:\n```json\n{\"draft\": 1}\n```\nand then\n```json\n"+reply+"\n```\n")}, 3, "1"},
		{"a bare object", []string{claudeResult(nil, `Done. {"a": {"b": 1}} The reply: `+reply+` That is all.`)}, 3, "1"},
		{"neither, then a reply", []string{claudeResult(nil, "No JSON here."), claudeResult(securityReply(finding), "")}, 3, "2"},
		{"neither, twice", []string{claudeResult(nil, "No JSON here.")}, 4, "2"},
		{"a category left out, twice", []string{claudeResult(map[string]any{"findings": []any{}, "unverified": []any{},
			"checked": Categories[1:]}, "")}, 4, "2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := setUp(t, true)
			f.claudeSays(t, "security-reviewer", c.outs...)
			code, run, stderr := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
			if code != c.code {
				t.Fatalf("exit %d, want %d\n%s", code, c.code, stderr)
			}
			if runs := strings.TrimSpace(f.claudeLog(t, "security-reviewer", "runs")); runs != c.runs {
				t.Errorf("claude ran %s times, want %s", runs, c.runs)
			}
			if c.runs == "2" {
				args := claudeArgs(t, f, "security-reviewer", 2)
				if !strings.Contains(args[len(args)-1], "Your last reply could not be used") {
					t.Errorf("the rerun was not told why")
				}
			}
			rec := must(record.ReadRange(run.Record))
			if c.code == 4 && (len(rec.Rounds) != 2 || len(rec.Rounds[0].Dropped) != 3 || len(rec.Findings) != 0) {
				t.Errorf("the dropped replies are not in the record: %+v", rec.Rounds)
			}
			f.checkCleanedUp(t)
		})
	}

	t.Run("no delegate config", func(t *testing.T) {
		f := setUp(t, true)
		writeConfig(t, `{}`)
		t.Setenv("AGENT_TOOLS_CONFIG", filepath.Join(t.TempDir(), "none.json"))
		code, run, stderr := runDelegate(t, "--reviewer", "security", "--goal", ".git/goal.md", f.base+"..HEAD")
		if code != 4 || len(run.NotRun) != 2 || !strings.Contains(run.NotRun[0]["why"], "delegate.agents") {
			t.Errorf("exit %d, %+v\n%s", code, run, stderr)
		}
	})

	t.Run("an unknown type", func(t *testing.T) {
		f := setUp(t, true)
		code, _, stderr := runDelegate(t, "--reviewer", "style", "--goal", ".git/goal.md", f.base+"..HEAD")
		refused(t, "an unknown type", code, stderr)
	})
}

// The security prompt carries the operator's threat notes, redacted, or
// says there are none, and the overlapping branches; notes the repository
// holds are refused.
func TestThreatNotesPrompt(t *testing.T) {
	notes := func(t *testing.T, value string) *fixture {
		t.Helper()
		f := setUp(t, true)
		value = strings.ReplaceAll(value, "TOP", run(t, f.repo, "git", "rev-parse", "--show-toplevel"))
		config := filepath.Join(t.TempDir(), "config.json")
		os.WriteFile(config, must(json.Marshal(map[string]any{"repos": map[string]any{"o/r": map[string]any{"threatNotes": value}}})), 0o644)
		t.Setenv("AGENT_TOOLS_CONFIG", config)
		return f
	}
	printed := func(t *testing.T, f *fixture) (int, string, string) {
		t.Helper()
		var stdout, stderr strings.Builder
		code := Main(t.Context(), []string{"--print-prompt", "--reviewer", "security", "--goal", ".git/goal.md", f.base + "..HEAD"},
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

// --print-prompt --reviewer security prints the security review's prompt,
// the contract a security-reviewer agent works to.
func TestFocusPrompt(t *testing.T) {
	f := setUp(t, false)
	var stdout, stderr strings.Builder
	if code := Main(t.Context(), []string{"--print-prompt", "--reviewer", "security", "--goal", ".git/goal.md", f.base + "..HEAD"},
		nil, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr.String())
	}
	securityPrompt(t, stdout.String())
}
