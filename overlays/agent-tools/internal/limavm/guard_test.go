package limavm

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestDeniedPathRegex holds the profile's glob translation to the one
// modules/home-manager/codex makes for the same deniedReads: ** crosses
// directories, * does not, and the rest is literal; Seatbelt, as Codex,
// also denies what a matched directory holds.
func TestDeniedPathRegex(t *testing.T) {
	for _, tc := range []struct {
		glob    string
		matches []string
		misses  []string
	}{
		{"/Users/wgn/git/**/.env",
			[]string{"/Users/wgn/git/.env", "/Users/wgn/git/a/.env", "/Users/wgn/git/a/b/.env", "/Users/wgn/git/a/.env/x"},
			[]string{"/Users/wgn/git/a.env", "/Users/wgn/git/a/b.env", "/Users/wgn/gitx/.env", "/Users/wgn/git/a/.envrc"}},
		{"/private/var/folders/*/*/T/secrets.d",
			[]string{"/private/var/folders/n0/x/T/secrets.d", "/private/var/folders/n0/x/T/secrets.d/1/key"},
			[]string{"/private/var/folders/n0/T/secrets.d", "/private/var/folders/n0/x/y/T/secrets.d", "/private/var/folders/n0/x/T/secretsXd"}},
		{"/Users/wgn/a+b/**",
			[]string{"/Users/wgn/a+b/c", "/Users/wgn/a+b/c/d"},
			[]string{"/Users/wgn/aab/c", "/Users/wgn/a+bc"}},
	} {
		t.Run(tc.glob, func(t *testing.T) {
			re := regexp.MustCompile(deniedPathRegex(tc.glob))
			for _, p := range tc.matches {
				if !re.MatchString(p) {
					t.Errorf("%s does not match %s", re, p)
				}
			}
			for _, p := range tc.misses {
				if re.MatchString(p) {
					t.Errorf("%s matches %s", re, p)
				}
			}
		})
	}
}

const (
	probeQEMU     = "/nix/store/59hhc8zlvdlbcls7dkbhkhw9afwb6mps-qemu-11.1.2/bin/qemu-system-aarch64"
	probeFirmware = "/nix/store/59hhc8zlvdlbcls7dkbhkhw9afwb6mps-qemu-11.1.2/share/qemu"
)

// probeConfig is a guard configuration with a Lima home of the test's
// own, holding the instance probe and its qcow2 boot disk. argvFixture
// moves the P0 probe's paths there.
func probeConfig(t *testing.T) *GuardConfig {
	t.Helper()
	home := t.TempDir()
	g, err := ParseGuardConfig([]byte(`{"home":"/Users/wgn","limaHome":"` + home + `","instance":"probe","qemu":"` + probeQEMU + `",
	"deniedReads":["/Users/wgn/.ssh","/private/var/folders/*/*/T/secrets.d","/Users/wgn/Library/Application Support/ngrok"],
	"connect":"192.168.5.100:3128","quic":"192.168.5.101:443"}`))
	if err != nil {
		t.Fatal(err)
	}
	writeDisk(t, g, qcow2Header(3, 0, 0))
	return g
}

// qcow2Header is the start of a qcow2 image's header: its magic, version,
// backing file offset and (version 3) incompatible feature bits.
func qcow2Header(version uint32, backing, features uint64) []byte {
	h := make([]byte, 104)
	copy(h, "QFI\xfb")
	binary.BigEndian.PutUint32(h[4:], version)
	binary.BigEndian.PutUint64(h[8:], backing)
	binary.BigEndian.PutUint64(h[72:], features)
	return h
}

func writeDisk(t *testing.T, g *GuardConfig, header []byte) {
	t.Helper()
	if err := os.MkdirAll(g.InstanceDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.InstanceDir(), "disk"), header, 0o600); err != nil {
		t.Fatal(err)
	}
}

// argvFixture reads testdata/NAME.argv, the arguments of one QEMU
// invocation Lima 2.2 made in the operator's P0 probe, one per line, with
// the probe's Lima home moved to g's and its firmware to the store QEMU's.
func argvFixture(t *testing.T, g *GuardConfig, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".argv"))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(data), "/private/tmp/lima-probe/probe", g.InstanceDir())
	s = strings.ReplaceAll(s, "/private/tmp/claude/limaprobe/share/qemu", probeFirmware)
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// replaced returns args with the argument old replaced by new.
func replaced(args []string, old, new string) []string {
	out := slices.Clone(args)
	out[slices.Index(out, old)] = new
	return out
}

