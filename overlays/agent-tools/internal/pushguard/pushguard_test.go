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

// TestCheck builds a checkout whose origin is a private fork of a public
// upstream: upstream's history is public, the fork's own commits are not.
func TestCheck(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "public base")
	base := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "private work")
	private := run(t, dir, "rev-parse", "HEAD")
	run(t, dir, "remote", "add", "origin", "git@github.com:org/private.git")
	run(t, dir, "remote", "add", "upstream", "https://github.com/org/public")
	run(t, dir, "remote", "add", "mirror", "git@git.sr.ht:~me/mirror")
	run(t, dir, "update-ref", "refs/remotes/origin/main", private)
	run(t, dir, "update-ref", "refs/remotes/upstream/main", base)
	run(t, dir, "switch", "-q", "-c", "fix", base)
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "new public fix")
	fix := run(t, dir, "rev-parse", "HEAD")
	git.Dir = dir
	defer func() { git.Dir = "" }()

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

	cases := []struct {
		name    string
		url     string
		updates []Update
		allow   string
		refused bool
	}{
		{"private history to a public repository", "git@github.com:org/other-public.git", push(private), "", true},
		{"private history to upstream", "https://github.com/org/public", push(private), "", true},
		{"new work on public history", "git@github.com:org/other-public.git", push(fix), "", false},
		{"to the private repository itself", "git@github.com:org/private.git", push(private), "", false},
		{"to a host other than GitHub", "git@git.sr.ht:~me/mirror", push(private), "", false},
		{"allowed for this repository", "git@github.com:org/other-public.git", push(private), "org/other-public", false},
		{"allowed for another repository", "git@github.com:org/other-public.git", push(private), "org/public", true},
		{"deleting a branch", "git@github.com:org/other-public.git", []Update{{"(delete)", zero, "refs/heads/x", private}}, "", false},
		{"visibility unknown", "git@github.com:org/unknown.git", push(fix), "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.allow != "" {
				run(t, dir, "config", AllowKey, c.allow)
				defer run(t, dir, "config", "--unset", AllowKey)
			}
			err := Check(c.url, c.updates, lookup)
			var r refusal.Refusal
			if c.refused != errors.As(err, &r) {
				t.Errorf("refused = %v, want %v (err: %v)", err != nil, c.refused, err)
			}
		})
	}
}

func TestParse(t *testing.T) {
	updates, err := Parse(strings.NewReader("refs/heads/a 1111 refs/heads/a 2222\n\n"))
	if err != nil || len(updates) != 1 || updates[0].Remote != "2222" {
		t.Fatalf("Parse = %v, %v", updates, err)
	}
	if _, err := Parse(strings.NewReader("garbage\n")); err == nil {
		t.Error("malformed input was accepted")
	}
}
