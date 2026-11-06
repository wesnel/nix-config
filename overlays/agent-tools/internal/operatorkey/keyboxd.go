package operatorkey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func publicKeyEnvironment(home string) []string {
	return []string{"HOME=" + home, "GNUPGHOME=" + home, "LC_ALL=C", "PATH=/usr/bin:/bin", "TMPDIR=" + home}
}

func checkKeyboxdRuntime(goos string, uid int, lstat func(string) (os.FileInfo, error)) error {
	// GnuPG's directory discovery can create runtime directories, and its
	// installation discovery can redirect system configuration. Support the
	// Darwin home-local socket layout only when neither mechanism applies.
	if goos != "darwin" {
		return refuse("keyboxd public-key verification requires the supported Darwin home-local socket layout")
	}
	user := strconv.Itoa(uid)
	for _, path := range []string{"/proc/curproc/file", "/run/gnupg/user/" + user, "/run/user/" + user, "/var/run/gnupg/user/" + user, "/var/run/user/" + user} {
		if _, err := lstat(path); !errors.Is(err, os.ErrNotExist) {
			return refuse("keyboxd public-key discovery requires absent runtime path %s", path)
		}
	}
	return nil
}

func (p Profile) checkKeyboxdFiles(database string) error {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		path := database + suffix
		if _, err := os.Lstat(path); suffix != "" && errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		real, err := p.protected(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(real)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return refuse("operator keyboxd public authority must be regular files")
		}
	}
	return nil
}

func checkKeyboxdCommonConfig(sysconf string) error {
	// --no-options still reads system common.conf. A private verifier must
	// not inherit a global keyboxd selection or helper program from it.
	if _, err := os.Lstat(filepath.Join(sysconf, "common.conf")); !errors.Is(err, os.ErrNotExist) {
		return refuse("keyboxd public-key verification requires absent system common.conf")
	}
	return nil
}

func checkKeyboxdSocketPath(home, socket string, private bool) error {
	if socket == filepath.Join(home, "S.keyboxd") {
		return nil
	}
	if private {
		return refuse("private keyboxd client socket resolves outside its home")
	}
	return refuse("operator keyboxd socket is outside its protected home")
}

func checkKeyboxdSocket(info os.FileInfo, uid int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || int64(stat.Uid) != int64(uid) || stat.Nlink != 1 {
		return refuse("operator keyboxd endpoint must be an unaliased Unix socket owned by the operator")
	}
	return nil
}

func checkKeyboxdSocketDirectory(info os.FileInfo, uid int) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || int64(stat.Uid) != int64(uid) || info.Mode().Perm()&0022 != 0 {
		return refuse("operator keyboxd socket directory must be owned and writable only by the operator")
	}
	return nil
}

// keyboxdPublicKeys exports through a private client of the protected daemon.
// The client never loads the operator's GPG options or starts a daemon, and
// the verifier receives a standalone public ring with revocation metadata.
func (p Profile) keyboxdPublicKeys(ctx context.Context, database string) ([]byte, error) {
	if err := checkKeyboxdRuntime(runtime.GOOS, os.Getuid(), os.Lstat); err != nil {
		return nil, err
	}
	if err := p.checkKeyboxdFiles(database); err != nil {
		return nil, err
	}
	gpgconf, err := p.protected(filepath.Join(filepath.Dir(p.program), "gpgconf"))
	if err != nil {
		return nil, err
	}
	// Keeping this home directly under protected state avoids Darwin's short
	// Unix socket path limit; it has no persistent contents.
	client, err := privateStateDir(ctx, "", ".gpg-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(client)
	run := func(program string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, program, args...)
		cmd.Env, cmd.Dir = publicKeyEnvironment(client), client
		cmd.WaitDelay = time.Second
		return cmd.Output()
	}
	discover := func(home, name string) (string, error) {
		out, err := run(gpgconf, "--homedir", home, "--list-dirs", name)
		if err != nil {
			return "", fmt.Errorf("cannot discover keyboxd %s: %w", name, err)
		}
		path := strings.TrimSuffix(string(out), "\n")
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "%\x00\r\n") {
			return "", refuse("keyboxd %s is not an unambiguous absolute path", name)
		}
		return path, nil
	}
	sysconf, err := discover(client, "sysconfdir")
	if err != nil {
		return nil, err
	}
	if err := checkKeyboxdCommonConfig(sysconf); err != nil {
		return nil, err
	}
	sourceHome := filepath.Join(p.home, ".gnupg")
	socket, err := discover(sourceHome, "keyboxd-socket")
	if err != nil {
		return nil, err
	}
	if err := checkKeyboxdSocketPath(sourceHome, socket, false); err != nil {
		return nil, err
	}
	socket, err = p.protected(socket)
	if err != nil {
		return nil, refuse("cannot export operator keyboxd public keys: protected daemon socket unavailable: %v", err)
	}
	if err := checkKeyboxdSocketPath(sourceHome, socket, false); err != nil {
		return nil, err
	}
	info, err := os.Stat(socket)
	if err != nil {
		return nil, err
	}
	if err := checkKeyboxdSocket(info, os.Getuid()); err != nil {
		return nil, err
	}
	parent, err := os.Stat(filepath.Dir(socket))
	if err != nil {
		return nil, err
	}
	if err := checkKeyboxdSocketDirectory(parent, os.Getuid()); err != nil {
		return nil, err
	}
	clientSocket, err := discover(client, "keyboxd-socket")
	if err != nil {
		return nil, err
	}
	if err := checkKeyboxdSocketPath(client, clientSocket, true); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(client, "common.conf"), []byte("use-keyboxd\n"), 0600); err != nil {
		return nil, err
	}
	if err := os.Symlink(socket, clientSocket); err != nil {
		return nil, err
	}
	keys, err := run(p.program, "--homedir", client, "--no-options", "--batch", "--no-tty", "--no-autostart", "--disable-dirmngr",
		"--no-auto-key-retrieve", "--no-auto-key-import", "--export-options", "export-local-sigs,export-sensitive-revkeys", "--export")
	if err != nil || len(keys) == 0 {
		return nil, refuse("cannot export operator keyboxd public keys from the protected running daemon")
	}
	return keys, nil
}
