package signpass

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/wesnel/agent-tools/internal/git"
)

// openPGPID is an OpenPGP key's long ID or fingerprint, as user.signingkey
// may name it; a short ID is too easy to collide with.
var openPGPID = regexp.MustCompile(`^(?:[0-9A-F]{16}|[0-9A-F]{40}|[0-9A-F]{64})$`)

// objectGit runs git reading history from the commit objects alone, as a
// push sends them: replace refs, grafts, a shallow file and the
// commit-graph, which anyone able to write the repository can change, could
// otherwise show other commits or other parents than those pushed.
func objectGit(args ...string) (string, error) {
	return git.RunEnv([]string{"GIT_NO_REPLACE_OBJECTS=1", "GIT_GRAFT_FILE=/dev/null", "GIT_SHALLOW_FILE=/dev/null"},
		append([]string{"-c", "core.commitGraph=false"}, args...)...)
}

// OperatorSigned refuses unless the operator's key signed every commit of
// branch that tip has and none of have does: git marks it G, or U for a good
// signature by a key git does not vouch for, and the signing key or its
// primary key is user.signingkey, the key the pass signs with. It reads
// only, verifying through git and this repository's config as the pass's
// signing does.
func OperatorSigned(branch, tip string, have []string) error {
	matches, key, err := operatorKey()
	if err != nil {
		return err
	}
	args := []string{"log", "--format=%H%x00%G?%x00%GF%x00%GP", tip}
	if len(have) > 0 {
		args = append(append(args, "--not"), have...)
	}
	out, err := objectGit(args...)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\x00")
		if len(f) != 4 {
			continue
		}
		if (f[1] == "G" || f[1] == "U") && matches(f[2], f[3]) {
			continue
		}
		by := ""
		if f[2] != "" {
			by = ", by key " + f[2]
		}
		return refuse("%s's commit %.12s is not signed by the operator's key %s (git marks it %s%s), and a pass pushes a branch "+
			"it does not move only when that key signed every commit the push sends; check the commit, and if it belongs, "+
			"have the operator push the branch with `git push`", branch, f[0], key, f[1], by)
	}
	return nil
}

// remoteHas is what t's remote is known to have of its branch's history:
// the branch itself where there is one, since the push is leased on it and
// goes only while the remote still has it; otherwise the remote-tracking
// refs of its main or master, by name and never through REMOTE/HEAD, which
// anyone able to write the repository may point anywhere. A remote whose
// default branch is named otherwise has everything checked.
func remoteHas(t target) []string {
	if t.lease != "" {
		return []string{t.lease}
	}
	var have []string
	for _, name := range []string{"main", "master"} {
		if oid, err := git.Run("rev-parse", "--verify", "-q", "refs/remotes/"+t.remote+"/"+name+"^{commit}"); err == nil && oid != "" {
			have = append(have, oid)
		}
	}
	return have
}

// operatorKey is user.signingkey as a test of the fingerprints git gives a
// signature, %GF for its key and %GP for that key's primary key, and the key
// as the refusal names it.
func operatorKey() (matches func(key, primary string) bool, name string, err error) {
	value, _ := git.Run("config", "--get", "user.signingkey")
	if value == "" {
		return nil, "", refuse("user.signingkey is not set, so the pass cannot tell the operator's signatures from others'; " +
			"set it with `git config --global user.signingkey KEY`")
	}
	format, _ := git.Run("config", "--get", "gpg.format")
	switch format {
	case "", "openpgp":
		id := strings.ToUpper(strings.TrimSuffix(value, "!"))
		id = strings.TrimPrefix(id, "0X")
		if !openPGPID.MatchString(id) {
			return nil, "", refuse("user.signingkey %q is not an OpenPGP key's long ID or fingerprint, so the pass cannot tell the "+
				"operator's signatures from others'; set it to the fingerprint `gpg --list-secret-keys` shows", value)
		}
		return func(key, primary string) bool {
			return key != "" && strings.HasSuffix(strings.ToUpper(key), id) || primary != "" && strings.HasSuffix(strings.ToUpper(primary), id)
		}, id, nil
	case "ssh":
		fingerprint, err := sshKeyFingerprint(value)
		if err != nil {
			return nil, "", err
		}
		return func(key, _ string) bool { return key == fingerprint }, fingerprint, nil
	}
	return nil, "", refuse("gpg.format is %s, whose signatures the pass cannot match to the operator's key; push the branch by hand with `git push`", format)
}

// sshKeyFingerprint is the SHA256 fingerprint git gives (%GF) a signature
// by the SSH key user.signingkey names: a literal `key::` public key, a
// public key file, or a private key file beside its .pub.
func sshKeyFingerprint(value string) (string, error) {
	public, ok := strings.CutPrefix(value, "key::")
	if !ok {
		path := value
		if rest, ok := strings.CutPrefix(path, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			path = filepath.Join(home, rest)
		}
		if !strings.HasSuffix(path, ".pub") {
			path += ".pub"
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", refuse("cannot read the operator's SSH public key %s (%v), so the pass cannot tell the operator's signatures "+
				"from others'; point user.signingkey at a key with its .pub beside it", path, err)
		}
		public = string(data)
	}
	return SSHFingerprint(public)
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
