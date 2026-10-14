package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// fakeHome points the guard's ~ at a directory of the test's own.
func fakeHome(t *testing.T) string {
	t.Helper()
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	old := home
	home = dir
	t.Cleanup(func() { home = old })
	return dir
}

// judge runs the baseline hook on a shell command run in dir, and returns
// the reason it denies it with.
func judge(t *testing.T, dir, command string) string {
	t.Helper()
	input, _ := json.Marshal(map[string]string{"command": command})
	return check("Bash", "", input, baseline, dir)
}

func TestSessionRules(t *testing.T) {
	// Not under /nix, where the Nix build's own temporary directory is.
	h, old := "/home-for-the-test", home
	home = h
	t.Cleanup(func() { home = old })
	cases := []struct {
		deny    bool
		command string
	}{
		{true, "cat ~/.ssh/id_ed25519"},
		{true, "base64 < $HOME/.ssh/id_rsa"},
		{true, "cp " + h + "/.codex/auth.json /tmp/x"},
		{true, "cat ~/.claude/.credentials.json"},
		{true, "cat ~/.local/state/mcp-proxy/token"},
		{true, "ls ~/.gnupg/private-keys-v1.d"},
		{true, "cat ~/.config/sops/age/keys.txt"},
		{true, "cat ~/.config/ngrok/ngrok.yml"},
		{true, "security find-generic-password -s x -w"},
		{true, "security find-internet-password -gs github.com"},
		{true, "security dump-keychain"},
		{true, "git credential fill"},
		{true, "git credential-osxkeychain get"},
		{true, "sops -d secrets/x.yaml"},
		{true, "sops decrypt secrets/x.yaml"},
		{true, "op read op://v/i/f"},
		{true, "op item get x"},
		{true, "pass show work/x"},
		{true, "pass work/x"},
		{true, "env"},
		{true, "env -0"},
		{true, "printenv"},
		{true, "printenv NGROK_AUTHTOKEN_PROD"},
		{true, "printenv GITHUB_TOKEN"},
		{true, "export -p"},
		{true, "export"},
		{true, "set"},
		{true, "declare -p"},
		{true, "git config core.bare true"},
		{true, "git config --local core.hooksPath /tmp/h"},
		{true, "git config include.path ../x"},
		{true, "git config filter.x.clean cat"},
		{true, "git config diff.x.textconv cat"},
		{true, "git config set core.fsmonitor true"},
		{true, "ngrok api endpoints create --url x"},
		{true, "ngrok api reserved-domains delete rd_1"},
		{true, "darwin-rebuild switch --flake ."},
		{true, "home-manager switch --flake .#devbox"},
		{true, "nixos-rebuild boot"},
		{true, "./result/activate"},
		{true, "nix profile install nixpkgs#hello"},
		{true, "nix-env -iA nixpkgs.hello"},
		{true, "nix-collect-garbage -d"},
		{true, "nix copy --to ssh://devbox ./result"},
		{true, "cachix push wgn ./result"},
		{true, "git reflog expire --expire=now --all"},
		{true, "git gc --prune=now"},
		{true, "git prune"},
		{true, "git filter-branch --tree-filter x HEAD"},
		{true, "git filter-repo --path x"},
		{true, "git update-ref -d refs/heads/x"},
		{true, "git worktree prune"},
		{true, "echo '{}' > ~/.claude/settings.json"},
		{true, "echo x >> .claude/settings.local.json"},
		{true, "sed -i '' s/x/y/ ~/.codex/hooks.json"},
		{true, "cp hooks.json ~/.codex/hooks.json"},
		{true, "cp /tmp/x.json ~/.claude/settings.json"},
		{true, "cp -t ~/.claude settings.json"},
		{true, "cp settings.json ~/.claude"},
		{true, "dd if=/tmp/x of=.claude/settings.json"},
		{true, "install -gstaff x ~/.claude/settings.json"},
		{true, "install -otom x ~/.codex/hooks.json"},
		{true, "install -m 644 x ~/.codex/hooks.json"},
		{true, "cp -S .old x ~/.codex/hooks.json"},
		{true, "perl -pi -e 's/a/b/' .claude/settings.json"},
		{true, "perl -lpi -e 1 .claude/settings.json"},
		{true, "perl -i.bak -pe 's/a/b/' .claude/settings.json"},
		{true, "sed -Ei 's/a/b/' .claude/settings.json"},
		{true, "sed -ni 's/a/b/p' .claude/settings.json"},
		{true, "sed -i.bak s/a/b/ .claude/settings.json"},
		{true, "sed --in-place=.bak s/a/b/ .claude/settings.json"},
		{true, "tee ~/.config/eca/config.json < x"},
		{true, "claude --dangerously-skip-permissions"},
		{true, "claude --permission-mode bypassPermissions -p x"},
		{true, "codex --dangerously-bypass-approvals-and-sandbox exec x"},
		{true, "sudo darwin-rebuild switch"},
		{true, "doas true"},
		{true, "su -"},
		{true, "pkexec ls"},
		{true, `osascript -e 'do shell script "x" with administrator privileges'`},
		{true, "npm publish"},
		{true, "yarn npm publish"},
		{true, "pnpm publish --access public"},
		{true, "cargo publish"},
		{true, "twine upload dist/*"},
		{true, "gem push x.gem"},
		{true, "docker push img"},
		{true, "podman image push img"},
		{true, "docker buildx build --push ."},
		{true, "goreleaser release --clean"},
		{true, "goreleaser"},
		{true, "vsce publish"},
		{true, "npx @vscode/vsce publish"},
		{true, "pkill emacs"},
		{true, "killall -9 gpg-agent"},
		{true, "pkill -f 'emacs --daemon'"},
		{true, "killall Emacs"},
		{true, "pkill mcp-proxy"},
		{true, "gpgconf --kill gpg-agent"},
		{true, "ssh -O exit devbox"},
		{true, "launchctl bootout gui/501/org.x"},
		{true, "launchctl kickstart -k gui/501/org.x"},
		{true, "emacsclient -e '(kill-emacs)'"},
		{true, "kill -9 -1"},
		{true, "kill 0"},
		{true, "rm -rf ~"},
		{true, "rm -rf $HOME/"},
		{true, `rm -rf "$HOME"`},
		{true, "rm -rf ~/*"},
		{true, "rm -rf /"},
		{true, "rm -r /nix"},
		{true, "rm ~/.ssh/known_hosts"},
		{true, "chmod -R 777 ~/.ssh"},
		{true, "chown -R x ~"},
		{true, "trash ~/.gnupg"},
		{true, "mv ~/.ssh /tmp/x"},
		{true, "find ~ -name '*.tmp' -delete"},
		{true, "cd /tmp && rm -rf ~"},
		{false, "cd ~ && rm -rf build"},
		{false, "cat ~/.ssh/id_ed25519.pub"},
		{false, "ls ~/.ssh"},
		{false, "cat ~/.claude/settings.json"},
		{false, "cp ~/.claude/settings.json /tmp/x.json"},
		{false, "diff ~/.claude/settings.json /tmp/x.json"},
		{false, "dd if=.claude/settings.json of=/tmp/x"},
		{false, "sed -n 's/a/b/p' .claude/settings.json"},
		{false, "perl -ne 'print if /i/' .claude/settings.json"},
		{false, "install -m 644 ~/.codex/hooks.json /tmp/h"},
		{false, "git config --global alias.lg log"},
		{false, "security find-generic-password -s x"},
		{false, "sops -e x.yaml"},
		{false, "pass ls"},
		{false, "printenv PATH"},
		{false, "env FOO=1 go test ./..."},
		{false, "export FOO=1"},
		{false, "set -euo pipefail"},
		{false, "git config --worktree core.bare false"},
		{false, "git config --get core.bare"},
		{false, "git config get core.hooksPath"},
		{false, "git config user.name T"},
		{false, "ngrok api endpoints list"},
		{false, "darwin-rebuild build --flake ."},
		{false, "home-manager build"},
		{false, "nix build .#x"},
		{false, "nix flake check"},
		{false, "nix copy --to /tmp/store ./result"},
		{false, "nix-env -q"},
		{false, "git gc"},
		{false, "git fetch --prune"},
		{false, "git remote prune origin"},
		{false, "git update-ref refs/heads/x HEAD"},
		{false, "echo x > notes.md"},
		{false, "claude -p 'hello'"},
		{false, "codex exec x"},
		{false, "echo issue"},
		{false, "npm test"},
		{false, "cargo build"},
		{false, "docker build ."},
		{false, "goreleaser check"},
		{false, "pkill -f my-test-server"},
		{false, "launchctl list"},
		{false, "emacsclient -e '(+ 1 2)'"},
		{false, "kill %1"},
		{false, "rm -rf ./build"},
		{false, "rm -rf ~/src/x/build"},
		{false, "rm ~/notes.txt"},
		{false, "chmod -R u+w result"},
		{false, "find . -name '*.tmp' -delete"},
		{false, "mv a b"},
	}
	for _, c := range cases {
		reason := judge(t, filepath.Join(h, "work"), c.command)
		if (reason != "") != c.deny {
			t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
		}
	}
}