// relays is what the guard appends to the user netdev for g.
func relays(g *GuardConfig) string {
	mcp := g.LimaHome + "/_mcp"
	return ",restrict=on,ipv6=off" +
		",guestfwd=tcp:192.168.5.100:3128-cmd:" + mcp + "/bin/lima-mcp lima-egress connect --config " + mcp + "/config.json" +
		",guestfwd=tcp:192.168.5.101:443-cmd:" + mcp + "/bin/lima-mcp lima-egress quic --config " + mcp + "/config.json"
}

func TestGuard(t *testing.T) {
	c := probeConfig(t)

	for _, name := range []string{"probe-accel", "probe-netdev", "probe-machine", "probe-cpu", "probe-version"} {
		t.Run("passes Lima's "+name+" through unchanged", func(t *testing.T) {
			args := argvFixture(t, c, name)
			launch, err := Guard(c, args)
			if err != nil {
				t.Fatal(err)
			}
			if launch.Path != probeQEMU || !slices.Equal(launch.Argv, append([]string{probeQEMU}, args...)) {
				t.Fatalf("launch %s %q", launch.Path, launch.Argv)
			}
		})
	}

	t.Run("confines the boot and restricts its network and disk", func(t *testing.T) {
		args := argvFixture(t, c, "boot")
		launch, err := Guard(c, args)
		if err != nil {
			t.Fatal(err)
		}
		if launch.Path != "/usr/bin/sandbox-exec" || len(launch.Argv) != len(args)+4 ||
			!slices.Equal(launch.Argv[:2], []string{"/usr/bin/sandbox-exec", "-p"}) || launch.Argv[3] != probeQEMU {
			t.Fatalf("launch %s %q", launch.Path, launch.Argv[:4])
		}
		qemuArgs := launch.Argv[4:]
		for i, a := range args {
			want := a
			if strings.HasPrefix(a, "user,id=net0,") {
				want = a + relays(c)
			}
			if strings.HasSuffix(a, ",id=boot-disk") {
				want = a + ",format=qcow2"
			}
			if qemuArgs[i] != want {
				t.Errorf("argument %d is %q; want %q", i, qemuArgs[i], want)
			}
		}
	})

	t.Run("takes firmware from any store QEMU", func(t *testing.T) {
		args := argvFixture(t, c, "boot")
		i := slices.IndexFunc(args, func(a string) bool { return strings.HasPrefix(a, "if=pflash") })
		args[i] = strings.Replace(args[i], probeFirmware, "/nix/store/0000000000000000000000000000000a-qemu-11.2.0/share/qemu", 1)
		if _, err := Guard(c, args); err != nil {
			t.Fatal(err)
		}
	})

	for name, header := range map[string][]byte{
		"a disk that is not qcow2":             make([]byte, 104),
		"a disk with a backing file":           qcow2Header(3, 0x200, 0),
		"a disk with an external data file":    qcow2Header(3, 0, 1<<2),
		"a version 2 disk with a backing file": qcow2Header(2, 0x200, 0),
		"a disk of another qcow2 version":      qcow2Header(4, 0, 0),
	} {
		t.Run("refuses "+name, func(t *testing.T) {
			d := probeConfig(t)
			writeDisk(t, d, header)
			if launch, err := Guard(d, argvFixture(t, d, "boot")); err == nil {
				t.Fatalf("launched %s", launch.Path)
			}
		})
	}

	t.Run("refuses a boot disk that is a link", func(t *testing.T) {
		d := probeConfig(t)
		disk := filepath.Join(d.InstanceDir(), "disk")
		os.Remove(disk)
		other := filepath.Join(t.TempDir(), "other")
		if err := os.WriteFile(other, qcow2Header(3, 0, 0), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(other, disk); err != nil {
			t.Fatal(err)
		}
		if _, err := Guard(d, argvFixture(t, d, "boot")); err == nil {
			t.Fatal("launched through a linked disk")
		}
	})

	boot := argvFixture(t, c, "boot")
	// with returns the boot argv with the value of the first flag given
	// replaced, or with flag and value appended when the flag is absent or
	// replace is false.
	with := func(flag, value string, replace bool) []string {
		args := slices.Clone(boot)
		if i := slices.Index(args, flag); replace && i >= 0 {
			args[i+1] = value
			return args
		}
		return append(args, flag, value)
	}
	netdev := "user,id=net0,net=192.168.5.0/24,dhcpstart=192.168.5.15,hostfwd=tcp:127.0.0.1:59607-:22"
	instance := c.InstanceDir()
	bootDrive := "file=" + instance + "/disk,if=none,discard=on,id=boot-disk"
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"a tftp server on the netdev", with("-netdev", netdev+",tftp=/", true)},
		{"an smb share on the netdev", with("-netdev", netdev+",smb=/Users", true)},
		{"a bootfile on the netdev", with("-netdev", netdev+",bootfile=x", true)},
		{"a guestfwd it did not write", with("-netdev", netdev+",guestfwd=tcp:10.0.2.100:1-cmd:/bin/sh", true)},
		{"restrict turned off", with("-netdev", netdev+",restrict=off", true)},
		{"a host forward to another port", with("-netdev", strings.Replace(netdev, "-:22", "-:2375", 1), true)},
		{"a host forward on every address", with("-netdev", strings.Replace(netdev, "127.0.0.1", "0.0.0.0", 1), true)},
		{"a second host forward", with("-netdev", netdev+",hostfwd=tcp:127.0.0.1:2-:22", true)},
		{"an escaped comma", with("-netdev", netdev+",,x", true)},
		{"a second netdev", with("-netdev", "user,id=net1", false)},
		{"a netdev of another kind", with("-netdev", "vmnet-shared,id=net0", true)},
		{"no netdev", slices.Delete(slices.Clone(boot), slices.Index(boot, "-netdev"), slices.Index(boot, "-netdev")+2)},
		{"-nic", with("-nic", "user", false)},
		{"-net", with("-net", "user", false)},
		{"-virtfs", with("-virtfs", "local,path=/,mount_tag=h,security_model=none", false)},
		{"-fsdev", with("-fsdev", "local,id=f,path=/", false)},
		{"-object", with("-object", "memory-backend-file,id=m,mem-path=/Users/wgn,size=1G", false)},
		{"-vnc", with("-vnc", ":0", false)},
		{"-fw_cfg", with("-fw_cfg", "name=opt/x,file=/Users/wgn/.ssh/id_ed25519", false)},
		{"-readconfig", with("-readconfig", "/tmp/q.cfg", false)},
		{"-set", with("-set", "netdev.net0.restrict=off", false)},
		{"-global", with("-global", "virtio-net-pci.romfile=/x", false)},
		{"-incoming", with("-incoming", "tcp:0:4444", false)},
		{"-loadvm", with("-loadvm", "x", false)},
		{"-plugin", with("-plugin", "/tmp/x.so", false)},
		{"-add-fd", with("-add-fd", "fd=3,set=1", false)},
		{"-D", with("-D", "/Users/wgn/log", false)},
		{"-trace", with("-trace", "file=/Users/wgn/t", false)},
		{"-monitor on stdio", with("-monitor", "stdio", false)},
		{"a 9p device", with("-device", "virtio-9p-pci,fsdev=f,mount_tag=h", false)},
		{"a device's option ROM", with("-device", "virtio-rng-pci,romfile=/Users/wgn/.ssh/id_ed25519", false)},
		{"a drive outside the instance", with("-drive", "file=/Users/wgn/.ssh/id_ed25519,if=none,id=x", false)},
		{"a drive in a sibling instance", with("-drive", "file="+instance+"2/disk,if=none,id=x", false)},
		{"a drive that climbs out", with("-drive", "file="+instance+"/../../x,if=none,id=x", false)},
		{"a drive over the network", with("-drive", "file=nbd://10.0.0.1/x,if=none,id=x", false)},
		{"a boot disk with a format Lima did not give", replaced(boot, bootDrive, bootDrive+",format=raw")},
		{"a boot disk elsewhere in the instance", replaced(boot, bootDrive, strings.Replace(bootDrive, "/disk,", "/basedisk,", 1))},
		{"a raw image without a format", with("-drive", "file="+instance+"/cidata.iso,if=none,readonly=on,id=x", false)},
		{"a writable raw image", with("-drive", "file="+instance+"/x.img,if=none,format=raw,id=x", false)},
		{"firmware outside the store", with("-drive", "if=pflash,format=raw,readonly=on,file=/tmp/edk2-aarch64-code.fd", false)},
		{"firmware that climbs out of the store", with("-drive", "if=pflash,format=raw,readonly=on,file=/nix/store/x/../../tmp/share/qemu/edk2-aarch64-code.fd", false)},
		{"writable firmware", with("-drive", "if=pflash,format=raw,file="+probeFirmware+"/edk2-aarch64-code.fd", false)},
		{"a chardev socket outside the instance", with("-chardev", "socket,id=c,path=/tmp/x.sock,server=on,wait=off", false)},
		{"a chardev that is a file", with("-chardev", "file,id=c,path="+instance+"/x", false)},
		{"a chardev on TCP", with("-chardev", "socket,id=c,host=0.0.0.0,port=4444,server=on,wait=off", false)},
		{"QMP on TCP", with("-qmp", "tcp:127.0.0.1:4444,server=on", true)},
		{"a serial port on an undeclared chardev", with("-serial", "chardev:other", true)},
		{"a display", with("-display", "cocoa", true)},
		{"another accelerator", with("-machine", "virt,accel=tcg,dumpdtb=/x", true)},
		{"a pid file elsewhere", with("-pidfile", "/tmp/qemu.pid", true)},
		{"an unknown flag", with("-sandbox", "off", false)},
		{"a positional argument", append(slices.Clone(boot), "/tmp/kernel")},
		{"a flag without its value", append(slices.Clone(boot), "-m")},
		{"a probe with an extra flag", []string{"--version", "-netdev", "user,id=x"}},
	} {
		t.Run("refuses "+tc.name, func(t *testing.T) {
			if launch, err := Guard(c, tc.args); err == nil {
				t.Fatalf("launched %s %q", launch.Path, launch.Argv)
			}
		})
	}
}

