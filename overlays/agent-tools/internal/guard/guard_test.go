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

// caseName names a subtest after what it checks and whether the guard is to
// deny it. It keeps no character a shell treats specially: t.TempDir names
// its directory after the subtest, and a case may put that path in a
// command.
func caseName(deny bool, what string) string {
	what = strings.Map(func(r rune) rune {
		if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune(" ./:=@+,-", r) {
			return r
		}
		return -1
	}, what)
	if deny {
		return "deny/" + what
	}
	return "allow/" + what
}

func TestCheck(t *testing.T) {
	t.Run("commands", testCheckCommands)
	t.Run("wrapped commands", testCheckWrappedCommands)
	t.Run("GraphQL query sources", testCheckGraphQLQuerySources)
	t.Run("alias", testCheckAlias)
}

// testCheckAlias covers the alias already in the git config, which the
// guard looks up at run time.
func testCheckAlias(t *testing.T) {
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

func testCheckCommands(t *testing.T) {
	cases := []struct {
		deny    bool
		tool    string
		command string
	}{
		{true, "Bash", "git push origin feature/x"},
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
		{true, "mcp__claude_ai_Github__get_pull_request", ""},
		{false, "Read", ""},
	}
	for _, c := range cases {
		t.Run(caseName(c.deny, strings.TrimSpace(c.tool+" "+c.command)), func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"command": c.command})
			reason := Check(c.tool, "", input)
			if (reason != "") != c.deny {
				t.Errorf("%s %q: deny=%v, reason %q", c.tool, c.command, !c.deny, reason)
			}
			if reason != "" && !saysInstead(reason) {
				t.Errorf("%s %q: reason %q does not say what to do instead", c.tool, c.command, reason)
			}
		})
	}
}

// testCheckWrappedCommands covers a command run through another program, a
// shell keyword or flags the shell combines, which the guard reads as the
// command it runs, and configuration handed to git through the environment.
func testCheckWrappedCommands(t *testing.T) {
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
		{true, `GIT_CONFIG_PARAMETERS="'alias.x=push'" git x`},
		{true, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.x GIT_CONFIG_VALUE_0=push git x"},
		{true, "env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=push-guard.allowPublic GIT_CONFIG_VALUE_0=o/r git status"},
		{true, "export GIT_CONFIG_PARAMETERS=\"'alias.x=push'\"; git x"},
		{true, "git --config-env=alias.x=CMD x"},
		{true, "$g pr merge 1"},
		{true, `"$GH" pr comment 1 -b hi`},
		{true, "$(which git) push"},
		{false, "timeout 60 gh pr view 1"},
		{false, "bash -lc 'git status'"},
		{false, "bash -o pipefail -c 'go test ./...'"},
		{false, "bash script.sh"},
		{false, "nice -n 5 go test ./..."},
		{false, "env -u FOO go test ./..."},
		{false, "env FOO=1 true"},
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
		t.Run(caseName(c.deny, c.command), func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"command": c.command})
			reason := Check("Bash", "", input)
			if (reason != "") != c.deny {
				t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
			}
			if reason != "" && !saysInstead(reason) {
				t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
			}
		})
	}
}

// queryFiles makes a workspace, inside root, holding a query and a mutation
// as GraphQL and as JSON, a link to a query outside it, and returns root
// and the workspace.
func queryFiles(t *testing.T) (root, workspace string) {
	t.Helper()
	root = t.TempDir()
	workspace = filepath.Join(root, "workspace")
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
	return root, workspace
}

