package loop

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// nextStep is the one step Next gives for branch.
func nextStep(t *testing.T, branch string) NextStep {
	t.Helper()
	steps, err := Next([]string{branch})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 {
		t.Fatalf("steps: %v", steps)
	}
	return steps[0]
}

// TestNext gives each state of a branch its one next step, from pushedBranch's
// checkout: feat, one unsigned commit past main, which GitHub has at its tip.
func TestNext(t *testing.T) {
	for _, state := range []string{"fetch", "push", "upstream"} {
		t.Run("quoted operands/"+state, func(t *testing.T) {
			f, dir := pushedBranch(t)
			branch, remote := "feat'`true`", "origin'`true`"
			gitIn(t, dir, "remote", "rename", "origin", remote)
			gitIn(t, dir, "branch", "-m", "feat", branch)
			var sign []string
			if state == "push" {
				sign = signing(t, dir)
				gitIn(t, dir, append(sign, "--amend", "--no-edit", "--allow-empty")...)
			}
			tip := gitIn(t, dir, "rev-parse", "HEAD")
			f.heads[branch], f.prs[branch] = tip, 7
			gitIn(t, dir, "update-ref", "refs/remotes/"+remote+"/"+branch, tip)
			gitIn(t, dir, "config", "branch."+branch+".remote", remote)
			gitIn(t, dir, "config", "branch."+branch+".merge", "refs/heads/"+branch)
			switch state {
			case "fetch":
				f.heads[branch] = gitIn(t, dir, "rev-parse", "main")
			case "push":
				gitIn(t, dir, append(sign, "--allow-empty", "-m", "Add local work")...)
			case "upstream":
				gitIn(t, dir, "config", "branch."+branch+".merge", "refs/heads/main")
			}
			step := nextStep(t, branch)
			command := ""
			if state == "push" {
				push, ok := step.(ToPush)
				if !ok {
					t.Fatalf("push state: %#v", step)
				}
				command = push.Command
			} else {
				blocked, ok := step.(Blocked)
				if !ok {
					t.Fatalf("blocked state: %#v", step)
				}
				start, end := strings.Index(blocked.Why, "`git "), strings.LastIndex(blocked.Why, "`, then")
				if start < 0 || end <= start {
					t.Fatalf("no recovery command: %s", blocked.Why)
				}
				command = blocked.Why[start+1 : end]
			}
			out, err := exec.CommandContext(t.Context(), "sh", "-c", "printf '%s\\n' "+command).CombinedOutput()
			if err != nil {
				t.Fatalf("cannot parse recovery command: %v: %s", err, out)
			}
			words := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
			if words[len(words)-1] != branch {
				t.Errorf("branch changed: %q", words)
			}
			if state == "fetch" && (len(words) != 4 || words[2] != remote) {
				t.Errorf("remote changed: %q", words)
			}
			if state == "upstream" && (len(words) != 4 || words[2] != "--set-upstream-to="+remote+"/"+branch) {
				t.Errorf("upstream changed: %q", words)
			}
		})
	}
	t.Run("not on GitHub: open it, signed or not", func(t *testing.T) {
		_, dir := pushedBranch(t)
		gitIn(t, dir, "config", "push-guard.allowPublic", "o/r")
		gitIn(t, dir, "switch", "-qc", "fresh", "main")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add fresh")
		step, ok := nextStep(t, "fresh").(ToOpen)
		if !ok || step.Kind != "open" || !strings.HasPrefix(step.Command, "pr-loop open --title") || !strings.HasSuffix(step.Command, " fresh") {
			t.Errorf("fresh: %+v", step)
		}
	})

	t.Run("on GitHub with no pull request: open it", func(t *testing.T) {
		pushedOperatorBranch(t)
		if step, ok := nextStep(t, "feat").(ToOpen); !ok || !strings.HasSuffix(step.Command, " feat") {
			t.Errorf("feat: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("unsigned on GitHub with no pull request: signing authority blocks opening", func(t *testing.T) {
		_, dir := pushedBranch(t)
		sshOperatorProfile(t, dir)
		step, ok := nextStep(t, "feat").(Blocked)
		if !ok || !strings.Contains(step.Why, "no signature") {
			t.Errorf("unsigned published branch: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("a pull request with unsigned commits: the operator signs and pushes", func(t *testing.T) {
		f, _ := pushedBranch(t)
		f.prs["feat"] = 7
		step, ok := nextStep(t, "feat").(ToSign)
		if !ok || step.Command != "git sign-pass --push --notify ID feat" || step.Notify != "agent-plan sign-id --branch feat" || step.PR != 7 {
			t.Errorf("feat: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("a pull request at the signed tip: nothing", func(t *testing.T) {
		f, dir := pushedBranch(t)
		f.prs["feat"] = 7
		gitIn(t, dir, append(signing(t, dir), "--amend", "--no-edit", "--allow-empty")...)
		published(t, f, dir, "feat")
		if step, ok := nextStep(t, "feat").(Done); !ok || step.PR != 7 {
			t.Errorf("feat: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("GitHub behind the signed tip: the operator pushes", func(t *testing.T) {
		f, dir := pushedBranch(t)
		f.prs["feat"] = 7
		sign := signing(t, dir)
		gitIn(t, dir, append(sign, "--amend", "--no-edit", "--allow-empty")...)
		published(t, f, dir, "feat")
		gitIn(t, dir, append(sign, "--allow-empty", "-m", "Add more")...)
		if step, ok := nextStep(t, "feat").(ToPush); !ok || step.Command != "git sign-pass --push --notify ID feat" {
			t.Errorf("feat: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("rewritten signed tip is blocked even with the same tree", func(t *testing.T) {
		f, dir := pushedBranch(t)
		f.prs["feat"] = 7
		gitIn(t, dir, append(signing(t, dir), "--amend", "--no-edit", "--allow-empty")...)
		if _, ok := nextStep(t, "feat").(Blocked); !ok {
			t.Errorf("feat: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("unsigned local history cannot replace remote commits", func(t *testing.T) {
		f, dir := pushedBranch(t)
		f.prs["feat"] = 7
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add remote work")
		published(t, f, dir, "feat")
		gitIn(t, dir, "reset", "--hard", "HEAD^")
		if step := nextStep(t, "feat"); step == nil {
			t.Fatal("no next state")
		} else if _, ok := step.(Blocked); !ok {
			t.Fatalf("remote-ahead history must be blocked: %#v", step)
		}
	})

	t.Run("empty local range cannot hide remote commits", func(t *testing.T) {
		f, dir := pushedBranch(t)
		f.prs["feat"] = 7
		gitIn(t, dir, "reset", "--hard", "main")
		if step := nextStep(t, "feat"); step == nil {
			t.Fatal("no next state")
		} else if _, ok := step.(Blocked); !ok {
			t.Fatalf("remote-ahead empty local range must be blocked: %#v", step)
		}
	})

	t.Run("unrelated remote history is blocked", func(t *testing.T) {
		f, dir := pushedBranch(t)
		f.prs["feat"] = 7
		gitIn(t, dir, "switch", "--orphan", "remote-side")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Start remote work")
		remote := gitIn(t, dir, "rev-parse", "HEAD")
		f.heads["feat"] = remote
		gitIn(t, dir, "update-ref", "refs/remotes/origin/feat", remote)
		gitIn(t, dir, "switch", "feat")
		if step := nextStep(t, "feat"); step == nil {
			t.Fatal("no next state")
		} else if _, ok := step.(Blocked); !ok {
			t.Fatalf("unrelated history must be blocked: %#v", step)
		}
	})

	t.Run("branch arguments survive shell parsing", func(t *testing.T) {
		f, dir := pushedBranch(t)
		branch := "feat'`true`"
		gitIn(t, dir, "branch", "-m", "feat", branch)
		published(t, f, dir, branch)
		f.prs[branch] = 7
		step, ok := nextStep(t, branch).(ToSign)
		if !ok {
			t.Fatalf("next state: %#v", nextStep(t, branch))
		}
		for name, command := range map[string]string{"command": step.Command, "notify": step.Notify} {
			out, err := exec.CommandContext(t.Context(), "sh", "-c", "printf '%s\\n' "+command).CombinedOutput()
			if err != nil {
				t.Errorf("%s cannot be parsed: %v: %s", name, err, out)
				continue
			}
			words := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
			if words[len(words)-1] != branch {
				t.Errorf("%s changed branch argument: %q", name, words)
			}
		}
	})

	t.Run("an unpublished quoted branch has one literal open argument", func(t *testing.T) {
		_, dir := pushedBranch(t)
		branch := "fresh'`true`"
		gitIn(t, dir, "config", "push-guard.allowPublic", "o/r")
		gitIn(t, dir, "switch", "-qc", branch, "main")
		gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "Add quoted branch")
		step, ok := nextStep(t, branch).(ToOpen)
		if !ok {
			t.Fatalf("next state: %#v", nextStep(t, branch))
		}
		out, err := exec.CommandContext(t.Context(), "sh", "-c", "printf '%s\\n' "+step.Command).CombinedOutput()
		if err != nil {
			t.Fatalf("open command cannot be parsed: %v: %s", err, out)
		}
		words := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
		if words[len(words)-1] != branch {
			t.Errorf("open command changed branch: %q", words)
		}
	})

	t.Run("GitHub moved since the last fetch: fetch first", func(t *testing.T) {
		f, dir := pushedBranch(t)
		f.prs["feat"] = 7
		f.heads["feat"] = gitIn(t, dir, "rev-parse", "main")
		if step, ok := nextStep(t, "feat").(Blocked); !ok || !strings.Contains(step.Why, "git fetch origin feat") {
			t.Errorf("feat: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("an upstream of another name: blocked, saying how to fix it", func(t *testing.T) {
		f, dir := pushedBranch(t)
		f.prs["feat"] = 7
		gitIn(t, dir, "config", "branch.feat.merge", "refs/heads/main")
		if step, ok := nextStep(t, "feat").(Blocked); !ok || !strings.Contains(step.Why, "--set-upstream-to=origin/feat feat") {
			t.Errorf("feat: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("no commits of its own: nothing", func(t *testing.T) {
		_, dir := pushedBranch(t)
		gitIn(t, dir, "branch", "empty", "main")
		if step, ok := nextStep(t, "empty").(Done); !ok || step.Kind != "nothing" {
			t.Errorf("empty: %#v", nextStep(t, "empty"))
		}
	})

	t.Run("another user's pull request: blocked", func(t *testing.T) {
		f, _ := pushedBranch(t)
		f.prs["feat"], f.authors = 7, map[string]string{"feat": "someone-else"}
		if step, ok := nextStep(t, "feat").(Blocked); !ok || !strings.Contains(step.Why, "#7 is not a same-repository pull request by me") {
			t.Errorf("feat: %#v", nextStep(t, "feat"))
		}
	})

	t.Run("the current branch by default, and the text", func(t *testing.T) {
		f, _ := pushedBranch(t)
		f.prs["feat"] = 7
		steps, err := Next(nil)
		if err != nil {
			t.Fatal(err)
		}
		text := FormatNext(steps)
		if !strings.HasPrefix(text, "feat: sign:") || !strings.Contains(text, "\n  agent-plan sign-id --branch feat\n") ||
			!strings.Contains(text, "\n  git sign-pass --push --notify ID feat\n") {
			t.Errorf("text:\n%s", text)
		}
		data, err := json.Marshal(steps)
		if err != nil {
			t.Fatal(err)
		}
		var decoded []map[string]any
		json.Unmarshal(data, &decoded)
		if len(decoded) != 1 || decoded[0]["kind"] != "sign" || decoded[0]["notify"] != "agent-plan sign-id --branch feat" {
			t.Errorf("JSON: %s", data)
		}
	})
}

// Next only reads, so the guard runs it as a read: with its config and state
// homes and the repository's git directory read-only, it still checks the
// operator's signatures, and leaves every file in them as it was. It runs no
// signature verifier either, which would write in the operator's GNUPGHOME
// (a trust database, a keybox): the verifier here is a stand-in for gpg's
// writes, an ssh-keygen that first writes a file in GNUPGHOME.
func TestNextWritesNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		// pr is the branch's pull request, 0 for none; ahead is whether the
		// branch has a signed commit GitHub lacks.
		pr    int
		ahead bool
		kind  string
	}{
		{"on GitHub with no pull request: open it", 0, false, "open"},
		{"GitHub behind the signed tip: push it", 7, true, "push"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, dir := pushedBranch(t)
			_, sign := sshOperatorProfile(t, dir)
			gitIn(t, dir, append(sign, "--amend", "--no-edit", "--allow-empty")...)
			published(t, f, dir, "feat")
			if c.pr != 0 {
				f.prs["feat"] = c.pr
			}
			if c.ahead {
				gitIn(t, dir, append(sign, "--allow-empty", "-m", "Add more")...)
			}
			gnupg := t.TempDir()
			t.Setenv("GNUPGHOME", gnupg)
			verifier := filepath.Join(t.TempDir(), "ssh-keygen")
			if err := os.WriteFile(verifier, []byte("#!/bin/sh\necho \"$*\" >> \"$GNUPGHOME/verified\"\nexec ssh-keygen \"$@\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			gitIn(t, dir, "config", "gpg.ssh.program", verifier)
			dirs := []string{hostpaths.BakedConfigHome(), hostpaths.BakedStateHome(), os.Getenv("XDG_STATE_HOME"), filepath.Join(dir, ".git")}
			before := files(t, append(dirs, gnupg)...)
			readOnly(t, dirs...)
			step := nextStep(t, "feat")
			data, _ := json.Marshal(step)
			var got struct{ Kind string }
			json.Unmarshal(data, &got)
			if got.Kind != c.kind {
				t.Errorf("step %#v, want %s", step, c.kind)
			}
			if after := files(t, append(dirs, gnupg)...); !maps.Equal(before, after) {
				t.Errorf("next wrote files:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}

// files is each file and directory under dirs, with its size and time of
// change.
func files(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	all := map[string]string{}
	for _, d := range dirs {
		err := filepath.WalkDir(d, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			all[path] = fmt.Sprintf("%d %s", info.Size(), info.ModTime())
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return all
}

// readOnly takes write permission from every directory under dirs until the
// test ends.
func readOnly(t *testing.T, dirs ...string) {
	t.Helper()
	var changed []string
	for _, d := range dirs {
		filepath.WalkDir(d, func(path string, e fs.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				changed = append(changed, path)
			}
			return nil
		})
	}
	for _, d := range changed {
		if err := os.Chmod(d, 0o500); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, d := range changed {
			os.Chmod(d, 0o700)
		}
	})
}
