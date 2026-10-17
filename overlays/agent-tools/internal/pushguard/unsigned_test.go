package pushguard

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

const (
	operator = "operator@example.com"
	other    = "other@example.com"
)

// signer is a repository whose commits can be signed with a throwaway SSH
// key, verified through an allowed-signers file, so no gpg or hardware key
// is involved.
type signer struct {
	t        *testing.T
	dir, key string
}

// newSigner builds the repository, with the operator's identity configured,
// a remote origin whose remote-tracking main holds one of the operator's
// unsigned commits, and points the package's git at it.
func newSigner(t *testing.T) (s *signer, published string) {
	t.Helper()
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not on PATH")
	}
	keys := t.TempDir()
	key := filepath.Join(keys, "key")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(keys, "allowed")
	if err := os.WriteFile(allowed, []byte(operator+" "+string(pub)), 0o600); err != nil {
		t.Fatal(err)
	}
	s = &signer{t: t, dir: t.TempDir(), key: key}
	s.git(nil, "init", "-q", "-b", "main")
	s.git(nil, "config", "user.name", "Operator")
	s.git(nil, "config", "user.email", operator)
	s.git(nil, "config", "gpg.format", "ssh")
	s.git(nil, "config", "user.signingkey", key)
	s.git(nil, "config", "gpg.ssh.allowedSignersFile", allowed)
	s.git(nil, "remote", "add", "origin", "git@github.com:org/repo.git")
	s.git(nil, "remote", "set-url", "--add", "--push", "origin", "git@github.com:org/repo.git")
	s.git(nil, "remote", "set-url", "--add", "--push", "origin", "git@git.sr.ht:~me/repo")
	published = s.commit(operator, operator, false, "published unsigned")
	s.git(nil, "update-ref", "refs/remotes/origin/main", published)
	git.Dir = s.dir
	t.Cleanup(func() { git.Dir = "" })
	return s, published
}