// testCheckGraphQLQuerySources covers a query that is not on the command
// line: the guard reads a plain file of the workspace, and denies what it
// cannot read. ROOT and WORKSPACE in a command stand for the directories
// queryFiles makes.
func testCheckGraphQLQuerySources(t *testing.T) {
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
		{true, "gh api graphql -F query=@ROOT/outside.graphql"},
		{true, "gh api graphql -F query=@link.graphql"},
		{true, "cp write.graphql read.graphql && gh api graphql -F query=@read.graphql"},
		{true, "bash -c 'gh api graphql -F query=@read.graphql'"},
		{true, "gh api -X GET graphql -f query='mutation { resolveReviewThread }'"},
		{false, "gh api graphql -F query=@read.graphql"},
		{false, "gh api graphql -F query=@WORKSPACE/read.graphql"},
		{false, "gh api graphql --input read.json"},
		{false, "gh api graphql -f query='query($n: Int!) { viewer { login } }' -F n=1"},
		{false, "gh api graphql -F query=@read.graphql -F owner=@missing.txt"},
		{false, "gh api -H 'Accept: application/json' graphql -f query='{ viewer { login } }'"},
	}
	for _, c := range cases {
		t.Run(caseName(c.deny, c.command), func(t *testing.T) {
			root, workspace := queryFiles(t)
			t.Chdir(workspace)
			command := strings.NewReplacer("ROOT", root, "WORKSPACE", workspace).Replace(c.command)
			input, _ := json.Marshal(map[string]string{"command": command})
			reason := Check("Bash", "", input)
			if (reason != "") != c.deny {
				t.Errorf("%q: deny=%v, reason %q", command, !c.deny, reason)
			}
			if reason != "" && !saysInstead(reason) {
				t.Errorf("%q: reason %q does not say what to do instead", command, reason)
			}
		})
	}

	// The hook event's cwd, not the guard's, is where a relative file is.
	for file, deny := range map[string]bool{"write.graphql": true, "read.graphql": false} {
		t.Run(caseName(deny, "from the event's cwd/"+file), func(t *testing.T) {
			root, workspace := queryFiles(t)
			t.Chdir(root)
			event, _ := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": workspace,
				"tool_input": map[string]string{"command": "gh api graphql -F query=@" + file}})
			var out bytes.Buffer
			Main(nil, bytes.NewReader(event), &out, io.Discard)
			if (out.Len() != 0) != deny {
				t.Errorf("%s from the event's cwd: deny=%v, output %s", file, !deny, out.String())
			}
		})
	}
}

