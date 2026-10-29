// Package operatorkey proves that commits carry good signatures by the
// operator's key, as the protected operator profile names it: the git config
// under the baked config home (hostpaths.BakedConfigHome), with the public
// keys and verifier programs it names, each resolved within that home's
// directory, its .ssh or .gnupg, or the system's toolchain paths. Nothing a
// repository or worktree configures takes part: not its user.signingkey,
// gpg programs or allowed signers, nor its replace refs, grafts, shallow file
// or commit-graph, any of which anyone able to write the repository can set.
// pr-loop open proves a branch GitHub already has with it, and git sign-pass
// every commit a push sends.
package operatorkey

import (
	"context"
	"crypto/sha1" //nolint:gosec // git's SHA-1 object IDs
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/hostpaths"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// These verifier defaults are linked to the installed toolchain by Nix.
// A development build needs an explicit, protected verifier in its profile.
var defaultGPGProgram, defaultSSHProgram string

var fullFingerprint = regexp.MustCompile(`^(?:[0-9A-Fa-f]{40}|[0-9A-Fa-f]{64})$`)

var refuse = refusal.New

// Profile is the protected operator profile: the operator's signing key, its
// format (openpgp or ssh) and the program that verifies it.
type Profile struct {
	configHome, home, format, key, program string
	fingerprint                            string
	exactSigner                            bool
	public                                 []byte
}

// Format is the profile's signature format, openpgp or ssh.
func (p Profile) Format() string { return p.format }

func checkAuthorityLinks(path string, info os.FileInfo) error {
	// Store optimization may link immutable files. Mutable authority must
	// have no extant alias through which another writable path changes it.
	if info.Mode().IsRegular() && !within(path, "/nix/store") {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return refuse("mutable operator signing authority has an unprovable file alias")
		}
	}
	return nil
}

func readPublicAuthority(path string) ([]byte, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("cannot open public signing authority: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, refuse("operator public signing authority must be a regular file")
	}
	if err := checkAuthorityLinks(path, info); err != nil {
		return nil, err
	}
	return io.ReadAll(file)
}

func verificationDir() (string, error) {
	root := hostpaths.BakedStateHome()
	if !filepath.IsAbs(root) {
		return "", refuse("there is no baked protected signing-verification state directory")
	}
	parent := filepath.Join(root, "agent-tools", "signature-verification")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	if real != parent {
		return "", refuse("operator signing-verification state resolves outside its baked location")
	}
	return os.MkdirTemp(parent, "view-")
}

// SignedIn reports whether the raw commit object carries a signature in
// format, openpgp or ssh.
func SignedIn(raw []byte, format string) bool {
	headers, _, _ := strings.Cut(string(raw), "\n\n")
	label := "PGP"
	if format == "ssh" {
		label = "SSH"
	}
	return strings.Contains("\n"+headers, "\ngpgsig -----BEGIN "+label+" SIGNATURE-----\n")
}

// Git runs git with no global, system or one-command config and no replace
// refs, under ctx, with env added; it fails with ctx's error once ctx is
// done.
func Git(ctx context.Context, env []string, stdin []byte, args ...string) ([]byte, error) {
	env = append([]string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "GIT_CONFIG_PARAMETERS="}, env...)
	out, err := git.BytesContext(git.WithEnv(ctx, env...), stdin, args...)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("signature inspection did not finish: %w", ctx.Err())
	}
	return out, err
}

func within(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

// protected resolves an authority file before reading or executing it. The
// baked config location, private signing directories and immutable system
// files are authority; an include or symlink into a worktree is not.
func (p Profile) protected(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		path = filepath.Join(p.home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return "", refuse("operator signing authority is not an absolute protected path")
	}
	real, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", refuse("required operator signing authority %s is missing; ask the operator to install it", path)
	}
	if err != nil {
		return "", fmt.Errorf("cannot resolve operator signing authority %s: %w", path, err)
	}
	for _, root := range []string{p.configHome, filepath.Join(p.home, ".ssh"), filepath.Join(p.home, ".gnupg"), "/nix/store", "/usr/bin", "/bin"} {
		if within(real, root) {
			info, err := os.Stat(real)
			if err != nil {
				return "", err
			}
			if err := checkAuthorityLinks(real, info); err != nil {
				return "", err
			}
			return real, nil
		}
	}
	return "", refuse("operator signing authority %s resolves outside protected configuration and toolchain paths", path)
}

// setting asks only for authority fields. Rendered includes containing the
// operator's name or email are neither returned nor used for authorization.
func (p Profile) setting(ctx context.Context, key string) (string, error) {
	config := filepath.Join(p.configHome, "git", "config")
	out, err := Git(ctx, []string{"HOME=" + p.home, "XDG_CONFIG_HOME=" + p.configHome}, nil,
		"config", "--file", config, "--includes", "--null", "--show-origin", "--get-all", key)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil
		}
		return "", err
	}
	parts := strings.Split(string(out), "\x00")
	if len(parts) < 3 || len(parts)%2 != 1 || parts[len(parts)-1] != "" {
		return "", refuse("operator signing setting %s has an unprovable origin", key)
	}
	value := ""
	for i := 0; i+1 < len(parts); i += 2 {
		origin, ok := strings.CutPrefix(parts[i], "file:")
		if !ok {
			return "", refuse("operator signing setting %s does not come from a protected file", key)
		}
		if _, err := p.protected(origin); err != nil {
			return "", err
		}
		value = parts[i+1]
	}
	return value, nil
}

