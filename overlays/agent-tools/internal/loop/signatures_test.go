package loop

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/hostpaths"
)

// signing makes dir's commits verifiable, signed with a throwaway SSH key
// that its allowed-signers file trusts for t@e, so a test signs commits
// without the hardware key. It returns the arguments that sign a commit.
func signing(t *testing.T, dir string) []string {
	t.Helper()
	key := filepath.Join(t.TempDir(), "key")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Skipf("no ssh-keygen: %v %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	signers := filepath.Join(t.TempDir(), "signers")
	if err := os.WriteFile(signers, []byte("t@e namespaces=\"git\" "+string(pub)), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "config", "gpg.format", "ssh")
	gitIn(t, dir, "config", "gpg.ssh.allowedSignersFile", signers)
	return []string{"-c", "user.signingkey=" + key, "commit", "-q", "-S"}
}

func bakeOperatorProfile(t *testing.T, configHome string) {
	t.Helper()
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hostpaths.Bake(t, configHome, state)
}

func sshOperatorProfile(t *testing.T, dir string) (string, []string) {
	t.Helper()
	sign := signing(t, dir)
	key := strings.TrimPrefix(sign[1], "user.signingkey=")
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	configHome := filepath.Join(home, ".config")
	if err := os.MkdirAll(filepath.Join(configHome, "git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(home, ".ssh", "operator.pub")
	if err := os.WriteFile(public, pub, 0600); err != nil {
		t.Fatal(err)
	}
	program, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatal(err)
	}
	program, err = filepath.EvalSymlinks(program)
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(configHome, "git", "config")
	for _, setting := range [][2]string{{"user.signingkey", public}, {"gpg.format", "ssh"}, {"gpg.ssh.program", program}} {
		gitIn(t, dir, "config", "--file", config, setting[0], setting[1])
	}
	bakeOperatorProfile(t, configHome)
	return home, sign
}

func pushedOperatorBranch(t *testing.T) (*fakeOpen, string) {
	t.Helper()
	f, dir := pushedBranch(t)
	_, sign := sshOperatorProfile(t, dir)
	gitIn(t, dir, append(sign, "--amend", "--no-edit", "--allow-empty")...)
	published(t, f, dir, "feat")
	return f, dir
}

func TestOpenAuthorityFailure(t *testing.T) {
	for _, name := range []string{"missing profile", "malformed include", "blocked include", "blocked public key"} {
		t.Run(name, func(t *testing.T) {
			f, dir := pushedOperatorBranch(t)
			home := hostpaths.BakedConfigHome()
			config := filepath.Join(home, "git", "config")
			switch name {
			case "missing profile":
				hostpaths.Bake(t, filepath.Join(t.TempDir(), ".config"), t.TempDir())
			case "blocked public key":
				public := filepath.Join(filepath.Dir(home), ".ssh", "operator.pub")
				if err := os.Remove(public); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(public, 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed include":
				include := filepath.Join(home, "bad-config")
				if err := os.WriteFile(include, []byte("[\n"), 0600); err != nil {
					t.Fatal(err)
				}
				gitIn(t, dir, "config", "--file", config, "include.path", include)
			case "blocked include":
				include := filepath.Join(home, "blocked-config")
				if err := syscall.Mkfifo(include, 0600); err != nil {
					t.Fatal(err)
				}
				gitIn(t, dir, "config", "--file", config, "include.path", include)
				old := operatorVerificationTimeout
				operatorVerificationTimeout = 100 * time.Millisecond
				t.Cleanup(func() { operatorVerificationTimeout = old })
			}
			_, err := Open("feat", "", "Add feat", "Why it is needed.", false)
			if len(f.calls) != 0 {
				t.Fatalf("authority failure wrote to GitHub: %v", f.calls)
			}
			switch name {
			case "missing profile", "blocked public key":
				var policy Refusal
				if !errors.As(err, &policy) {
					t.Fatalf("missing authority should refuse: %v", err)
				}
			case "malformed include":
				var command *exec.ExitError
				if !errors.As(err, &command) {
					t.Fatalf("config failure lost command cause: %v", err)
				}
			case "blocked include":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("blocked config lost timeout cause: %v", err)
				}
			}
		})
	}
}

func TestOperatorVerifierCanceled(t *testing.T) {
	_, dir := pushedOperatorBranch(t)
	configHome := hostpaths.BakedConfigHome()
	config := filepath.Join(configHome, "git", "config")
	program, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(configHome, "verifier-ready")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	sentinel, err := os.OpenFile(fifo, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sentinel.Close() })
	ready, err := os.OpenFile(fifo, os.O_RDONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ready.Close() })
	verifier := filepath.Join(configHome, "waiting-verifier")
	script := "#!/bin/sh\nif test \"$1\" = -lf; then exec " + shellQuote(program) + " \"$@\"; fi\nexec 3> " + shellQuote(fifo) + "\nprintf '%s\\n' \"$$\" >&3\nexec /bin/sleep 30\n"
	if err := os.WriteFile(verifier, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "config", "--file", config, "gpg.ssh.program", verifier)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pid := make(chan int, 1)
	reader := bufio.NewReader(ready)
	go func() {
		line, _ := reader.ReadString('\n')
		id, _ := strconv.Atoi(strings.TrimSpace(line))
		pid <- id
		sentinel.Close()
		cancel()
	}()
	err = verifyOperatorRange(ctx, gitIn(t, dir, "rev-parse", "main"), gitIn(t, dir, "rev-parse", "feat"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled verifier lost context cause: %v", err)
	}
	select {
	case id := <-pid:
		if id <= 0 {
			t.Fatal("verifier did not report its process")
		}
		exited := make(chan error, 1)
		go func() { _, err := reader.ReadByte(); exited <- err }()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case err := <-exited:
			if !errors.Is(err, io.EOF) {
				t.Errorf("verifier ready pipe: %v", err)
			}
		case <-timer.C:
			syscall.Kill(id, syscall.SIGKILL)
			<-exited
			t.Error("canceled verification left its child holding the ready pipe")
		}
	default:
		t.Error("verifier never reached its controlled wait")
	}
}

