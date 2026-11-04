// Package limaegress relays one TCP connection of an untrusted Lima guest
// as an HTTPS CONNECT proxy, to the hosts a Policy allows and only at
// public addresses, checking that the TLS ClientHello names the host the
// guest asked for. It owns the types and checks every path out of the
// guest shares: Name, Endpoint, Policy and CheckAddrs.
package limaegress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// Name is a host name in canonical form: lowercase letters, digits and
// hyphens in dot-separated labels, with no trailing dot, and never an IP
// address. ParseName makes one.
type Name string

// ParseName returns s as a Name: lowercased, its trailing dot removed. It
// refuses any byte outside ASCII before lowercasing, so that no Unicode
// letter folds to an ASCII one (U+212A, the Kelvin sign, to k); anything
// else that is not a name of LDH labels (RFC 1123) of 1 to 63 bytes, 253
// in all; and an IP literal, including what resolvers may read as an IPv4
// address in inet_aton's forms: a name whose last label is all digits
// ("127.1"), or whose every label is a decimal, 0x-hexadecimal or
// 0-octal number ("0x7f.0x1", "0x7f000001").
func ParseName(s string) (Name, error) {
	for i := range len(s) {
		if s[i] >= 0x80 {
			return "", errors.New("host name: a byte outside ASCII")
		}
	}
	s = strings.TrimSuffix(strings.ToLower(s), ".")
	if s == "" || len(s) > 253 {
		return "", fmt.Errorf("host name %q: length must be 1 to 253", s)
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return "", fmt.Errorf("host name %q is an IP address", s)
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return "", fmt.Errorf("host name %q: each label must be 1 to 63 bytes", s)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("host name %q: a label starts or ends with a hyphen", s)
		}
		for i := range len(label) {
			if c := label[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("host name %q: only letters, digits and hyphens may appear in a label", s)
			}
		}
	}
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", fmt.Errorf("host name %q: its last label is all digits", s)
	}
	if !slices.ContainsFunc(labels, func(l string) bool { return !isAtonNumber(l) }) {
		return "", fmt.Errorf("host name %q reads as an IPv4 address", s)
	}
	return Name(s), nil
}

// isAtonNumber reports whether a lowercase label is a number as inet_aton
// reads one: hexadecimal after "0x" (glibc reads "0x" alone as 0), or
// decimal and octal digits.
func isAtonNumber(label string) bool {
	digits, hex := strings.CutPrefix(label, "0x")
	if hex {
		return strings.Trim(digits, "0123456789abcdef") == ""
	}
	return strings.Trim(label, "0123456789") == ""
}

// pattern matches a host name: an exact name, or with wildcard set every
// name strictly under it ("*.example.com" matches a.example.com and
// a.b.example.com, not example.com), as the host sandbox's allowedDomains
// read.
type pattern struct {
	name     Name
	wildcard bool
}

func parsePattern(s string) (pattern, error) {
	rest, wildcard := strings.CutPrefix(s, "*.")
	name, err := ParseName(rest)
	if err != nil {
		return pattern{}, fmt.Errorf("pattern %q: %w", s, err)
	}
	return pattern{name: name, wildcard: wildcard}, nil
}

// matches compares whole DNS labels: "*.x" matches a name with at least one
// label before all of x's.
func (p pattern) matches(name Name) bool {
	if !p.wildcard {
		return name == p.name
	}
	labels := strings.Split(string(name), ".")
	suffix := strings.Split(string(p.name), ".")
	return len(labels) > len(suffix) && slices.Equal(labels[len(labels)-len(suffix):], suffix)
}

// fixedDeny is refused whatever a policy says: private dial over HTTP/2
// and every other ngrok connect endpoint, which would bypass the QUIC
// broker, and the ngrok API.
var fixedDeny = []pattern{
	{name: "connect-endpoint.ngrok.com"},
	{name: "connect-endpoint.ngrok.com", wildcard: true},
	{name: "api.ngrok.com"},
}