// Load reads the protected operator profile. It writes nothing.
func Load(ctx context.Context) (Profile, error) {
	p := Profile{configHome: hostpaths.BakedConfigHome()}
	if p.configHome == "" || !filepath.IsAbs(p.configHome) {
		return p, refuse("there is no baked operator signing profile; use the installed agent-tools, or ask the operator to run the step")
	}
	p.configHome = filepath.Clean(p.configHome)
	p.home = filepath.Dir(p.configHome)
	config := filepath.Join(p.configHome, "git", "config")
	if _, err := p.protected(config); err != nil {
		return p, err
	}
	var err error
	if p.key, err = p.setting(ctx, "user.signingkey"); err != nil {
		return p, err
	}
	if p.key == "" {
		return p, refuse("the protected operator profile names no signing key; ask the operator to set user.signingkey in %s", config)
	}
	if p.format, err = p.setting(ctx, "gpg.format"); err != nil {
		return p, err
	}
	if p.format == "" {
		p.format = "openpgp"
	}
	if p.format != "openpgp" && p.format != "ssh" {
		return p, refuse("the operator signing format cannot be proved by this verifier; ask the operator to sign with openpgp or ssh")
	}
	if p.program, err = p.setting(ctx, "gpg."+p.format+".program"); err != nil {
		return p, err
	}
	if p.program == "" && p.format == "openpgp" {
		if p.program, err = p.setting(ctx, "gpg.program"); err != nil {
			return p, err
		}
	}
	if p.program == "" {
		p.program = defaultGPGProgram
		if p.format == "ssh" {
			p.program = defaultSSHProgram
		}
	}
	if p.program, err = p.protected(p.program); err != nil {
		return p, err
	}
	if p.format == "openpgp" {
		p.exactSigner = strings.HasSuffix(p.key, "!")
		p.fingerprint = strings.TrimPrefix(strings.TrimSuffix(p.key, "!"), "0x")
		if !fullFingerprint.MatchString(p.fingerprint) {
			return p, refuse("the operator OpenPGP signing key must be a full fingerprint to prove its identity; ask the operator to set user.signingkey in %s to one", config)
		}
		p.fingerprint = strings.ToUpper(p.fingerprint)
		if _, err := p.protected(filepath.Join(p.home, ".gnupg")); err != nil {
			return p, err
		}
		return p, nil
	}
	if strings.HasPrefix(p.key, "key::") {
		p.public = []byte(strings.TrimPrefix(p.key, "key::"))
	} else {
		public := p.key
		if !strings.HasSuffix(public, ".pub") {
			public += ".pub"
		}
		public, err = p.protected(public)
		if err != nil {
			return p, err
		}
		p.public, err = readPublicAuthority(public)
		if err != nil {
			return p, err
		}
	}
	fields := strings.Fields(string(p.public))
	if len(fields) < 2 || strings.Contains(fields[0], "-cert-") {
		return p, refuse("the operator SSH signing key is not a supported public identity; ask the operator to name a plain public key")
	}
	p.public = []byte(fields[0] + " " + fields[1] + "\n")
	return p, nil
}

