package loop

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestOpenCapturedTip(t *testing.T) {
	f, dir := pushedBranch(t)
	_, sign := sshOperatorProfile(t, dir)
	publishedTip := gitIn(t, dir, "rev-parse", "feat")
	gitIn(t, dir, append(sign, "--amend", "--no-edit", "--allow-empty")...)
	signedTip := gitIn(t, dir, "rev-parse", "feat")
	gitIn(t, dir, "update-ref", "refs/heads/feat", publishedTip)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// The fixture changes its local ref just after returning the captured
	// value. All actual Git behavior still comes from the installed binary.
	script := "#!/bin/sh\ncase \"$*\" in\n*'rev-parse --verify -q refs/heads/feat')\n value=$(" + shellQuote(realGit) + " \"$@\") || exit $?\n " + shellQuote(realGit) + " -C " + shellQuote(dir) + " update-ref refs/heads/feat " + signedTip + " || exit $?\n printf '%s\\n' \"$value\";;\n*) exec " + shellQuote(realGit) + " \"$@\";;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	_, err = Open("feat", "", "Add feat", "Why it is needed.", false)
	var policy Refusal
	if !errors.As(err, &policy) || len(f.calls) != 0 {
		t.Fatalf("published captured tip was not validated: %v, mutations %v", err, f.calls)
	}
	if got := gitIn(t, dir, "rev-parse", "feat"); got != signedTip {
		t.Fatal("fixture did not move the local ref")
	}
}
