package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	cases := []struct {
		deny    bool
		tool    string
		command string
	}{
		{true, "Bash", "git push origin wesley/x"},
		{true, "Bash", "git -C /tmp/r push --force-with-lease"},
		{true, "Bash", "cd repo && GIT_SSH_COMMAND=ssh git push"},
		{true, "Bash", `bash -c "git push origin main"`},
		{true, "Bash", "/usr/bin/git push"},
		{true, "Bash", "echo $(git push)"},
		{true, "Bash", "git commit --no-verify --fixup=abc"},
		{true, "Bash", "git commit -m 'plain commit'"},
		{true, "Bash", "git commit --amend --no-edit"},
		{false, "Bash", "git commit --fixup=abc123 --trailer 'Review-Thread: https://x/1'"},
		{false, "Bash", `git commit -m "mentions; git push in a message" --fixup abc`},
		{true, "Bash", "git config pr-loop.x 1"},
		{true, "Bash", "git config --unset pr-loop.x"},
		{false, "Bash", "git config --get pr-loop.x"},
		{true, "Bash", "gh pr merge 123"},
		{true, "Bash", "gh pr comment 123 -b hi"},
		{true, "Bash", "gh pr ready 123"},
		{true, "Bash", "gh api -X POST repos/o/r/issues/1/comments -f body=x"},
		{true, "Bash", "gh api repos/o/r/issues/1/comments -f body=x"},
		{true, "Bash", "gh api graphql -f query='mutation { resolveReviewThread }'"},
		{false, "Bash", "gh api graphql -f query='{ viewer { login } }'"},
		{false, "Bash", "gh api repos/o/r/pulls/1/reviews --paginate"},
		{false, "Bash", "gh pr view 123 --json title"},
		{false, "Bash", "git log --oneline -5 && git status"},
		{false, "Bash", "pr-loop push"},
		{false, "Bash", "git rebase --onto a b c"},
		{false, "Bash", "# git push in a comment"},
		{true, "Bash", "git config alias.p push"},
		{true, "Bash", "git config --global alias.p '!git push'"},
		{true, "Bash", "git -c alias.x=push x origin HEAD"},
		{false, "Bash", "git config --get-regexp alias"},
		{true, "Bash", "pr-loop request --accept-failure windows-tuntap 663"},
		{true, "Bash", "pr-loop request 663 --accept-failure=ci"},
		{false, "Bash", "pr-loop request 663"},
		{true, "Bash", "pr-loop end --prune"},
		{false, "Bash", "pr-loop end"},
		{true, "Bash", "git sign-pass --push"},
		{true, "Bash", "git-sign-pass"},
		{true, "git", "git push origin HEAD"},
		{false, "git", "git status"},
		{true, "mcp__claude_ai_Linear__save_issue", ""},
		{true, "mcp__claude_ai_Linear__create_comment", ""},
		{false, "mcp__claude_ai_Linear__get_issue", ""},
		{false, "mcp__claude_ai_Linear__list_comments", ""},
		{true, "mcp__claude_ai_Github__create_pull_request_review", ""},
		{false, "mcp__claude_ai_Github__get_pull_request", ""},
		{false, "Read", ""},
	}
	for _, c := range cases {
		input, _ := json.Marshal(map[string]string{"command": c.command})
		reason := Check(c.tool, "", input)
		if (reason != "") != c.deny {
			t.Errorf("%s %q: deny=%v, reason %q", c.tool, c.command, !c.deny, reason)
		}
	}
}

// TestPolicy holds the guard to the examples in policy.json, which ECA's deny
// patterns are built from and checked against at package build time, so the
// two cannot drift apart. Owner-only rules restrict an agent, which a hook
// cannot tell apart, so only the patterns enforce them.
func TestPolicy(t *testing.T) {
	data, err := os.ReadFile("policy.json")
	if err != nil {
		t.Fatal(err)
	}
	var rules []struct {
		Pattern   string   `json:"pattern"`
		OwnerOnly bool     `json:"ownerOnly"`
		Deny      []string `json:"deny"`
		Allow     []string `json:"allow"`
	}
	if err := json.Unmarshal(data, &rules); err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if r.OwnerOnly {
			continue
		}
		if len(r.Deny) == 0 || len(r.Allow) == 0 {
			t.Errorf("%s: needs examples both ways", r.Pattern)
		}
		for want, commands := range map[bool][]string{true: r.Deny, false: r.Allow} {
			for _, command := range commands {
				input, _ := json.Marshal(map[string]string{"command": command})
				if got := Check("Bash", "", input) != ""; got != want {
					t.Errorf("%s: guard deny=%v for %q", r.Pattern, got, command)
				}
			}
		}
	}
}

func TestFormats(t *testing.T) {
	var out bytes.Buffer
	Main(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`), &out)
	if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
		t.Errorf("Claude Code format: %s", out.String())
	}
	out.Reset()
	Main(strings.NewReader(`{"hook_type":"preToolCall","server":"eca","tool_name":"shell_command","tool_input":{"command":"git push"}}`), &out)
	if !strings.Contains(out.String(), `"approval":"deny"`) {
		t.Errorf("ECA format: %s", out.String())
	}
	out.Reset()
	Main(strings.NewReader(`{"hook_type":"preToolCall","server":"linear","tool_name":"save_issue","tool_input":{}}`), &out)
	if !strings.Contains(out.String(), `"approval":"deny"`) {
		t.Errorf("ECA MCP tool: %s", out.String())
	}
	out.Reset()
	Main(strings.NewReader(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`), &out)
	if out.Len() != 0 {
		t.Errorf("allowed call printed %s", out.String())
	}
}