// Verify refuses unless every commit tip reaches and none of have does
// carries a good signature by the protected operator key, and returns how
// many there are. It reads both the graph and the signatures in an
// object-only view, a bare repository of its own in the protected state
// home sharing only the objects, so no checkout config, graft, shallow
// boundary, replace ref or commit-graph changes what it reads.
func Verify(ctx context.Context, tip string, have []string) (int, error) {
	ctx = git.KillingTree(ctx)
	p, err := Load(ctx)
	if err != nil {
		return 0, err
	}
	objects, err := Git(ctx, nil, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return 0, err
	}
	format, err := Git(ctx, nil, nil, "rev-parse", "--show-object-format")
	if err != nil {
		return 0, err
	}
	objectFormat := strings.TrimSpace(string(format))
	if objectFormat != "sha1" && objectFormat != "sha256" {
		return 0, refuse("unsupported Git object format for signing authorization")
	}
	dir, err := verificationDir()
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	if _, err := Git(ctx, nil, nil, "-C", dir, "init", "-q", "--bare", "--template=", "--object-format="+objectFormat); err != nil {
		return 0, err
	}
	env := []string{
		"GIT_OBJECT_DIRECTORY=" + strings.TrimSpace(string(objects)),
		"GIT_GRAFT_FILE=" + filepath.Join(dir, "info", "grafts"),
		"GIT_SHALLOW_FILE=" + filepath.Join(dir, "shallow"),
		"TMPDIR=" + dir, "TMP=" + dir, "TEMP=" + dir,
		"HOME=" + p.home, "XDG_CONFIG_HOME=" + p.configHome,
		"GNUPGHOME=" + filepath.Join(p.home, ".gnupg"),
	}
	readView := func(stdin []byte, args ...string) ([]byte, error) {
		// Parent caches in the shared object directory are mutable too.
		view := []string{"-C", dir, "-c", "core.commitGraph=false"}
		return Git(ctx, env, stdin, append(view, args...)...)
	}
	out, err := readView(nil, append([]string{"rev-list", tip, "--not"}, have...)...)
	if err != nil {
		return 0, fmt.Errorf("cannot establish the commits to authorize: %w", err)
	}
	commits := strings.Fields(string(out))
	if len(commits) == 0 {
		return 0, nil
	}
	args := []string{"-c", "gpg.format=" + p.format, "-c", "gpg." + p.format + ".program=" + p.program}
	if p.format == "openpgp" {
		keyring := filepath.Join(dir, "public-keyring")
		if err := os.Mkdir(keyring, 0700); err != nil {
			return 0, err
		}
		copied := false
		for _, name := range []string{"pubring.kbx", "pubring.gpg"} {
			source := filepath.Join(p.home, ".gnupg", name)
			if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return 0, err
			}
			source, err = p.protected(source)
			if err != nil {
				return 0, err
			}
			data, err := readPublicAuthority(source)
			if err != nil {
				return 0, err
			}
			if err := os.WriteFile(filepath.Join(keyring, name), data, 0600); err != nil {
				return 0, err
			}
			copied = true
		}
		if !copied {
			return 0, refuse("operator public keyring is unavailable or uses an unsupported layout; ask the operator to run the step")
		}
		// A private verifier home avoids gpg.conf, agents and keyboxd. Only
		// public rings are copied; key retrieval and importing stay disabled.
		wrapper := filepath.Join(dir, "verify-openpgp")
		script := "#!/bin/sh\nexec " + quote(p.program) + " --no-options --no-auto-key-retrieve --no-auto-key-import --homedir " + quote(keyring) + " \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil { //nolint:gosec // Git executes this owner-only verifier wrapper.
			return 0, err
		}
		args = append(args, "-c", "gpg.openpgp.program="+wrapper, "-c", "gpg.program="+wrapper)
	} else {
		p.fingerprint, err = SSHFingerprint(string(p.public))
		if err != nil {
			return 0, err
		}
		p.exactSigner = true
		signers := filepath.Join(dir, "allowed_signers")
		if err := os.WriteFile(signers, append([]byte("operator namespaces=\"git\" "), p.public...), 0600); err != nil {
			return 0, err
		}
		args = append(args, "-c", "gpg.ssh.allowedSignersFile="+signers)
		if revoked, err := p.setting(ctx, "gpg.ssh.revocationFile"); err != nil {
			return 0, err
		} else if revoked != "" {
			path, err := p.protected(revoked)
			if err != nil {
				return 0, err
			}
			args = append(args, "-c", "gpg.ssh.revocationFile="+path)
		}
	}
	for _, commit := range commits {
		raw, err := readView(nil, "cat-file", "commit", commit)
		if err != nil {
			return 0, err
		}
		// git reads an object file without checking that its content is the
		// object it is named for.
		if named(objectFormat, raw) != commit {
			return 0, fmt.Errorf("hash mismatch: the object of commit %.12s holds another commit", commit)
		}
		if !SignedIn(raw, p.format) {
			return 0, refuse("commit %.12s has no signature in the operator's signing format", commit)
		}
		check := append(append([]string{}, args...), "log", "-1", "--format=%G?%x00%GF%x00%GP", commit)
		out, err := readView(nil, check...)
		if err != nil {
			return 0, err
		}
		fields := strings.Split(strings.TrimSuffix(string(out), "\n"), "\x00")
		if len(fields) != 3 || fields[0] != "G" && fields[0] != "U" ||
			!strings.EqualFold(fields[1], p.fingerprint) && (p.exactSigner || !strings.EqualFold(fields[2], p.fingerprint)) {
			return 0, refuse("commit %.12s has no verified signature by the protected operator key", commit)
		}
	}
	return len(commits), nil
}

// SSHFingerprint is the SHA256 fingerprint of an SSH public key line, as
// ssh-keygen -l and git's %GF write it.
func SSHFingerprint(public string) (string, error) {
	fields := strings.Fields(public)
	if len(fields) < 2 {
		return "", refuse("the SSH public key %q is incomplete; give the key's whole public line", public)
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", refuse("the SSH public key's encoding is invalid; give the key's whole public line")
	}
	digest := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(digest[:]), nil
}

// named is the object ID of the commit whose content is raw, in format.
func named(format string, raw []byte) string {
	header := fmt.Sprintf("commit %d\x00", len(raw))
	if format == "sha256" {
		sum := sha256.Sum256(append([]byte(header), raw...))
		return hex.EncodeToString(sum[:])
	}
	sum := sha1.Sum(append([]byte(header), raw...)) //nolint:gosec // git's object ID, not a security hash of ours
	return hex.EncodeToString(sum[:])
}

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