func (s *signer) git(env []string, args ...string) string {
	s.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = s.dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit makes an empty commit on HEAD, authored and committed by the
// given addresses, signed or not, and returns it.
func (s *signer) commit(author, committer string, signed bool, subject string) string {
	s.t.Helper()
	sign := "--no-gpg-sign"
	if signed {
		sign = "-S"
	}
	s.git([]string{"GIT_AUTHOR_EMAIL=" + author, "GIT_COMMITTER_EMAIL=" + committer},
		"commit", "-q", "--allow-empty", sign, "-m", subject)
	return s.git(nil, "rev-parse", "HEAD")
}

// forged is a copy of signed, which must be signed, with another message
// and the same signature, which no longer matches it.
func (s *signer) forged(signed string) string {
	s.t.Helper()
	raw := s.git(nil, "cat-file", "commit", signed)
	head, _, _ := strings.Cut(raw, "\n\n")
	cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
	cmd.Dir = s.dir
	cmd.Stdin = strings.NewReader(head + "\n\nforged\n")
	out, err := cmd.Output()
	if err != nil {
		s.t.Fatalf("git hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestCheckUnsigned(t *testing.T) {
	branch := func(local, remote string) []Update {
		return []Update{{"refs/heads/x", local, "refs/heads/x", remote}}
	}
	const url = "git@github.com:org/repo.git"

	cases := []struct {
		name string
		// build adds commits to the repository and returns what is pushed
		// and to which remote, as git names it to the hook, and URL.
		build   func(s *signer, published string) (remote, url string, updates []Update)
		refused bool
	}{
		{"own unsigned commit", func(s *signer, _ string) (string, string, []Update) {
			return "origin", url, branch(s.commit(operator, operator, false, "mine"), zero)
		}, true},
		{"own signed commit", func(s *signer, _ string) (string, string, []Update) {
			return "origin", url, branch(s.commit(operator, operator, true, "mine"), zero)
		}, false},
		{"own address in another case", func(s *signer, _ string) (string, string, []Update) {
			return "origin", url, branch(s.commit(operator, strings.ToUpper(operator), false, "mine"), zero)
		}, true},
		{"someone else's unsigned commit", func(s *signer, _ string) (string, string, []Update) {
			return "origin", url, branch(s.commit(other, other, false, "theirs"), zero)
		}, false},
		{"theirs, committed by the operator", func(s *signer, _ string) (string, string, []Update) {
			return "origin", url, branch(s.commit(other, operator, false, "picked"), zero)
		}, true},
		{"the operator's, committed by someone else", func(s *signer, _ string) (string, string, []Update) {
			return "origin", url, branch(s.commit(operator, other, false, "rebased"), zero)
		}, false},
		{"unsigned commit the remote ref already has", func(s *signer, _ string) (string, string, []Update) {
			old := s.commit(operator, operator, false, "pushed before")
			return "origin", url, branch(s.commit(operator, operator, true, "signed"), old)
		}, false},
		{"unsigned commits replaced by signed ones", func(s *signer, published string) (string, string, []Update) {
			old := s.commit(operator, operator, false, "unsigned")
			s.git(nil, "reset", "-q", "--hard", published)
			return "origin", url, branch(s.commit(operator, operator, true, "signed"), old)
		}, false},
		{"history the remote's tracking refs hold, by remote name", func(s *signer, _ string) (string, string, []Update) {
			return "origin", url, branch(s.commit(operator, operator, true, "signed"), zero)
		}, false},
		{"history the remote's tracking refs hold, by push URL", func(s *signer, _ string) (string, string, []Update) {
			return "git@git.sr.ht:~me/repo", "git@git.sr.ht:~me/repo", branch(s.commit(operator, operator, true, "signed"), zero)
		}, false},
		{"history another remote holds", func(s *signer, _ string) (string, string, []Update) {
			return "git@github.com:org/fork.git", "git@github.com:org/fork.git", branch(s.commit(operator, operator, true, "signed"), zero)
		}, true},
		{"signed where this machine cannot verify it", func(s *signer, _ string) (string, string, []Update) {
			s.git(nil, "config", "--unset", "gpg.ssh.allowedSignersFile")
			return "origin", url, branch(s.commit(operator, operator, true, "signed"), zero)
		}, false},
		{"bad signature", func(s *signer, _ string) (string, string, []Update) {
			return "origin", url, branch(s.forged(s.commit(operator, operator, true, "signed")), zero)
		}, true},
		{"annotated tag", func(s *signer, _ string) (string, string, []Update) {
			s.commit(operator, operator, false, "tagged")
			s.git(nil, "tag", "-a", "-m", "v1", "v1")
			return "origin", url, []Update{{"refs/tags/v1", s.git(nil, "rev-parse", "v1"), "refs/tags/v1", zero}}
		}, true},
		{"tag of a tree", func(s *signer, _ string) (string, string, []Update) {
			tree := s.git(nil, "rev-parse", "HEAD^{tree}")
			return "origin", url, []Update{{"refs/tags/t", tree, "refs/tags/t", zero}}
		}, false},
		{"deleting a branch", func(s *signer, _ string) (string, string, []Update) {
			old := s.commit(operator, operator, false, "mine")
			return "origin", url, []Update{{"(delete)", zero, "refs/heads/x", old}}
		}, false},
		{"to a path on this machine", func(s *signer, _ string) (string, string, []Update) {
			return "/srv/repo.git", "/srv/repo.git", branch(s.commit(operator, operator, false, "mine"), zero)
		}, false},
		{"to a file URL", func(s *signer, _ string) (string, string, []Update) {
			return "file:///srv/repo.git", "file:///srv/repo.git", branch(s.commit(operator, operator, false, "mine"), zero)
		}, false},
		{"allowed for this push", func(s *signer, _ string) (string, string, []Update) {
			s.git(nil, "config", UnsignedKey, "true")
			return "origin", url, branch(s.commit(operator, operator, false, "mine"), zero)
		}, false},
		{"allowance turned off", func(s *signer, _ string) (string, string, []Update) {
			s.git(nil, "config", UnsignedKey, "false")
			return "origin", url, branch(s.commit(operator, operator, false, "mine"), zero)
		}, true},
		{"no identity configured", func(s *signer, _ string) (string, string, []Update) {
			tip := s.commit(operator, operator, false, "mine")
			s.git(nil, "config", "--unset", "user.email")
			return "origin", url, branch(tip, zero)
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, published := newSigner(t)
			remote, url, updates := c.build(s, published)
			err := CheckUnsigned(remote, url, updates)
			var r refusal.Refusal
			if c.refused && !errors.As(err, &r) || !c.refused && err != nil {
				t.Errorf("err = %v, want refused = %v", err, c.refused)
			}
		})
	}
}

// TestCheckUnsignedReason checks that a refusal names each commit and both
// ways forward.
func TestCheckUnsignedReason(t *testing.T) {
	s, _ := newSigner(t)
	first := s.commit(operator, operator, false, "first change")
	s.commit(operator, operator, true, "signed change")
	second := s.commit(other, operator, false, "second change")
	err := CheckUnsigned("origin", "git@github.com:org/repo.git", []Update{{"refs/heads/x", second, "refs/heads/x", zero}})
	var r refusal.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("not refused: %v", err)
	}
	msg := r.Error()
	for _, want := range []string{
		s.git(nil, "rev-parse", "--short", first) + " first change",
		s.git(nil, "rev-parse", "--short", second) + " second change",
		"`git sign-pass`",
		"git -c push-guard.allowUnsigned=true push",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "signed change") || strings.Contains(msg, "published unsigned") {
		t.Errorf("refusal names a commit it should not:\n%s", msg)
	}
	if !refusal.SaysInstead(msg) {
		t.Errorf("refusal does not say what to do instead:\n%s", msg)
	}
}

// TestCheckUnsignedTimeout checks that a signature check that hangs, as one
// waiting on an agent or a key would, refuses the push rather than holding
// it.
func TestCheckUnsignedTimeout(t *testing.T) {
	s, _ := newSigner(t)
	tip := s.commit(operator, operator, true, "signed")
	hang := filepath.Join(t.TempDir(), "hang")
	if err := os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	s.git(nil, "config", "gpg.ssh.program", hang)
	defer func(d time.Duration) { verifyTimeout = d }(verifyTimeout)
	verifyTimeout = time.Second

	start := time.Now()
	err := CheckUnsigned("origin", "git@github.com:org/repo.git", []Update{{"refs/heads/x", tip, "refs/heads/x", zero}})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("the check took %v", elapsed)
	}
	var r refusal.Refusal
	if !errors.As(err, &r) || !strings.Contains(r.Error(), "took over") || !strings.Contains(r.Error(), UnsignedKey) {
		t.Errorf("err = %v, want a refusal for the time taken naming %s", err, UnsignedKey)
	}
}
