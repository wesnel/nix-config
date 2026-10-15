package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/gitenv"
)

// A change keeps its patch ID however its commits are cut, signed or
// rebased, and a different change gets another.
func TestPatchID(t *testing.T) {
	repo := repository(t)
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(message string) {
		t.Helper()
		gitIn(t, repo, "add", "-A")
		gitIn(t, repo, "commit", "-q", "-m", message)
	}
	gitIn(t, repo, "branch", "base")
	gitIn(t, repo, "switch", "-q", "-c", "two")
	write("a", "1\n")
	commit("Add a")
	write("a", "1\n2\n")
	commit("Grow a")
	gitIn(t, repo, "switch", "-q", "-c", "one", "base")
	write("a", "1\n2\n")
	commit("Add a, grown")
	gitIn(t, repo, "switch", "-q", "-c", "other", "base")
	write("a", "1\n3\n")
	commit("Add another a")
	// The base moves on, and a copy of two is rebased onto it.
	gitIn(t, repo, "switch", "-q", "-c", "moved", "base")
	write("b", "b\n")
	commit("Add b")
	gitIn(t, repo, "switch", "-q", "-c", "rebased", "two")
	gitIn(t, repo, "rebase", "-q", "moved")
	gitIn(t, repo, "switch", "-q", "base")

	id := func(base, tip string) string {
		t.Helper()
		got, err := PatchID([]string{"-C", repo}, base, tip)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	// git's own pipeline for the change, as a person would type it.
	cmd := exec.Command("sh", "-c", "git diff base...two | git patch-id --stable")
	cmd.Dir, cmd.Env = repo, append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	if len(want) != 40 {
		t.Fatalf("git patch-id printed %q", out)
	}

	if got := id("base", "two"); got != want {
		t.Errorf("two commits: %q, want %q", got, want)
	}
	if got := id("base", "one"); got != want {
		t.Errorf("the same change in one commit: %q, want %q", got, want)
	}
	if got := id("moved", "rebased"); got != want {
		t.Errorf("rebased onto a moved base: %q, want %q", got, want)
	}
	if got := id("base", "other"); got == want || got == "" {
		t.Errorf("another change: %q", got)
	}
	if got := id("two", "two"); got != "" {
		t.Errorf("no change: %q", got)
	}
}
