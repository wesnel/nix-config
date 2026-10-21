package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if reason := CheckBaseline("Bash", input); !saysInstead(reason) {
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
		{false, "Bash", "git commit -m 'plain commit'"},
		{false, "Bash", "git commit --amend --no-edit"},
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
		{true, "Bash", "pr-loop post 12"},
		{true, "Bash", "claude-review-loop --bots 12"},
		{true, "Bash", "PR_LOOP_MODE=bots pr-loop reply T --body-file -"},
		{false, "Bash", "pr-loop end 12"},
		{true, "Bash", "git sign-pass --push"},
		{true, "Bash", "git-sign-pass"},
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
			reason := CheckBaseline(c.tool, input)
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
		{true, "git --config-env=alias.x=CMD x"}, // CMD's value is not on the line
		{false, "CMD=status git --config-env=alias.x=CMD x"},
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
			reason := CheckBaseline("Bash", input)
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
			reason := CheckBaseline("Bash", input)
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
			Main([]string{"--baseline"}, bytes.NewReader(event), &out, io.Discard)
			if (out.Len() != 0) != deny {
				t.Errorf("%s from the event's cwd: deny=%v, output %s", file, !deny, out.String())
			}
		})
	}
}

// TestPolicy holds the guard to the examples in policy.json. A rule's scope
// is the least strict role it holds for: baseline (every session) or
// reviewer. Each denial's reason, which is what the
// agent sees, must say what to do instead; a rule's `why` is for whoever
// maintains it and reaches no agent.
func TestPolicy(t *testing.T) {
	data, err := os.ReadFile("policy.json")
	if err != nil {
		t.Fatal(err)
	}
	// The examples name no repository, so they run outside one, as in the
	// Nix build; a checkout's remotes would decide `git fetch origin`.
	t.Chdir(t.TempDir())
	var rules []struct {
		Why   string   `json:"why"`
		Scope string   `json:"scope"`
		Deny  []string `json:"deny"`
		Allow []string `json:"allow"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rules); err != nil {
		t.Fatal(err)
	}
	checks := map[role]func(string, json.RawMessage) string{baseline: CheckBaseline, reviewer: CheckReviewer}
	names := map[role]string{baseline: "baseline", reviewer: "reviewer"}
	scopes := map[string]role{"baseline": baseline, "reviewer": reviewer}
	for _, r := range rules {
		t.Run(r.Why, func(t *testing.T) {
			if len(r.Deny) == 0 || len(r.Allow) == 0 {
				t.Errorf("%s: needs examples both ways", r.Why)
			}
			scope, ok := scopes[r.Scope]
			if !ok {
				t.Errorf("%s: scope %q is not a role", r.Why, r.Scope)
			}
			for denied, commands := range map[bool][]string{true: r.Deny, false: r.Allow} {
				for _, command := range commands {
					input, _ := json.Marshal(map[string]string{"command": command})
					for who, check := range checks {
						// A denial holds for the rule's role and every stricter
						// one. An allow example holds only for the rule's own
						// role: another rule can deny it to a stricter one.
						want, held := denied && who >= scope, who == scope
						if denied {
							held = who >= scope
						}
						reason := check("Bash", input)
						if got := reason != ""; held && got != want {
							t.Errorf("%s: guard deny=%v for the %s's %q", r.Why, got, names[who], command)
						}
						if reason != "" && !saysInstead(reason) {
							t.Errorf("%s: reason %q for the %s's %q does not say what to do instead", r.Why, reason, names[who], command)
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

// testBaselineCommands covers the rules for every session that policy.json's
// examples do not, and the baseline's allowances that a loop role does not
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
		{true, "emacs -Q --daemon=/tmp/et.ab12/server"},
		{true, "emacs -Q --batch --bg-daemon"},
		{true, "emacs -Q --batch -daemon"},
		{true, "emacs -Q --batch --daem"},
		{true, "emacs -Q --batch --da=x"},
		{true, "emacs -Q --batch -bg-daemon"},
		{true, "emacs -Q --batch -fg-daemon=x"},
		{true, "emacs -Q --batch --fg-d"},
		{true, "emacs -Q --batch -daemon=/tmp/et.ab12/server"},
		{true, "emacs-30.2 --daemon"},
		{true, "emacs-30.2 -Q --batch --daemon"},
		{false, "emacs-30.2 -Q --batch -l test.el"},
		{false, "emacs -Q --batch -d :0 -bg red -l test.el"},
		{false, "emacs -Q --batch --eval '(message \"x\")' -l test.el"},
		{false, "emacs -Q --batch --init-directory=/tmp/et.ab12 -l test.el -f ert-run-tests-batch-and-exit"},
		{false, "timeout 60 emacs --quick --script check.el"},
		{false, "/Applications/Emacs.app/Contents/MacOS/Emacs -Q -batch -l x.el"},
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
			reason := CheckBaseline("Bash", input)
			if (reason != "") != c.deny {
				t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
			}
			if reason != "" && !saysInstead(reason) {
				t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
			}
		})
	}
}

// testBaselineRoles covers the two hooks: the one every session runs, and
// the one a reviewer agent's definition adds, which also keeps it to
// reading. A call's agent type changes neither.
func testBaselineRoles(t *testing.T) {
	for _, c := range []struct {
		deny    bool
		hook    string
		command string
	}{
		{true, "--baseline", "git push origin x"},
		{true, "--baseline", "gh pr merge 1"},
		{false, "--baseline", "git commit -m x"},
		{false, "--baseline", "pr-loop push"},
		{false, "--baseline", "pr-loop link 3 4"},
		{true, "--reviewer", "git commit --fixup=abc"},
		{true, "--reviewer", "pr-loop link 3 4"},
		{false, "--reviewer", "pr-loop stack 3"},
		{true, "--reviewer", "git commit -m x"},
		{true, "--reviewer", "agent-plan log p --kind done"},
		{false, "--reviewer", "agent-plan status p"},
		{true, "--reviewer", "git stash"},
		{true, "--reviewer", "git delegate a..b"},
		{true, "--reviewer", "gh pr create --draft --title t --body-file b"},
		{false, "--reviewer", "gh pr view 1"},
	} {
		t.Run(caseName(c.deny, c.hook+" "+c.command), func(t *testing.T) {
			event, _ := json.Marshal(map[string]any{"tool_name": "Bash", "agent_type": "adversarial-reviewer",
				"tool_input": map[string]string{"command": c.command}})
			var out, errs bytes.Buffer
			if code := Main([]string{c.hook}, bytes.NewReader(event), &out, &errs); code != 0 {
				t.Errorf("%s %q: exit %d: %s", c.hook, c.command, code, errs.String())
			}
			if denied := strings.Contains(out.String(), `"permissionDecision":"deny"`); denied != c.deny {
				t.Errorf("%s %q: deny=%v, output %s", c.hook, c.command, denied, out.String())
			}
		})
	}
}

// testBaselineFormats covers the hook every session runs, as each agent
// calls it: Claude Code with its agent_type, Codex with turn_id and no
// agent.
func testBaselineFormats(t *testing.T) {
	for _, c := range []struct {
		name, event, want string
	}{
		{"Claude Code push", `{"tool_name":"Bash","tool_input":{"command":"git push"}}`, `"permissionDecision":"deny"`},
		{"Claude Code sub-agent commit", `{"tool_name":"Bash","agent_type":"builder","tool_input":{"command":"git commit -m x"}}`, ""},
		{"Claude Code commit", `{"tool_name":"Bash","tool_input":{"command":"git commit -m x"}}`, ""},
		{"Codex Bash merge", `{"tool_name":"Bash","turn_id":"t","tool_input":{"command":"gh pr merge 1"}}`, `"permissionDecision":"deny"`},
		{"Codex exec_command push", `{"tool_name":"exec_command","turn_id":"t","tool_input":{"command":"git push"}}`, `"permissionDecision":"deny"`},
		{"Codex patch of settings", `{"tool_name":"apply_patch","turn_id":"t","tool_input":{"command":"*** Begin Patch\n*** Add File: .claude/settings.local.json\n+{\"disableAllHooks\": true}\n*** End Patch\n"}}`, `"permissionDecision":"deny"`},
		{"Codex patch of code", `{"tool_name":"apply_patch","turn_id":"t","tool_input":{"command":"*** Begin Patch\n*** Update File: main.go\n@@\n-a\n+b\n*** End Patch\n"}}`, ""},
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

// TestReviewerGit holds a reviewer to git's reading commands in the
// checkout: every other command, or a reading one's writing form, is
// denied.
func TestReviewerGit(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, c := range []struct {
		deny    bool
		command string
	}{
		{false, "git log --oneline a..b"},
		{false, "git -C /x show abc --stat"},
		{false, "git diff a b"},
		{false, "git status --short"},
		{false, "git grep -n foo"},
		{false, "git blame a.go"},
		{false, "git range-diff a...b"},
		{false, "git merge-tree --write-tree a b"},
		{false, "git branch"},
		{false, "git branch -a --contains abc"},
		{false, "git branch --list 'p*'"},
		{false, "git tag -l"},
		{false, "git worktree list"},
		{false, "git stash list"},
		{false, "git remote -v"},
		{false, "git config --get user.name"},
		{false, "git symbolic-ref HEAD"},
		{false, "git archive HEAD"},
		{false, "git apply --check a.diff"},
		{false, "git --version"},
		{true, "git checkout-index -f -a"},
		{true, "git symbolic-ref HEAD refs/heads/x"},
		{true, "git branch -f other HEAD"},
		{true, "git branch new"},
		{true, "git sparse-checkout set x"},
		{true, "git bisect start"},
		{true, "git fetch . a:b"},
		{true, "git remote remove origin"},
		{true, "git config core.hooksPath x"},
		{true, "git tag v1"},
		{true, "git worktree add ../x"},
		{true, "git apply a.diff"},
		{true, "git archive -o a.tar HEAD"},
		{true, "git archive -oREADME.md HEAD"},
		{true, "git format-patch --stdout -oout -1"},
		{true, "git reflog write refs/heads/main a b msg"},
		{true, "git diff --output=a.diff a b"},
		{true, "git grep -Ovim foo"},
		{true, "git format-patch -1"},
		{true, "git hash-object -w a.go"},
		{true, "git reflog expire --all"},
		{true, "git lg"},
		{true, "git submodule foreach 'git status'"},
		{true, "git bisect run make"},
		// A reading command's configuration and environment are its own.
		{false, "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git log"},
		{false, "git -c core.pager=cat log"},
		{false, "git log -p --textconv"},
		{false, "git diff --no-ext-diff"},
		{false, "PAGER=cat git log"},
		{false, "HOME=/tmp/h go vet ./..."},
		// Searching the checkout, with git or without it.
		{false, "rg foo"},
		{false, "rg -n foo ."},
		{false, "git grep -n foo -- ."},
	} {
		t.Run(caseName(c.deny, c.command), func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"command": c.command})
			reason := CheckReviewer("Bash", input)
			if (reason != "") != c.deny {
				t.Errorf("%q: deny=%v, %q", c.command, reason != "", reason)
			}
		})
	}
}

// TestReviewerScratchRepo lets a reviewer run any git command but push in
// a repository of its own under scratch, and holds it to reading wherever
// the command could reach the checkout under review or its repository:
// through a copy of the worktree's .git file, a link, core.worktree, or a
// file it writes.
func TestReviewerScratchRepo(t *testing.T) {
	// The checkout is a linked worktree of a repository outside scratch.
	repo, checkout, scratch := filepath.Join(t.TempDir(), "repo"), filepath.Join(t.TempDir(), "w"), t.TempDir()
	gitIn(t, filepath.Dir(repo), "init", "-q", "-b", "main", repo)
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "feat", checkout)
	if err := os.Mkdir(filepath.Join(checkout, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", scratch)
	dir := func(name string) string {
		d := filepath.Join(scratch, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		return d
	}
	x := dir("x")
	own := dir("own") // a repository of the reviewer's own
	gitIn(t, own, "init", "-q")
	gitIn(t, own, "commit", "-q", "--allow-empty", "-m", "own")
	// `cp -R CHECKOUT copy` copies the worktree's .git file, which leads
	// back to the reviewed repository.
	copied := dir("copy")
	gitfile, err := os.ReadFile(filepath.Join(checkout, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copied, ".git"), gitfile, 0o600); err != nil {
		t.Fatal(err)
	}
	// A link from scratch into the checkout.
	linked := dir("linked")
	if err := os.Symlink(filepath.Join(checkout, "sub"), filepath.Join(linked, "link")); err != nil {
		t.Fatal(err)
	}
	// A repository in scratch whose work tree is the checkout.
	elsewhere := dir("elsewhere")
	gitIn(t, elsewhere, "init", "-q")
	gitIn(t, elsewhere, "config", "core.worktree", checkout)
	inner := dir("repo") // a checkout inside scratch
	for _, c := range []struct {
		deny    bool
		cwd     string
		command string
	}{
		{false, checkout, "git -C " + x + " init"},
		{false, checkout, "git -C " + x + " -c commit.gpgsign=false commit -m x"},
		{false, checkout, "cd " + x + " && git add f && git commit -m x"},
		{false, checkout, "git -C " + own + " commit --allow-empty -m mutant"},
		{false, checkout, "git -C " + own + " branch -f other HEAD"},
		{false, checkout, "git -C $TMPDIR/x init"},
		{false, checkout, `git -C "${TMPDIR}/x" init`},
		{false, checkout, "git init " + scratch + "/new"},
		{false, checkout, "git clone -q . " + scratch + "/clone"},
		{false, checkout, "git -C " + own + " archive -o " + scratch + "/a.tar HEAD"},
		{false, checkout, "cd $TMPDIR/own && git commit --allow-empty -m mutant"},
		{false, checkout, `cd "${TMPDIR}/own" && git branch -f other HEAD`},
		{true, checkout, "git commit -m x"},
		{true, checkout, "git -C '$TMPDIR/x' init"},
		{true, checkout, "cd '$TMPDIR/own' && git commit --allow-empty -m mutant"},
		{true, checkout, "cd $ELSEWHERE/own && git commit --allow-empty -m mutant"},
		// A cd holds only over what && joins to it: past any other
		// separator the commit may run in the checkout.
		{false, checkout, "cd $TMPDIR/own && git add f && git commit --allow-empty -m mutant"},
		{false, checkout, "(cd $TMPDIR/own && git commit --allow-empty -m mutant)"},
		{true, checkout, "cd $TMPDIR/nope; git commit --allow-empty -m mutant"},
		{true, checkout, "cd $TMPDIR/own; git commit --allow-empty -m mutant"},
		{true, checkout, "cd $TMPDIR/own || git commit --allow-empty -m mutant"},
		{true, checkout, "cd $TMPDIR/own | git commit --allow-empty -m mutant"},
		{true, checkout, "cd $TMPDIR/own & git commit --allow-empty -m mutant"},
		{true, checkout, "(cd $TMPDIR/own) && git commit --allow-empty -m mutant"},
		{true, checkout, "cd $TMPDIR/own && git log; git commit --allow-empty -m mutant"},
		// A cd that is the condition holds over the branch it guards, and
		// only there.
		{false, checkout, "if cd $TMPDIR/own; then git commit --allow-empty -m mutant; fi"},
		{false, checkout, "if cd $TMPDIR/own; then git branch -f other HEAD; git commit --allow-empty -m mutant; fi"},
		{false, checkout, "while cd $TMPDIR/own; do git commit --allow-empty -m mutant; break; done"},
		{false, checkout, "cd $TMPDIR/own && { git branch -f other HEAD; git commit --allow-empty -m mutant; }"},
		{true, checkout, "if cd $TMPDIR/own; then true; fi; git commit --allow-empty -m mutant"},
		{true, checkout, "if cd $TMPDIR/own; then true; else git commit --allow-empty -m mutant; fi"},
		{true, checkout, "cd $TMPDIR/own && { true; }; git commit --allow-empty -m mutant"},
		{true, checkout, "if true; cd $TMPDIR/own; then git commit --allow-empty -m mutant; fi"},
		{true, checkout, "{ cd $TMPDIR/own; git commit --allow-empty -m mutant; }"},
		{true, checkout, "git clone -q ."},
		{true, checkout, "git -C " + x + " push " + checkout + " HEAD:main"},
		{true, checkout, "git -C " + own + " send-pack " + checkout + " HEAD:refs/heads/x"},
		{true, checkout, "git -C " + x + " --git-dir=" + checkout + "/.git commit"},
		{true, checkout, "git --git-dir=" + checkout + "/.git -C " + x + " commit"},
		{true, checkout, "GIT_DIR=" + checkout + "/.git git -C " + x + " commit"},
		{true, checkout, "GIT_WORK_TREE=" + checkout + " git -C " + x + " add ."},
		{true, x, "git commit -m x"},
		{true, inner, "git -C " + scratch + " init"},
		{true, inner, "git -C " + inner + "/sub init"},
		// The copied worktree's git directory is the reviewed repository's.
		{true, checkout, "git -C " + copied + " commit --allow-empty -m mutant"},
		{true, checkout, "git -C " + copied + " branch -f feat HEAD"},
		{true, checkout, "cd " + copied + " && git reset --hard"},
		// Each -C is a chdir through the link, so .. lands in the checkout.
		{true, checkout, "git -C " + linked + "/link -C .. update-ref refs/heads/x HEAD"},
		{true, checkout, "git -C " + linked + "/link/.. update-ref refs/heads/x HEAD"},
		{true, checkout, "git -C " + elsewhere + " add ."},
		// Files written into the checkout from a repository of its own.
		{true, checkout, "git -C " + own + " archive -o " + checkout + "/a.tar HEAD"},
		{true, checkout, "git -C " + own + " format-patch -o" + checkout + "/p -1"},
		{true, checkout, "git -C " + own + " worktree add " + checkout + "/w2"},
		{true, checkout, "git -C " + own + " diff --output=" + checkout + "/d HEAD"},
		{true, checkout, "git -C " + x + " init " + checkout + "/sub2"},
	} {
		t.Run(caseName(c.deny, c.command), func(t *testing.T) {
			t.Chdir(c.cwd)
			input, _ := json.Marshal(map[string]string{"command": c.command})
			reason := CheckReviewer("Bash", input)
			if (reason != "") != c.deny || c.deny && reason != reviewerGit {
				t.Errorf("%q in %s: %q", c.command, c.cwd, reason)
			}
		})
	}
}

// TestGitShellAlias reads a shell alias given for one git command, on its
// command line or in the environment, as the shell command it is, for
// every session: what the guard denies typed alone, it denies there. An
// alias it cannot read, or an included file of settings, is refused.
func TestGitShellAlias(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	// Configuration files a command may name as git's global or system one.
	for name, text := range map[string]string{
		"alias":   "[alias]\n\tp = !git push\n",
		"include": "[include]\n\tpath = elsewhere\n",
		"plain":   "[user]\n\tname = x\n[alias]\n\tst = status\n\ts = !git status\n",
	} {
		if err := os.WriteFile(filepath.Join(scratch, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	checkout := t.TempDir()
	t.Chdir(checkout)
	for _, c := range []struct {
		deny    bool
		check   func(string, json.RawMessage) string
		command string
	}{
		{true, CheckBaseline, "git -c alias.p='!git push origin HEAD' p"},
		{true, CheckBaseline, "git -c ALIAS.P='!git push' p"},
		{true, CheckBaseline, `git -c "alias.s=!sleep 60" s`},
		{false, CheckBaseline, "git -c alias.s='!git status' s"},
		{false, CheckBaseline, "git -c alias.st=status st"},
		{true, CheckBaseline, "git -c alias.p=' !git push' p"},
		{true, CheckBaseline, `git -c "alias.x=$CMD" x`},
		{true, CheckBaseline, "V='!git push' git --config-env=alias.p=V p"},
		{true, CheckBaseline, "git --config-env alias.p=V p"},
		{true, CheckBaseline, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0='!git push' git p"},
		{true, CheckBaseline, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p git p"},
		{true, CheckBaseline, `GIT_CONFIG_PARAMETERS="'alias.p=!git push'" git p`},
		{true, CheckBaseline, `GIT_CONFIG_PARAMETERS="'alias.p'='!git push'" git p`},
		{true, CheckBaseline, `export GIT_CONFIG_PARAMETERS="'alias.p=!git push'"; git p`},
		{true, CheckBaseline, "export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0='!git push'; git p"},
		{true, CheckBaseline, "export GIT_CONFIG_KEY_0=alias.p; git p"},
		{false, CheckBaseline, `GIT_CONFIG_PARAMETERS="'alias.s=!git status'" git s`},
		{false, CheckBaseline, "V='!git status' git --config-env=alias.s=V s"},
		{true, CheckBaseline, "git -c include.path=" + scratch + "/inc status"},
		{true, CheckBaseline, "git -c includeIf.onbranch:main.path=" + scratch + "/inc status"},
		{true, CheckBaseline, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=include.path GIT_CONFIG_VALUE_0=" + scratch + "/inc git status"},
		{true, CheckBaseline, `GIT_CONFIG_PARAMETERS="'include.path=` + scratch + `/inc'" git status`},
		// A key that comes from a variable could be an alias's.
		{true, CheckBaseline, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=$K GIT_CONFIG_VALUE_0='!git push' git p"},
		{true, CheckBaseline, "git -c $K='!git push' p"},
		{true, CheckBaseline, `git -c "$E" p`},
		{true, CheckBaseline, "V='!git push' git --config-env=$K=V p"},
		{true, CheckBaseline, `GIT_CONFIG_PARAMETERS="'$K=!git push'" git p`},
		{false, CheckBaseline, `git -c user.name="$NAME" status`},
		// A global or system configuration file is read as an include is.
		{true, CheckBaseline, "GIT_CONFIG_GLOBAL=" + scratch + "/alias git p"},
		{true, CheckBaseline, "GIT_CONFIG_SYSTEM=" + scratch + "/alias git p"},
		{true, CheckBaseline, "export GIT_CONFIG_GLOBAL=" + scratch + "/alias; git p"},
		{true, CheckBaseline, "GIT_CONFIG_GLOBAL=" + scratch + "/include git status"},
		{true, CheckBaseline, "GIT_CONFIG_GLOBAL=" + scratch + "/missing git status"},
		{true, CheckBaseline, "GIT_CONFIG_GLOBAL=$CFG git status"},
		{false, CheckBaseline, "GIT_CONFIG_GLOBAL=" + scratch + "/plain GIT_CONFIG_NOSYSTEM=1 git s"},
		{false, CheckBaseline, "GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 git status"},
		{false, CheckBaseline, "GIT_CONFIG_GLOBAL=$TMPDIR/plain git status"},
		{true, CheckReviewer, "git -C " + scratch + " -c alias.zz='!git -C " + checkout + " commit -m x' zz"},
		{true, CheckReviewer, "V='!git -C " + checkout + " commit -m x' git -C " + scratch + " --config-env=alias.zz=V zz"},
	} {
		t.Run(caseName(c.deny, c.command), func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"command": c.command})
			if reason := c.check("Bash", input); (reason != "") != c.deny {
				t.Errorf("%q: deny=%v, %q", c.command, reason != "", reason)
			}
		})
	}
	// A reviewer sub-agent's alias writing into the checkout, as the same
	// redirection typed alone is.
	for _, command := range []string{
		"echo x > " + checkout + "/zz",
		"git -C " + scratch + " -c alias.zz='!echo x > " + checkout + "/zz' zz",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.zz GIT_CONFIG_VALUE_0='!echo x > " + checkout + "/zz' git -C " + scratch + " zz",
		// A cd that may fail leaves the write in the checkout.
		"cd $TMPDIR/nope; echo x > zz",
	} {
		t.Run(caseName(true, command), func(t *testing.T) {
			event, _ := json.Marshal(map[string]any{"tool_name": "Bash", "agent_type": "adversarial-reviewer",
				"agent_id": "a1", "cwd": checkout, "tool_input": map[string]string{"command": command}})
			var out bytes.Buffer
			Main([]string{"--baseline"}, bytes.NewReader(event), &out, io.Discard)
			if !strings.Contains(out.String(), `"deny"`) {
				t.Errorf("%q: %s", command, out.String())
			}
		})
	}
}

// TestOpenedRepo holds the scratch rule's look at a repository to git's own
// answer, refusing when git errs or hangs there.
func TestOpenedRepo(t *testing.T) {
	scratch := t.TempDir()
	own := filepath.Join(scratch, "own")
	gitIn(t, scratch, "init", "-q", own)
	empty := filepath.Join(scratch, "empty")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if paths, ok := openedRepo(own); !ok || len(paths) != 3 {
		t.Errorf("a repository of its own: %q, %v", paths, ok)
	}
	for _, dir := range []string{empty, filepath.Join(scratch, "missing")} {
		if paths, ok := openedRepo(dir); !ok || len(paths) != 0 {
			t.Errorf("no repository yet in %s: %q, %v", dir, paths, ok)
		}
	}
	// A git that fails otherwise, or does not answer in time, is a refusal.
	fake := t.TempDir()
	for name, script := range map[string]string{
		"fails": "#!/bin/sh\necho 'fatal: detected dubious ownership' >&2\nexit 128\n",
		"hangs": "#!/bin/sh\nexec sleep 30\n",
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(fake, "git"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", fake)
			old := repoTimeout
			repoTimeout = 200 * time.Millisecond
			t.Cleanup(func() { repoTimeout = old })
			if paths, ok := openedRepo(own); ok {
				t.Errorf("git %s: %q passed", name, paths)
			}
		})
	}
}

// TestReviewerReasons holds every denial a reviewer meets to the one thing
// it should do instead, wherever the rule it broke comes from.
func TestReviewerReasons(t *testing.T) {
	for _, command := range []string{"git push origin x", "git commit -m x", "gh pr comment 1 -b hi",
		"git sign-pass", "pr-loop reply T --body-file -"} {
		t.Run(command, func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{"command": command})
			if reason := CheckReviewer("Bash", input); !strings.HasPrefix(reason, "A reviewer only reads.") || !saysInstead(reason) {
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
		{"a sub-agent's fixup", `{"tool_name":"Bash","agent_type":"adversarial-reviewer","tool_input":{"command":"git commit --fixup=abc"}}`, ""},
		{"pr-loop push", `{"tool_name":"Bash","tool_input":{"command":"pr-loop push"}}`, ""},
		{"ls", `{"tool_name":"Bash","tool_input":{"command":"ls"}}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			Main([]string{"--baseline"}, strings.NewReader(c.event), &out, io.Discard)
			if (c.want == "" && out.Len() != 0) || !strings.Contains(out.String(), c.want) {
				t.Errorf("%s: printed %s", c.event, out.String())
			}
		})
	}
}
