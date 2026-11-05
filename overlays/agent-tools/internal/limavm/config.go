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
	"net/netip"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/wesnel/agent-tools/internal/limaegress"
)

// GuardConfig is what the launch guard needs, which Nix renders into a
// store file of its own: the guard reads it from that immutable path, never
// from the Lima home, which QEMU and lima-mcp write. It names no guard, so
// the guard can name it.
type GuardConfig struct {
	// Home is the operator's home directory, which QEMU may not read but
	// for LimaHome.
	Home string
	// LimaHome is the managed VM's own Lima home, which only lima-mcp and
	// the operator's lima-dev use. Its _mcp directory holds what lima-mcp
	// keeps: the CA, the stable links and the relays' slots and log.
	LimaHome string
	// Instance is the one instance's name.
	Instance string
	// QEMU is the pinned QEMU in the store, which the guard executes.
	QEMU string
	// DeniedReads are absolute paths, possibly holding "*" for one path
	// component, that QEMU and its relays may not read.
	DeniedReads []string
	// Connect and QUIC are the guest addresses QEMU forwards to the
	// CONNECT relay and the QUIC broker.
	Connect, QUIC netip.AddrPort
}

// Config is the managed VM's configuration, parsed once from the JSON Nix
// renders (rawConfig is its form): the guard's, and what lima-mcp and the
// relays need besides. Its paths are absolute and clean.
type Config struct {
	GuardConfig
	// Workdir is where guest commands run, relative to the guest user's
	// home.
	Workdir string
	// Limactl is the pinned Lima in the store.
	Limactl string
	// Guard is the executable Lima starts as QEMU (QEMU_SYSTEM_AARCH64),
	// which runs lima-mcp qemu-guard with the guard's configuration. Lima
	// finds firmware in share/qemu beside its bin directory.
	Guard string
	// LimaMCP is this program in the store, which BinLink names for the
	// relays QEMU spawns.
	LimaMCP string
	// Template is the instance's Lima template in the store.
	Template string
	// Shim is the guest's UDP shim, a linux/arm64 executable.
	Shim string
	// Egress is the CONNECT relay's policy.
	Egress *limaegress.Policy
	// EgressJSON is that policy's JSON form, as configured.
	EgressJSON json.RawMessage
	// QUICServers are the private-dial QUIC server names the broker
	// terminates, which the CA is constrained to.
	QUICServers []limaegress.Name
	// Endpoints are the private endpoints a dial may reach.
	Endpoints []limaegress.Endpoint
}

type rawGuard struct {
	Home        string   `json:"home"`
	LimaHome    string   `json:"limaHome"`
	Instance    string   `json:"instance"`
	QEMU        string   `json:"qemu"`
	DeniedReads []string `json:"deniedReads"`
	Connect     string   `json:"connect"`
	QUIC        string   `json:"quic"`
}

type rawConfig struct {
	rawGuard
	Workdir              string          `json:"workdir"`
	Limactl              string          `json:"limactl"`
	Guard                string          `json:"guard"`
	LimaMCP              string          `json:"limaMcp"`
	Template             string          `json:"template"`
	Shim                 string          `json:"shim"`
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

// decodeStrict decodes data into v, refusing unknown fields and trailing
// data.
func decodeStrict(what string, data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: data after the JSON object", what)
	}
	return nil
}

func checkPaths(what string, paths ...[2]string) error {
	for _, p := range paths {
		if !safePath.MatchString(p[1]) || path.Clean(p[1]) != p[1] || p[1] == "/" {
			return fmt.Errorf("%s: %s %q is not a clean absolute path of letters, digits and ._/+-", what, p[0], p[1])
		}
	}
	return nil
}

// parse checks the guard's part of a configuration.
func (r rawGuard) parse(what string) (GuardConfig, error) {
	g := GuardConfig{Home: r.Home, LimaHome: r.LimaHome, Instance: r.Instance, QEMU: r.QEMU}
	if err := checkPaths(what, [2]string{"home", r.Home}, [2]string{"limaHome", r.LimaHome}, [2]string{"qemu", r.QEMU}); err != nil {
		return g, err
	}
	for _, p := range r.DeniedReads {
		// The profile translates * and ** alone (deniedPathRegex).
		if !strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\"\\\x00\n?[") {
			return g, fmt.Errorf("%s: deniedReads %q is not an absolute path without quotes, backslashes, ? or [", what, p)
		}
		g.DeniedReads = append(g.DeniedReads, path.Clean(p))
	}
	if !instanceName.MatchString(g.Instance) {
		return g, fmt.Errorf("%s: instance %q is not a lowercase name", what, g.Instance)
	}
	var err error
	if g.Connect, err = guestAddr(what, "connect", r.Connect); err != nil {
		return g, err
	}
	if g.QUIC, err = guestAddr(what, "quic", r.QUIC); err != nil {
		return g, err
	}
	if g.QUIC.Addr() == g.Connect.Addr() {
		return g, fmt.Errorf("%s: quic and connect share an address", what)
	}
	return g, nil
}

