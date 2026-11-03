package operatorkey

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/wesnel/agent-tools/internal/refusal"
)

func requireKeyboxdRefusal(t *testing.T, err error, want string) {
	t.Helper()
	var r refusal.Refusal
	if !errors.As(err, &r) || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v; want refusal containing %q", err, want)
	}
}

func TestKeyboxdRuntime(t *testing.T) {
	// Root-owned runtime paths must not be created or changed by a test.
	missing := func(string) (os.FileInfo, error) { return nil, os.ErrNotExist }
	t.Run("absent", func(t *testing.T) {
		if err := checkKeyboxdRuntime("darwin", 501, missing); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("other-platform", func(t *testing.T) {
		requireKeyboxdRefusal(t, checkKeyboxdRuntime("linux", 501, missing), "supported Darwin home-local socket layout")
	})
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, path string }{
		{"proc", "/proc/curproc/file"},
		{"run-gnupg", "/run/gnupg/user/501"},
		{"run", "/run/user/501"},
		{"var-run-gnupg", "/var/run/gnupg/user/501"},
		{"var-run", "/var/run/user/501"},
	} {
		for _, inaccessible := range []bool{false, true} {
			name := c.name + "-present"
			if inaccessible {
				name = c.name + "-inaccessible"
			}
			t.Run(name, func(t *testing.T) {
				lstat := func(path string) (os.FileInfo, error) {
					if path != c.path {
						return nil, os.ErrNotExist
					}
					if inaccessible {
						return nil, &os.PathError{Op: "lstat", Path: path, Err: os.ErrPermission}
					}
					return info, nil
				}
				requireKeyboxdRefusal(t, checkKeyboxdRuntime("darwin", 501, lstat), "absent runtime path "+c.path)
			})
		}
	}
}

func TestKeyboxdCommonConfig(t *testing.T) {
	for _, name := range []string{"absent", "file", "directory", "dangling-symlink", "invalid-parent"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "common.conf")
			switch name {
			case "file":
				if err := os.WriteFile(path, []byte("use-keyboxd\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "dangling-symlink":
				if err := os.Symlink(filepath.Join(dir, "missing"), path); err != nil {
					t.Fatal(err)
				}
			case "invalid-parent":
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				dir = path
			}
			err := checkKeyboxdCommonConfig(dir)
			if name == "absent" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireKeyboxdRefusal(t, err, "absent system common.conf")
			}
		})
	}
}

