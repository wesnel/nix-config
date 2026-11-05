package limavm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// sandboxExec is macOS's Seatbelt launcher, which applies a profile and
// then executes the command, so QEMU keeps the guard's process ID.
const sandboxExec = "/usr/bin/sandbox-exec"

// Launch is what the guard executes: Path, with Argv as its argv.
type Launch struct {
	Path string
	Argv []string
}

// probes are the invocations Lima 2.2 makes before booting, to learn
// QEMU's version and what it supports, as the P0 probe logged them. They
// start no VM, so the guard runs them unchanged and unconfined.
var probes = [][]string{
	{"-M", "none", "-accel", "help"},
	{"-M", "none", "-netdev", "help"},
	{"-machine", "help"},
	{"-cpu", "help", "-machine", "virt"},
	{"--version"},
}

// Guard decides what to run for the QEMU arguments Lima gave: a probe as
// it is; a boot whose every flag and value the allowlist accepts, with its
// network restricted to the two relays, under sandbox-exec with Profile;
// and nothing else, which it refuses.
func Guard(c *GuardConfig, args []string) (Launch, error) {
	for _, p := range probes {
		if slices.Equal(args, p) {
			return Launch{Path: c.QEMU, Argv: append([]string{c.QEMU}, args...)}, nil
		}
	}
	if err := checkBoot(c, args); err != nil {
		return Launch{}, err
	}
	if err := checkQcow2(bootDisk(c)); err != nil {
		return Launch{}, err
	}
	argv := append([]string{sandboxExec, "-p", Profile(c), c.QEMU}, rewrite(c, args)...)
	return Launch{Path: sandboxExec, Argv: argv}, nil
}

// storeQEMU and storeFirmware match a QEMU and its aarch64 firmware in any
// store path: the store is immutable and root's, and a switch to another
// QEMU leaves a VM it launched running from the earlier one.
var (
	storeQEMU     = regexp.MustCompile(`^/nix/store/[0-9a-z][^/]*/bin/qemu-system-aarch64$`)
	storeFirmware = regexp.MustCompile(`^/nix/store/[0-9a-z][^/]*/share/qemu/edk2-aarch64-code\.fd$`)
)

// CheckLive checks a running QEMU's executable and argv, as
// kern.procargs2 gives them, against what Guard launches: a store QEMU,
// with arguments that are an allowed boot as the guard rewrote it.
//
// It cannot tell whether that QEMU runs under the guard's Seatbelt
// profile: a process's sandbox is observable only through
// libsystem_sandbox, which needs cgo. That is a recorded residual; what
// keeps QEMU confined is that only the guard launches it with these
// arguments.
func CheckLive(c *GuardConfig, exe string, argv []string) error {
	if !storeQEMU.MatchString(exe) || len(argv) == 0 || argv[0] != exe {
		return fmt.Errorf("the running QEMU is %q (argv[0] %q), not a store QEMU", exe, first(argv))
	}
	args := slices.Clone(argv[1:])
	i := slices.Index(args, "-netdev")
	if i < 0 || i+1 >= len(args) {
		return errors.New("the running QEMU has no netdev")
	}
	original, ok := strings.CutSuffix(args[i+1], relaySuffix(c))
	if !ok {
		return errors.New("the running QEMU's netdev is not restricted to the relays")
	}
	args[i+1] = original
	for j := 1; j < len(args); j++ {
		if args[j-1] == "-drive" {
			args[j] = strings.TrimSuffix(args[j], bootDiskFormat)
		}
	}
	if err := checkBoot(c, args); err != nil {
		return fmt.Errorf("the running QEMU: %w", err)
	}
	if !slices.Equal(rewrite(c, args), argv[1:]) {
		return errors.New("the running QEMU's argv is not the guard's")
	}
	return nil
}

func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// relaySuffix is what the guard appends to the user netdev: no route out
// of slirp but the two guestfwd relays, which run lima-mcp through the
// stable links.
func relaySuffix(c *GuardConfig) string {
	relay := func(at, mode string) string {
		return fmt.Sprintf(",guestfwd=tcp:%s-cmd:%s lima-egress %s --config %s", at, c.BinLink(), mode, c.ConfigLink())
	}
	return ",restrict=on,ipv6=off" + relay(c.Connect.String(), "connect") + relay(c.QUIC.String(), "quic")
}

