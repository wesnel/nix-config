// Package limavm is the host side of the managed Lima VM: its
// configuration, which Nix renders into the store; the CA the guest trusts
// for the private-dial broker; the guard that checks and confines each QEMU
// launch; and the attestation of the QEMU that runs.
package limavm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/wesnel/agent-tools/internal/limaegress"
)

// Config is the managed VM's configuration, parsed once from the JSON Nix
// renders (rawConfig is its form). Its paths are absolute and clean.
type Config struct {
	// Home is the operator's home directory, which QEMU may not read but
	// for LimaHome.
	Home string
	// LimaHome is the managed VM's own Lima home, which only lima-mcp and
	// the operator's lima-dev use. Its _mcp directory holds what lima-mcp
	// keeps: the CA, the stable links and the relays' slots and log.
	LimaHome string
	// Instance is the one instance's name.
	Instance string
	// Workdir is where guest commands run, relative to the guest user's
	// home.
	Workdir string
	// Limactl and QEMU are the pinned Lima and QEMU in the store.
	Limactl, QEMU string
	// Guard is the executable Lima starts as QEMU (QEMU_SYSTEM_AARCH64),
	// which runs lima-mcp qemu-guard. Lima finds firmware in share/qemu
	// beside its bin directory.
	Guard string
	// LimaMCP is this program in the store, which BinLink names for the
	// relays QEMU spawns.
	LimaMCP string
	// Template is the instance's Lima template in the store.
	Template string
	// Shim is the guest's UDP shim, a linux/arm64 executable.
	Shim string
	// DeniedReads are absolute paths, possibly holding "*" for one path
	// component, that QEMU and its relays may not read.
	DeniedReads []string
	// Connect and QUIC are the guest addresses QEMU forwards to the
	// CONNECT relay and the QUIC broker.
	Connect, QUIC netip.AddrPort
	// Egress is the CONNECT relay's policy.
	Egress *limaegress.Policy
	// EgressJSON is that policy's JSON form, as configured.
	EgressJSON json.RawMessage
	// QUICServers are the private-dial QUIC server names the broker
	// terminates, which the CA is constrained to.
	QUICServers []string
	// Endpoints are the private endpoints, host:port, a dial may reach.
	Endpoints []string
}

type rawConfig struct {
	Home                 string          `json:"home"`
	LimaHome             string          `json:"limaHome"`
	Instance             string          `json:"instance"`
	Workdir              string          `json:"workdir"`
	Limactl              string          `json:"limactl"`
	QEMU                 string          `json:"qemu"`
	Guard                string          `json:"guard"`
	LimaMCP              string          `json:"limaMcp"`
	Template             string          `json:"template"`
	Shim                 string          `json:"shim"`
	DeniedReads          []string        `json:"deniedReads"`
	Connect              string          `json:"connect"`
	QUIC                 string          `json:"quic"`
	Egress               json.RawMessage `json:"egress"`
	PrivateDialEndpoints []string        `json:"privateDialEndpoints"`
}

// safePath is the bytes a path may hold where it is written into QEMU's
// netdev option (commas separate options), a guestfwd command (spaces
// separate arguments) or a Seatbelt profile (quotes end a string).
var safePath = regexp.MustCompile(`^/[A-Za-z0-9._/+-]*$`)

var (
	instanceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)
	workdir      = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	// guestNet is QEMU user networking's network, as Lima configures it.
	guestNet = netip.MustParsePrefix("192.168.5.0/24")
	// slirp itself answers on its gateway and DNS addresses, which a relay
	// may not take.
	slirpGateway, slirpDNS = netip.MustParseAddr("192.168.5.2"), netip.MustParseAddr("192.168.5.3")
)

// LoadConfig reads and parses the configuration at file.
func LoadConfig(file string) (*Config, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return ParseConfig(data)
}

