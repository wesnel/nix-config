package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/refusal"
)

var saysInstead = refusal.SaysInstead

// TestAliasDenial covers the alias already in the git config, which the
// guard looks up at run time.
func TestAliasDenial(t *testing.T) {
	config := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(config, []byte("[alias]\n\tpp = push\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", config)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	input, _ := json.Marshal(map[string]string{"command": "git pp origin HEAD"})
	if reason := Check("Bash", "", input); !saysInstead(reason) {
		t.Errorf("alias denial %q does not say what to do instead", reason)
	}
}

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
		{false, "Bash", "pr-loop open --title T --body-file - x"},
		{false, "Bash", "git rebase --onto a b c"},
		{false, "Bash", "# git push in a comment"},
		{false, "Bash", "git config alias.p push"},
		{false, "Bash", "git config --global alias.p '!git push'"},
		{false, "Bash", "git -c alias.x=push x origin HEAD"},
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
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%s %q: reason %q does not say what to do instead", c.tool, c.command, reason)
		}
	}
}

// TestWrappedCommands covers a command run through another program, a shell
// keyword or flags the shell combines, which the guard reads as the command
// it runs, and configuration handed to git through the environment.
func TestWrappedCommands(t *testing.T) {
	cases := []struct {
		deny    bool
		command string
	}{
		{true, "timeout 60 gh pr merge 1"},
		{true, "timeout -s KILL -k 5 60 git push"},
		{true, "timeout --signal=KILL 1m git push"},
		{true, "bash -lc 'gh pr merge 1'"},
		{true, "bash -ec 'git push'"},
		{true, "sh -c -- 'git push'"},
		{true, "bash -o pipefail -c 'git push'"},
		{true, "fish --command='git push'"},
		{true, "zsh -l -c 'git push'"},
		{true, "gh api -XPOST repos/o/r/merges"},
		{true, "gh api --method=PUT repos/o/r/pulls/1/merge"},
		{true, "gh api -X=POST repos/o/r/merges"},
		{true, "gh api repos/o/r/issues/1/comments -fbody=x"},
		{true, "gh api repos/o/r/issues/1/comments --raw-field=body=x"},
		{true, "gh api repos/o/r/issues/1/comments --input=body.json"},
		{true, "nice -n 5 git push"},
		{true, "nice -5 git push"},
		{true, "env -i git push"},
		{true, "env -u HOME PATH=/bin git push"},
		{true, "X=$Y git push"},
		{true, `X="$(date)" git push`},
		{true, "env -S 'git push origin x'"},
		{true, "env -- git push"},
		{true, "xargs -n 1 gh pr merge"},
		{true, "xargs -I {} gh pr merge {}"},
		{true, "command -p git push"},
		{true, "builtin eval 'git push'"},
		{true, "exec -a x git push"},
		{true, "stdbuf -oL git push"},
		{true, "stdbuf -o L git push"},
		{true, "caffeinate -i -t 60 git push"},
		{true, "nohup git push"},
		{true, "time -p git push"},
		{true, "nix shell nixpkgs#gh -c gh pr merge 1"},
		{true, "nix develop --command git push"},
		{true, "nix develop .#ci -c bash -c 'git push'"},
		{true, "nix run nixpkgs#gh -- pr merge 1"},
		{true, "nix run nixpkgs#gh pr merge 1"},
		{true, "nix-shell -p gh --run 'gh pr merge 1'"},
		{true, "direnv exec . git push"},
		{true, `find . -maxdepth 0 -exec git push \;`},
		{true, "find . -execdir gh pr merge 1 +"},
		{true, "watch -n 5 git push"},
		{true, "if true; then git push; fi"},
		{true, "while true; do git push; done"},
		{true, "! git push"},
		{true, "{ git push; }"},
		{true, "time git push"},
		{false, `GIT_CONFIG_PARAMETERS="'alias.x=push'" git x`},
		{false, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.x GIT_CONFIG_VALUE_0=push git x"},
		{true, "env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=push-guard.allowPublic GIT_CONFIG_VALUE_0=o/r git status"},
		{false, "export GIT_CONFIG_PARAMETERS=\"'alias.x=push'\"; git x"},
		{false, "git --config-env=alias.x=CMD x"},
		{false, "$g pr merge 1"},
		{false, `"$GH" pr comment 1 -b hi`},
		{false, "$(which git) push"},
		{false, `bash -c 'bash -c "bash -c \"bash -c \\\"git push\\\"\""'`},
		{false, "timeout 60 gh pr view 1"},
		{false, "bash -lc 'git status'"},
		{false, "bash -o pipefail -c 'go test ./...'"},
		{false, "bash script.sh"},
		{false, "nice -n 5 go test ./..."},
		{false, "env -u FOO go test ./..."},
		{false, "env"},
		{false, `find . -name '*.go' -exec grep -n push {} \;`},
		{false, "nix develop -c go test ./..."},
		{false, "nix run nixpkgs#hello -- --greeting push"},
		{false, "$EDITOR notes.md"},
		{false, `"$PAGER" README.md`},
		{false, "command -v gh"},
		{false, "gh api -XGET repos/o/r/pulls"},
		{false, "gh api --method=GET repos/o/r/pulls"},
		{false, "watch -n 5 gh pr checks 1"},
		{false, "xargs -n 1 echo"},
		{false, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=user.name GIT_CONFIG_VALUE_0=T git status"},
		{false, "if git diff --quiet; then echo clean; fi"},
	}
	for _, c := range cases {
		input, _ := json.Marshal(map[string]string{"command": c.command})
		reason := Check("Bash", "", input)
		if (reason != "") != c.deny {
			t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
		}
	}
}

// TestGraphQLQuerySources covers a query that is not on the command line: the
// guard reads a plain file of the workspace, and denies what it cannot read.
func TestGraphQLQuerySources(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	files := map[string]string{
		"workspace/read.graphql":  "query { viewer { login } }",
		"workspace/write.graphql": "mutation { resolveReviewThread(input: {threadId: \"T\"}) { clientMutationId } }",
		"workspace/read.json":     `{"query": "query { viewer { login } }"}`,
		"workspace/write.json":    `{"query": "mutation { resolveReviewThread(input: {threadId: \"T\"}) { clientMutationId } }"}`,
		"outside.graphql":         "query { viewer { login } }",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "outside.graphql"), filepath.Join(workspace, "link.graphql")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	cases := []struct {
		deny    bool
		command string
	}{
		{true, "gh api graphql -F query=@write.graphql"},
		{true, "gh api graphql --field query=@write.graphql"},
		{true, "gh api graphql --field=query=@write.graphql"},
		{true, "gh api graphql -Fquery=@write.graphql"},
		{true, "gh api graphql -f query=@write.graphql"},
		{true, "gh api graphql --raw-field query=@write.graphql"},
		{true, "gh api graphql --input write.json"},
		{true, "gh api graphql --input=write.json"},
		{true, "gh api graphql --input -"},
		{true, "cat read.json | gh api graphql --input -"},
		{true, "gh api graphql -F query=@- <<'EOF'\nquery { viewer { login } }\nEOF"},
		{true, `gh api graphql -f query="$QUERY"`},
		{true, "gh api graphql -f query=$QUERY"},
		{true, `gh api graphql -f query="$(cat read.graphql)"`},
		{true, "gh api graphql -F query=@missing.graphql"},
		{true, "gh api graphql -F query=@../outside.graphql"},
		{true, "gh api graphql -F query=@" + filepath.Join(root, "outside.graphql")},
		{true, "gh api graphql -F query=@link.graphql"},
		{true, "cp write.graphql read.graphql && gh api graphql -F query=@read.graphql"},
		{true, "bash -c 'gh api graphql -F query=@read.graphql'"},
		{true, "gh api -X GET graphql -f query='mutation { resolveReviewThread }'"},
		{false, "gh api graphql -F query=@read.graphql"},
		{false, "gh api graphql -F query=@" + filepath.Join(workspace, "read.graphql")},
		{false, "gh api graphql --input read.json"},
		{false, "gh api graphql -f query='query($n: Int!) { viewer { login } }' -F n=1"},
		{false, "gh api graphql -F query=@read.graphql -F owner=@missing.txt"},
		{false, "gh api -H 'Accept: application/json' graphql -f query='{ viewer { login } }'"},
	}
	for _, c := range cases {
		input, _ := json.Marshal(map[string]string{"command": c.command})
		reason := Check("Bash", "", input)
		if (reason != "") != c.deny {
			t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
		}
	}

	// The hook event's cwd, not the guard's, is where a relative file is.
	t.Chdir(root)
	for file, deny := range map[string]bool{"write.graphql": true, "read.graphql": false} {
		event, _ := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": workspace,
			"tool_input": map[string]string{"command": "gh api graphql -F query=@" + file}})
		var out bytes.Buffer
		Main(nil, bytes.NewReader(event), &out, io.Discard)
		if (out.Len() != 0) != deny {
			t.Errorf("%s from the event's cwd: deny=%v, output %s", file, !deny, out.String())
		}
	}
}