// TestEnvSearch covers searching the environment with grep, which passes
// only when no credential's line can match.
func TestEnvSearch(t *testing.T) {
	old := environ
	t.Cleanup(func() { environ = old })
	vars := []string{"PATH=/usr/bin", "GOPATH=/go", "GOFLAGS=-mod=mod", "NGROK_AUTHTOKEN=2abc",
		"GUARD_TEST_TOKEN=(abc)", "SIGNING_KEY=-----BEGIN\nMIIabc\n-----END"}
	environ = func() []string { return vars }
	cases := []struct {
		deny    bool
		command string
	}{
		{false, "env | grep GO"},
		{false, "printenv | grep -i path"},
		{false, "env | grep -n -E '^GO(PATH|FLAGS)='"},
		{false, "bash -c 'env | grep GO'"},
		{false, "env | grep '^GO'"},
		{false, "env | grep 'mod$'"},
		{false, "env | grep -F 'a.b'"},
		{true, "env | grep -F 'GUARD_TEST_TOKEN=(abc)'"},
		{true, "env | grep 'GUARD_TEST_TOKEN=(abc)'"},
		{true, "env | grep '=(a'"},
		{true, "env | grep '^MII'"},
		{true, "env | grep -E '^MII'"},
		{true, "env | grep 'END$'"},
		{true, "env | grep 'x^y'"},
		{true, "env | grep 'x$y'"},
		{true, "env | grep -E -F GO"},
		{true, "env"},
		{true, "env | grep -v ZZZ"},
		{true, "env | grep ."},
		{true, "env | grep -i token"},
		{true, "env | grep 2abc"},
		{true, "env | grep -e GO"},
		{true, "env | grep -f patterns"},
		{true, "env | grep GO | grep -v x"},
		{true, "env; grep GO x"},
		{true, "env || grep GO"},
		{true, "env | grep $P"},
		{true, `env | grep 'GO\|NGROK'`},
		{true, "env | grep '['"},
		{true, "env | grep GO > /tmp/x; cat /tmp/x"},
	}
	for _, c := range cases {
		reason := judge(t, "", c.command)
		if (reason != "") != c.deny {
			t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
		}
	}
	vars = append(vars, "GH_TOKEN=ghp_x")
	if judge(t, "", "env | grep GH") == "" {
		t.Error("a search matching GH_TOKEN's line passed")
	}
}