// ParseConfig parses the configuration, refusing unknown fields, trailing
// data, and any value the guard could not write safely into QEMU's argv
// or a Seatbelt profile.
func ParseConfig(data []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw rawConfig
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("lima config: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("lima config: data after the JSON object")
	}
	c := &Config{
		Home: raw.Home, LimaHome: raw.LimaHome, Instance: raw.Instance, Workdir: raw.Workdir,
		Limactl: raw.Limactl, QEMU: raw.QEMU, Guard: raw.Guard, LimaMCP: raw.LimaMCP,
		Template: raw.Template, Shim: raw.Shim, EgressJSON: raw.Egress,
	}
	for _, p := range []struct{ name, value string }{
		{"home", c.Home}, {"limaHome", c.LimaHome}, {"limactl", c.Limactl}, {"qemu", c.QEMU}, {"guard", c.Guard},
		{"limaMcp", c.LimaMCP}, {"template", c.Template}, {"shim", c.Shim},
	} {
		if !safePath.MatchString(p.value) || path.Clean(p.value) != p.value || p.value == "/" {
			return nil, fmt.Errorf("lima config: %s %q is not a clean absolute path of letters, digits and ._/+-", p.name, p.value)
		}
	}
	for _, p := range raw.DeniedReads {
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\"\\\x00\n") {
			return nil, fmt.Errorf("lima config: deniedReads %q is not an absolute path without quotes or backslashes", p)
		}
		c.DeniedReads = append(c.DeniedReads, path.Clean(p))
	}
	if !instanceName.MatchString(c.Instance) {
		return nil, fmt.Errorf("lima config: instance %q is not a lowercase name", c.Instance)
	}
	if !workdir.MatchString(c.Workdir) || strings.Contains("/"+c.Workdir+"/", "/../") || strings.Contains("/"+c.Workdir+"/", "/./") {
		return nil, fmt.Errorf("lima config: workdir %q is not a path inside the guest home", c.Workdir)
	}
	var err error
	if c.Connect, err = guestAddr("connect", raw.Connect); err != nil {
		return nil, err
	}
	if c.QUIC, err = guestAddr("quic", raw.QUIC); err != nil {
		return nil, err
	}
	if c.QUIC.Addr() == c.Connect.Addr() {
		return nil, errors.New("lima config: quic and connect share an address")
	}
	if c.Egress, err = limaegress.ParsePolicy(raw.Egress); err != nil {
		return nil, fmt.Errorf("lima config: egress: %w", err)
	}
	var servers struct {
		QUICServers []string `json:"quicServers"`
	}
	if err := json.Unmarshal(raw.Egress, &servers); err != nil {
		return nil, fmt.Errorf("lima config: egress: %w", err)
	}
	if len(servers.QUICServers) == 0 {
		return nil, errors.New("lima config: egress quicServers is empty")
	}
	c.QUICServers = servers.QUICServers
	for _, e := range raw.PrivateDialEndpoints {
		if !endpoint(e) {
			return nil, fmt.Errorf("lima config: privateDialEndpoints %q is not host:port, a lowercase LDH name or an IP address and a decimal port 1 to 65535", e)
		}
	}
	c.Endpoints = raw.PrivateDialEndpoints
	return c, nil
}

// endpoint reports whether e is host:port in canonical form, as the
// broker compares a dial's destination with it: a lowercase LDH name
// without a trailing dot, or an IP address as netip writes it with no
// zone, and a decimal port 1 to 65535 without a leading zero.
func endpoint(e string) bool {
	host, port, err := net.SplitHostPort(e)
	if err != nil {
		return false
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != port {
		return false
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return a.Zone() == "" && a.String() == host
	}
	name, err := limaegress.ParseName(host)
	return err == nil && string(name) == host
}

func guestAddr(name, s string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil || !ap.Addr().Is4() || !guestNet.Contains(ap.Addr()) || ap.Port() == 0 ||
		ap.Addr() == slirpGateway || ap.Addr() == slirpDNS || ap.Addr().As4()[3] < 4 || ap.Addr().As4()[3] == 255 {
		return netip.AddrPort{}, fmt.Errorf("lima config: %s %q is not an unused IPv4 address and port in %s", name, s, guestNet)
	}
	return ap, nil
}

// MCPDir is where lima-mcp keeps the VM's CA, links, slots and log,
// outside the instance directory, so that they outlive the instance.
func (c *Config) MCPDir() string { return path.Join(c.LimaHome, "_mcp") }

// InstanceDir is the instance's directory, where Lima keeps its disk,
// sockets and logs.
func (c *Config) InstanceDir() string { return path.Join(c.LimaHome, c.Instance) }

// CACert and CAKey are the broker CA's certificate, which the template's
// caCerts names, and its key.
func (c *Config) CACert() string { return path.Join(c.MCPDir(), "ca.pem") }
func (c *Config) CAKey() string  { return path.Join(c.MCPDir(), "ca.key") }

// ConfigLink names the configuration in the store that lima-mcp last
// started with. The guard and the relays read the configuration through
// it, so a new configuration reaches them without restarting the VM.
func (c *Config) ConfigLink() string { return path.Join(c.MCPDir(), "config.json") }

// BinLink names LimaMCP. The guestfwd commands run it, so that the store
// path of a running VM's relays is not collected.
func (c *Config) BinLink() string { return path.Join(c.MCPDir(), "bin", "lima-mcp") }

// Slots and QUICSlots hold the CONNECT and QUIC relays' slot files.
func (c *Config) Slots() string     { return path.Join(c.MCPDir(), "slots") }
func (c *Config) QUICSlots() string { return path.Join(c.MCPDir(), "slots-quic") }

// EgressLog is where the relays log, since their stderr is the guest's
// connection.
func (c *Config) EgressLog() string { return path.Join(c.MCPDir(), "egress.log") }

// Firmware are the directories a firmware drive may name: share/qemu
// beside the guard, where Lima looks, and the store QEMU's.
func (c *Config) Firmware() []string {
	return []string{
		path.Join(path.Dir(path.Dir(c.Guard)), "share", "qemu"),
		path.Join(path.Dir(path.Dir(c.QEMU)), "share", "qemu"),
	}
}
