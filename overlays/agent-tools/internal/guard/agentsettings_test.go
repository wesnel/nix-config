package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentSettings covers a clone's agent settings, info/agents.json in
// its common git directory: no agent writes them by any route, through a
// link, a hard link or another worktree's .git file included, and any
// agent reads them.
func TestAgentSettings(t *testing.T) {
	h := fakeHome(t)
	work := filepath.Join(h, "work")
	main := filepath.Join(work, "repo")
	os.MkdirAll(main, 0o755)
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(work, "wt")
	gitIn(t, main, "worktree", "add", "-q", "-b", "side", wt)
	settings := filepath.Join(main, ".git", "info", "agents.json")
	os.MkdirAll(filepath.Dir(settings), 0o755)
	os.WriteFile(settings, []byte("{}"), 0o644)

	// A clone whose git directory is apart from its worktree, which a .git
	// file names, and a hard link to its settings under a plain name.
	sepDir := filepath.Join(h, "gitdirs", "sep")
	sep := filepath.Join(work, "sep")
	os.MkdirAll(filepath.Dir(sepDir), 0o755)
	gitIn(t, work, "init", "-q", "-b", "main", "--separate-git-dir="+sepDir, sep)
	sepSettings := filepath.Join(sepDir, "info", "agents.json")
	os.MkdirAll(filepath.Dir(sepSettings), 0o755)
	os.WriteFile(sepSettings, []byte("{}"), 0o644)
	hard := filepath.Join(work, "plain")
	if err := os.Link(sepSettings, hard); err != nil {
		t.Fatal(err)
	}

	dirLink := filepath.Join(work, "info-link")
	os.Symlink(filepath.Dir(settings), dirLink)
	fileLink := filepath.Join(work, "settings-link")
	os.Symlink(settings, fileLink)
	os.WriteFile(filepath.Join(work, "x.json"), []byte("{}"), 0o644)
	scratch := filepath.Join(work, "scratch")
	os.MkdirAll(scratch, 0o755)
	gitIn(t, scratch, "init", "-q")

	write := func(path string) map[string]string { return map[string]string{"file_path": path, "content": "{}"} }
	bash := func(command string) map[string]string { return map[string]string{"command": command} }
	for _, c := range []struct {
		name, tool string
		input      map[string]string
		dir        string
		deny       bool
	}{
		{"written", "Write", write(settings), wt, true},
		{"edited", "Edit", map[string]string{"file_path": settings, "old_string": "{}", "new_string": `{"test": "true"}`}, main, true},
		{"patched", "apply_patch", map[string]string{"command": "*** Begin Patch\n*** Add File: " + settings +
			"\n+{}\n*** End Patch\n"}, main, true},
		{"written through a linked directory", "Write", write(filepath.Join(dirLink, "agents.json")), work, true},
		{"written through a link to it", "Write", write(fileLink), work, true},
		{"written through a hard link, found by a .git file", "Write", write(hard), sep, true},
		{"written in a git directory a .git file names", "Write", write(sepSettings), work, true},
		{"redirected", "Bash", bash("echo '{}' > .git/info/agents.json"), main, true},
		{"redirected from another worktree", "Bash", bash("echo '{}' >> ../repo/.git/info/agents.json"), wt, true},
		{"teed", "Bash", bash("echo '{}' | tee " + settings), wt, true},
		{"copied", "Bash", bash("cp ../x.json .git/info/agents.json"), main, true},
		{"copied into its directory", "Bash", bash("cp ../agents.json .git/info/"), main, true},
		{"moved", "Bash", bash("mv ../x.json " + settings), wt, true},
		{"moved away", "Bash", bash("mv .git/info/agents.json /tmp/x"), main, true},
		{"removed", "Bash", bash("rm -f " + settings), wt, true},
		{"its directory removed", "Bash", bash("rm -rf .git/info"), main, true},
		{"linked", "Bash", bash("ln -sf ../x.json " + settings), wt, true},
		{"installed", "Bash", bash("install -m 644 ../x.json .git/info/agents.json"), main, true},
		{"through git -C", "Bash", bash("git -C .git/info diff --output=agents.json"), main, true},
		{"by git config -f under git -C", "Bash", bash("git -C .git/info config -f agents.json a.b c"), main, true},
		{"read", "Bash", bash("cat .git/info/agents.json"), main, false},
		{"read with jq", "Bash", bash("jq . " + settings), wt, false},
		{"copied out", "Bash", bash("cp .git/info/agents.json /tmp/copy"), main, false},
		{"a worktree's own info/agents.json", "Write", write(filepath.Join(wt, "info", "agents.json")), wt, false},
		{"the git directory's other files", "Bash", bash("echo x >> .git/info/exclude-new"), main, false},
		// Relative, as the guard refuses a recursive removal of an absolute
		// path under /nix, where the Nix build's temporary directory is.
		{"a scratch repository without settings, removed", "Bash", bash("rm -rf scratch/.git"), work, false},
	} {
		t.Run(caseName(c.deny, c.tool+" "+c.name), func(t *testing.T) {
			input, _ := json.Marshal(c.input)
			reason := check(c.tool, input, baseline, c.dir)
			switch {
			case c.deny && reason != agentSettings:
				t.Errorf("%s %s in %s: reason %q, want the agent settings'", c.tool, input, c.dir, reason)
			case !c.deny && reason != "":
				t.Errorf("%s %s in %s: denied, %q", c.tool, input, c.dir, reason)
			}
		})
	}
	if !saysInstead(agentSettings) {
		t.Errorf("%q does not say what to do instead", agentSettings)
	}
	if foldsCase {
		input, _ := json.Marshal(write(strings.Replace(settings, "agents.json", "Agents.JSON", 1)))
		if reason := check("Write", input, baseline, main); reason != agentSettings {
			t.Errorf("a case variant of the settings: %q", reason)
		}
	}
}