// gitIn runs git in dir, isolated from the caller's repository.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = gitenv.Without(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestGitState covers the rules that depend on where a git command runs
// and what the repository holds there.
func TestGitState(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	main := filepath.Join(root, "main")
	linked := filepath.Join(root, "linked")
	os.Mkdir(main, 0o755)
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, main, "worktree", "add", "-q", "-b", "feature", linked)
	gitIn(t, main, "branch", "pushed")
	gitIn(t, linked, "commit", "-q", "--allow-empty", "-m", "only here")
	gitIn(t, main, "branch", "unpushed", "feature")
	gitIn(t, main, "update-ref", "refs/remotes/origin/pushed", "pushed")
	gitIn(t, main, "worktree", "add", "-q", "--detach", filepath.Join(root, "clean"))
	gitIn(t, main, "worktree", "add", "-q", "--detach", filepath.Join(root, "dirty"))
	os.WriteFile(filepath.Join(root, "dirty", "new"), []byte("x\n"), 0o644)

	cases := []struct {
		deny         bool
		dir, command string
	}{
		{true, main, "git reset --hard"},
		{true, main, "git checkout ."},
		{true, main, "git checkout -f feature"},
		{true, main, "git restore ."},
		{true, main, "git switch --discard-changes feature"},
		{true, main, "git clean -fdx"},
		{true, main, "git stash -u"},
		{true, main, "git stash drop"},
		{true, main, "git stash clear"},
		{true, linked, "git stash drop"},
		{true, linked, "git stash drop stash@{1}"},
		{true, linked, "git stash clear"},
		{true, root, "git stash clear"},
		{false, linked, "git stash list"},
		{true, linked, "git -C " + main + " clean -fd"},
		{true, linked, "git -C ../main reset --hard"},
		{true, linked, "cd " + main + " && git checkout ."},
		{true, linked, "cd $SOMEWHERE && git reset --hard"},
		{false, linked, "git reset --hard"},
		{false, linked, "git checkout ."},
		{false, linked, "git clean -fdx"},
		{false, main, "git restore --staged ."},
		{false, main, "git reset --soft HEAD~1"},
		{true, main, "git stash"},
		{true, linked, "git stash"},
		{true, linked, "git stash push"},
		{true, linked, "git stash push -u -- src"},
		{true, linked, "git stash -k"},
		{true, linked, "git stash save"},
		{true, linked, "git stash pop"},
		{true, linked, "git stash pop --index"},
		{true, linked, "git stash apply -q"},
		{false, linked, "git stash push -u -m agent-x-1"},
		{false, linked, "git stash push --message=agent-x-1"},
		{false, linked, "git stash -um agent-x-1"},
		{false, linked, "git stash save agent-x-1"},
		{false, linked, "git stash apply 3f2a9c1"},
		{false, linked, "git stash pop --index stash@{2}"},
		{false, linked, "git stash show -p 3f2a9c1"},
		{false, linked, "git stash branch b 3f2a9c1"},
		{true, main, "git add -A"},
		{true, main, "git add --all src"},
		{true, main, "git add ."},
		{true, main, "git add :/"},
		{true, main, "git add '*'"},
		{true, linked, "git -C ../main add -A"},
		{false, main, "git add src/x.go README.md"},
		{false, main, "git add -p src/x.go"},
		{false, linked, "git add -A"},
		{false, linked, "git add ."},
		{true, main, "git stash push -u -m agent-x-1"},
		{false, main, "git stash push -m agent-x-1"},
		{false, main, "git clean -n"},
		{false, root, "git reset --hard"},
		{true, main, "git branch -D unpushed"},
		{true, main, "git branch -D feature"},
		{true, main, "git branch -d --force unpushed"},
		{true, main, "git branch -D gone"},
		{false, main, "git branch -D pushed"},
		{false, main, "git branch -d unpushed"},
		{true, main, "git worktree remove --force ../dirty"},
		{false, main, "git worktree remove --force ../clean"},
		{false, main, "git worktree remove ../dirty"},
	}
	for _, c := range cases {
		reason := judge(t, c.dir, c.command)
		if (reason != "") != c.deny {
			t.Errorf("%s: %q: deny=%v, reason %q", filepath.Base(c.dir), c.command, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
		}
	}
}

