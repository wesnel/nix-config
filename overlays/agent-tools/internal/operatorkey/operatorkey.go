// Package operatorkey proves that commits carry good signatures by the
// operator's key, as the protected operator profile names it: the git config
// under the baked config home (hostpaths.BakedConfigHome), with the public
// keys and verifier programs it names, each resolved within that home's
// directory, its .ssh or .gnupg, or the system's toolchain paths. Nothing a
// repository or worktree configures takes part: not its user.signingkey,
// gpg programs or allowed signers, nor its replace refs, grafts, shallow file
// or commit-graph, any of which anyone able to write the repository can set.
// agent-pr open proves a branch GitHub already has with it, and git sign-pass
// commits outside its authenticated destination and upstream exclusions.
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

// Nix supplies coreutils' env; development builds use the protected system path.
var defaultEnvProgram = "/usr/bin/env"

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
	return privateStateDir(filepath.Join("agent-tools", "signature-verification"), "view-")
}

func privateStateDir(subdir, prefix string) (string, error) {
	root := hostpaths.BakedStateHome()
	if !filepath.IsAbs(root) {
		return "", refuse("there is no baked protected signing-verification state directory")
	}
	parent := filepath.Join(root, subdir)
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
	return os.MkdirTemp(parent, prefix)
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
	values, err := p.settings(ctx, key)
	if err != nil || len(values) == 0 {
		return "", err
	}
	return values[len(values)-1].value, nil
}

type protectedSetting struct{ origin, value string }

func (p Profile) settings(ctx context.Context, key string) ([]protectedSetting, error) {
	config := filepath.Join(p.configHome, "git", "config")
	out, err := Git(ctx, []string{"HOME=" + p.home, "XDG_CONFIG_HOME=" + p.configHome}, nil,
		"config", "--file", config, "--includes", "--null", "--show-origin", "--get-all", key)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	parts := strings.Split(string(out), "\x00")
	if len(parts) < 3 || len(parts)%2 != 1 || parts[len(parts)-1] != "" {
		return nil, refuse("operator signing setting %s has an unprovable origin", key)
	}
	var values []protectedSetting
	for i := 0; i+1 < len(parts); i += 2 {
		origin, ok := strings.CutPrefix(parts[i], "file:")
		if !ok {
			return nil, refuse("operator signing setting %s does not come from a protected file", key)
		}
		origin, err = p.protected(origin)
		if err != nil {
			return nil, err
		}
		values = append(values, protectedSetting{origin, parts[i+1]})
	}
	return values, nil
}

var githubSlug = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// MirrorUpstream returns the GitHub slug authorized for destination in this
// repository, or empty when no mapping applies. All fields must occur once
// in the same protected file. The common directory binds linked worktrees
// to the configured clone; repository and command-line config cannot grant
// this exception. An incomplete or conflicting mapping is an error.
func (p Profile) MirrorUpstream(ctx context.Context, destination string) (string, error) {
	var fields [3]protectedSetting
	n := 0
	for i, key := range []string{"commonDir", "destination", "github"} {
		values, err := p.settings(ctx, "signpass.mirror."+key)
		if err != nil {
			return "", err
		}
		if len(values) > 1 {
			return "", refuse("conflicting protected mirror mapping")
		}
		if len(values) == 1 {
			fields[i] = values[0]
			n++
		}
	}
	if n == 0 {
		return "", nil
	}
	if n != 3 || fields[0].origin != fields[1].origin || fields[0].origin != fields[2].origin {
		return "", refuse("protected mirror mapping must be complete in one protected file")
	}
	if !filepath.IsAbs(fields[0].value) || fields[1].value == "" || !githubSlug.MatchString(fields[2].value) || strings.HasSuffix(fields[2].value, ".git") {
		return "", refuse("invalid protected mirror mapping")
	}
	if destination != fields[1].value {
		return "", nil
	}
	configured, err := filepath.EvalSymlinks(fields[0].value)
	if err != nil {
		return "", fmt.Errorf("cannot resolve protected mirror repository: %w", err)
	}
	common, err := Git(ctx, nil, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	actual, err := filepath.EvalSymlinks(strings.TrimSpace(string(common)))
	if err != nil {
		return "", err
	}
	if configured != actual {
		return "", nil
	}
	return fields[2].value, nil
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
	err = p.loadIdentity(config)
	return p, err
}

func (p *Profile) loadIdentity(config string) error {
	var err error
	if p.format == "openpgp" {
		p.exactSigner = strings.HasSuffix(p.key, "!")
		p.fingerprint = strings.TrimPrefix(strings.TrimSuffix(p.key, "!"), "0x")
		if !fullFingerprint.MatchString(p.fingerprint) {
			return refuse("the operator OpenPGP signing key must be a full fingerprint to prove its identity; ask the operator to set user.signingkey in %s to one", config)
		}
		p.fingerprint = strings.ToUpper(p.fingerprint)
		if _, err := p.protected(filepath.Join(p.home, ".gnupg")); err != nil {
			return err
		}
		return nil
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
			return err
		}
		p.public, err = readPublicAuthority(public)
		if err != nil {
			return err
		}
	}
	fields := strings.Fields(string(p.public))
	if len(fields) < 2 || strings.Contains(fields[0], "-cert-") {
		return refuse("the operator SSH signing key is not a supported public identity; ask the operator to name a plain public key")
	}
	p.public = []byte(fields[0] + " " + fields[1] + "\n")
	return nil
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
	args, err := p.verifierArgs(ctx, dir)
	if err != nil {
		return 0, err
	}
	for _, commit := range commits {
		if err := p.verifyCommit(commit, objectFormat, args, readView); err != nil {
			return 0, err
		}
	}
	return len(commits), nil
}

