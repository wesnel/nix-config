package operatorkey

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/gitenv"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/refusal"
)

func TestMain(m *testing.M) {
	gitenv.Isolate()
	os.Exit(m.Run())
}

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

// operator is the full fingerprint of the OpenPGP key the profile in
// TestVerifyMarks names.
const operator = "0123456789ABCDEF0123456789ABCDEF01234567"

// Verify counts a commit as the operator's only for a good signature, which
// git marks G or U, by the profile's key: one by another key, a bad one, one
// git cannot check, and one by the operator's key revoked or expired, or
// whose signature expired, are refused. gpg signs only through its agent,
// whose Unix socket the sandbox the tests may run in refuses, so a stand-in
// for gpg in the profile prints the status gpg prints for each.
func TestVerifyMarks(t *testing.T) {
	valid := func(fpr string) string {
		return "VALIDSIG " + fpr + " 2023-11-14 1700000000 0 4 0 22 8 00 " + fpr
	}
	for _, c := range []struct {
		name   string
		status []string
		ok     bool
	}{
		{"good", []string{"GOODSIG 89ABCDEF01234567 T <t@e>", valid(operator), "TRUST_ULTIMATE 0 pgp"}, true},
		{"good, another key", []string{"GOODSIG 1111111111111111 O <o@e>", valid(strings.Repeat("1", 40)), "TRUST_ULTIMATE 0 pgp"}, false},
		{"bad", []string{"BADSIG 89ABCDEF01234567 T <t@e>"}, false},
		{"cannot be checked", []string{"ERRSIG 89ABCDEF01234567 22 8 00 1700000000 9 -", "NO_PUBKEY 89ABCDEF01234567"}, false},
		{"revoked key", []string{"REVKEYSIG 89ABCDEF01234567 T <t@e>", valid(operator)}, false},
		{"expired signature", []string{"EXPSIG 89ABCDEF01234567 T <t@e>", valid(operator)}, false},
		{"expired key", []string{"EXPKEYSIG 89ABCDEF01234567 T <t@e>", valid(operator)}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub := pgpProfile(t)
			script := "#!/bin/sh\necho '[GNUPG:] NEWSIG'\n"
			for _, s := range c.status {
				script += "echo '[GNUPG:] " + s + "'\n"
			}
			if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			base, tip := pgpRepo(t)
			n, err := Verify(t.Context(), tip, []string{base})
			if c.ok {
				if err != nil || n != 1 {
					t.Errorf("a good signature by the operator's key: %d, %v", n, err)
				}
				return
			}
			var r refusal.Refusal
			if !errors.As(err, &r) || !strings.Contains(err.Error(), "no verified signature by the protected operator key") {
				t.Errorf("%s: %d, %v", c.name, n, err)
			}
		})
	}
}

// pgpProfile bakes in an OpenPGP operator profile for operator, verified by
// the stand-in program it returns the path of, in the protected config home.
func pgpProfile(t *testing.T) string {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configHome := filepath.Join(home, ".config")
	for _, d := range []string{filepath.Join(configHome, "git"), filepath.Join(home, ".gnupg")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, ".gnupg", "pubring.kbx"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	stub := filepath.Join(configHome, "gpg")
	config := fmt.Sprintf("[user]\n\tsigningkey = %s\n[gpg]\n\tformat = openpgp\n[gpg \"openpgp\"]\n\tprogram = %s\n", operator, stub)
	if err := os.WriteFile(filepath.Join(configHome, "git", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hostpaths.Bake(t, configHome, state)
	return stub
}

// pgpRepo is a repository with an unsigned base commit and, on it, tip, a
// commit with an OpenPGP signature header; the package's git runs there.
func pgpRepo(t *testing.T) (base, tip string) {
	t.Helper()
	repo := t.TempDir()
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@e", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = repo
		cmd.Env = append(gitenv.Without(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "init", "-q", "-b", "main")
	run("", "commit", "-q", "--allow-empty", "-m", "base")
	base = run("", "rev-parse", "HEAD")
	object := "tree " + run("", "rev-parse", "HEAD^{tree}") + "\nparent " + base + "\nauthor T <t@e> 1700000000 +0000\n" +
		"committer T <t@e> 1700000000 +0000\ngpgsig -----BEGIN PGP SIGNATURE-----\n \n " +
		"iHUEABYKAB0WIQQAAAAAAAAAAAAAAAAAAAAAAAAAAAUCZQAAAAAACgkQAAAAAAAAAAAA\n =AAAA\n -----END PGP SIGNATURE-----\n\nSigned\n"
	tip = run(object, "hash-object", "-t", "commit", "-w", "--stdin")
	git.Dir = repo
	t.Cleanup(func() { git.Dir = "" })
	return base, tip
}