func TestProfile(t *testing.T) {
	g := probeConfig(t)
	p := Profile(g)
	h := g.LimaHome
	for _, want := range []string{
		`(deny file-read* (subpath "/Users/wgn")`,
		`(subpath "/Users/wgn/.ssh")`,
		`(regex #"^/private/var/folders/[^/]*/[^/]*/T/secrets\.d(/.*)?$")`,
		`(subpath "/Users/wgn/Library/Application Support/ngrok")`,
		`(allow file-read* (subpath "` + h + `"))`,
		`(deny file-write*)`,
		// Not the links, the configuration, the CA or Lima's _config.
		`(allow file-write* (subpath "` + h + `/probe") (subpath "` + h + `/_mcp/slots") (subpath "` + h + `/_mcp/slots-quic") (literal "` + h + `/_mcp/egress.log") (subpath "/dev"))`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile lacks %s:\n%s", want, p)
		}
	}
	// Seatbelt applies the last rule that matches, so the Lima home's
	// allowances must come after the denials that cover it.
	if strings.Index(p, "(allow file-read*") < strings.Index(p, "(deny file-read*") {
		t.Errorf("the read allowance precedes the denial:\n%s", p)
	}
}

func TestCheckLive(t *testing.T) {
	c := probeConfig(t)
	launch, err := Guard(c, argvFixture(t, c, "boot"))
	if err != nil {
		t.Fatal(err)
	}
	// QEMU's own argv, as kern.procargs2 shows it once sandbox-exec has
	// executed it.
	live := launch.Argv[3:]
	if err := CheckLive(c, probeQEMU, live); err != nil {
		t.Fatalf("the guard's own launch: %v", err)
	}
	t.Run("accepts it after a switch to another QEMU", func(t *testing.T) {
		next := *c
		next.QEMU = "/nix/store/0000000000000000000000000000000a-qemu-11.2.0/bin/qemu-system-aarch64"
		if err := CheckLive(&next, probeQEMU, live); err != nil {
			t.Fatal(err)
		}
	})
	for _, tc := range []struct {
		name, exe string
		argv      []string
	}{
		{"another executable", "/opt/homebrew/bin/qemu-system-aarch64", live},
		{"another argv[0]", probeQEMU, append([]string{"qemu-system-aarch64"}, live[1:]...)},
		{"a QEMU outside the store", "/tmp/nix/store/x/bin/qemu-system-aarch64", append([]string{"/tmp/nix/store/x/bin/qemu-system-aarch64"}, live[1:]...)},
		{"Lima's argv, unrestricted", probeQEMU, append([]string{probeQEMU}, argvFixture(t, c, "boot")...)},
		{"a boot disk without its format", probeQEMU, func() []string {
			a := slices.Clone(live)
			for i := range a {
				a[i] = strings.TrimSuffix(a[i], ",format=qcow2")
			}
			return a
		}()},
		{"an extra guestfwd", probeQEMU, func() []string {
			a := slices.Clone(live)
			i := slices.Index(a, "-netdev")
			a[i+1] += ",guestfwd=tcp:192.168.5.102:1-cmd:/bin/sh"
			return a
		}()},
		{"an extra flag", probeQEMU, append(slices.Clone(live), "-vnc", ":0")},
		{"a probe", probeQEMU, []string{probeQEMU, "--version"}},
	} {
		t.Run("reports "+tc.name, func(t *testing.T) {
			if err := CheckLive(c, tc.exe, tc.argv); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
