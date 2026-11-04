package signpass

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
)

// TestOutsideRefs checks the summary of a pass during which another session
// commits to a branch unrelated to the signed range, as an agent in its own
// worktree does while the operator touches the key: that branch is not one
// the pass moved or pushed, and is reported apart with its old and new
// values, while the branches stacked inside the range still are moved and
// pushed.
func TestOutsideRefs(t *testing.T) {
	t.Run("single branch", func(t *testing.T) {
		repo, hub, newOther := outsideRepo(t)
		result, err := Run("", false, false, true)
		if err != nil {
			t.Fatal(err)
		}
		summary := result.(map[string]any)
		checkOutside(t, repo, hub, newOther, []map[string]any{summary}, summary["pushed"].(map[string]string), "lower", "upper")
	})
	t.Run("stacked plan", func(t *testing.T) {
		repo, hub, newOther := outsideRepo(t)
		result, err := Plan("", []string{"upper"}, false, true)
		if err != nil {
			t.Fatal(err)
		}
		summary := result.(map[string]any)
		results := []map[string]any{summary["branches"].(map[string]any)["upper"].(map[string]any)}
		checkOutside(t, repo, hub, newOther, results, summary["pushed"].(map[string]string), "lower", "upper")
	})
}

// outsideRepo is a repository whose stack lower, upper is unsigned, with a
// branch other at main that the signer moves to an unsigned commit at its
// first signature, newOther. It leaves upper checked out.
func outsideRepo(t *testing.T) (repo, hub, newOther string) {
	t.Helper()
	dir, repo, hub := unmovedRepo(t)
	gitIn(t, repo, "branch", "other", "main")
	gitIn(t, repo, "switch", "-qc", "lower", "main")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "L1")
	gitIn(t, repo, "switch", "-qc", "upper")
	gitIn(t, repo, "commit", "-q", "--allow-empty", "-m", "U1")
	oldOther := gitIn(t, repo, "rev-parse", "other")
	newOther = gitIn(t, repo, "-c", "commit.gpgsign=false", "commit-tree", "-p", oldOther, "-m", "Another session's commit", "other^{tree}")
	moved := filepath.Join(dir, "moved")
	signer := filepath.Join(dir, "signer")
	script := fmt.Sprintf("#!/bin/sh\ncase \"$*\" in *'-Y sign'*) if [ ! -e '%[1]s' ]; then touch '%[1]s'; "+
		"env -u GIT_DIR -u GIT_INDEX_FILE -u GIT_WORK_TREE git -C '%[2]s' update-ref refs/heads/other %[3]s %[4]s; fi;; esac\n"+
		"exec ssh-keygen \"$@\"\n", moved, repo, newOther, oldOther)
	if err := os.WriteFile(signer, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "config", "gpg.ssh.program", signer)
	t.Cleanup(func() {
		if _, err := os.Stat(moved); err != nil {
			t.Errorf("the signer never moved the other branch: %v", err)
		}
	})
	return repo, hub, newOther
}

// checkOutside fails unless the results of the pass's steps list each of the
// branches in own as moved and the pushes include them, neither names other,
// the remote lacks other, and the results report other as changed outside
// the pass.
func checkOutside(t *testing.T, repo, hub, newOther string, results []map[string]any, pushed map[string]string, own ...string) {
	t.Helper()
	moved := map[string]string{}
	changed := map[string]json.RawMessage{}
	for _, result := range results {
		for b, oid := range result["moved"].(map[string]string) {
			moved[b] = oid
		}
		raw, err := json.Marshal(result["changedOutsidePass"])
		if err != nil {
			t.Fatal(err)
		}
		var step map[string]json.RawMessage
		if err := json.Unmarshal(raw, &step); err != nil {
			t.Fatal(err)
		}
		for b, change := range step {
			changed[b] = change
		}
	}
	for _, b := range own {
		if _, ok := moved[b]; !ok {
			t.Errorf("%s is stacked in the range but not moved: %v", b, moved)
		}
		if _, ok := pushed[b]; !ok {
			t.Errorf("%s is stacked in the range but not pushed: %v", b, pushed)
		}
	}
	if oid, ok := moved["other"]; ok {
		t.Errorf("another session's branch is among the moved, at %s: %v", oid, moved)
	}
	if why, ok := pushed["other"]; ok {
		t.Errorf("another session's branch is among the pushed: %q", why)
	}
	if got, _ := git.Run("-C", hub, "rev-parse", "-q", "--verify", "refs/heads/other"); got != "" {
		t.Errorf("the remote has another session's branch at %s", got)
	}
	want := fmt.Sprintf(`{"other":{"old":%q,"new":%q}}`, gitIn(t, repo, "rev-parse", "main"), newOther)
	got, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("changed outside the pass: %s, want %s", got, want)
	}
}