// TestPolicy holds the guard to the examples in policy.json, which ECA's deny
// patterns are built from and checked against at package build time, so the
// two cannot drift apart. A rule's scope is the least strict role it holds
// for: baseline (every session), coordinator, owner or reviewer. Each
// denial's reason, which is what the agent sees, must say what to do
// instead; a rule's `why` is for whoever maintains it and reaches no agent.
func TestPolicy(t *testing.T) {
	data, err := os.ReadFile("policy.json")
	if err != nil {
		t.Fatal(err)
	}
	// The examples name no repository, so they run outside one, as in the
	// Nix build; a checkout's remotes would decide `git fetch origin`.
	t.Chdir(t.TempDir())
	var rules []struct {
		Why     string   `json:"why"`
		Pattern string   `json:"pattern"`
		Scope   string   `json:"scope"`
		Deny    []string `json:"deny"`
		Allow   []string `json:"allow"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rules); err != nil {
		t.Fatal(err)
	}
	checks := map[role]func(string, string, json.RawMessage) string{
		baseline: CheckBaseline, coordinator: Check, owner: CheckOwner, reviewer: CheckReviewer}
	names := map[role]string{baseline: "baseline", coordinator: "coordinator", owner: "owner", reviewer: "reviewer"}
	scopes := map[string]role{"baseline": baseline, "coordinator": coordinator, "owner": owner, "reviewer": reviewer}
	for _, r := range rules {
		t.Run(r.Pattern, func(t *testing.T) {
			if len(r.Deny) == 0 || len(r.Allow) == 0 {
				t.Errorf("%s: needs examples both ways", r.Pattern)
			}
			scope, ok := scopes[r.Scope]
			if !ok {
				t.Errorf("%s: scope %q is not a role", r.Pattern, r.Scope)
			}
			for denied, commands := range map[bool][]string{true: r.Deny, false: r.Allow} {
				for _, command := range commands {
					input, _ := json.Marshal(map[string]string{"command": command})
					for who, check := range checks {
						// A denial holds for the rule's role and every stricter
						// one, and an owner's denials are steps the coordinator
						// takes. An allow example holds only for the rule's own
						// role: another rule can deny it to a stricter one.
						want, held := denied && who >= scope, who == scope
						if denied {
							held = who >= scope || (scope == owner && who == coordinator)
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
		})
	}
}

func TestBaseline(t *testing.T) {
	t.Run("commands", testBaselineCommands)
	t.Run("roles", testBaselineRoles)
	t.Run("formats", testBaselineFormats)
}

// testBaselineCommands covers the rules for every session that no ECA
// pattern expresses, and the baseline's allowances that a loop role does not
// share.
func testBaselineCommands(t *testing.T) {
	cases := []struct {
		deny    bool
		command string
	}{
		{true, "git push https://github.com/o/r HEAD"},
		{true, "timeout 60 gh pr merge 1"},
		{true, "bash -lc 'gh pr merge 1'"},
		{true, "gh api -XPOST repos/o/r/merges"},
		{true, "gh -R o/r pr merge 1"},
		{true, "gh pr -R o/r merge 1"},
		{true, "gh pr ready 1"},
		{true, "gh pr create --title t --body b"},
		{true, "gh pr create --draft=false --title t --body b"},
		{true, "gh pr create -d=false --title t"},
		{true, "gh pr create --draft --draft=false --title t"},
		{false, "gh pr create --draft=true --title t --body b"},
		{false, "gh pr create --draft=false --draft --title t"},
		{true, "gh pr edit 1 --add-label bug"},
		{true, "gh pr edit 1 --title t --add-reviewer u"},
		{true, "gh issue comment 1 -b x"},
		{true, "gh auth token"},
		{true, "gh auth status --show-token"},
		{true, "gh auth status -t"},
		{true, "gh auth setup-git"},
		{true, "gh repo deploy-key add k.pub"},
		{true, "gh secret set X"},
		{true, "gh run rerun 1"},
		{true, "gh workflow run ci.yml"},
		{true, "gh codespace create"},
		{true, "gh frobnicate"},
		{true, "gh api graphql -f query='mutation { x }'"},
		{true, "git -c credential.helper=osxkeychain fetch"},
		{true, "git -c http.https://github.com/.extraHeader='Authorization: x' fetch"},
		{true, "git config --global url.git@github.com:.insteadOf https://github.com/"},
		{true, "git config core.sshCommand 'ssh -i k'"},
		{true, "git config --global credential.helper store"},
		{true, "GIT_SSH_COMMAND='ssh -i k' git fetch"},
		{true, "GIT_ASKPASS=/tmp/p git fetch"},
		{true, "env GIT_SSH=/tmp/s git fetch"},
		{true, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=credential.helper GIT_CONFIG_VALUE_0=store git fetch"},
		{true, `GIT_CONFIG_PARAMETERS="'url.x.insteadof'='https://github.com/'" git fetch`},
		{true, "git remote add fork https://github.com/me/r"},
		{true, "git remote set-url origin https://github.com/me/r"},
		{true, "git config remote.origin.pushurl https://github.com/me/r"},
		{true, "curl -X POST https://api.github.com/repos/o/r/issues -d '{}'"},
		{true, "curl --json '{}' https://api.github.com/repos/o/r/issues"},
		{true, "wget --post-data=x https://api.github.com/repos/o/r/issues"},
		{true, "http POST https://api.github.com/repos/o/r/issues title=x"},
		{true, "curl -T f https://uploads.github.com/repos/o/r/releases/1/assets"},
		{true, "pr-loop request --accept-failure ci 1"},
		{true, "gh api -X POST repos/o/r/stacks -H 'X-GitHub-Api-Version: 2026-03-10' --input stack.json"},
		{true, "gh api repos/o/r/stacks/7/unstack -X POST"},
		{true, "git commit --no-verify -m x"},
		{true, "git commit -n -m x"},
		{true, "git merge --no-verify feature"},
		{true, "git tag v1"},
		{true, "git tag -a v1 -m x"},
		{true, "git tag -d v1"},
		{true, "git tag -f v1 HEAD"},
		{true, "git -C r tag -s v1"},
		{false, "git tag"},
		{false, "git tag -l"},
		{false, "git tag --list 'v*'"},
		{false, "git tag -v v1"},
		{false, "git tag --contains HEAD"},
		{false, "git tag -n5"},
		{false, "git tag --sort -v:refname"},
		{false, "git tag --format '%(refname)'"},
		{true, "emacs"},
		{true, "emacs --batch -l test.el -f ert-run-tests-batch-and-exit"},
		{true, "emacs -Q -nw"},
		{true, "timeout 60 emacs --batch -f x"},
		{true, "/Applications/Emacs.app/Contents/MacOS/Emacs --batch"},
		{false, "emacsclient -e '(+ 1 2)'"},
		{false, "grep -n emacs init.el"},
		{false, "cd ~/git/github.com/wesnel/emacs-config && git status"},
		{false, "nix build .#emacs"},
		{false, "gh api -H 'X-GitHub-Api-Version: 2026-03-10' 'repos/o/r/stacks?pull_request=3'"},
		{false, "pr-loop link 3 4"},
		{false, "gh pr view 1"},
		{false, "gh pr create --draft --title t --body-file b"},
		{false, "gh pr create -d --title t --body b"},
		{false, "pr-loop open --title T --body-file - x"},
		{false, "pr-loop push"},
		{false, "gh pr edit 1 --title t --body-file b"},
		{false, "gh pr edit 1 -B main"},
		{false, "gh pr ready 1 --undo"},
		{false, "gh pr diff 1"},
		{false, "gh pr checks 1"},
		{false, "gh run watch 1"},
		{false, "gh run download 1"},
		{false, "gh release download v1"},
		{false, "gh search prs --author @me"},
		{false, "gh repo clone o/r"},
		{false, "gh repo set-default o/r"},
		{false, "gh browse"},
		{false, "gh auth status"},
		{false, "gh status"},
		{false, "gh"},
		{false, "gh api repos/o/r/pulls"},
		{false, "gh api -X GET search/issues -f q=x"},
		{false, "gh api graphql -f query='{ viewer { login } }'"},
		{false, "git commit -m 'plain commit'"},
		{false, "git commit --amend --no-edit"},
		{false, "git fetch origin"},
		{false, "git remote -v"},
		{false, "git config --get credential.helper"},
		{false, "git config --get remote.origin.url"},
		{false, "git config user.name T"},
		{false, "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 go test ./..."},
		{false, "curl -s https://api.github.com/repos/o/r"},
		{false, "curl -X POST https://example.com -d x"},
		{false, "http GET https://api.github.com/repos/o/r"},
	}
	// Outside any repository, as in the Nix build; a checkout's remotes
	// would decide `git fetch origin`.
	t.Chdir(t.TempDir())
	for _, c := range cases {
		t.Run(caseName(c.deny, c.command), func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"command": c.command})
			reason := CheckBaseline("Bash", "", input)
			if (reason != "") != c.deny {
				t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
			}
			if reason != "" && !saysInstead(reason) {
				t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
			}
		})
	}
}

// testBaselineRoles covers the hook every ordinary session runs: an agent
// with a loop role has that role's rules, and every other call the
// baseline's.
func testBaselineRoles(t *testing.T) {
	for _, c := range []struct {
		deny      bool
		agentType string
		command   string
	}{
		{true, "", "git push origin x"},
		{true, "Explore", "gh pr merge 1"},
		{false, "", "git commit -m x"},
		{false, "", "pr-loop push"},
		{true, "reviewer", "git commit -m x"},
		{true, "some-plugin:reviewer", "git stash"},
		{true, "pr-loop:pr-owner", "pr-loop push"},
		{true, "pr-loop:pr-owner", "pr-loop link 3 4"},
		{true, "pr-loop:pr-owner", "pr-loop fetch-base"},
		{false, "", "pr-loop fetch-base"},
		{false, "pr-loop:pr-owner", "pr-loop stack 3"},
		{true, "reviewer", "pr-loop link 3 4"},
		{false, "reviewer", "pr-loop stack 3"},
		{false, "", "pr-loop link 3 4"},
		{false, "pr-loop:pr-owner", "git commit --fixup=abc"},
		{true, "pr-loop:pr-owner", "git commit -m x"},
	} {
		t.Run(caseName(c.deny, strings.TrimSpace(c.agentType+" "+c.command)), func(t *testing.T) {
			event, _ := json.Marshal(map[string]any{"tool_name": "Bash", "agent_type": c.agentType,
				"tool_input": map[string]string{"command": c.command}})
			var out, errs bytes.Buffer
			if code := Main([]string{"--baseline"}, bytes.NewReader(event), &out, &errs); code != 0 {
				t.Errorf("%s %q: exit %d: %s", c.agentType, c.command, code, errs.String())
			}
			if denied := strings.Contains(out.String(), `"permissionDecision":"deny"`); denied != c.deny {
				t.Errorf("%s %q: deny=%v, output %s", c.agentType, c.command, denied, out.String())
			}
		})
	}
}

// testBaselineFormats covers the hook every session runs, as each agent
// calls it: Claude Code with its agent_type, Codex with turn_id and no
// agent, ECA with its agent's name in agent.
func testBaselineFormats(t *testing.T) {
	for _, c := range []struct {
		name, event, want string
	}{
		{"Claude Code push", `{"tool_name":"Bash","tool_input":{"command":"git push"}}`, `"permissionDecision":"deny"`},
		{"Claude Code reviewer commit", `{"tool_name":"Bash","agent_type":"reviewer","tool_input":{"command":"git commit -m x"}}`, `"permissionDecision":"deny"`},
		{"Claude Code commit", `{"tool_name":"Bash","tool_input":{"command":"git commit -m x"}}`, ""},
		{"Codex Bash merge", `{"tool_name":"Bash","turn_id":"t","tool_input":{"command":"gh pr merge 1"}}`, `"permissionDecision":"deny"`},
		{"Codex exec_command push", `{"tool_name":"exec_command","turn_id":"t","tool_input":{"command":"git push"}}`, `"permissionDecision":"deny"`},
		{"Codex patch of settings", `{"tool_name":"apply_patch","turn_id":"t","tool_input":{"command":"*** Begin Patch\n*** Add File: .claude/settings.local.json\n+{\"disableAllHooks\": true}\n*** End Patch\n"}}`, `"permissionDecision":"deny"`},
		{"Codex patch of code", `{"tool_name":"apply_patch","turn_id":"t","tool_input":{"command":"*** Begin Patch\n*** Update File: main.go\n@@\n-a\n+b\n*** End Patch\n"}}`, ""},
		{"ECA push", `{"hook_type":"preToolCall","agent":"code","tool_name":"shell_command","tool_input":{"command":"git push"}}`, `"approval":"deny"`},
		{"ECA commit", `{"hook_type":"preToolCall","agent":"code","tool_name":"shell_command","tool_input":{"command":"git commit -m x"}}`, ""},
		{"ECA coordinator commit", `{"hook_type":"preToolCall","agent":"pr-coordinator","tool_name":"shell_command","tool_input":{"command":"git commit -m x"}}`, `"approval":"deny"`},
		{"ECA owner push", `{"hook_type":"preToolCall","agent":"pr-owner","tool_name":"shell_command","tool_input":{"command":"pr-loop push"}}`, `"approval":"deny"`},
		{"ECA reviewer stash", `{"hook_type":"preToolCall","agent":"reviewer","tool_name":"shell_command","tool_input":{"command":"git stash"}}`, `"approval":"deny"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out, errs bytes.Buffer
			if code := Main([]string{"--baseline"}, strings.NewReader(c.event), &out, &errs); code != 0 {
				t.Errorf("%s: exit %d: %s", c.event, code, errs.String())
			}
			if (c.want == "" && out.Len() != 0) || !strings.Contains(out.String(), c.want) {
				t.Errorf("%s: printed %s", c.event, out.String())
			}
		})
	}

	t.Run("unknown flag", func(t *testing.T) {
		var out, errs bytes.Buffer
		if code := Main([]string{"--only", "reviewer"}, strings.NewReader(`{"tool_name":"Bash"}`), &out, &errs); code == 0 {
			t.Errorf("an unknown flag was accepted: %s", out.String())
		}
	})
}

