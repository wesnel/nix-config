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
		{"mixed keyrings", []string{"GOODSIG 89ABCDEF01234567 T <t@e>", valid(operator), "TRUST_ULTIMATE 0 pgp"}, false},
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
			if c.name == "mixed keyrings" {
				dir := filepath.Join(filepath.Dir(filepath.Dir(stub)), ".gnupg", "public-keys.d")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "pubring.db"), nil, 0600); err != nil {
					t.Fatal(err)
				}
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
			want := "no verified signature by the protected operator key"
			if c.name == "mixed keyrings" {
				want = "both legacy and keyboxd public keys"
			}
			if !errors.As(err, &r) || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %d, %v", c.name, n, err)
			}
		})
	}
}

func TestVerifyMalformedBatch(t *testing.T) {
	stub := pgpProfile(t)
	script := "#!/bin/sh\necho '[GNUPG:] NEWSIG'\necho '[GNUPG:] GOODSIG 89ABCDEF01234567 T <t@e>'\n" +
		"echo '[GNUPG:] VALIDSIG " + operator + " 2023-11-14 1700000000 0 4 0 22 8 00 " + operator + "'\n" +
		"echo '[GNUPG:] TRUST_ULTIMATE 0 pgp'\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	base, tip := pgpRepo(t)
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "intercepted")
	wrapper := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = --batch ]; then : > \"$MARKER\"; printf malformed; exit 0; fi\ndone\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", real)
	t.Setenv("MARKER", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if n, err := Verify(t.Context(), tip, []string{base}); err == nil {
		t.Fatalf("malformed commit response was accepted: %d", n)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("object response was not intercepted: %v", err)
	}
}