func TestKeyboxdFiles(t *testing.T) {
	for _, file := range []struct{ name, suffix string }{
		{"database", ""}, {"wal", "-wal"}, {"shm", "-shm"}, {"journal", "-journal"},
	} {
		t.Run(file.name, func(t *testing.T) {
			for _, name := range []string{"regular", "missing", "directory", "hardlink", "escape"} {
				t.Run(name, func(t *testing.T) {
					home, err := filepath.EvalSymlinks(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					p := Profile{home: home, configHome: filepath.Join(home, ".config")}
					dir := filepath.Join(home, ".gnupg", "public-keys.d")
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					database := filepath.Join(dir, "pubring.db")
					if file.suffix != "" {
						if err := os.WriteFile(database, []byte("fixture public database"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					path := database + file.suffix
					want := ""
					switch name {
					case "regular":
						if err := os.WriteFile(path, []byte("fixture public authority"), 0600); err != nil {
							t.Fatal(err)
						}
					case "directory":
						if err := os.Mkdir(path, 0700); err != nil {
							t.Fatal(err)
						}
						want = "must be regular files"
					case "hardlink", "escape":
						outside := filepath.Join(t.TempDir(), "outside")
						if err := os.WriteFile(outside, []byte("fixture public authority"), 0600); err != nil {
							t.Fatal(err)
						}
						if name == "hardlink" {
							err = os.Link(outside, path)
							want = "unprovable file alias"
						} else {
							err = os.Symlink(outside, path)
							want = "outside protected configuration"
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					err = p.checkKeyboxdFiles(database)
					if name == "missing" && file.suffix == "" {
						if !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("missing public database: %v", err)
						}
					} else if want != "" {
						requireKeyboxdRefusal(t, err, want)
					} else if err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func TestKeyboxdSocketPath(t *testing.T) {
	for _, private := range []bool{false, true} {
		kind, want := "source", "operator keyboxd socket is outside"
		if private {
			kind, want = "client", "private keyboxd client socket resolves outside"
		}
		t.Run(kind, func(t *testing.T) {
			for _, c := range []struct {
				name, socket string
				ok           bool
			}{
				{"exact", "/protected/home/S.keyboxd", true},
				{"other-home", "/protected/other/S.keyboxd", false},
				{"prefix-home", "/protected/home-other/S.keyboxd", false},
				{"nested", "/protected/home/subdir/S.keyboxd", false},
				{"other-socket", "/protected/home/S.gpg-agent", false},
				{"traversal", "/protected/home/subdir/../S.keyboxd", false},
				{"relative", "S.keyboxd", false},
			} {
				t.Run(c.name, func(t *testing.T) {
					err := checkKeyboxdSocketPath("/protected/home", c.socket, private)
					if !c.ok {
						requireKeyboxdRefusal(t, err, want)
					} else if err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

// Socket ownership and alias metadata cannot be created by an unprivileged
// test, and the sandbox refuses real Unix listeners.
type keyboxdFileInfo struct {
	mode os.FileMode
	stat any
}

func (f keyboxdFileInfo) Name() string       { return "fixture" }
func (f keyboxdFileInfo) Size() int64        { return 0 }
func (f keyboxdFileInfo) Mode() os.FileMode  { return f.mode }
func (f keyboxdFileInfo) ModTime() time.Time { return time.Time{} }
func (f keyboxdFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f keyboxdFileInfo) Sys() any           { return f.stat }

func TestKeyboxdSocket(t *testing.T) {
	for _, c := range []struct {
		name string
		info keyboxdFileInfo
		ok   bool
	}{
		{"socket", keyboxdFileInfo{os.ModeSocket | 0600, &syscall.Stat_t{Uid: 501, Nlink: 1}}, true},
		{"regular-file", keyboxdFileInfo{0600, &syscall.Stat_t{Uid: 501, Nlink: 1}}, false},
		{"directory", keyboxdFileInfo{os.ModeDir | 0700, &syscall.Stat_t{Uid: 501, Nlink: 1}}, false},
		{"symlink", keyboxdFileInfo{os.ModeSymlink | 0700, &syscall.Stat_t{Uid: 501, Nlink: 1}}, false},
		{"other-owner", keyboxdFileInfo{os.ModeSocket | 0600, &syscall.Stat_t{Uid: 502, Nlink: 1}}, false},
		{"alias", keyboxdFileInfo{os.ModeSocket | 0600, &syscall.Stat_t{Uid: 501, Nlink: 2}}, false},
		{"no-links", keyboxdFileInfo{os.ModeSocket | 0600, &syscall.Stat_t{Uid: 501, Nlink: 0}}, false},
		{"missing-stat", keyboxdFileInfo{os.ModeSocket | 0600, nil}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := checkKeyboxdSocket(c.info, 501)
			if !c.ok {
				requireKeyboxdRefusal(t, err, "unaliased Unix socket owned by the operator")
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestKeyboxdSocketDirectory(t *testing.T) {
	for _, c := range []struct {
		name string
		info keyboxdFileInfo
		ok   bool
	}{
		{"private", keyboxdFileInfo{os.ModeDir | 0700, &syscall.Stat_t{Uid: 501}}, true},
		{"readable", keyboxdFileInfo{os.ModeDir | 0755, &syscall.Stat_t{Uid: 501}}, true},
		{"regular-file", keyboxdFileInfo{0600, &syscall.Stat_t{Uid: 501}}, false},
		{"other-owner", keyboxdFileInfo{os.ModeDir | 0700, &syscall.Stat_t{Uid: 502}}, false},
		{"group-writable", keyboxdFileInfo{os.ModeDir | 0720, &syscall.Stat_t{Uid: 501}}, false},
		{"other-writable", keyboxdFileInfo{os.ModeDir | 0702, &syscall.Stat_t{Uid: 501}}, false},
		{"missing-stat", keyboxdFileInfo{os.ModeDir | 0700, nil}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := checkKeyboxdSocketDirectory(c.info, 501)
			if !c.ok {
				requireKeyboxdRefusal(t, err, "directory must be owned and writable only by the operator")
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