// TestPolicy holds the guard to the examples in policy.json, which ECA's deny
// patterns are built from and checked against at package build time, so the
// two cannot drift apart. An owner-only rule holds for an owner and a
// reviewer, and a reviewer-only rule for a reviewer alone. Each denial's
// reason, which is what the agent sees, must say what to do instead; a rule's
// `why` is for whoever maintains it and reaches no agent.
func TestPolicy(t *testing.T) {
	data, err := os.ReadFile("policy.json")
	if err != nil {
		t.Fatal(err)
	}
	var rules []struct {
		Pattern      string   `json:"pattern"`
		OwnerOnly    bool     `json:"ownerOnly"`
		ReviewerOnly bool     `json:"reviewerOnly"`
		Deny         []string `json:"deny"`
		Allow        []string `json:"allow"`
	}
	if err := json.Unmarshal(data, &rules); err != nil {
		t.Fatal(err)
	}
	checks := map[role]func(string, string, json.RawMessage) string{
		coordinator: Check, owner: CheckOwner, reviewer: CheckReviewer}
	names := map[role]string{coordinator: "coordinator", owner: "owner", reviewer: "reviewer"}
	for _, r := range rules {
		if len(r.Deny) == 0 || len(r.Allow) == 0 {
			t.Errorf("%s: needs examples both ways", r.Pattern)
		}
		scope := coordinator
		switch {
		case r.ReviewerOnly:
			scope = reviewer
		case r.OwnerOnly:
			scope = owner
		}
		for denied, commands := range map[bool][]string{true: r.Deny, false: r.Allow} {
			for _, command := range commands {
				input, _ := json.Marshal(map[string]string{"command": command})
				for who, check := range checks {
					// A denial holds for the rule's role and every stricter
					// one, and a narrower rule's denials are steps the
					// coordinator takes. An allow example holds only for the
					// rule's own role: another rule can deny it to a
					// stricter one.
					want, held := denied && who >= scope, who == scope
					if denied {
						held = who >= scope || who == coordinator
					}
					reason := check("Bash", "", input)
					if got := reason != ""; held && got != want {
						t.Errorf("%s: guard deny=%v for the %s's %q", r.Pattern, got, names[who], command)
					}
					if reason != "" && !saysInstead(reason) {
						t.Errorf("%s: reason %q for the %s's %q does not say what to do instead", r.Pattern, reason, names[who], command)
					}
				}
			}
		}
	}
}

