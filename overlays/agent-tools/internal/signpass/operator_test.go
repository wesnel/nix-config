package signpass

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/wesnel/agent-tools/internal/refusal"
)

func TestSSHFingerprint(t *testing.T) {
	public := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n"
	// ssh-keygen -lf for this fixed public fixture reports this digest.
	const want = "SHA256:kmYcvdi2GkPeWxB6XLjrZB8JHsy2Hm8luHMFp9GMvqk"
	if got, err := SSHFingerprint(public); err != nil || got != want {
		t.Fatalf("public fingerprint = %q, %v; want %q", got, err, want)
	}
	if _, err := SSHFingerprint("ssh-ed25519 !"); err == nil {
		t.Fatal("invalid public encoding accepted")
	}
}

// The operator's key is the one user.signingkey names in a form a signature's
// fingerprint can be matched to; any other is refused, so no commit passes
// for the operator's on a key the pass cannot tell.
func TestOperatorKey(t *testing.T) {
	for _, c := range []struct {
		name   string
		config map[string]string
	}{
		{"no signing key", map[string]string{}},
		{"a short OpenPGP ID", map[string]string{"user.signingkey": "0x89ABCDEF"}},
		{"an OpenPGP key by its user ID", map[string]string{"user.signingkey": "T <t@e>"}},
		{"an SSH key without its public half", map[string]string{"gpg.format": "ssh", "user.signingkey": "/nonexistent/key"}},
		{"an X.509 key", map[string]string{"gpg.format": "x509", "user.signingkey": "0x89ABCDEF01234567"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := t.TempDir()
			gitIn(t, repo, "init", "-q")
			for k, v := range c.config {
				gitIn(t, repo, "config", k, v)
			}
			t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repo, "no-global"))
			useDir(t, repo)
			var r refusal.Refusal
			if _, _, err := operatorKey(); !errors.As(err, &r) {
				t.Errorf("operatorKey: %v, want a refusal", err)
			}
		})
	}
	t.Run("a long OpenPGP ID", func(t *testing.T) {
		repo := t.TempDir()
		gitIn(t, repo, "init", "-q")
		gitIn(t, repo, "config", "user.signingkey", "0x89abcdef01234567!")
		t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(repo, "no-global"))
		useDir(t, repo)
		matches, _, err := operatorKey()
		if err != nil {
			t.Fatal(err)
		}
		if !matches("0123456789ABCDEF0123456789ABCDEF01234567", "") || !matches("", "0123456789abcdef0123456789abcdef01234567") ||
			matches("1111111111111111111111111111111111111111", "") {
			t.Error("a long ID matches the fingerprints it ends, and only those")
		}
	})
}