// Policy says which host names the guest may reach. A name is allowed when
// an allow pattern matches it and no deny pattern does.
type Policy struct {
	allow       []pattern
	deny        []pattern
	quicServers []Name
}

// policyJSON is the policy's JSON form. Each allow and deny entry is an
// exact name or "*.suffix"; each QUIC server is an exact name, the
// private-dial servers the broker terminates, which the relay therefore
// refuses.
type policyJSON struct {
	Allow       []string `json:"allow"`
	Deny        []string `json:"deny"`
	QUICServers []string `json:"quicServers"`
}

// ParsePolicy parses a policy from its JSON form, refusing unknown fields,
// trailing data and any pattern that is not a name or "*." and a name. The
// policy denies, besides its own deny list, its QUIC servers and fixedDeny.
func ParsePolicy(data []byte) (*Policy, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var raw policyJSON
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("egress policy: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("egress policy: data after the JSON object")
	}
	p := &Policy{deny: append([]pattern(nil), fixedDeny...)}
	for _, s := range raw.Allow {
		pat, err := parsePattern(s)
		if err != nil {
			return nil, fmt.Errorf("egress policy allow: %w", err)
		}
		p.allow = append(p.allow, pat)
	}
	for _, s := range raw.Deny {
		pat, err := parsePattern(s)
		if err != nil {
			return nil, fmt.Errorf("egress policy deny: %w", err)
		}
		p.deny = append(p.deny, pat)
	}
	for _, s := range raw.QUICServers {
		name, err := ParseName(s)
		if err != nil {
			return nil, fmt.Errorf("egress policy quicServers: %w", err)
		}
		p.deny = append(p.deny, pattern{name: name})
		p.quicServers = append(p.quicServers, name)
	}
	return p, nil
}

// QUICServers are the private-dial QUIC servers, as the policy lists them:
// the names the broker terminates and its CA vouches for, which the relay
// refuses.
func (p *Policy) QUICServers() []Name { return slices.Clone(p.quicServers) }

// Endpoint is a private endpoint a private dial may reach: a host, a Name
// or an IP address as netip writes it with no zone, and a port 1 to
// 65535. ParseEndpoint makes one, from its one spelling, so that the
// broker compares a dial's destination with Endpoints as they are written.
type Endpoint struct {
	Host string
	Port uint16
}

// ParseEndpoint parses host:port, refusing every spelling but String's:
// an uppercase or dotted name, a zoned or non-canonical IP address, or a
// port with a sign or a leading zero.
func ParseEndpoint(s string) (Endpoint, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return Endpoint{}, fmt.Errorf("endpoint %q: %w", s, err)
	}
	if err := CheckHost(host); err != nil {
		return Endpoint{}, fmt.Errorf("endpoint %q: %w", s, err)
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != port {
		return Endpoint{}, fmt.Errorf("endpoint %q: the port is not a decimal 1 to 65535", s)
	}
	return Endpoint{Host: host, Port: uint16(n)}, nil
}

// CheckHost refuses host unless it is a private endpoint's host in its one
// spelling: a Name as ParseName returns it, or an IP address as netip
// writes it, with no zone.
func CheckHost(host string) error {
	if a, err := netip.ParseAddr(host); err == nil {
		if a.Zone() != "" || a.String() != host {
			return fmt.Errorf("host %q is not an IP address as netip writes it", host)
		}
		return nil
	}
	name, err := ParseName(host)
	if err != nil {
		return err
	}
	if string(name) != host {
		return fmt.Errorf("host %q is not written as %q", host, name)
	}
	return nil
}

// String is host:port, with an IPv6 host in brackets.
func (e Endpoint) String() string {
	return net.JoinHostPort(e.Host, strconv.FormatUint(uint64(e.Port), 10))
}

// Allows reports whether the guest may connect to name.
func (p *Policy) Allows(name Name) bool {
	for _, pat := range p.deny {
		if pat.matches(name) {
			return false
		}
	}
	for _, pat := range p.allow {
		if pat.matches(name) {
			return true
		}
	}
	return false
}
