package pushguard

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

const zero = "0000000000000000000000000000000000000000"

// fork builds a checkout whose origin is a private fork of a public
// upstream: upstream's history is public, the fork's own commits are not.
// It returns the checkout, the fork's private commit and a new commit on
// public history, and points the package's git at the checkout.
func fork(t *testing.T) (dir, private, fix string) {
	t.Helper()
	dir = t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "public base")
	base := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "private work")
	private = run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "remote", "add", "origin", "git@github.com:org/private.git")
	run(t, dir, "remote", "add", "upstream", "https://github.com/org/public")
	run(t, dir, "remote", "add", "mirror", "git@git.sr.ht:~me/mirror")
	run(t, dir, "update-ref", "refs/remotes/origin/main", private)
	run(t, dir, "update-ref", "refs/remotes/upstream/main", base)
	run(t, dir, "switch", "-q", "-c", "fix", base)
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "new public fix")
	fix = run(t, dir, "rev-parse", "HEAD")
	git.Dir = dir
	t.Cleanup(func() { git.Dir = "" })
	return dir, private, fix
}

func TestCheck(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// Stands in for GitHub's visibility lookup, which reaches outside the
	// machine.
	visibility := map[string]bool{"org/public": true, "org/other-public": true, "org/private": false}
	lookup := func(slug string) (bool, error) {
		public, ok := visibility[slug]
		if !ok {
			return false, errors.New("offline")
		}
		return public, nil
	}
	push := func(local string) []Update {
		return []Update{{"refs/heads/x", local, "refs/heads/x", zero}}
	}
	deleting := func(remote string) []Update {
		return []Update{{"(delete)", zero, "refs/heads/x", remote}}
	}

	// Each case names the commit it pushes: the fork's private commit, or
	// the new commit on public history.
	cases := []struct {
		name    string
		url     string
		updates func(private, fix string) []Update
		allow   string
		refused bool
	}{
		{"private history to a public repository", "git@github.com:org/other-public.git", func(p, _ string) []Update { return push(p) }, "", true},
		{"private history to upstream", "https://github.com/org/public", func(p, _ string) []Update { return push(p) }, "", true},
		{"new work on public history", "git@github.com:org/other-public.git", func(_, f string) []Update { return push(f) }, "", false},
		{"to the private repository itself", "git@github.com:org/private.git", func(p, _ string) []Update { return push(p) }, "", false},
		{"to a host other than GitHub", "git@git.sr.ht:~me/mirror", func(p, _ string) []Update { return push(p) }, "", false},
		{"allowed for this repository", "git@github.com:org/other-public.git", func(p, _ string) []Update { return push(p) }, "org/other-public", false},
		{"allowed for another repository", "git@github.com:org/other-public.git", func(p, _ string) []Update { return push(p) }, "org/public", true},
		{"deleting a branch", "git@github.com:org/other-public.git", func(p, _ string) []Update { return deleting(p) }, "", false},
		{"visibility unknown", "git@github.com:org/unknown.git", func(_, f string) []Update { return push(f) }, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, private, fix := fork(t)
			if c.allow != "" {
				run(t, dir, "config", AllowKey, c.allow)
			}
			err := Check(c.url, c.updates(private, fix), lookup)
			var r refusal.Refusal
			if c.refused != errors.As(err, &r) {
				t.Errorf("refused = %v, want %v (err: %v)", err != nil, c.refused, err)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Run("one update", func(t *testing.T) {
		updates, err := Parse(strings.NewReader("refs/heads/a 1111 refs/heads/a 2222\n\n"))
		if err != nil || len(updates) != 1 || updates[0].Remote != "2222" {
			t.Fatalf("Parse = %v, %v", updates, err)
		}
	})

	t.Run("malformed input", func(t *testing.T) {
		if _, err := Parse(strings.NewReader("garbage\n")); err == nil {
			t.Error("malformed input was accepted")
		}
	})
}