// bootDiskFormat is what the guard appends to Lima's boot disk, which
// names no format: QEMU would otherwise probe it, and a raw disk the guest
// wrote a qcow2 header into would open its backing file. Lima 2.2 renames
// the downloaded image to the disk (EnsureDisk in pkg/driver/qemu), and the
// pinned Ubuntu image is qcow2.
const bootDiskFormat = ",format=qcow2"

func bootDisk(c *GuardConfig) string { return path.Join(c.InstanceDir(), "disk") }

func rewrite(c *GuardConfig, args []string) []string {
	out := slices.Clone(args)
	for i := range out {
		if i > 0 && out[i-1] == "-netdev" {
			out[i] += relaySuffix(c)
		}
		if i > 0 && out[i-1] == "-drive" && strings.Contains(out[i], ",id=boot-disk") {
			out[i] += bootDiskFormat
		}
	}
	return out
}

// checkQcow2 refuses a boot disk that is not a qcow2 image standing alone:
// one with a backing file, or (version 3) an external data file, would let
// QEMU open another file than the disk.
func checkQcow2(disk string) error {
	f, err := os.OpenFile(disk, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("the boot disk: %w", err)
	}
	defer f.Close()
	var h [80]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return fmt.Errorf("the boot disk %s: %w", disk, err)
	}
	be := binary.BigEndian
	version := be.Uint32(h[4:8])
	switch {
	case string(h[:4]) != "QFI\xfb":
		return fmt.Errorf("the boot disk %s is not qcow2", disk)
	case version != 2 && version != 3:
		return fmt.Errorf("the boot disk %s is qcow2 version %d", disk, version)
	case be.Uint64(h[8:16]) != 0:
		return fmt.Errorf("the boot disk %s has a backing file", disk)
	case version == 3 && be.Uint64(h[72:80])&(1<<2) != 0:
		return fmt.Errorf("the boot disk %s has an external data file", disk)
	}
	return nil
}

// options is one QEMU option value split at its commas: the leading
// positional value, if any, and the key=value pairs. An escaped comma
// (",,") is refused rather than parsed.
type options struct {
	head string
	kv   map[string]string
	keys []string
}

func parseOptions(value string, head bool) (options, error) {
	if strings.Contains(value, ",,") {
		return options{}, fmt.Errorf("%q holds an escaped comma", value)
	}
	o := options{kv: map[string]string{}}
	parts := strings.Split(value, ",")
	if head {
		o.head, parts = parts[0], parts[1:]
	}
	for _, p := range parts {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return options{}, fmt.Errorf("%q: %q is not key=value", value, p)
		}
		if _, dup := o.kv[k]; dup {
			return options{}, fmt.Errorf("%q repeats %s", value, k)
		}
		o.kv[k] = v
		o.keys = append(o.keys, k)
	}
	return o, nil
}

// only refuses a key outside allowed, and requires those in required.
func (o options) only(allowed []string, required ...string) error {
	for _, k := range o.keys {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("option %s is not allowed", k)
		}
	}
	for _, k := range required {
		if _, ok := o.kv[k]; !ok {
			return fmt.Errorf("option %s is missing", k)
		}
	}
	return nil
}