// TestSigning covers commands that write commits where the configuration
// signs them, which would wait for a key touch.
func TestSigning(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	signing := filepath.Join(root, "signing")
	loop := filepath.Join(root, "loop")
	plain := filepath.Join(root, "plain")
	os.Mkdir(signing, 0o755)
	os.Mkdir(plain, 0o755)
	gitIn(t, signing, "init", "-q", "-b", "main")
	gitIn(t, signing, "commit", "-q", "--allow-empty", "-m", "base")
	gitIn(t, signing, "config", "commit.gpgsign", "true")
	gitIn(t, signing, "config", "extensions.worktreeConfig", "true")
	gitIn(t, signing, "worktree", "add", "-q", "-b", "loop", loop)
	gitIn(t, loop, "config", "--worktree", "commit.gpgsign", "false")
	gitIn(t, plain, "init", "-q", "-b", "main")

	cases := []struct {
		deny         bool
		dir, command string
	}{
		{true, signing, "git commit -m x"},
		{true, signing, "git commit --fixup=abc"},
		{true, signing, "git rebase --onto a b c"},
		{true, signing, "git rebase --continue"},
		{true, signing, "git rebase --skip"},
		{true, signing, "git cherry-pick abc"},
		{true, signing, "git revert HEAD"},
		{true, signing, "git merge feature"},
		{true, signing, "git am --continue"},
		{true, signing, "git pull"},
		{true, signing, "git -c commit.gpgsign=true commit -m x"},
		{true, plain, "git -C " + signing + " commit -m x"},
		{true, plain, "git commit -S -m x"},
		{true, loop, "git commit --gpg-sign -m x"},
		{false, signing, "git -c commit.gpgsign=false commit -m x"},
		{false, signing, "git -c commit.gpgSign=no rebase --continue"},
		{false, signing, "git commit --no-gpg-sign -m x"},
		{false, signing, "GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=commit.gpgsign GIT_CONFIG_VALUE_0=false git commit -m x"},
		{false, signing, "git rebase --abort"},
		{false, signing, "git rebase --quit"},
		{false, signing, "git rebase --edit-todo"},
		{false, signing, "git cherry-pick --abort"},
		{false, signing, "git cherry-pick -n abc"},
		{false, signing, "git revert --no-commit HEAD"},
		{false, signing, "git merge --ff-only feature"},
		{false, signing, "git merge --squash feature"},
		{false, signing, "git am --show-current-patch"},
		{false, signing, "git pull --ff-only"},
		{false, signing, "git commit --dry-run"},
		{false, signing, "git status"},
		{false, loop, "git commit -m x"},
		{false, loop, "git rebase --continue"},
		{false, plain, "git commit -m x"},
		{false, signing, "cd $SOMEWHERE && git commit -m x"},
	}
	for _, c := range cases {
		reason := judge(t, c.dir, c.command)
		if (reason != "") != c.deny {
			t.Errorf("%s: %q: deny=%v, reason %q", filepath.Base(c.dir), c.command, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
		}
	}
}

// TestShellEdits covers shell commands that write a tracked file, which go
// through the file-editing tools instead, and the scratch, ignored and
// untracked files they may write.
func TestShellEdits(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	work := filepath.Join(root, "work")
	scratch := filepath.Join(root, "scratch")
	outside := filepath.Join(root, "outside")
	scratchRepo := filepath.Join(scratch, "repo")
	for _, d := range []string{filepath.Join(work, "sub"), filepath.Join(work, "build"), outside, scratchRepo} {
		os.MkdirAll(d, 0o755)
	}
	files := map[string]string{"a.go": "a\n", "sub/b.go": "b\n", ".gitignore": "build/\nvendored.txt\n", "vendored.txt": "v\n",
		"new.txt": "n\n", "build/out.log": "o\n"}
	for name, content := range files {
		os.WriteFile(filepath.Join(work, name), []byte(content), 0o644)
	}
	os.WriteFile(filepath.Join(outside, "f"), []byte("f\n"), 0o644)
	os.WriteFile(filepath.Join(scratchRepo, "a"), []byte("a\n"), 0o644)
	gitIn(t, work, "init", "-q", "-b", "main")
	gitIn(t, work, "add", "a.go", "sub/b.go", ".gitignore")
	gitIn(t, work, "add", "-f", "vendored.txt")
	gitIn(t, work, "commit", "-q", "-m", "base")
	gitIn(t, scratchRepo, "init", "-q", "-b", "main")
	gitIn(t, scratchRepo, "add", "a")
	gitIn(t, scratchRepo, "commit", "-q", "-m", "base")
	old := scratchRoots
	scratchRoots = func() []string { return []string{scratch} }
	t.Cleanup(func() { scratchRoots = old })

	cases := []struct {
		deny    bool
		command string
	}{
		{true, "echo x > a.go"},
		{true, "echo x >> a.go"},
		{true, "printf x >a.go"},
		{true, "go run gen.go 2> a.go"},
		{true, "cat > a.go <<'EOF'\npackage a\nEOF"},
		{true, "sed -i '' s/a/b/ a.go"},
		{true, "sed -i.bak -e s/a/b/ a.go"},
		{true, "sed --in-place s/a/b/ sub/b.go"},
		{true, "perl -pi -e 's/a/b/' a.go"},
		{true, "tee a.go < x"},
		{true, "echo x | tee -a sub/b.go"},
		{true, "ed -s a.go < cmds"},
		{true, "cd sub && echo x > b.go"},
		{true, "cd " + outside + " && echo x > " + work + "/a.go"},
		{false, "echo x > new.txt"},
		{false, "echo x > build/out.log"},
		{false, "echo x > vendored.txt"},
		{false, "go test ./... > " + scratch + "/out.txt"},
		{false, "agent-plan goal PLAN > " + scratch + "/goal.md"},
		{false, "echo x > " + scratchRepo + "/a"},
		{false, "echo x > /dev/null"},
		{false, "echo x >&2"},
		{false, "echo x > $F"},
		{false, "sed -n 1p a.go"},
		{false, "sed -i '' s/a/b/ " + outside + "/f"},
		{false, "sed s/a/b/ a.go > " + scratch + "/a.go"},
		{false, "perl -ne 'print' a.go"},
		{false, "cat a.go | tee " + scratch + "/copy"},
		{false, "echo x > " + outside + "/f"},
		{false, "cd $SOMEWHERE && echo x > a.go"},
	}
	for _, c := range cases {
		reason := judge(t, work, c.command)
		if (reason != "") != c.deny {
			t.Errorf("%q: deny=%v, reason %q", c.command, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%q: reason %q does not say what to do instead", c.command, reason)
		}
	}
}

// TestKill covers kill by process ID, which the guard looks up with ps.
func TestKill(t *testing.T) {
	// The protected process is a copy of this test binary named mcp-proxy,
	// running TestKillTarget. A copy of sleep will not do: where sleep is a
	// multi-call coreutils, the copy exits on its unknown name, and ps names
	// the zombie <defunct>.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	named := filepath.Join(t.TempDir(), "mcp-proxy")
	os.WriteFile(named, data, 0o755)
	protected := exec.Command(named, "-test.run=^TestKillTarget$")
	protected.Env = append(os.Environ(), "GUARD_KILL_TARGET=1")
	hold, err := protected.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := protected.Start(); err != nil {
		t.Skipf("cannot run a copy of the test binary: %v", err)
	}
	defer func() {
		hold.Close()
		protected.Wait()
	}()
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Process.Kill()
	out, _ := exec.Command("ps", "-o", "comm=", "-p", itoa(protected.Process.Pid)).Output()
	switch name := strings.TrimSpace(string(out)); {
	case name == "":
		t.Skip("ps cannot see this test's processes, as in the Nix build's sandbox")
	case filepath.Base(name) != "mcp-proxy":
		t.Fatalf("ps names the protected process %q, not mcp-proxy", name)
	}
	for command, deny := range map[string]bool{
		"kill " + itoa(protected.Process.Pid):                                         true,
		"kill -9 " + itoa(protected.Process.Pid):                                      true,
		"kill -s TERM " + itoa(other.Process.Pid) + " " + itoa(protected.Process.Pid): true,
		"kill " + itoa(other.Process.Pid):                                             false,
		"kill -TERM " + itoa(other.Process.Pid):                                       false,
	} {
		if reason := judge(t, "/", command); (reason != "") != deny {
			t.Errorf("%q: deny=%v, reason %q", command, !deny, reason)
		}
	}
}

// TestKillTarget is the process TestKill protects. It runs until its
// standard input closes, and returns so that TestMain cleans up.
func TestKillTarget(t *testing.T) {
	if os.Getenv("GUARD_KILL_TARGET") == "" {
		t.Skip("TestKill runs it")
	}
	io.Copy(io.Discard, os.Stdin)
}

var itoa = strconv.Itoa

// TestFileTools covers Claude Code's file tools and Codex's apply_patch.
func TestFileTools(t *testing.T) {
	h := fakeHome(t)
	work := filepath.Join(h, "work")
	os.MkdirAll(filepath.Join(h, ".claude"), 0o755)
	os.WriteFile(filepath.Join(h, ".claude", "settings.json"), []byte(`{"hooks": {}}`), 0o644)
	patch := func(path, body string) map[string]string {
		return map[string]string{"command": "*** Begin Patch\n*** Update File: " + path + "\n@@\n" + body + "\n*** End Patch\n"}
	}
	cases := []struct {
		deny  bool
		tool  string
		input any
	}{
		{true, "Edit", map[string]string{"file_path": h + "/.claude/settings.json", "old_string": `"hooks": {}`, "new_string": `"x": 1`}},
		{true, "Write", map[string]string{"file_path": h + "/.claude/settings.json", "content": "{}"}},
		{true, "Write", map[string]string{"file_path": work + "/.claude/settings.local.json", "content": `{"disableAllHooks": true}`}},
		{true, "MultiEdit", map[string]any{"file_path": h + "/.config/eca/config.json", "edits": []map[string]string{{"old_string": "a", "new_string": `"hooks": {}`}}}},
		{true, "apply_patch", patch(h+"/.codex/hooks.json", "-x\n+y")},
		{true, "apply_patch", patch(h+"/.codex/config.toml", "-[hooks]\n+")},
		{true, "Edit", map[string]string{"file_path": work + "/.git/config", "old_string": "a", "new_string": "b"}},
		{true, "Write", map[string]string{"file_path": h + "/.ssh/id_ed25519", "content": "x"}},
		{false, "Edit", map[string]string{"file_path": work + "/README.md", "old_string": "hooks", "new_string": "more hooks"}},
		{false, "Write", map[string]string{"file_path": work + "/.claude/settings.local.json", "content": `{"permissions": {}}`}},
		{false, "apply_patch", patch("src/main.go", "-a\n+b")},
	}
	for _, c := range cases {
		input, _ := json.Marshal(c.input)
		reason := check(c.tool, "", input, baseline, work)
		if (reason != "") != c.deny {
			t.Errorf("%s %s: deny=%v, reason %q", c.tool, input, !c.deny, reason)
		}
		if reason != "" && !saysInstead(reason) {
			t.Errorf("%s: reason %q does not say what to do instead", c.tool, reason)
		}
	}
}

// TestConnectors covers the claude.ai connectors' writes: Claude Code asks,
// and Codex, which cannot ask, denies, as does a loop.
func TestConnectors(t *testing.T) {
	for _, c := range []struct {
		event, want string
	}{
		{`{"tool_name":"mcp__claude_ai_Slack__slack_send_message","tool_input":{}}`, `"permissionDecision":"ask"`},
		{`{"tool_name":"mcp__claude_ai_Gmail__create_draft","tool_input":{}}`, `"permissionDecision":"ask"`},
		{`{"tool_name":"mcp__claude_ai_Notion__notion-create-pages","tool_input":{}}`, `"permissionDecision":"ask"`},
		{`{"tool_name":"mcp__claude_ai_Linear__save_issue","tool_input":{}}`, `"permissionDecision":"ask"`},
		{`{"tool_name":"mcp__claude_ai_Github__create_pull_request_review","tool_input":{}}`, `"permissionDecision":"ask"`},
		{`{"tool_name":"mcp__claude_ai_Slack__slack_send_message","turn_id":"t","tool_input":{}}`, `"permissionDecision":"deny"`},
		{`{"tool_name":"mcp__claude_ai_Slack__slack_send_message","agent_type":"reviewer","tool_input":{}}`, `"permissionDecision":"deny"`},
		{`{"tool_name":"mcp__claude_ai_Slack__slack_search_public","tool_input":{}}`, ""},
		{`{"tool_name":"mcp__claude_ai_Notion__notion-fetch","tool_input":{}}`, ""},
		{`{"tool_name":"mcp__claude_ai_Linear__get_issue","tool_input":{}}`, ""},
		{`{"tool_name":"mcp__ngrok__list_endpoints","tool_input":{}}`, ""},
	} {
		var out bytes.Buffer
		Main([]string{"--baseline"}, strings.NewReader(c.event), &out, io.Discard)
		if (c.want == "" && out.Len() != 0) || !strings.Contains(out.String(), c.want) {
			t.Errorf("%s: printed %s", c.event, out.String())
		}
	}
}