func (p *Profile) verifierArgs(ctx context.Context, dir string) ([]string, error) {
	var err error
	args := []string{"-c", "gpg.format=" + p.format, "-c", "gpg." + p.format + ".program=" + p.program}
	if p.format == "openpgp" {
		wrapper, err := p.openPGPVerifier(ctx, dir)
		if err != nil {
			return nil, err
		}
		args = append(args, "-c", "gpg.openpgp.program="+wrapper, "-c", "gpg.program="+wrapper)
	} else {
		p.fingerprint, err = SSHFingerprint(string(p.public))
		if err != nil {
			return nil, err
		}
		p.exactSigner = true
		signers := filepath.Join(dir, "allowed_signers")
		if err := os.WriteFile(signers, append([]byte("operator namespaces=\"git\" "), p.public...), 0600); err != nil {
			return nil, err
		}
		args = append(args, "-c", "gpg.ssh.allowedSignersFile="+signers)
		if revoked, err := p.setting(ctx, "gpg.ssh.revocationFile"); err != nil {
			return nil, err
		} else if revoked != "" {
			path, err := p.protected(revoked)
			if err != nil {
				return nil, err
			}
			args = append(args, "-c", "gpg.ssh.revocationFile="+path)
		}
	}
	return args, nil
}

func (p Profile) openPGPVerifier(ctx context.Context, dir string) (string, error) {
	keyring := filepath.Join(dir, "public-keyring")
	if err := os.Mkdir(keyring, 0700); err != nil {
		return "", err
	}
	if err := p.copyPublicKeyring(ctx, keyring); err != nil {
		return "", err
	}
	// Verification uses only the acquired public rings. Environment and
	// configuration cannot select another authority or start a daemon.
	envProgram := defaultEnvProgram
	if _, err := p.protected(envProgram); err != nil {
		return "", refuse("protected environment program is unavailable: %v", err)
	}
	wrapper := filepath.Join(dir, "verify-openpgp")
	// Preserve the validated entrypoint name for multicall coreutils.
	script := "#!/bin/sh\nexec " + quote(envProgram) + " -i"
	for _, entry := range publicKeyEnvironment(keyring) {
		script += " " + quote(entry)
	}
	script += " " + quote(p.program) + " --no-options --no-autostart --disable-dirmngr --no-auto-key-retrieve --no-auto-key-import --homedir " + quote(keyring) + " \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil { //nolint:gosec // Git executes this owner-only verifier wrapper.
		return "", err
	}
	return wrapper, nil
}

func (p Profile) copyPublicKeyring(ctx context.Context, keyring string) error {
	database := filepath.Join(p.home, ".gnupg", "public-keys.d", "pubring.db")
	_, dbErr := os.Lstat(database)
	if dbErr != nil && !errors.Is(dbErr, os.ErrNotExist) {
		return dbErr
	}
	copied := false
	for _, name := range []string{"pubring.kbx", "pubring.gpg"} {
		source := filepath.Join(p.home, ".gnupg", name)
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if dbErr == nil {
			return refuse("operator has both legacy and keyboxd public keys; select one authority before verification")
		}
		source, err := p.protected(source)
		if err != nil {
			return err
		}
		data, err := readPublicAuthority(source)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(keyring, name), data, 0600); err != nil {
			return err
		}
		copied = true
	}
	if dbErr == nil {
		data, err := p.keyboxdPublicKeys(ctx, database)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(keyring, "pubring.gpg"), data, 0600); err != nil {
			return err
		}
		copied = true
	}
	if !copied {
		return refuse("operator public keyring is unavailable or uses an unsupported layout; ask the operator to run the step")
	}
	return nil
}

func (p Profile) verifyCommit(commit, objectFormat string, args []string, readView func([]byte, ...string) ([]byte, error)) error {
	raw, err := readView(nil, "cat-file", "commit", commit)
	if err != nil {
		return err
	}
	// git reads an object file without checking that its content is the
	// object it is named for.
	if named(objectFormat, raw) != commit {
		return fmt.Errorf("hash mismatch: the object of commit %.12s holds another commit", commit)
	}
	if !SignedIn(raw, p.format) {
		return refuse("commit %.12s has no signature in the operator's signing format", commit)
	}
	check := append(append([]string{}, args...), "log", "-1", "--format=%G?%x00%GF%x00%GP", commit)
	out, err := readView(nil, check...)
	if err != nil {
		return err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\n"), "\x00")
	if len(fields) != 3 || fields[0] != "G" && fields[0] != "U" ||
		!strings.EqualFold(fields[1], p.fingerprint) && (p.exactSigner || !strings.EqualFold(fields[2], p.fingerprint)) {
		return refuse("commit %.12s has no verified signature by the protected operator key", commit)
	}
	return nil
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