var (
	number   = regexp.MustCompile(`^[1-9][0-9]{0,6}$`)
	smp      = regexp.MustCompile(`^[1-9][0-9]?,sockets=1,cores=[1-9][0-9]?,threads=1$`)
	ident    = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,31}$`)
	mac      = regexp.MustCompile(`^52:55:55(:[0-9a-f]{2}){3}$`)
	hostfwd  = regexp.MustCompile(`^tcp:127\.0\.0\.1:[1-9][0-9]{0,4}-:22$`)
	bootSpec = regexp.MustCompile(`^order=[cd],splash-time=0,menu=on$`)
)

// devices are the QEMU devices Lima 2.2 adds for the template, as the P0
// probe logged them, each with the properties it may set. None of these
// properties names a host file.
var devices = map[string][]string{
	"qemu-xhci":         {"id"},
	"virtio-blk-pci":    {"drive", "bootindex"},
	"virtio-scsi":       {"id"},
	"scsi-cd":           {"bus", "drive"},
	"virtio-net-pci":    {"netdev", "mac"},
	"virtio-rng-pci":    {},
	"pci-serial":        {"chardev"},
	"virtio-serial-pci": {"id", "max_ports"},
	"virtconsole":       {"chardev", "id"},
}

// checkBoot refuses any flag, and any value of a flag, that the allowlist
// does not name: the flags Lima 2.2 emits for the managed template, as the
// P0 probe logged them, with values that keep files to the instance
// directory and the store firmware, sockets to Unix sockets in the
// instance directory, and the network to one user netdev with only the
// SSH forward.
func checkBoot(c *GuardConfig, args []string) error {
	instance := c.InstanceDir()
	inInstance := func(p string) bool {
		return path.Clean(p) == p && strings.HasPrefix(p, instance+"/")
	}
	firmware := func(p string) bool {
		return path.Clean(p) == p && storeFirmware.MatchString(p)
	}
	// The first pass reads the chardevs and drives, which other flags
	// name by id wherever they come.
	chardevs, drives := map[string]bool{}, map[string]bool{}
	counts := map[string]int{}
	if len(args)%2 != 0 {
		return errors.New("a flag lacks its value")
	}
	for i := 0; i < len(args); i += 2 {
		flag, value := args[i], args[i+1]
		if !strings.HasPrefix(flag, "-") {
			return fmt.Errorf("positional argument %q", flag)
		}
		counts[flag]++
		switch flag {
		case "-chardev":
			o, err := parseOptions(value, true)
			if err != nil {
				return err
			}
			if o.head != "socket" {
				return fmt.Errorf("-chardev %q is not a socket", value)
			}
			if err := o.only([]string{"id", "path", "server", "wait", "logfile"}, "id", "path"); err != nil {
				return fmt.Errorf("-chardev %q: %w", value, err)
			}
			if !ident.MatchString(o.kv["id"]) || !inInstance(o.kv["path"]) || o.kv["server"] != "on" || o.kv["wait"] != "off" {
				return fmt.Errorf("-chardev %q is not a listening Unix socket in the instance directory", value)
			}
			if l, ok := o.kv["logfile"]; ok && !inInstance(l) {
				return fmt.Errorf("-chardev %q logs outside the instance directory", value)
			}
			chardevs[o.kv["id"]] = true
		case "-drive":
			o, err := parseOptions(value, false)
			if err != nil {
				return err
			}
			if err := o.only([]string{"if", "format", "readonly", "file", "discard", "id"}, "file", "if"); err != nil {
				return fmt.Errorf("-drive %q: %w", value, err)
			}
			// Three drives, as Lima 2.2 writes them: the firmware, raw
			// and read-only; the boot disk, with no format, which the guard
			// sets; and the read-only raw cidata ISO.
			file, format, hasFormat := o.kv["file"], o.kv["format"], o.kv["format"] != ""
			switch {
			case o.kv["if"] == "pflash" && firmware(file) && o.kv["readonly"] == "on" && format == "raw":
			case o.kv["if"] == "none" && o.kv["id"] == "boot-disk" && file == bootDisk(c) && !hasFormat && o.kv["discard"] == "on":
				drives["boot-disk"] = true
			case o.kv["if"] == "none" && inInstance(file) && ident.MatchString(o.kv["id"]) && o.kv["id"] != "boot-disk" &&
				format == "raw" && o.kv["readonly"] == "on":
				drives[o.kv["id"]] = true
			default:
				return fmt.Errorf("-drive %q is not read-only raw store firmware, the instance's boot disk, or a read-only raw image in the instance directory", value)
			}
			if _, ok := o.kv["discard"]; ok && o.kv["id"] != "boot-disk" {
				return fmt.Errorf("-drive %q: discard", value)
			}
		}
	}
	for flag, n := range counts {
		if n > 1 && flag != "-device" && flag != "-drive" && flag != "-chardev" {
			return fmt.Errorf("%s appears %d times", flag, n)
		}
	}
	if counts["-netdev"] != 1 || counts["-pidfile"] != 1 {
		return errors.New("a boot needs exactly one -netdev and one -pidfile")
	}
	chardev := func(v string) bool {
		id, ok := strings.CutPrefix(v, "chardev:")
		return ok && chardevs[id]
	}
	for i := 0; i < len(args); i += 2 {
		flag, value := args[i], args[i+1]
		var ok bool
		switch flag {
		case "-chardev", "-drive":
			ok = true
		case "-m":
			ok = number.MatchString(value)
		case "-cpu":
			ok = value == "max" || value == "host"
		case "-machine":
			ok = value == "virt,accel=hvf"
		case "-smp":
			ok = smp.MatchString(value)
		case "-boot":
			ok = bootSpec.MatchString(value)
		case "-audiodev":
			o, err := parseOptions(value, true)
			ok = err == nil && o.head == "none" && o.only([]string{"id"}, "id") == nil && ident.MatchString(o.kv["id"])
		case "-display", "-vga", "-parallel":
			ok = value == "none"
		case "-serial", "-qmp":
			ok = chardev(value)
		case "-name":
			ok = value == "lima-"+c.Instance
		case "-pidfile":
			ok = value == path.Join(instance, "qemu.pid")
		case "-netdev":
			ok = checkNetdev(value) == nil
		case "-device":
			ok = checkDevice(value, chardevs, drives) == nil
		}
		if !ok {
			return fmt.Errorf("QEMU flag %s %q is not allowed", flag, value)
		}
	}
	return nil
}

// checkNetdev allows only the user netdev Lima configures, with its one
// SSH forward on loopback, to which the guard adds the restriction and the
// relays; any other option (tftp, smb, bootfile, guestfwd, restrict) is
// refused.
func checkNetdev(value string) error {
	o, err := parseOptions(value, true)
	if err != nil {
		return err
	}
	if o.head != "user" {
		return fmt.Errorf("netdev %s", o.head)
	}
	if err := o.only([]string{"id", "net", "dhcpstart", "hostfwd"}, "id", "net", "dhcpstart", "hostfwd"); err != nil {
		return err
	}
	if o.kv["id"] != "net0" || o.kv["net"] != "192.168.5.0/24" || o.kv["dhcpstart"] != "192.168.5.15" || !hostfwd.MatchString(o.kv["hostfwd"]) {
		return fmt.Errorf("netdev %q", value)
	}
	return nil
}

func checkDevice(value string, chardevs, drives map[string]bool) error {
	o, err := parseOptions(value, true)
	if err != nil {
		return err
	}
	allowed, ok := devices[o.head]
	if !ok {
		return fmt.Errorf("device %s", o.head)
	}
	if err := o.only(allowed); err != nil {
		return err
	}
	for k, v := range o.kv {
		var valid bool
		switch k {
		case "chardev":
			valid = chardevs[v]
		case "drive":
			valid = drives[v]
		case "netdev":
			valid = v == "net0"
		case "mac":
			valid = mac.MatchString(v)
		case "bootindex", "max_ports":
			valid = number.MatchString(v)
		case "bus":
			valid = regexp.MustCompile(`^[a-z][a-z0-9]*\.[0-9]$`).MatchString(v)
		case "id":
			valid = ident.MatchString(v)
		}
		if !valid {
			return fmt.Errorf("device %s %s=%q", o.head, k, v)
		}
	}
	return nil
}

// Profile is the Seatbelt profile QEMU runs under, which its guestfwd
// relays inherit: it may not read the operator's home, nor the paths the
// host sandbox denies, but for the Lima home. It may write only the
// instance directory, the relays' slots and log, and devices: not the
// links, configuration or CA under _mcp, nor Lima's _config. Seatbelt
// applies the last rule that matches.
func Profile(c *GuardConfig) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n")
	fmt.Fprintf(&b, "(deny file-read* (subpath %q)", c.Home)
	for _, p := range c.DeniedReads {
		if strings.Contains(p, "*") {
			fmt.Fprintf(&b, " (regex #\"%s\")", deniedPathRegex(p))
		} else {
			fmt.Fprintf(&b, " (subpath %q)", p)
		}
	}
	b.WriteString(")\n")
	fmt.Fprintf(&b, "(allow file-read* (subpath %q))\n", c.LimaHome)
	b.WriteString("(deny file-write*)\n")
	fmt.Fprintf(&b, "(allow file-write* (subpath %q) (subpath %q) (subpath %q) (literal %q) (subpath \"/dev\"))\n",
		c.InstanceDir(), c.Slots(), c.QUICSlots(), c.EgressLog())
	return b.String()
}

// deniedPathRegex is a Seatbelt regex for a deniedReads glob, translated
// as modules/home-manager/codex translates the same globs for Codex:
// "**/" is any run of directories, none included, "**" anything, "*"
// anything within one path component, and every other byte itself. It
// matches the path and, as a subpath would, everything under it. ParseConfig
// refuses "?" and "[", which Codex also reads and this does not.
func deniedPathRegex(glob string) string {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(glob); {
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 3
		case strings.HasPrefix(glob[i:], "**"):
			b.WriteString(".*")
			i += 2
		case glob[i] == '*':
			b.WriteString("[^/]*")
			i++
		default:
			j := i + strings.IndexByte(glob[i:]+"*", '*')
			b.WriteString(regexp.QuoteMeta(glob[i:j]))
			i = j
		}
	}
	b.WriteString("(/.*)?$")
	return b.String()
}