// TestOnlyReviewer covers the hook every ordinary session runs, which acts
// only on the reviewer agent.
func TestOnlyReviewer(t *testing.T) {
	for _, c := range []struct {
		deny      bool
		agentType string
		command   string
	}{
		{true, "reviewer", "git commit -m x"},
		{true, "some-plugin:reviewer", "git stash"},
		{true, "reviewer", "gh pr comment 1 -b hi"},
		{true, "reviewer", "pr-loop setup"},
		{false, "reviewer", "git log --oneline"},
		{false, "reviewer", "pr-loop wait ci --timeout 3600"},
		{false, "reviewer", "pr-loop setup-command"},
		{false, "", "git push origin x"},
		{false, "pr-loop:pr-owner", "pr-loop push"},
		{false, "Explore", "git commit -m x"},
	} {
		event, _ := json.Marshal(map[string]any{"tool_name": "Bash", "agent_type": c.agentType,
			"tool_input": map[string]string{"command": c.command}})
		var out, errs bytes.Buffer
		if code := Main([]string{"--only", "reviewer"}, bytes.NewReader(event), &out, &errs); code != 0 {
			t.Errorf("%s %q: exit %d: %s", c.agentType, c.command, code, errs.String())
		}
		if denied := strings.Contains(out.String(), `"permissionDecision":"deny"`); denied != c.deny {
			t.Errorf("%s %q: deny=%v, output %s", c.agentType, c.command, denied, out.String())
		}
	}
	var out, errs bytes.Buffer
	if code := Main([]string{"--only", "nobody"}, strings.NewReader(`{"tool_name":"Bash"}`), &out, &errs); code == 0 {
		t.Errorf("an unknown role was accepted: %s", out.String())
	}
}

// TestReviewerReasons holds every denial a reviewer meets to the one thing
// it should do instead, wherever the rule it broke comes from.
func TestReviewerReasons(t *testing.T) {
	for _, command := range []string{"git push origin x", "git commit -m x", "gh pr comment 1 -b hi",
		"git sign-pass", "pr-loop reply T --body-file -"} {
		input, _ := json.Marshal(map[string]string{"command": command})
		if reason := CheckReviewer("Bash", "", input); !strings.HasPrefix(reason, "A reviewer only reads.") || !saysInstead(reason) {
			t.Errorf("%q: %q", command, reason)
		}
	}
}

func TestFormats(t *testing.T) {
	for _, c := range []struct {
		event, want string
	}{
		{`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`, `"permissionDecision":"deny"`},
		{`{"hook_type":"preToolCall","server":"eca","tool_name":"shell_command","tool_input":{"command":"git push"}}`, `"approval":"deny"`},
		{`{"hook_type":"preToolCall","server":"linear","tool_name":"save_issue","tool_input":{}}`, `"approval":"deny"`},
		{`{"tool_name":"Bash","agent_type":"pr-loop:pr-owner","tool_input":{"command":"pr-loop push"}}`, `"permissionDecision":"deny"`},
		{`{"tool_name":"Bash","agent_type":"reviewer","tool_input":{"command":"git commit --fixup=abc"}}`, `"permissionDecision":"deny"`},
		{`{"tool_name":"Bash","agent_type":"pr-loop:pr-owner","tool_input":{"command":"git commit --fixup=abc"}}`, ""},
		{`{"tool_name":"Bash","tool_input":{"command":"pr-loop push"}}`, ""},
		{`{"tool_name":"Bash","tool_input":{"command":"ls"}}`, ""},
	} {
		var out bytes.Buffer
		Main(nil, strings.NewReader(c.event), &out, io.Discard)
		if (c.want == "" && out.Len() != 0) || !strings.Contains(out.String(), c.want) {
			t.Errorf("%s: printed %s", c.event, out.String())
		}
	}
}