// LoadGuardConfig reads and parses the guard's configuration at file.
func LoadGuardConfig(file string) (*GuardConfig, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return ParseGuardConfig(data)
}

// ParseGuardConfig parses the guard's configuration: exactly the guard's
// fields of the managed VM's configuration.
func ParseGuardConfig(data []byte) (*GuardConfig, error) {
	var raw rawGuard
	if err := decodeStrict("lima guard config", data, &raw); err != nil {
		return nil, err
	}
	g, err := raw.parse("lima guard config")
	if err != nil {
		return nil, err
	}
	return &g, nil
}

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
	var raw rawConfig
	if err := decodeStrict("lima config", data, &raw); err != nil {
		return nil, err
	}
	g, err := raw.rawGuard.parse("lima config")
	if err != nil {
		return nil, err
	}
	c := &Config{
		GuardConfig: g, Workdir: raw.Workdir, Limactl: raw.Limactl, Guard: raw.Guard, LimaMCP: raw.LimaMCP,
		Template: raw.Template, Shim: raw.Shim, EgressJSON: raw.Egress,
	}
	if err := checkPaths("lima config", [2]string{"limactl", c.Limactl}, [2]string{"guard", c.Guard},
		[2]string{"limaMcp", c.LimaMCP}, [2]string{"template", c.Template}, [2]string{"shim", c.Shim}); err != nil {
		return nil, err
	}
	if !workdir.MatchString(c.Workdir) || strings.Contains("/"+c.Workdir+"/", "/../") || strings.Contains("/"+c.Workdir+"/", "/./") {
		return nil, fmt.Errorf("lima config: workdir %q is not a path inside the guest home", c.Workdir)
	}
	if c.Egress, err = limaegress.ParsePolicy(raw.Egress); err != nil {
		return nil, fmt.Errorf("lima config: egress: %w", err)
	}
	if c.QUICServers = c.Egress.QUICServers(); len(c.QUICServers) == 0 {
		return nil, errors.New("lima config: egress quicServers is empty")
	}
	for _, s := range raw.PrivateDialEndpoints {
		e, err := limaegress.ParseEndpoint(s)
		if err != nil {
			return nil, fmt.Errorf("lima config: privateDialEndpoints: %w", err)
		}
		c.Endpoints = append(c.Endpoints, e)
	}
	return c, nil
}

func guestAddr(what, name, s string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil || !ap.Addr().Is4() || !guestNet.Contains(ap.Addr()) || ap.Port() == 0 ||
		ap.Addr() == slirpGateway || ap.Addr() == slirpDNS || ap.Addr().As4()[3] < 4 || ap.Addr().As4()[3] == 255 {
		return netip.AddrPort{}, fmt.Errorf("%s: %s %q is not an unused IPv4 address and port in %s", what, name, s, guestNet)
	}
	return ap, nil
}

// MCPDir is where lima-mcp keeps the VM's CA, links, slots and log,
// outside the instance directory, so that they outlive the instance.
func (g *GuardConfig) MCPDir() string { return path.Join(g.LimaHome, "_mcp") }

// InstanceDir is the instance's directory, where Lima keeps its disk,
// sockets and logs.
func (g *GuardConfig) InstanceDir() string { return path.Join(g.LimaHome, g.Instance) }

// CACert and CAKey are the broker CA's certificate, which the template's
// caCerts names, and its key.
func (g *GuardConfig) CACert() string { return path.Join(g.MCPDir(), "ca.pem") }
func (g *GuardConfig) CAKey() string  { return path.Join(g.MCPDir(), "ca.key") }

// ConfigLink names the configuration in the store that lima-mcp or lima-dev
// last refreshed it to. The relays read the configuration through it, so a
// new policy reaches a running VM without restarting it. QEMU's profile
// does not let it or the relays write it.
func (g *GuardConfig) ConfigLink() string { return path.Join(g.MCPDir(), "config.json") }

// BinLink names LimaMCP. The guestfwd commands run it, so that the store
// path of a running VM's relays is not collected.
func (g *GuardConfig) BinLink() string { return path.Join(g.MCPDir(), "bin", "lima-mcp") }

// Slots and QUICSlots hold the CONNECT and QUIC relays' slot files.
func (g *GuardConfig) Slots() string     { return path.Join(g.MCPDir(), "slots") }
func (g *GuardConfig) QUICSlots() string { return path.Join(g.MCPDir(), "slots-quic") }

// EgressLog is where the relays log, since their stderr is the guest's
// connection.
func (g *GuardConfig) EgressLog() string { return path.Join(g.MCPDir(), "egress.log") }
