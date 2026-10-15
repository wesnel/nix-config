// Package sshmux shares one SSH login per push URL among the pushes a pass
// makes there, so a hardware key that signs each login is touched once per
// URL, however many branches go to it one push at a time.
package sshmux

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/wesnel/agent-tools/internal/git"
	"github.com/wesnel/agent-tools/internal/refusal"
)

// persist is how long a shared connection outlives the push that used it
// last: long enough to reach the pass's next push there, which follows at
// once, and short enough that a pass killed before Close leaves it up only
// briefly.
const persist = "5"

// pattern names the pass's directory under TMPDIR, to which os.MkdirTemp
// adds up to 10 digits. The sockets in it are named by push URL index, so
// their paths stay short: a %C there would add 40 characters, which macOS's
// own TMPDIR leaves no room for.
const pattern = "ssh"

// longest is the longest socket path under base, as ssh binds it: a new
// master's socket is first bound with a dot and 16 random characters
// appended, then renamed.
func longest(base string) int {
	return len(filepath.Join(base, pattern+"4294967295", "999")) + len(".0123456789abcdef")
}

// Mux is a pass's private directory of control sockets, one per push URL,
// and the ssh command git would run, which pushes share them through.
type Mux struct {
	dir, ssh string
}

// Check refuses when a socket under TMPDIR would be too long for a Unix
// socket path, which ssh cannot bind. fix ends the refusal, saying how to
// carry on once TMPDIR is shorter.
func Check(fix string) error {
	if !takesOptions(command()) {
		return nil
	}
	base := filepath.Clean(os.TempDir())
	if n, limit := longest(base), len(syscall.RawSockaddrUnix{}.Path)-1; n > limit {
		return refusal.New("SSH's shared connections would need socket paths of up to %d bytes under %s (TMPDIR), "+
			"more than the %d a Unix socket takes; set a shorter TMPDIR, as `env TMPDIR=/tmp` before the command does, "+
			"then %s", n, base, limit, fix)
	}
	return nil
}

// Open makes the pass's directory, after Check. It returns nil, which shares
// nothing, for an ssh that takes no OpenSSH options: GIT_SSH_VARIANT or
// ssh.variant simple, plink, putty or tortoiseplink, or a plink by name.
func Open(fix string) (*Mux, error) {
	ssh := command()
	if !takesOptions(ssh) {
		return nil, nil
	}
	if err := Check(fix); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return nil, err
	}
	return &Mux{dir: dir, ssh: ssh}, nil
}

// Env is the environment for a push to the pass's push URL n: the ssh
// command git would run, sharing URL n's connection. Options given first
// win in ssh, so any the command already sets stand.
func (m *Mux) Env(n int) []string {
	if m == nil {
		return nil
	}
	return []string{fmt.Sprintf("GIT_SSH_COMMAND=%s -o ControlMaster=auto -o ControlPath=%s -o ControlPersist=%s",
		m.ssh, quote(filepath.Join(m.dir, fmt.Sprint(n))), persist)}
}

// Close ends each shared connection, as `ssh -O exit` does, and removes the
// directory. The destination names no host: the control path alone picks
// the connection.
func (m *Mux) Close() {
	if m == nil {
		return
	}
	entries, _ := os.ReadDir(m.dir)
	for _, e := range entries {
		// As git runs an ssh command: through sh, with the arguments after it.
		exec.Command("sh", "-c", m.ssh+` "$@"`, "ssh",
			"-o", "ControlPath="+filepath.Join(m.dir, e.Name()), "-O", "exit", "sign-pass").Run()
	}
	os.RemoveAll(m.dir)
}

// command is the ssh command git would run, as a shell command: from
// GIT_SSH_COMMAND, core.sshCommand, GIT_SSH (a program), or ssh.
func command() string {
	if c := os.Getenv("GIT_SSH_COMMAND"); c != "" {
		return c
	}
	if c, _ := git.Run("config", "--get", "core.sshCommand"); c != "" {
		return c
	}
	if p := os.Getenv("GIT_SSH"); p != "" {
		return quote(p)
	}
	return "ssh"
}

// takesOptions reports whether git counts the ssh command as OpenSSH, which
// takes -o options, as its ssh.variant settings and name tell.
func takesOptions(cmd string) bool {
	variant := os.Getenv("GIT_SSH_VARIANT")
	if variant == "" {
		variant, _ = git.Run("config", "--get", "ssh.variant")
	}
	switch strings.ToLower(variant) {
	case "simple", "plink", "putty", "tortoiseplink":
		return false
	case "ssh":
		return true
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(strings.Trim(fields[0], `'"`))), ".exe")
	return name != "plink" && name != "tortoiseplink"
}

func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