// TestGitLinkFiles covers the files that tell git which repository a
// worktree belongs to, and so which agent settings apply there: a
// worktree's .git file, and a git directory's commondir and gitdir. No
// agent writes, moves or removes them by any route the agent settings'
// rule covers; any agent reads them, and git's own worktree commands
// change them.
func TestGitLinkFiles(t *testing.T) {
	h := fakeHome(t)
	work := filepath.Join(h, "work")
	main := filepath.Join(work, "repo")
	os.MkdirAll(main, 0o755)
	gitIn(t, main, "init", "-q", "-b", "main")
	gitIn(t, main, "commit", "-q", "--allow-empty", "-m", "x")
	wt := filepath.Join(work, "wt")
	gitIn(t, main, "worktree", "add", "-q", "-b", "side", wt)
	dotGit := filepath.Join(wt, ".git")
	admin := filepath.Join(main, ".git", "worktrees", "wt")
	commondir, gitdir := filepath.Join(admin, "commondir"), filepath.Join(admin, "gitdir")
	os.MkdirAll(filepath.Join(wt, "sub"), 0o755)
	link := filepath.Join(work, "dotgit-link")
	os.Symlink(dotGit, link)
	hard := filepath.Join(work, "plain")
	if err := os.Link(commondir, hard); err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(work, "scratch")
	os.MkdirAll(scratch, 0o755)
	gitIn(t, scratch, "init", "-q")
	os.WriteFile(filepath.Join(work, "x"), []byte("gitdir: /tmp/x\n"), 0o644)

	write := func(path string) map[string]string {
		return map[string]string{"file_path": path, "content": "gitdir: /tmp/elsewhere\n"}
	}
	bash := func(command string) map[string]string { return map[string]string{"command": command} }
	for _, c := range []struct {
		name, tool string
		input      map[string]string
		dir        string
		deny       bool
	}{
		{"a worktree's .git file, written", "Write", write(dotGit), wt, true},
		{"a worktree's .git file, edited", "Edit", map[string]string{"file_path": dotGit, "old_string": "gitdir", "new_string": "gitdir"}, wt, true},
		{"a worktree's .git file, patched", "apply_patch", map[string]string{"command": "*** Begin Patch\n*** Update File: " + dotGit +
			"\n@@\n-a\n+b\n*** End Patch\n"}, wt, true},
		{"a new .git file", "Write", write(filepath.Join(wt, "sub", ".git")), wt, true},
		{"a .git file through a link", "Write", write(link), work, true},
		{"commondir, written", "Write", write(commondir), wt, true},
		{"gitdir, written", "Write", write(gitdir), main, true},
		{"commondir through a hard link", "Write", write(hard), wt, true},
		{"a .git file, redirected", "Bash", bash("echo 'gitdir: /tmp/x' > .git"), wt, true},
		{"a new .git file, redirected", "Bash", bash("echo 'gitdir: /tmp/x' > sub/.git"), wt, true},
		{"a .git file, teed", "Bash", bash("echo 'gitdir: /tmp/x' | tee " + dotGit), main, true},
		{"a .git file, copied", "Bash", bash("cp ../x .git"), wt, true},
		{"a .git file, moved over", "Bash", bash("mv ../x " + dotGit), main, true},
		{"a .git file, moved away", "Bash", bash("mv .git /tmp/x"), wt, true},
		{"a .git file, removed", "Bash", bash("rm .git"), wt, true},
		{"a .git file, linked", "Bash", bash("ln -sf /tmp/x .git"), wt, true},
		{"a .git file, installed", "Bash", bash("install ../x sub/.git"), wt, true},
		{"commondir, redirected", "Bash", bash("echo /tmp/x > ../repo/.git/worktrees/wt/commondir"), wt, true},
		{"gitdir, removed", "Bash", bash("rm " + gitdir), wt, true},
		{"commondir through git -C", "Bash", bash("git -C ../repo/.git/worktrees/wt diff --output=commondir"), wt, true},
		{"a .git file by git config -f under git -C", "Bash", bash("git -C sub config -f .git a.b c"), wt, true},
		{"a .git file, read", "Bash", bash("cat .git"), wt, false},
		{"commondir, read", "Bash", bash("cat " + commondir), wt, false},
		{"git's worktree commands", "Bash", bash("git worktree list"), wt, false},
		{"a file named like one", "Write", write(filepath.Join(wt, "sub", "x.git")), wt, false},
		// Relative, as in TestAgentSettings.
		{"a scratch repository's .git directory, removed", "Bash", bash("rm -rf scratch/.git"), work, false},
	} {
		t.Run(caseName(c.deny, c.tool+" "+c.name), func(t *testing.T) {
			input, _ := json.Marshal(c.input)
			reason := check(c.tool, input, baseline, c.dir)
			switch {
			case c.deny && reason != gitLink:
				t.Errorf("%s %s in %s: reason %q, want the git link files'", c.tool, input, c.dir, reason)
			case !c.deny && reason != "":
				t.Errorf("%s %s in %s: denied, %q", c.tool, input, c.dir, reason)
			}
		})
	}
	if !saysInstead(gitLink) {
		t.Errorf("%q does not say what to do instead", gitLink)
	}
}