func TestVerifyMalformedStatuses(t *testing.T) {
	for _, tc := range []struct{ name, response string }{
		{"malformed", "printf malformed"},
		{"missing", ":"},
		{"duplicate", `"$REAL_GIT" "$@"; "$REAL_GIT" "$@"`},
		{"wrong object", `printf '%s\000G\000%s\000%s\n' 0000000000000000000000000000000000000000 "$OPERATOR" "$OPERATOR"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := pgpProfile(t)
			script := "#!/bin/sh\necho '[GNUPG:] NEWSIG'\necho '[GNUPG:] GOODSIG 89ABCDEF01234567 T <t@e>'\n" +
				"echo '[GNUPG:] VALIDSIG " + operator + " 2023-11-14 1700000000 0 4 0 22 8 00 " + operator + "'\n" +
				"echo '[GNUPG:] TRUST_ULTIMATE 0 pgp'\n"
			if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			base, tip := pgpRepo(t)
			real, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			marker := filepath.Join(bin, "intercepted")
			wrapper := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = --no-walk=unsorted ]; then : > \"$MARKER\"; " + tc.response + "; exit 0; fi\ndone\nexec \"$REAL_GIT\" \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("REAL_GIT", real)
			t.Setenv("MARKER", marker)
			t.Setenv("OPERATOR", operator)
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if n, err := Verify(t.Context(), tip, []string{base}); err == nil {
				t.Fatalf("invalid signature statuses were accepted: %d", n)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("signature statuses were not intercepted: %v", err)
			}
		})
	}
}

func TestVerifyMultipleStatuses(t *testing.T) {
	stub := pgpProfile(t)
	script := "#!/bin/sh\necho '[GNUPG:] NEWSIG'\necho '[GNUPG:] GOODSIG 89ABCDEF01234567 T <t@e>'\n" +
		"echo '[GNUPG:] VALIDSIG " + operator + " 2023-11-14 1700000000 0 4 0 22 8 00 " + operator + "'\n" +
		"echo '[GNUPG:] TRUST_ULTIMATE 0 pgp'\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	base, tip := pgpRepo(t)
	raw, err := git.Bytes(nil, "cat-file", "commit", tip)
	if err != nil {
		t.Fatal(err)
	}
	second := strings.Replace(string(raw), "parent "+base+"\n", "parent "+tip+"\n", 1)
	if second == string(raw) {
		t.Fatal("first commit had no expected parent")
	}
	oid, err := git.Bytes([]byte(second), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	tip2 := strings.TrimSpace(string(oid))
	if n, err := Verify(t.Context(), tip2, []string{base}); err != nil || n != 2 {
		t.Fatalf("two valid commits: %d, %v", n, err)
	}
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "intercepted")
	wrapper := "#!/bin/sh\nfor arg do\n  if [ \"$arg\" = --no-walk=unsorted ]; then : > \"$MARKER\"; " +
		"printf '%s\\000G\\000%s\\000%s\\n' \"$OID2\" \"$OPERATOR\" \"$OPERATOR\"; " +
		"printf '%s\\000G\\000%s\\000%s\\n' \"$OID1\" bad bad; exit 0; fi\ndone\nexec \"$REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REAL_GIT", real)
	t.Setenv("MARKER", marker)
	t.Setenv("OID1", tip)
	t.Setenv("OID2", tip2)
	t.Setenv("OPERATOR", operator)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if n, err := Verify(t.Context(), tip2, []string{base}); err == nil {
		t.Fatalf("a second, wrong signer was accepted: %d", n)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("signature statuses were not intercepted: %v", err)
	}
}

// TestVerifyStatusChunks checks signature statuses beyond the first chunk
// Verify reads them in, with chunks of two. The stub signer refuses only the
// commit whose message says Rejected, the middle one of seven, which is in
// the second chunk whichever order the commits are listed in.
func TestVerifyStatusChunks(t *testing.T) {
	saved := statusBatch
	statusBatch = 2
	t.Cleanup(func() { statusBatch = saved })
	stub := pgpProfile(t)
	// Verify runs the signer with PATH=/usr/bin:/bin, which in a Linux build
	// sandbox holds no grep, so the stub reads the payload with sh builtins.
	script := "#!/bin/sh\nbad=\nwhile IFS= read -r line || [ -n \"$line\" ]; do\n  [ \"$line\" = Rejected ] && bad=1\ndone\nif [ -n \"$bad\" ]; then\n  echo '[GNUPG:] NEWSIG'\n  echo '[GNUPG:] BADSIG 89ABCDEF01234567 T <t@e>'\n  exit 1\nfi\n" +
		"echo '[GNUPG:] NEWSIG'\necho '[GNUPG:] GOODSIG 89ABCDEF01234567 T <t@e>'\n" +
		"echo '[GNUPG:] VALIDSIG " + operator + " 2023-11-14 1700000000 0 4 0 22 8 00 " + operator + "'\n" +
		"echo '[GNUPG:] TRUST_ULTIMATE 0 pgp'\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	base, tip := pgpRepo(t)
	raw, err := git.Bytes(nil, "cat-file", "commit", tip)
	if err != nil {
		t.Fatal(err)
	}
	// chain[0] is tip; each later commit copies it onto the one before.
	chain := []string{tip}
	for i := 1; i < 7; i++ {
		next := strings.Replace(string(raw), "parent "+base+"\n", "parent "+chain[i-1]+"\n", 1)
		if i == 3 {
			next = strings.Replace(next, "\n\nSigned\n", "\n\nRejected\n", 1)
		}
		oid, err := git.Bytes([]byte(next), "hash-object", "-t", "commit", "-w", "--stdin")
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, strings.TrimSpace(string(oid)))
	}
	if n, err := Verify(t.Context(), chain[2], []string{base}); err != nil || n != 3 {
		t.Fatalf("three valid commits across two chunks: %d, %v", n, err)
	}
	n, err := Verify(t.Context(), chain[6], []string{base})
	var r refusal.Refusal
	if !errors.As(err, &r) || !strings.Contains(err.Error(), "no verified signature by the protected operator key") {
		t.Fatalf("a bad signature in the second chunk was not refused: %d, %v", n, err)
	}
	if !strings.Contains(err.Error(), chain[3][:12]) {
		t.Fatalf("refusal names another commit than the bad one %.12s: %v", chain[3], err)
	}
}

func TestVerifyEnvironmentProgram(t *testing.T) {
	for _, name := range []string{"configured", "symlink", "missing"} {
		t.Run(name, func(t *testing.T) {
			stub := pgpProfile(t)
			script := "#!/bin/sh\n" +
				"[ -z \"${OPERATOR_ENV_CANARY+x}\" ] || exit 1\n" +
				"[ \"$HOME\" = \"$GNUPGHOME\" ] && [ \"$TMPDIR\" = \"$GNUPGHOME\" ] || exit 1\n" +
				"[ \"$LC_ALL\" = C ] && [ \"$PATH\" = /usr/bin:/bin ] || exit 1\n" +
				"echo '[GNUPG:] NEWSIG'\n" +
				"echo '[GNUPG:] GOODSIG 89ABCDEF01234567 T <t@e>'\n" +
				"echo '[GNUPG:] VALIDSIG " + operator + " 2023-11-14 1700000000 0 4 0 22 8 00 " + operator + "'\n" +
				"echo '[GNUPG:] TRUST_ULTIMATE 0 pgp'\n"
			if err := os.WriteFile(stub, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			original := defaultEnvProgram
			defaultEnvProgram = filepath.Join(filepath.Dir(stub), "env 'fixture")
			t.Cleanup(func() { defaultEnvProgram = original })
			marker := filepath.Join(t.TempDir(), "environment-program-ran")
			if name != "missing" {
				// The shim records selection, then uses the real env to isolate GPG.
				script := "#!/bin/sh\nprintf used > " + quote(marker) + "\nexec " + quote(original) + " \"$@\"\n"
				program := defaultEnvProgram
				if name == "symlink" {
					// Nix's multicall coreutils dispatches by its invoked symlink name.
					program = filepath.Join(filepath.Dir(stub), "multicall")
					script = "#!/bin/sh\n[ \"$0\" = " + quote(defaultEnvProgram) + " ] || exit 1\n" +
						"printf used > " + quote(marker) + "\nexec " + quote(original) + " \"$@\"\n"
					if err := os.Symlink(program, defaultEnvProgram); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(program, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("OPERATOR_ENV_CANARY", "inherited")
			base, tip := pgpRepo(t)
			n, err := Verify(t.Context(), tip, []string{base})
			if name == "missing" {
				var r refusal.Refusal
				if !errors.As(err, &r) || !strings.Contains(err.Error(), "protected environment program") {
					t.Fatalf("missing configured env: %d, %v; want environment-program refusal", n, err)
				}
				return
			}
			if err != nil || n != 1 {
				t.Fatalf("verification through configured env: %d, %v", n, err)
			}
			if got, err := os.ReadFile(marker); err != nil || string(got) != "used" {
				t.Fatalf("configured environment program was not used: %q, %v", got, err)
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