// agentSocket skips the test unless gpg-agent could listen at path: a
// sandbox that refuses Unix sockets, or a path too long for one, leaves the
// fixture no agent to sign with.
func agentSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("gpg-agent cannot sign the fixture's commits: %v; run outside a sandbox that refuses Unix sockets, with a short TMPDIR such as /tmp", err)
	}
	l.Close()
	os.Remove(path)
}

func TestOpenOperatorOpenPGP(t *testing.T) {
	// gpg-agent, which signs the fixture's commits, listens on sockets in
	// the keyring, and macOS limits a socket's path to 103 bytes: each case's
	// home is under this test's directory, whose name is shorter than a
	// subtest's.
	homes, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"primary permits certified subkey", "pinned subkey", "forced subkey excludes primary", "wrong protected fingerprint", "profile options ignored"} {
		t.Run(name, func(t *testing.T) {
			home := filepath.Join(homes, strconv.Itoa(i))
			configHome, keyring := filepath.Join(home, ".config"), filepath.Join(home, ".gnupg")
			for _, path := range []string{filepath.Join(configHome, "git"), keyring} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			agentSocket(t, filepath.Join(keyring, "S.gpg-agent.browser"))
			f, dir := pushedBranch(t)
			program, err := exec.LookPath("gpg")
			if err != nil {
				t.Fatal(err)
			}
			program, err = filepath.EvalSymlinks(program)
			if err != nil {
				t.Fatal(err)
			}
			gpg := func(args ...string) string {
				t.Helper()
				all := append([]string{"--homedir", keyring, "--batch", "--pinentry-mode", "loopback", "--passphrase", ""}, args...)
				out, err := exec.CommandContext(t.Context(), program, all...).CombinedOutput()
				if err != nil {
					t.Fatalf("fixture gpg: %v: %s", err, out)
				}
				return string(out)
			}
			gpgconf, err := exec.LookPath("gpgconf")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				exec.CommandContext(ctx, gpgconf, "--homedir", keyring, "--kill", "gpg-agent").Run()
			})
			fingerprints := func() []string {
				var out []string
				for _, line := range strings.Split(gpg("--with-colons", "--fingerprint", "--list-keys"), "\n") {
					fields := strings.Split(line, ":")
					if len(fields) > 9 && fields[0] == "fpr" {
						out = append(out, fields[9])
					}
				}
				return out
			}
			gpg("--quick-generate-key", "Fixture <fixture@invalid>", "ed25519", "cert,sign", "0")
			keys := fingerprints()
			if len(keys) != 1 {
				t.Fatalf("fixture primary fingerprints: %v", keys)
			}
			primary := keys[0]
			gpg("--quick-add-key", primary, "ed25519", "sign", "0")
			keys = fingerprints()
			if len(keys) != 2 {
				t.Fatalf("fixture subkey fingerprints: %v", keys)
			}
			subkey := keys[1]
			expected, signer, want := primary, subkey+"!", true
			switch name {
			case "pinned subkey":
				expected = subkey + "!"
			case "forced subkey excludes primary":
				expected, signer, want = subkey+"!", primary+"!", false
			case "wrong protected fingerprint":
				first := "A"
				if strings.HasPrefix(primary, first) {
					first = "B"
				}
				expected, want = first+primary[1:], false
			}
			config := filepath.Join(configHome, "git", "config")
			for _, setting := range [][2]string{{"user.signingkey", expected}, {"gpg.format", "openpgp"}, {"gpg.openpgp.program", program}} {
				gitIn(t, dir, "config", "--file", config, setting[0], setting[1])
			}
			bakeOperatorProfile(t, configHome)
			t.Setenv("GNUPGHOME", keyring)
			gitIn(t, dir, "-c", "gpg.format=openpgp", "-c", "gpg.program="+program, "-c", "user.signingkey="+signer,
				"commit", "-q", "-S", "--amend", "--no-edit", "--allow-empty")
			marker := filepath.Join(home, "profile-option-used")
			if name == "profile options ignored" {
				if err := os.WriteFile(filepath.Join(keyring, "gpg.conf"), []byte("status-file "+marker+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			published(t, f, dir, "feat")
			_, err = Open("feat", "", "Add feat", "Why it is needed.", false)
			if want {
				if err != nil || len(f.calls) != 1 {
					t.Fatalf("authorized OpenPGP: %v, calls %v", err, f.calls)
				}
			} else {
				var policy Refusal
				if !errors.As(err, &policy) || len(f.calls) != 0 {
					t.Fatalf("unauthorized OpenPGP: %v, calls %v", err, f.calls)
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("verification used ambient gpg options: %v", err)
			}
		})
	}
}

func TestOpenOperatorSignature(t *testing.T) {
	for _, name := range []string{"operator", "different author", "unsigned", "wrong key", "public key hardlink", "configuration hardlink", "tracking exclusion", "replacement traversal", "grafted traversal", "shallow traversal", "cached traversal", "object identity", "temporary environment ignored", "environment override", "included key override", "irrelevant include", "public key escape", "verifier override", "tampered payload", "unbaked"} {
		t.Run(name, func(t *testing.T) {
			f, dir := pushedBranch(t)
			home, sign := sshOperatorProfile(t, dir)
			unsigned := gitIn(t, dir, "rev-parse", "HEAD")
			config := filepath.Join(home, ".config", "git", "config")
			want := name == "operator" || name == "different author" || name == "environment override" || name == "irrelevant include" || name == "temporary environment ignored"
			marker := filepath.Join(t.TempDir(), "verifier-ran")
			switch name {
			case "unsigned":
			case "wrong key", "verifier override":
				gitIn(t, dir, append(signing(t, dir), "--amend", "--no-edit", "--allow-empty")...)
			case "public key hardlink", "configuration hardlink":
				other := signing(t, dir)
				gitIn(t, dir, append(other, "--amend", "--no-edit", "--allow-empty")...)
				public := filepath.Join(home, ".ssh", "operator.pub")
				if name == "configuration hardlink" {
					public = config
				}
				alias := filepath.Join(dir, "public-key-alias")
				if err := os.Link(public, alias); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(strings.TrimPrefix(other[1], "user.signingkey=") + ".pub")
				if err != nil {
					t.Fatal(err)
				}
				if name == "configuration hardlink" {
					original, err := os.ReadFile(config)
					if err != nil {
						t.Fatal(err)
					}
					data = append(original, []byte("\n[user]\n signingkey = key::"+strings.TrimSpace(string(data))+"\n")...)
				}
				if err := os.WriteFile(alias, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "tracking exclusion", "replacement traversal", "grafted traversal", "shallow traversal", "cached traversal":
				if name == "tracking exclusion" {
					gitIn(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
				}
				gitIn(t, dir, append(sign, "--allow-empty", "-m", "Add signed work")...)
			default:
				if name == "different author" {
					sign = append([]string{"-c", "user.email=another@invalid"}, sign...)
				}
				gitIn(t, dir, append(sign, "--amend", "--no-edit", "--reset-author", "--allow-empty")...)
			}
			switch name {
			case "cached traversal":
				interior := gitIn(t, dir, "rev-parse", "HEAD")
				base := gitIn(t, dir, "rev-parse", "main")
				gitIn(t, dir, append(sign, "--allow-empty", "-m", "Add signed endpoint")...)
				poisonCommitGraphParent(t, dir, interior, unsigned, base)
				for _, enabled := range []string{"true", "false"} {
					rangeOIDs := gitIn(t, dir, "-c", "core.commitGraph="+enabled, "rev-list", base+"..HEAD")
					if strings.Contains(rangeOIDs, unsigned) != (enabled == "false") {
						t.Fatalf("commit-graph %s control did not exercise altered ancestry: %s", enabled, rangeOIDs)
					}
				}
			case "grafted traversal":
				tip := gitIn(t, dir, "rev-parse", "HEAD")
				base := gitIn(t, dir, "rev-parse", "main")
				if err := os.WriteFile(filepath.Join(dir, ".git", "info", "grafts"), []byte(tip+" "+base+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "shallow traversal":
				tip := gitIn(t, dir, "rev-parse", "HEAD")
				if err := os.WriteFile(filepath.Join(dir, ".git", "shallow"), []byte(tip+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "replacement traversal":
				replacement := gitIn(t, dir, "commit-tree", "HEAD^{tree}", "-p", "main", "-m", "Shortened traversal")
				gitIn(t, dir, "replace", "HEAD", replacement)
			case "object identity":
				// The named object must match its content even when the other
				// payload carries a legitimate signature.
				raw, err := git.Bytes(nil, "cat-file", "commit", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				var object bytes.Buffer
				writer := zlib.NewWriter(&object)
				fmt.Fprintf(writer, "commit %d%c", len(raw), 0)
				writer.Write(raw)
				writer.Close()
				gitIn(t, dir, "update-ref", "HEAD", unsigned)
				published(t, f, dir, "feat")
				path := filepath.Join(dir, ".git", "objects", unsigned[:2], unsigned[2:])
				if err := os.Chmod(path, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, object.Bytes(), 0600); err != nil {
					t.Fatal(err)
				}
			case "tampered payload":
				raw, err := git.Bytes(nil, "cat-file", "commit", "HEAD")
				if err != nil {
					t.Fatal(err)
				}
				changed := bytes.Replace(raw, []byte("Add feat"), []byte("Add forged"), 1)
				if bytes.Equal(raw, changed) {
					t.Fatal("fixture message was not changed")
				}
				oid, err := git.Bytes(changed, "hash-object", "-t", "commit", "-w", "--stdin")
				if err != nil {
					t.Fatal(err)
				}
				gitIn(t, dir, "update-ref", "HEAD", strings.TrimSpace(string(oid)))
			case "environment override":
				t.Setenv("HOME", t.TempDir())
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(dir, "forged-global"))
			case "temporary environment ignored":
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing-untrusted-temp"))
			case "included key override", "irrelevant include":
				include := filepath.Join(dir, "included-config")
				gitIn(t, dir, "config", "--file", config, "include.path", include)
				key, value := "user.signingkey", filepath.Join(dir, "attacker.pub")
				if name == "irrelevant include" {
					key, value = "user.name", "Fixture"
				}
				gitIn(t, dir, "config", "--file", include, key, value)
			case "public key escape":
				public := filepath.Join(home, ".ssh", "operator.pub")
				data, err := os.ReadFile(public)
				if err != nil {
					t.Fatal(err)
				}
				outside := filepath.Join(dir, "untrusted.pub")
				if err := os.WriteFile(outside, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(public); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, public); err != nil {
					t.Fatal(err)
				}
			case "verifier override":
				fake := filepath.Join(dir, "verify")
				if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf ran > '"+marker+"'\nexit 0\n"), 0700); err != nil {
					t.Fatal(err)
				}
				gitIn(t, dir, "config", "gpg.ssh.program", fake)
			case "unbaked":
				hostpaths.Bake(t, "", "")
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			}
			if name != "object identity" {
				published(t, f, dir, "feat")
			}
			_, err := Open("feat", "", "Add feat", "Why it is needed.", false)
			if want {
				if err != nil || strings.Join(f.calls, " ") != "createPullRequest" {
					t.Fatalf("authorized: error %v, mutations %v", err, f.calls)
				}
			} else if name == "object identity" {
				var command *exec.ExitError
				if !errors.As(err, &command) || len(f.calls) != 0 {
					t.Fatalf("corrupt object: error %v, mutations %v", err, f.calls)
				}
			} else {
				var policy Refusal
				if !errors.As(err, &policy) || len(f.calls) != 0 {
					t.Fatalf("unauthorized: error %v, mutations %v", err, f.calls)
				}
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("repository verifier was executed: %v", err)
			}
		})
	}
}