// TestReviewerReasons holds every denial a reviewer meets to the one thing
// it should do instead, wherever the rule it broke comes from.
func TestReviewerReasons(t *testing.T) {
	for _, command := range []string{"git push origin x", "git commit -m x", "gh pr comment 1 -b hi",
		"git sign-pass", "pr-loop reply T --body-file -"} {
		t.Run(command, func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"command": command})
			if reason := CheckReviewer("Bash", "", input); !strings.HasPrefix(reason, "A reviewer only reads.") || !saysInstead(reason) {
				t.Errorf("%q: %q", command, reason)
			}
		})
	}
}

func TestFormats(t *testing.T) {
	for _, c := range []struct {
		name, event, want string
	}{
		{"Claude Code PreToolUse", `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push"}}`, `"permissionDecision":"deny"`},
		{"ECA shell command", `{"hook_type":"preToolCall","server":"eca","tool_name":"shell_command","tool_input":{"command":"git push"}}`, `"approval":"deny"`},
		{"ECA connector", `{"hook_type":"preToolCall","server":"linear","tool_name":"save_issue","tool_input":{}}`, `"approval":"deny"`},
		{"owner push", `{"tool_name":"Bash","agent_type":"pr-loop:pr-owner","tool_input":{"command":"pr-loop push"}}`, `"permissionDecision":"deny"`},
		{"reviewer fixup", `{"tool_name":"Bash","agent_type":"reviewer","tool_input":{"command":"git commit --fixup=abc"}}`, `"permissionDecision":"deny"`},
		{"owner fixup", `{"tool_name":"Bash","agent_type":"pr-loop:pr-owner","tool_input":{"command":"git commit --fixup=abc"}}`, ""},
		{"coordinator push", `{"tool_name":"Bash","tool_input":{"command":"pr-loop push"}}`, ""},
		{"ls", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			Main(nil, strings.NewReader(c.event), &out, io.Discard)
			if (c.want == "" && out.Len() != 0) || !strings.Contains(out.String(), c.want) {
				t.Errorf("%s: printed %s", c.event, out.String())
			}
		})
	}
}
